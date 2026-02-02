package alerts

import (
	"context"
	"fmt"
	"sync"
	"time"

	"lecca.io/pharos-watchtower/internal/config"
	"lecca.io/pharos-watchtower/internal/logger"
	"lecca.io/pharos-watchtower/internal/rpc"
	"lecca.io/pharos-watchtower/internal/validators"
)

type Manager struct {
	cfg             config.AlertsConfig
	chainID         string
	alertValidators []string
	registry        *validators.Registry
	nodeMgr         *rpc.Manager
	notifier        Notifier
	state           *StateStore
	alerts          map[string]AlertStateItem
	mu              sync.Mutex
}

func NewManager(cfg config.AlertsConfig, chainID string, stateFile string, validators []string, registry *validators.Registry, nodeMgr *rpc.Manager) *Manager {
	return &Manager{
		cfg:             cfg,
		chainID:         chainID,
		alertValidators: validators,
		registry:        registry,
		nodeMgr:         nodeMgr,
		notifier:        NewNotifier(cfg),
		state:           NewStateStore(stateFile),
		alerts:          make(map[string]AlertStateItem),
	}
}

func (m *Manager) Start(ctx context.Context) {
	// Load persisted state
	loaded, err := m.state.Load(m.chainID)
	if err != nil {
		logger.Warn("ALERT", "Failed to load alert state: %v", err)
	} else {
		m.alerts = loaded
		logger.Info("ALERT", "Loaded %d alert states from disk", len(m.alerts))
	}

	ticker := time.NewTicker(30 * time.Second)
	go func() {
		// Initial check
		m.checkRules(ctx)

		for {
			select {
			case <-ctx.Done():
				ticker.Stop()
				return
			case <-ticker.C:
				m.checkRules(ctx)
			}
		}
	}()
}

// shouldAlertValidator returns true if alert should be sent for this validator
// - If alertValidators is empty, alert for all validators
// - If alertValidators is not empty, only alert if validator is in the list
func (m *Manager) shouldAlertValidator(blsKey string) bool {
	if len(m.alertValidators) == 0 {
		return true // Empty list = alert for all
	}
	// Use validators.NormalizeBlsKey for consistent normalization (removes 0x and 4003 prefix)
	normalizedKey := validators.NormalizeBlsKey(blsKey)
	for _, v := range m.alertValidators {
		normalizedV := validators.NormalizeBlsKey(v)
		if normalizedV == normalizedKey {
			return true
		}
	}
	return false
}

func (m *Manager) checkRules(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()

	// Check validator rules
	if m.cfg.Rules.ValidatorDown.Enabled() {
		m.checkValidatorDown(ctx, now)
	}

	// Check node rules
	if m.cfg.Rules.NodeDown.Enabled() {
		m.checkNodeDown(ctx, now)
	}

	// Check WS rules
	if m.cfg.Rules.WsDown.Enabled() {
		m.checkWsDown(ctx, now)
	}

	// Check validator uptime rules
	if m.cfg.Rules.ValidatorUptime.Enabled() {
		m.checkValidatorUptime(ctx, now)
	}

	// Save state
	if err := m.state.Save(m.chainID, m.alerts); err != nil {
		logger.Warn("ALERT", "Failed to save alert state: %v", err)
	}
}

func (m *Manager) checkValidatorDown(ctx context.Context, now time.Time) {
	vals := m.registry.GetValidators()
	fireDuration := m.cfg.Rules.ValidatorDown.FireDuration()
	resolveDuration := m.cfg.Rules.ValidatorDown.ResolveDuration()
	if resolveDuration == 0 {
		resolveDuration = fireDuration
	}

	for blsKey, v := range vals {
		if !m.shouldAlertValidator(blsKey) {
			continue
		}

		v.Mu.RLock()
		isDown := v.Down
		description := v.Meta.Description
		validatorID := v.Meta.ValidatorID
		v.Mu.RUnlock()

		key := fmt.Sprintf("validator_down:%s", blsKey)
		state, exists := m.alerts[key]

		if isDown {
			if !exists {
				// Start tracking - NEW down state
				m.alerts[key] = AlertStateItem{
					Key:          key,
					RuleID:       RuleValidatorDowntime,
					SubjectType:  SubjectValidator,
					SubjectID:    blsKey,
					Status:       AlertFiring,
					FiringSince:  now,
					LastObserved: now,
				}
			} else {
				// Update last observed
				state.LastObserved = now
				m.alerts[key] = state

				// Check if should fire alert (after fire_after duration)
				if state.Status == AlertFiring && now.Sub(state.FiringSince) >= fireDuration && state.LastEventAt.IsZero() {
					// Fire alert
					actualDowntime := now.Sub(state.FiringSince).Round(time.Second)
					event := AlertEvent{
						Key:         key,
						RuleID:      RuleValidatorDowntime,
						SubjectType: SubjectValidator,
						SubjectID:   blsKey,
						SubjectName: description,
						ChainID:     m.chainID,
						Status:      AlertFiring,
						Severity:    "critical",
						Title:       "Validator Down",
						Message:     fmt.Sprintf("Validator %s (%s) has been DOWN for %v", validatorID, description, actualDowntime),
						Details: []AlertDetail{
							{Label: "Downtime", Value: fmt.Sprintf("down %s", actualDowntime)},
						},
						Timestamp: now,
					}
					if err := m.notifier.Notify(ctx, event); err != nil {
						logger.Warn("ALERT", "Failed to send validator down alert: %v", err)
					}
					state.LastEventAt = now
					m.alerts[key] = state
				}
			}
		} else if exists {
			// Validator is back up
			if state.Status == AlertFiring {
				// Send resolve alert (only if we sent a firing alert)
				if !state.LastEventAt.IsZero() {
					totalDowntime := now.Sub(state.FiringSince).Round(time.Second)
					event := AlertEvent{
						Key:         key,
						RuleID:      RuleValidatorDowntime,
						SubjectType: SubjectValidator,
						SubjectID:   blsKey,
						SubjectName: description,
						ChainID:     m.chainID,
						Status:      AlertResolved,
						Severity:    "info",
						Title:       "Validator Recovered",
						Message:     fmt.Sprintf("Validator %s (%s) recovered after being DOWN for %v", validatorID, description, totalDowntime),
						Details: []AlertDetail{
							{Label: "Downtime", Value: fmt.Sprintf("down %s → recovered", totalDowntime)},
						},
						Timestamp: now,
					}
					if err := m.notifier.Notify(ctx, event); err != nil {
						logger.Warn("ALERT", "Failed to send validator resolved alert: %v", err)
					}
				}
				// Remove from tracking
				delete(m.alerts, key)
			}
		}
	}
}

func (m *Manager) checkNodeDown(ctx context.Context, now time.Time) {
	if m.nodeMgr == nil {
		return
	}

	nodes := m.nodeMgr.GetNodes()
	fireDuration := m.cfg.Rules.NodeDown.FireDuration()
	resolveDuration := m.cfg.Rules.NodeDown.ResolveDuration()
	if resolveDuration == 0 {
		resolveDuration = fireDuration
	}

	for _, n := range nodes {
		if !n.Config.AlertOnDown {
			continue
		}

		status := n.GetStatus()
		key := fmt.Sprintf("node_down:%s", n.Config.Label)
		state, exists := m.alerts[key]

		if !status.Healthy {
			if !exists {
				m.alerts[key] = AlertStateItem{
					Key:          key,
					RuleID:       RuleNodeDowntime,
					SubjectType:  SubjectNode,
					SubjectID:    n.Config.Label,
					Status:       AlertFiring,
					FiringSince:  now,
					LastObserved: now,
				}
			} else {
				state.LastObserved = now
				m.alerts[key] = state

				if state.Status == AlertFiring && now.Sub(state.FiringSince) >= fireDuration && state.LastEventAt.IsZero() {
					actualDowntime := now.Sub(state.FiringSince).Round(time.Second)
					event := AlertEvent{
						Key:         key,
						RuleID:      RuleNodeDowntime,
						SubjectType: SubjectNode,
						SubjectID:   n.Config.Label,
						SubjectName: n.Config.Label,
						ChainID:     m.chainID,
						Status:      AlertFiring,
						Severity:    "warning",
						Title:       "Node Down",
						Message:     fmt.Sprintf("Node %s (%s) has been DOWN for %v", n.Config.Label, n.Config.RPC, actualDowntime),
						Details: []AlertDetail{
							{Label: "Downtime", Value: fmt.Sprintf("down %s", actualDowntime)},
						},
						Timestamp: now,
					}
					if err := m.notifier.Notify(ctx, event); err != nil {
						logger.Warn("ALERT", "Failed to send node down alert: %v", err)
					}
					state.LastEventAt = now
					m.alerts[key] = state
				}
			}
		} else if exists {
			if state.Status == AlertFiring {
				if !state.LastEventAt.IsZero() {
					totalDowntime := now.Sub(state.FiringSince).Round(time.Second)
					event := AlertEvent{
						Key:         key,
						RuleID:      RuleNodeDowntime,
						SubjectType: SubjectNode,
						SubjectID:   n.Config.Label,
						SubjectName: n.Config.Label,
						ChainID:     m.chainID,
						Status:      AlertResolved,
						Severity:    "info",
						Title:       "Node Recovered",
						Message:     fmt.Sprintf("Node %s (%s) recovered after being DOWN for %v", n.Config.Label, n.Config.RPC, totalDowntime),
						Details: []AlertDetail{
							{Label: "Downtime", Value: fmt.Sprintf("down %s → recovered", totalDowntime)},
						},
						Timestamp: now,
					}
					if err := m.notifier.Notify(ctx, event); err != nil {
						logger.Warn("ALERT", "Failed to send node resolved alert: %v", err)
					}
				}
				delete(m.alerts, key)
			}
		}
	}
}

func (m *Manager) checkWsDown(ctx context.Context, now time.Time) {
	// WS down detection would need WebSocket connection status tracking
	// This is a placeholder for future implementation
	// For now, WS issues are typically caught by node health checks
}

func (m *Manager) checkValidatorUptime(ctx context.Context, now time.Time) {
	vals := m.registry.GetValidators()
	thresholdPercent := m.cfg.Rules.ValidatorUptime.ThresholdPercent()
	threshold := float64(thresholdPercent) / 100.0

	for blsKey, v := range vals {
		if !m.shouldAlertValidator(blsKey) {
			continue
		}

		_, total, ratio := v.Window.GetStats()
		uptime := 0.0
		if total > 0 {
			uptime = 1.0 - ratio
		}

		v.Mu.RLock()
		description := v.Meta.Description
		validatorID := v.Meta.ValidatorID
		v.Mu.RUnlock()

		key := fmt.Sprintf("validator_uptime:%s", blsKey)
		state, exists := m.alerts[key]

		if uptime < threshold {
			if !exists {
				m.alerts[key] = AlertStateItem{
					Key:          key,
					RuleID:       RuleValidatorUptime,
					SubjectType:  SubjectValidator,
					SubjectID:    blsKey,
					Status:       AlertFiring,
					FiringSince:  now,
					LastObserved: now,
				}
			} else {
				state.LastObserved = now
				m.alerts[key] = state

				if state.Status == AlertFiring && state.LastEventAt.IsZero() {
					uptimePercent := uptime * 100
					event := AlertEvent{
						Key:         key,
						RuleID:      RuleValidatorUptime,
						SubjectType: SubjectValidator,
						SubjectID:   blsKey,
						SubjectName: description,
						ChainID:     m.chainID,
						Status:      AlertFiring,
						Severity:    "warning",
						Title:       "Validator Uptime Low",
						Message:     fmt.Sprintf("Validator %s (%s) uptime is %.1f%% (threshold: %d%%)", validatorID, description, uptimePercent, thresholdPercent),
						Details: []AlertDetail{
							{Label: "Threshold", Value: fmt.Sprintf("%d%%", thresholdPercent)},
							{Label: "Current Uptime", Value: fmt.Sprintf("%.2f%%", uptimePercent)},
							{Label: "Window", Value: fmt.Sprintf("%d/%d missed", int(ratio*float64(total)), total)},
						},
						Timestamp: now,
					}
					if err := m.notifier.Notify(ctx, event); err != nil {
						logger.Warn("ALERT", "Failed to send validator uptime alert: %v", err)
					}
					state.LastEventAt = now
					m.alerts[key] = state
				}
			}
		} else if exists {
			if state.Status == AlertFiring && uptime >= threshold {
				if !state.LastEventAt.IsZero() {
					uptimePercent := uptime * 100
					event := AlertEvent{
						Key:         key,
						RuleID:      RuleValidatorUptime,
						SubjectType: SubjectValidator,
						SubjectID:   blsKey,
						SubjectName: description,
						ChainID:     m.chainID,
						Status:      AlertResolved,
						Severity:    "info",
						Title:       "Validator Uptime Recovered",
						Message:     fmt.Sprintf("Validator %s (%s) uptime recovered to %.1f%%", validatorID, description, uptimePercent),
						Details: []AlertDetail{
							{Label: "Threshold", Value: fmt.Sprintf("%d%%", thresholdPercent)},
							{Label: "Current Uptime", Value: fmt.Sprintf("%.2f%%", uptimePercent)},
						},
						Timestamp: now,
					}
					if err := m.notifier.Notify(ctx, event); err != nil {
						logger.Warn("ALERT", "Failed to send validator uptime resolved alert: %v", err)
					}
				}
				delete(m.alerts, key)
			}
		}
	}
}
