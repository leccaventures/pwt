package rpc

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
	"lecca.io/pharos-watchtower/internal/config"
	"lecca.io/pharos-watchtower/internal/logger"
)

// ipv4WithOptionalPort matches IPv4 and optional :port (e.g. 135.181.229.187:18200).
var ipv4WithOptionalPort = regexp.MustCompile(`[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}(?::[0-9]+)?`)

const ipRedacted = "[redacted]"

// SanitizeForLog returns a log-safe error message (IPs redacted via regex).
func SanitizeForLog(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if strings.Contains(msg, "<html") || strings.Contains(msg, "<HTML") {
		if idx := strings.Index(strings.ToLower(msg), "<html"); idx > 0 {
			msg = strings.TrimSpace(msg[:idx])
		} else {
			return "HTTP error response"
		}
	}
	return ipv4WithOptionalPort.ReplaceAllString(msg, ipRedacted)
}

type NodeStatus struct {
	Healthy     bool
	BlockHeight uint64
	Syncing     bool
	Latency     time.Duration
	LastError   error
	LastCheck   time.Time
}

const (
	scoreWindowSize    = 20   // sliding window: track last N proof fetch results
	scoreSuccessWeight = 0.7  // 70% of score from success rate
	scoreLatencyWeight = 0.3  // 30% of score from latency
	scoreLatencyCap    = 5000 // latency cap in ms (anything above = 0 latency score)
)

type proofResult struct {
	success bool
	latency time.Duration
}

type NodeScore struct {
	results []proofResult
	head    int // next write position (circular buffer)
	count   int // number of results recorded (max scoreWindowSize)
}

func (s *NodeScore) RecordSuccess(latency time.Duration) {
	s.record(proofResult{success: true, latency: latency})
}

func (s *NodeScore) RecordFailure() {
	s.record(proofResult{success: false})
}

func (s *NodeScore) record(r proofResult) {
	if s.results == nil {
		s.results = make([]proofResult, scoreWindowSize)
	}
	s.results[s.head] = r
	s.head = (s.head + 1) % scoreWindowSize
	if s.count < scoreWindowSize {
		s.count++
	}
}

// GetScore returns a composite score in [0, 1]. Higher is better.
// New nodes with no history return 0.5 (neutral).
func (s *NodeScore) GetScore() float64 {
	if s.count == 0 {
		return 0.5 // neutral score for unknown nodes
	}

	var successes int
	var totalLatencyMs float64
	for i := 0; i < s.count; i++ {
		idx := (s.head - s.count + i + scoreWindowSize) % scoreWindowSize
		r := s.results[idx]
		if r.success {
			successes++
			ms := float64(r.latency.Milliseconds())
			if ms > scoreLatencyCap {
				ms = scoreLatencyCap
			}
			totalLatencyMs += ms
		}
	}

	successRate := float64(successes) / float64(s.count)

	latencyScore := 0.0
	if successes > 0 {
		avgLatencyMs := totalLatencyMs / float64(successes)
		// Invert: lower latency = higher score. 0ms → 1.0, scoreLatencyCap ms → 0.0
		latencyScore = 1.0 - (avgLatencyMs / scoreLatencyCap)
		if latencyScore < 0 {
			latencyScore = 0
		}
	}

	return successRate*scoreSuccessWeight + latencyScore*scoreLatencyWeight
}

func (s *NodeScore) GetSuccessRate() float64 {
	if s.count == 0 {
		return 0.5
	}
	var successes int
	for i := 0; i < s.count; i++ {
		idx := (s.head - s.count + i + scoreWindowSize) % scoreWindowSize
		if s.results[idx].success {
			successes++
		}
	}
	return float64(successes) / float64(s.count)
}

type Node struct {
	Config config.NodeConfig
	RPC    *ethclient.Client
	RawRPC *rpc.Client
	Status NodeStatus
	Score  NodeScore
	mu     sync.RWMutex
}

type Manager struct {
	nodes       []*Node
	mu          sync.RWMutex
	checkTicker *time.Ticker
}

func NewManager(cfg []config.NodeConfig) *Manager {
	var nodes []*Node
	for _, nc := range cfg {
		nodes = append(nodes, &Node{
			Config: nc,
		})
	}

	return &Manager{
		nodes: nodes,
	}
}

func (m *Manager) Start(ctx context.Context) {
	m.checkTicker = time.NewTicker(10 * time.Second)

	logger.Info("RPC", "Starting initial check for %d nodes...", len(m.nodes))
	m.checkAll(ctx)

	active := 0
	for _, n := range m.nodes {
		status := "DOWN"
		n.mu.RLock()
		if n.Status.Healthy {
			status = fmt.Sprintf("UP (Height: %d)", n.Status.BlockHeight)
			active++
		}
		n.mu.RUnlock()
		logger.Info("RPC", "Node '%s' : %s", n.Config.Label, status)
	}
	logger.Info("RPC", "Active nodes: %d/%d", active, len(m.nodes))

	go func() {
		for {
			select {
			case <-ctx.Done():
				if m.checkTicker != nil {
					m.checkTicker.Stop()
				}
				return
			case <-m.checkTicker.C:
				m.checkAll(ctx)
			}
		}
	}()
}

func (m *Manager) checkAll(ctx context.Context) {
	var wg sync.WaitGroup
	for _, n := range m.nodes {
		wg.Add(1)
		go func(node *Node) {
			defer wg.Done()
			m.checkNode(ctx, node)
		}(n)
	}
	wg.Wait()
}

func (m *Manager) checkNode(ctx context.Context, n *Node) {
	n.mu.Lock()
	defer n.mu.Unlock()

	start := time.Now()

	if n.RawRPC == nil {
		raw, err := rpc.Dial(n.Config.RPC)
		if err != nil {
			logger.Warn("NODE", "%s connection failed: %s", n.Config.Label, SanitizeForLog(err))
			n.Status.Healthy = false
			n.Status.LastError = err
			n.Status.LastCheck = time.Now()
			return
		}
		n.RawRPC = raw
		n.RPC = ethclient.NewClient(raw)
	}

	ctxWithTimeout, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	_, err := n.RPC.BlockNumber(ctxWithTimeout)
	if err != nil {
		logger.Warn("NODE", "%s check failed: %s", n.Config.Label, SanitizeForLog(err))
		n.Status.Healthy = false
		n.Status.LastError = err
		n.Status.LastCheck = time.Now()

		if n.RawRPC != nil {
			n.RawRPC.Close()
		}
		n.RawRPC = nil
		n.RPC = nil
		return
	}

	syncing, err := n.RPC.SyncProgress(ctxWithTimeout)
	if err != nil {
		n.Status.Healthy = false
		n.Status.LastError = err
		n.Status.LastCheck = time.Now()
		return
	}

	n.Status.Healthy = true
	// NOTE: Height is updated only via WebSocket (ws/listener.go -> UpdateHeight)
	// RPC polling only checks connectivity and sync status.
	// If node has no WS URL configured, height remains 0.
	n.Status.Syncing = (syncing != nil)
	n.Status.Latency = time.Since(start)
	n.Status.LastError = nil
	n.Status.LastCheck = time.Now()
}

func (m *Manager) GetBestNode() *Node {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var candidates []*Node
	for _, n := range m.nodes {
		n.mu.RLock()
		if n.Status.Healthy && !n.Status.Syncing {
			candidates = append(candidates, n)
		}
		n.mu.RUnlock()
	}

	if len(candidates) == 0 {
		for _, n := range m.nodes {
			n.mu.RLock()
			if n.Status.Healthy {
				candidates = append(candidates, n)
			}
			n.mu.RUnlock()
		}
	}

	if len(candidates) == 0 {
		return nil
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Status.BlockHeight != candidates[j].Status.BlockHeight {
			return candidates[i].Status.BlockHeight > candidates[j].Status.BlockHeight
		}
		return candidates[i].Status.Latency < candidates[j].Status.Latency
	})

	return candidates[0]
}

func (m *Manager) GetNodes() []*Node {
	return m.nodes
}

// GetBlockHeight returns the current block height of the node in a thread-safe manner
func (n *Node) GetBlockHeight() uint64 {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.Status.BlockHeight
}

// GetStatus returns a copy of the node status in a thread-safe manner
func (n *Node) GetStatus() NodeStatus {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.Status
}

// UpdateHeight updates the block height of the node in a thread-safe manner
// This is used when receiving blocks via WebSocket for real-time updates
func (n *Node) UpdateHeight(height uint64) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if height > n.Status.BlockHeight {
		n.Status.BlockHeight = height
		n.Status.LastCheck = time.Now()
	}
}

func (n *Node) RecordProofSuccess(latency time.Duration) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.Score.RecordSuccess(latency)
}

func (n *Node) RecordProofFailure() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.Score.RecordFailure()
}

func (n *Node) GetProofScore() float64 {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.Score.GetScore()
}

func (n *Node) GetProofSuccessRate() float64 {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.Score.GetSuccessRate()
}
