package processor

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/core/types"
	"lecca.io/pharos-watchtower/internal/config"
	"lecca.io/pharos-watchtower/internal/logger"
	"lecca.io/pharos-watchtower/internal/metrics"
	"lecca.io/pharos-watchtower/internal/rpc"
	"lecca.io/pharos-watchtower/internal/validators"
)

type BlockProofResponse struct {
	BlockNumber            string   `json:"blockNumber"`
	BlockProofHash         string   `json:"blockProofHash"`
	BlsAggregatedSignature string   `json:"blsAggregatedSignature"`
	SignedBlsKeys          []string `json:"signedBlsKeys"`
}

type StateBroadcaster interface {
	BroadcastUpdate()
	Log(format string, v ...interface{})
}

type Processor struct {
	cfg         config.ChainConfig
	advanced    config.AdvancedConfig
	nodeMgr     *rpc.Manager
	registry    *validators.Registry
	blockCh     <-chan *types.Header
	broadcaster StateBroadcaster
	exporter    *metrics.Exporter
	cacheMu     sync.RWMutex
	blockTime   map[uint64]uint64
	cacheMin    uint64
}

const blockTimeCacheSize = 200

func NewProcessor(cfg config.ChainConfig, advanced config.AdvancedConfig, nodeMgr *rpc.Manager, registry *validators.Registry, blockCh <-chan *types.Header, broadcaster StateBroadcaster, exporter *metrics.Exporter) *Processor {
	return &Processor{
		cfg:         cfg,
		advanced:    advanced,
		nodeMgr:     nodeMgr,
		registry:    registry,
		blockCh:     blockCh,
		broadcaster: broadcaster,
		exporter:    exporter,
		blockTime:   make(map[uint64]uint64),
	}
}

func (p *Processor) Start(ctx context.Context) {
	maxConcurrent := p.advanced.Workers
	if maxConcurrent <= 0 {
		maxConcurrent = 50 // Default: allow up to 50 concurrent proof fetches
	}

	// Semaphore to limit concurrent goroutines
	sem := make(chan struct{}, maxConcurrent)
	var wg sync.WaitGroup

	for {
		select {
		case <-ctx.Done():
			wg.Wait() // Wait for all goroutines to finish
			return
		case header := <-p.blockCh:
			if header == nil {
				continue
			}

			wg.Add(1)
			// Start async processing for each block immediately (no queue)
			go func(header *types.Header) {
				defer wg.Done()

				// Acquire semaphore to limit concurrent RPC calls
				sem <- struct{}{}
				defer func() {
					<-sem // Release semaphore
				}()

				p.processBlock(ctx, header)
			}(header)
		}
	}
}

func (p *Processor) processBlock(ctx context.Context, header *types.Header) {
	var height uint64
	if header.Number != nil {
		height = header.Number.Uint64()
	}
	if height == 0 {
		if node := p.nodeMgr.GetBestNode(); node != nil && node.RPC != nil {
			resolvedHeader, err := node.RPC.HeaderByHash(ctx, header.Hash())
			if err != nil {
				logger.Warn("PROC", "Block header height missing; hash=%s: %v", header.Hash().Hex(), err)
			} else if resolvedHeader != nil && resolvedHeader.Number != nil {
				height = resolvedHeader.Number.Uint64()
			}
		}
	}
	var blockTime time.Time

	// Broadcast update immediately when we start processing a block
	// This ensures node height (updated by WS listener) is reflected immediately
	if p.broadcaster != nil {
		p.broadcaster.BroadcastUpdate()
	}

	node := p.nodeMgr.GetBestNode()
	if node == nil {
		logger.Warn("PROC", "No healthy node to fetch proof for block %d", height)
		return
	}

	// Check if RawRPC is initialized (it can be nil even if node is not nil)
	// This can happen if the node became unhealthy between GetBestNode() and here
	if node.RawRPC == nil {
		logger.Warn("PROC", "Node RPC client not initialized for block %d, skipping", height)
		return
	}

	// Check if node has processed this block yet
	nodeHeight := node.GetBlockHeight()

	// If the block is newer than what the node has, wait a bit more
	if height > nodeHeight {
		// Wait for node to catch up (with timeout)
		maxWait := 10 * time.Second
		waitInterval := 500 * time.Millisecond
		waited := time.Duration(0)
		for height > nodeHeight && waited < maxWait {
			time.Sleep(waitInterval)
			waited += waitInterval
			nodeHeight = node.GetBlockHeight()
		}
	}

	if node != nil && node.RPC != nil && height > 0 {
		var currentTs uint64
		p.cacheMu.RLock()
		currentTs, okCurrent := p.blockTime[height]
		p.cacheMu.RUnlock()
		if !okCurrent {
			var block *types.Block
			var err error
			// Retry: node may return "block is not available" briefly after newHeads (block not yet in BlockByNumber).
			for attempt := 0; attempt < 4; attempt++ {
				if attempt > 0 {
					time.Sleep(200 * time.Millisecond)
				}
				ctxWithTimeout, cancel := context.WithTimeout(ctx, 5*time.Second)
				block, err = node.RPC.BlockByNumber(ctxWithTimeout, new(big.Int).SetUint64(height))
				cancel()
				if err == nil && block != nil {
					break
				}
				if err != nil && !strings.Contains(err.Error(), "block is not available") {
					break
				}
			}
			if err != nil {
				logger.Warn("PROC", "Failed to fetch block for timestamp (height=%d): %v", height, err)
			} else if block != nil {
				currentTs = block.Time()
				p.cacheMu.Lock()
				p.blockTime[height] = currentTs
				if p.cacheMin == 0 || height < p.cacheMin {
					p.cacheMin = height
				}
				for len(p.blockTime) > blockTimeCacheSize {
					delete(p.blockTime, p.cacheMin)
					p.cacheMin++
				}
				p.cacheMu.Unlock()
			}
		}
		if currentTs > 0 {
			blockTime = time.Unix(int64(currentTs), 0)
		}

		if p.exporter != nil {
			prevHeight := height - 1
			if height > 1 {
				prevTs := p.getCachedBlockTime(ctx, node, prevHeight)
				if prevTs > 0 && currentTs > 0 {
					intervalSec := float64(currentTs - prevTs)
					p.exporter.SetLastBlockIntervalSeconds(intervalSec)
				}
			}

			avgHeight := height - 100
			if height > 100 {
				avgTs := p.getCachedBlockTime(ctx, node, avgHeight)
				if avgTs > 0 && currentTs > 0 {
					avgSec := float64(currentTs-avgTs) / 100.0
					p.exporter.SetAvgBlockTime100Seconds(avgSec)
				}
			}
		}
	}
	if blockTime.IsZero() {
		blockTime = time.Now()
	}

	var proof BlockProofResponse
	var err error

	// Retry loop for fetching block proof with exponential backoff
	// The proof might not be available immediately after the block header is received
	// First attempt immediately, then retry with backoff
	maxRetries := 10                       // Reduced from 15, but with smarter retry logic
	initialDelay := 200 * time.Millisecond // Reduced initial delay
	maxDelay := 3 * time.Second            // Reduced max delay

	// First attempt immediately (no delay)
	err = node.RawRPC.CallContext(ctx, &proof, "debug_getBlockProof", fmt.Sprintf("0x%x", height))
	if err == nil {
		if p.broadcaster != nil {
			logger.Info("PROC", "Block #%d | Proof Fetched via %s", height, node.Config.Label)
		}
	} else {
		// Retry with exponential backoff
		for i := 0; i < maxRetries; i++ {
			// Exponential backoff: 0.2s, 0.4s, 0.8s, 1.6s, 3s, 3s, ...
			delay := initialDelay
			if i > 0 {
				delay = time.Duration(1<<uint(i)) * initialDelay
				if delay > maxDelay {
					delay = maxDelay
				}
			}

			time.Sleep(delay)

			// Re-check RawRPC before retry (it might have become nil)
			if node.RawRPC == nil {
				if p.broadcaster != nil {
					logger.Warn("PROC", "Connection lost during retry for block %d", height)
				}
				return
			}

			err = node.RawRPC.CallContext(ctx, &proof, "debug_getBlockProof", fmt.Sprintf("0x%x", height))
			if err == nil {
				if p.broadcaster != nil {
					logger.Info("PROC", "Block #%d | Proof Fetched (Retried %d times) via %s", height, i+1, node.Config.Label)
				}
				break
			}
		}
	}

	if err != nil {
		// More detailed error logging
		errMsg := err.Error()
		if strings.Contains(errMsg, "block is not available") {
			logger.Warn("PROC", "Failed to fetch proof for block %d (0x%x): block proof not yet available (node height: %d)",
				height, height, nodeHeight)
		} else {
			logger.Error("PROC", "Failed to fetch proof for block %d (0x%x): %v (node height: %d)",
				height, height, err, nodeHeight)
		}
		return
	}

	signedSet := make(map[string]bool)
	for _, k := range proof.SignedBlsKeys {
		// Use the same normalization function as registry
		normalizedKey := validators.NormalizeBlsKey(k)
		signedSet[normalizedKey] = true
	}

	allValidators := p.registry.GetValidators()
	var missedValidators []string

	for _, val := range allValidators {
		participated := signedSet[val.Meta.BlsKeyHex]

		val.Window.Add(participated, blockTime, height)

		val.Mu.Lock()
		if participated {
			// Only update LastHeight if this block is higher than the current one
			// This prevents LastHeight from decreasing due to out-of-order async processing
			if height > val.LastHeight {
				val.LastHeight = height
				val.LastSeenAt = time.Now()
			}
			if val.Down {
				val.Down = false
				if p.broadcaster != nil {
					logger.Info("STATE", "Validator '%s' status changed: DOWN -> UP", val.Meta.Description)
				}
			}
		} else {
			missedValidators = append(missedValidators, val.Meta.Description)

			missed, total, _ := val.Window.GetStats()
			// Alert if 100% missed in window and window has enough blocks
			// We use a heuristic: at least 10 blocks to avoid false positives on start
			if total >= 10 && missed == total {
				if !val.Down {
					val.Down = true
					if p.broadcaster != nil {
						logger.Error("STATE", "Validator '%s' status changed: UP -> DOWN (100%% Missed in Window)", val.Meta.Description)
					}
				}
			}
		}
		val.Mu.Unlock()
	}

	if p.broadcaster != nil {
		if len(missedValidators) > 0 {
			logger.Warn("MISS", "Block #%d Missed by %d/%d validators: %s",
				height, len(missedValidators), len(allValidators), strings.Join(missedValidators, ", "))
		}
		p.broadcaster.BroadcastUpdate()
	}

	if p.exporter != nil {
		p.exporter.Update()
	}

}

func (p *Processor) getCachedBlockTime(ctx context.Context, node *rpc.Node, height uint64) uint64 {
	if node == nil || node.RPC == nil || height == 0 {
		return 0
	}

	p.cacheMu.RLock()
	if ts, ok := p.blockTime[height]; ok {
		p.cacheMu.RUnlock()
		return ts
	}
	p.cacheMu.RUnlock()

	ctxWithTimeout, cancel := context.WithTimeout(ctx, 5*time.Second)
	block, err := node.RPC.BlockByNumber(ctxWithTimeout, new(big.Int).SetUint64(height))
	cancel()
	if err != nil {
		// "block is not available" often means pruned block (e.g. height-100) or node lag; log at Debug to reduce noise.
		if strings.Contains(err.Error(), "block is not available") {
			logger.Debug("PROC", "Block not available for timestamp (height=%d): %v", height, err)
		} else {
			logger.Warn("PROC", "Failed to fetch block for timestamp (height=%d): %v", height, err)
		}
		return 0
	}
	if block == nil {
		return 0
	}

	blockTime := block.Time()
	p.cacheMu.Lock()
	p.blockTime[height] = blockTime
	if p.cacheMin == 0 || height < p.cacheMin {
		p.cacheMin = height
	}
	for len(p.blockTime) > blockTimeCacheSize {
		delete(p.blockTime, p.cacheMin)
		p.cacheMin++
	}
	p.cacheMu.Unlock()

	return blockTime
}
