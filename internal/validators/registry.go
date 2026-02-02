package validators

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"lecca.io/pharos-watchtower/internal/config"
	"lecca.io/pharos-watchtower/internal/logger"
	"lecca.io/pharos-watchtower/internal/rpc"
	"lecca.io/pharos-watchtower/internal/window"
)

type Registry struct {
	cfg        config.ChainConfig
	advanced   config.AdvancedConfig
	nodeMgr    *rpc.Manager
	validators map[string]*ValidatorState
	stateStore *StateStore
	mu         sync.RWMutex
}

func NewRegistry(cfg config.ChainConfig, advanced config.AdvancedConfig, nodeMgr *rpc.Manager) *Registry {
	statePath := "./data/validators-state.json"
	if advanced.StateFile != "" {
		dir := filepath.Dir(advanced.StateFile)
		statePath = filepath.Join(dir, "validators-state.json")
	}

	return &Registry{
		cfg:        cfg,
		advanced:   advanced,
		nodeMgr:    nodeMgr,
		validators: make(map[string]*ValidatorState),
		stateStore: NewStateStore(statePath),
	}
}

func (r *Registry) Start(ctx context.Context) {
	state, err := r.stateStore.Load(r.cfg.ChainID)
	if err != nil {
		logger.Warn("REGISTRY", "Failed to load validator state: %v", err)
	} else if len(state.Validators) > 0 {
		if r.shouldLoadState(state) {
			logger.Info("REGISTRY", "Loaded %d validator states from disk", len(state.Validators))
			selectedKeys := make(map[string]bool)
			if r.cfg.Mode == "selected" {
				for _, k := range r.cfg.Validators {
					selectedKeys[NormalizeBlsKey(k)] = true
				}
				logger.Info("REGISTRY", "Mode 'selected': keeping %d validator states from disk", len(selectedKeys))
			}
			r.mu.Lock()
			windowDuration := config.ParseDuration(r.advanced.Window)
			for blsKey, snapshot := range state.Validators {
				if r.cfg.Mode == "selected" && !selectedKeys[NormalizeBlsKey(blsKey)] {
					continue
				}
				r.validators[blsKey] = SnapshotToState(snapshot, windowDuration)
			}
			r.mu.Unlock()
		} else {
			logger.Info("REGISTRY", "Ignoring validator state from disk due to mode/validator mismatch")
		}
	}

	if err := r.Reload(ctx); err != nil {
		logger.Warn("REGISTRY", "Initial validator reload failed: %v", err)
	}

	ticker := time.NewTicker(config.ParseDuration(r.advanced.ReloadInterval))
	go func() {
		for {
			select {
			case <-ctx.Done():
				ticker.Stop()
				return
			case <-ticker.C:
				if err := r.Reload(ctx); err != nil {
					logger.Warn("REGISTRY", "Validator reload failed: %v", err)
				}
			}
		}
	}()
}

func (r *Registry) Reload(ctx context.Context) error {
	node := r.nodeMgr.GetBestNode()
	if node == nil {
		return fmt.Errorf("no healthy node available")
	}

	// Create contract service
	contractService, err := NewContractService(node.Config.RPC, r.advanced.ContractAddr)
	if err != nil {
		return fmt.Errorf("failed to create contract service: %w", err)
	}
	defer contractService.Close()

	// Get active validators
	validatorIDs, err := contractService.GetActiveValidators(ctx)
	if err != nil {
		return fmt.Errorf("failed to get active validators: %w", err)
	}
	logger.Info("REGISTRY", "Found %d validators in contract", len(validatorIDs))

	r.mu.Lock()
	defer r.mu.Unlock()

	selectedKeys := make(map[string]bool)
	if r.cfg.Mode == "selected" {
		for _, k := range r.cfg.Validators {
			selectedKeys[NormalizeBlsKey(k)] = true
		}
		logger.Info("REGISTRY", "Mode 'selected': filtering for %d target validators", len(selectedKeys))
		for key := range r.validators {
			if !selectedKeys[NormalizeBlsKey(key)] {
				delete(r.validators, key)
			}
		}
	}

	// Process each validator
	for _, validatorID := range validatorIDs {
		validatorInfo, err := contractService.GetValidatorInfo(ctx, validatorID)
		if err != nil {
			logger.Warn("REGISTRY", "Failed to get validator info for %s: %v", bytes32ToHex(validatorID), err)
			continue
		}

		// Normalize BLS key
		normKey := NormalizeBlsKey(validatorInfo.BlsPublicKey)

		// Filter by mode
		if r.cfg.Mode == "selected" && !selectedKeys[normKey] {
			continue
		}

		state, exists := r.validators[normKey]
		if !exists {
			state = &ValidatorState{
				Window: window.NewRollingWindow(config.ParseDuration(r.advanced.Window)),
			}
			r.validators[normKey] = state
		}

		// Set stake (use TotalStake from contract)
		stake := new(big.Int)
		if validatorInfo.TotalStake != nil {
			stake = validatorInfo.TotalStake
		}

		state.Meta = ValidatorMeta{
			ValidatorID: bytes32ToHex(validatorID),
			Description: validatorInfo.Description,
			BlsKeyHex:   normKey,
			Staking:     stake,
			Status:      validatorInfo.Status,
			Endpoint:    validatorInfo.Endpoint,
			Owner:       validatorInfo.Owner,
		}
	}

	logger.Info("REGISTRY", "Tracking %d validators", len(r.validators))
	return nil
}

func parseStake(s string) (*big.Int, error) {
	s = strings.TrimSpace(s)
	b := new(big.Int)
	if _, ok := b.SetString(s, 0); !ok {
		if strings.HasPrefix(strings.ToLower(s), "0x") {
			if _, ok2 := b.SetString(trim0x(s), 16); ok2 {
				return b, nil
			}
		}
		return nil, fmt.Errorf("cannot parse stake value: %s", s)
	}
	return b, nil
}

func trim0x(s string) string {
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		return s[2:]
	}
	return s
}

// NormalizeBlsKey normalizes a BLS public key to lowercase hex without 0x prefix
// Also removes the "4003" prefix if present (contract stores BLS keys with this prefix)
// This function is exported so other packages can use the same normalization logic
func NormalizeBlsKey(key string) string {
	normalized := strings.ToLower(trim0x(key))
	// Remove "4003" prefix if present (BLS keys from contract have this prefix)
	// "4003" is 4 hex characters
	if strings.HasPrefix(normalized, "4003") && len(normalized) > 4 {
		normalized = normalized[4:] // Remove "4003" (4 hex chars)
	}
	return normalized
}

func (r *Registry) GetValidators() map[string]*ValidatorState {
	r.mu.RLock()
	defer r.mu.RUnlock()

	copy := make(map[string]*ValidatorState)
	for k, v := range r.validators {
		copy[k] = v
	}
	return copy
}

func (r *Registry) GetValidator(blsKey string) *ValidatorState {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.validators[NormalizeBlsKey(blsKey)]
}

func (r *Registry) SaveState() error {
	r.mu.RLock()
	defer r.mu.RUnlock()

	validatorsHash := hashValidators(r.cfg.Validators)
	return r.stateStore.Save(r.cfg.ChainID, r.cfg.Mode, validatorsHash, r.validators)
}

func (r *Registry) shouldLoadState(state ValidatorStateFile) bool {
	if state.ChainID != "" && state.ChainID != r.cfg.ChainID {
		return false
	}
	if state.Mode != "" && state.Mode != r.cfg.Mode {
		return false
	}
	currentHash := hashValidators(r.cfg.Validators)
	if state.ValidatorsHash != "" && state.ValidatorsHash != currentHash {
		return false
	}
	return true
}

func hashValidators(validators []string) string {
	if len(validators) == 0 {
		return ""
	}

	normalized := make([]string, 0, len(validators))
	for _, v := range validators {
		normalized = append(normalized, NormalizeBlsKey(v))
	}
	sort.Strings(normalized)

	hasher := sha256.New()
	for _, v := range normalized {
		hasher.Write([]byte(v))
		hasher.Write([]byte("\n"))
	}

	return hex.EncodeToString(hasher.Sum(nil))
}
