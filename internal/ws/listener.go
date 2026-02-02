package ws

import (
	"context"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"lecca.io/pharos-watchtower/internal/config"
	"lecca.io/pharos-watchtower/internal/logger"
	"lecca.io/pharos-watchtower/internal/rpc"
)

type DashboardBroadcaster interface {
	BroadcastUpdate()
}

type Listener struct {
	cfg         config.ChainConfig
	nodeMgr     *rpc.Manager
	blockCh     chan<- *types.Header
	broadcaster DashboardBroadcaster
	mu          sync.Mutex // Mutex to protect concurrent access
	seenBlocks  map[uint64]bool
}

func NewListener(cfg config.ChainConfig, nodeMgr *rpc.Manager, blockCh chan<- *types.Header, broadcaster DashboardBroadcaster) *Listener {
	return &Listener{
		cfg:         cfg,
		nodeMgr:     nodeMgr,
		blockCh:     blockCh,
		broadcaster: broadcaster,
		seenBlocks:  make(map[uint64]bool),
	}
}

func (l *Listener) Start(ctx context.Context) {
	// Start a goroutine for each node to subscribe concurrently
	nodes := l.nodeMgr.GetNodes()
	for _, n := range nodes {
		if n.Config.WS == "" {
			continue
		}
		go l.subscribeNode(ctx, n)
	}

	// Periodic cleanup of seen blocks map
	go l.cleanupSeenBlocks(ctx)
}

func (l *Listener) subscribeNode(ctx context.Context, node *rpc.Node) {
	backoff := 1 * time.Second

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		client, err := ethclient.DialContext(ctx, node.Config.WS)
		if err != nil {
			logger.Warn("WS", "Connection failed to %s: %v. Retrying in %v", node.Config.Label, err, backoff)
			time.Sleep(backoff)
			if backoff < 60*time.Second {
				backoff *= 2
			}
			continue
		}

		headers := make(chan *types.Header)
		sub, err := client.SubscribeNewHead(ctx, headers)
		if err != nil {
			client.Close()
			logger.Warn("WS", "Subscribe failed for %s: %v", node.Config.Label, err)
			time.Sleep(backoff)
			continue
		}

		backoff = 1 * time.Second
		logger.Info("WS", "Subscribed to newHeads via %s", node.Config.Label)

	SubscribeLoop:
		for {
			select {
			case <-ctx.Done():
				sub.Unsubscribe()
				client.Close()
				return
			case err := <-sub.Err():
				logger.Warn("WS", "Subscription error from %s: %v", node.Config.Label, err)
				sub.Unsubscribe()
				client.Close()
				break SubscribeLoop
			case header := <-headers:
				// Update node height
				node.UpdateHeight(header.Number.Uint64())

				// Dedup logic: Check if we've already seen this block
				l.mu.Lock()
				seen := l.seenBlocks[header.Number.Uint64()]
				if !seen {
					l.seenBlocks[header.Number.Uint64()] = true
					l.mu.Unlock()

					// Send block to processor (only once per height)
					select {
					case l.blockCh <- header:
					case <-ctx.Done():
						sub.Unsubscribe()
						client.Close()
						return
					}
				} else {
					l.mu.Unlock()
				}
			}
		}
		time.Sleep(1 * time.Second)
	}
}

func (l *Listener) cleanupSeenBlocks(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			l.mu.Lock()
			// Keep only recent blocks (simple heuristic)
			// In a real production system, you'd use a more sophisticated cache or sliding window
			if len(l.seenBlocks) > 1000 {
				// Reset map if it gets too big (simplest cleanup)
				// A better approach would be to remove keys < current_height - 1000
				// But since we don't track current height globally here easily, reset is safe enough
				// for a monitoring tool.
				l.seenBlocks = make(map[uint64]bool)
			}
			l.mu.Unlock()
		}
	}
}
