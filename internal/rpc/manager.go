package rpc

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
	"lecca.io/pharos-watchtower/internal/config"
	"lecca.io/pharos-watchtower/internal/logger"
)

func sanitizeRPCError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if strings.Contains(msg, "<html") || strings.Contains(msg, "<HTML") {
		// Keep the status line before HTML payload, if present
		if idx := strings.Index(strings.ToLower(msg), "<html"); idx > 0 {
			return strings.TrimSpace(msg[:idx])
		}
		return "HTTP error response"
	}
	return msg
}

type NodeStatus struct {
	Healthy     bool
	BlockHeight uint64
	Syncing     bool
	Latency     time.Duration
	LastError   error
	LastCheck   time.Time
}

type Node struct {
	Config config.NodeConfig
	RPC    *ethclient.Client
	RawRPC *rpc.Client
	Status NodeStatus
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
			logger.Warn("NODE", "%s connection failed: %s", n.Config.Label,  sanitizeRPCError(err))
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
		logger.Warn("NODE", "%s check failed: %s", n.Config.Label,  sanitizeRPCError(err))
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
