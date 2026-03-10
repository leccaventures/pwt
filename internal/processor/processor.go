package processor

import (
	"context"
	"fmt"
	"math/big"
	"sort"
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

const (
	blockTimeCacheSize    = 200
	maxNodeCatchUpWait    = 10 * time.Second
	nodeCatchUpInterval   = 500 * time.Millisecond
	blockFetchMaxAttempts = 4
	blockFetchRetryDelay  = 500 * time.Millisecond // block fetch failed, retry before delay
	proofFetchMaxRetries  = 10
	proofInitialDelay     = 200 * time.Millisecond
	proofMaxDelay         = 3 * time.Second
	minBlocksForDownAlert = 10
)

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

func (p *Processor) selectProofNode(height uint64) *rpc.Node {
	nodes := p.nodeMgr.GetNodes()
	if len(nodes) == 0 {
		return nil
	}

	type candidate struct {
		node  *rpc.Node
		score float64
	}

	var atHeight []candidate
	var healthy []candidate

	for _, n := range nodes {
		status := n.GetStatus()
		if !status.Healthy || n.RawRPC == nil {
			continue
		}
		score := n.GetProofScore()
		if status.BlockHeight >= height && height > 0 {
			atHeight = append(atHeight, candidate{node: n, score: score})
		} else {
			healthy = append(healthy, candidate{node: n, score: score})
		}
	}

	sortByScore := func(c []candidate) {
		sort.Slice(c, func(i, j int) bool {
			return c[i].score > c[j].score
		})
	}

	if len(atHeight) > 0 {
		sortByScore(atHeight)
		return atHeight[0].node
	}
	if len(healthy) > 0 {
		sortByScore(healthy)
		return healthy[0].node
	}
	return nil
}

// resolveBlockHeight returns block height from header; if height is 0, resolves via HeaderByHash.
func (p *Processor) resolveBlockHeight(ctx context.Context, header *types.Header) uint64 {
	height := uint64(0)
	if header.Number != nil {
		height = header.Number.Uint64()
	}
	if height != 0 {
		return height
	}
	node := p.nodeMgr.GetBestNode()
	if node == nil || node.RPC == nil {
		return 0
	}
	resolvedHeader, err := node.RPC.HeaderByHash(ctx, header.Hash())
	if err != nil {
		logger.Warn("PROC", "Block header height missing; hash=%s: %v", header.Hash().Hex(), err)
		return 0
	}
	if resolvedHeader != nil && resolvedHeader.Number != nil {
		height = resolvedHeader.Number.Uint64()
	}
	return height
}

// ensureProofNode selects a proof node, ensures RawRPC is set, and waits for node to have the block. Returns (node, nodeHeight, true) or (nil, 0, false).
func (p *Processor) ensureProofNode(ctx context.Context, height uint64) (node *rpc.Node, nodeHeight uint64, ok bool) {
	node = p.selectProofNode(height)
	if node == nil {
		logger.Warn("PROC", "No healthy node to fetch proof for block %d", height)
		return nil, 0, false
	}
	if node.RawRPC == nil {
		logger.Warn("PROC", "Node RPC client not initialized for block %d, skipping", height)
		return nil, 0, false
	}

	nodeHeight = node.GetBlockHeight()
	if height <= nodeHeight {
		return node, nodeHeight, true
	}

	waited := time.Duration(0)
	for height > nodeHeight && waited < maxNodeCatchUpWait {
		time.Sleep(nodeCatchUpInterval)
		waited += nodeCatchUpInterval
		nodeHeight = node.GetBlockHeight()
	}
	return node, nodeHeight, true
}

// getBlockTimeAndUpdateMetrics returns block time for height (from cache or RPC) and updates exporter metrics. currentTs may be 0.
func (p *Processor) getBlockTimeAndUpdateMetrics(ctx context.Context, node *rpc.Node, height, nodeHeight uint64) (blockTime time.Time, currentTs uint64) {
	canFetchBlockTime := node != nil && node.RPC != nil && height > 0 && height <= nodeHeight
	if !canFetchBlockTime {
		return time.Time{}, 0
	}
	p.cacheMu.RLock()
	currentTs, okCurrent := p.blockTime[height]
	p.cacheMu.RUnlock()
	if !okCurrent {
		currentTs = p.fetchAndCacheBlockTime(ctx, node, height)
	}
	if currentTs > 0 {
		blockTime = time.Unix(int64(currentTs), 0)
	}
	if p.exporter != nil && currentTs > 0 {
		p.updateExporterBlockMetrics(ctx, node, height, currentTs)
	}
	return blockTime, currentTs
}

func (p *Processor) fetchAndCacheBlockTime(ctx context.Context, node *rpc.Node, height uint64) uint64 {
	var currentTs uint64
	var lastErr error
	for attempt := 0; attempt < blockFetchMaxAttempts; attempt++ {
		if attempt > 0 {
			time.Sleep(blockFetchRetryDelay)
		}
		block, err := node.RPC.BlockByNumber(ctx, new(big.Int).SetUint64(height))
		if err == nil && block != nil {
			currentTs = block.Time()
			break
		}
		lastErr = err
		// "block is not available"(geth) / "not found" (etc node) → retry, block not synchronized
		if err != nil && !strings.Contains(err.Error(), "block is not available") && !strings.Contains(err.Error(), "not found") {
			break
		}
	}
	if currentTs == 0 && lastErr != nil {
		logger.Warn("PROC", "Failed to fetch block for timestamp (height=%d): %v", height, lastErr)
	}
	if currentTs == 0 {
		return 0
	}
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
	return currentTs
}

func proofRetryDelay(retry int) time.Duration {
	delay := time.Duration(1<<uint(retry)) * proofInitialDelay
	if delay > proofMaxDelay {
		return proofMaxDelay
	}
	return delay
}

func (p *Processor) proofNodeCandidates(height uint64, primary *rpc.Node) []*rpc.Node {
	nodes := p.nodeMgr.GetNodes()
	if len(nodes) == 0 {
		return nil
	}

	type scored struct {
		node     *rpc.Node
		score    float64
		atHeight bool
	}

	seen := make(map[*rpc.Node]struct{}, len(nodes))
	candidates := make([]scored, 0, len(nodes))

	addNode := func(node *rpc.Node) {
		if node == nil || node.RawRPC == nil {
			return
		}
		if _, ok := seen[node]; ok {
			return
		}
		status := node.GetStatus()
		if !status.Healthy {
			return
		}
		seen[node] = struct{}{}
		candidates = append(candidates, scored{
			node:     node,
			score:    node.GetProofScore(),
			atHeight: status.BlockHeight >= height && height > 0,
		})
	}

	addNode(primary)
	for _, node := range nodes {
		addNode(node)
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].atHeight != candidates[j].atHeight {
			return candidates[i].atHeight
		}
		return candidates[i].score > candidates[j].score
	})

	result := make([]*rpc.Node, len(candidates))
	for i, c := range candidates {
		result[i] = c.node
	}
	return result
}

func (p *Processor) updateExporterBlockMetrics(ctx context.Context, node *rpc.Node, height, currentTs uint64) {
	if height > 1 {
		prevTs := p.getCachedBlockTime(ctx, node, height-1)
		if prevTs > 0 && currentTs > 0 {
			p.exporter.SetLastBlockIntervalSeconds(float64(currentTs - prevTs))
		}
	}
	if height > 100 {
		avgTs := p.getCachedBlockTime(ctx, node, height-100)
		if avgTs > 0 && currentTs > 0 {
			p.exporter.SetAvgBlockTime100Seconds(float64(currentTs-avgTs) / 100.0)
		}
	}
}

// fetchBlockProofWithRetry fetches block proof with exponential backoff. First attempt is immediate.
// Failures are deferred — only recorded once per node when the retry loop ends, preventing
// retry-induced score poisoning on nodes that eventually succeed.
func (p *Processor) fetchBlockProofWithRetry(ctx context.Context, node *rpc.Node, height uint64) (BlockProofResponse, error) {
	var lastErr error
	totalAttempts := proofFetchMaxRetries + 1
	failedNodes := make(map[*rpc.Node]struct{})

	for attempt := 0; attempt < totalAttempts; attempt++ {
		if attempt > 0 {
			time.Sleep(proofRetryDelay(attempt - 1))
		}

		candidates := p.proofNodeCandidates(height, node)
		if len(candidates) == 0 {
			if p.broadcaster != nil {
				logger.Warn("PROC", "No healthy proof node available for block %d", height)
			}
			if lastErr != nil {
				return BlockProofResponse{}, lastErr
			}
			return BlockProofResponse{}, fmt.Errorf("no healthy proof node available")
		}

		candidate := candidates[attempt%len(candidates)]
		var proof BlockProofResponse
		fetchStart := time.Now()
		err := candidate.RawRPC.CallContext(ctx, &proof, "debug_getBlockProof", fmt.Sprintf("0x%x", height))
		if err == nil {
			candidate.RecordProofSuccess(time.Since(fetchStart))
			delete(failedNodes, candidate)
			for fn := range failedNodes {
				fn.RecordProofFailure()
			}
			if p.broadcaster != nil {
				if attempt == 0 {
					logger.Info("PROC", "Block #%d | Proof Fetched via %s", height, candidate.Config.Label)
				} else {
					logger.Info("PROC", "Block #%d | Proof Fetched (Retried %d times) via %s", height, attempt, candidate.Config.Label)
				}
			}
			return proof, nil
		}

		failedNodes[candidate] = struct{}{}
		lastErr = err
		if ctx.Err() != nil {
			return BlockProofResponse{}, ctx.Err()
		}
	}

	for fn := range failedNodes {
		fn.RecordProofFailure()
	}
	return BlockProofResponse{}, lastErr
}

func buildSignedSet(proof BlockProofResponse) map[string]bool {
	signedSet := make(map[string]bool)
	for _, k := range proof.SignedBlsKeys {
		signedSet[validators.NormalizeBlsKey(k)] = true
	}
	return signedSet
}

// updateValidatorsFromProof updates each validator's window and down state; returns descriptions of validators who missed the block.
func (p *Processor) updateValidatorsFromProof(height uint64, blockTime time.Time, signedSet map[string]bool) []string {
	allValidators := p.registry.GetValidators()
	var missedValidators []string
	for _, val := range allValidators {
		participated := signedSet[val.Meta.BlsKeyHex]
		val.Window.Add(participated, blockTime, height)
		val.Mu.Lock()
		if participated {
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
			if total >= minBlocksForDownAlert && missed == total {
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
	return missedValidators
}

func (p *Processor) Start(ctx context.Context) {
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
				p.processBlock(ctx, header)
			}(header)
		}
	}
}

func (p *Processor) processBlock(ctx context.Context, header *types.Header) {
	height := p.resolveBlockHeight(ctx, header)

	if p.broadcaster != nil {
		p.broadcaster.BroadcastUpdate()
	}

	node, nodeHeight, ok := p.ensureProofNode(ctx, height)
	if !ok || node == nil {
		return
	}

	blockTime, _ := p.getBlockTimeAndUpdateMetrics(ctx, node, height, nodeHeight)
	if blockTime.IsZero() {
		blockTime = time.Now()
	}

	proof, err := p.fetchBlockProofWithRetry(ctx, node, height)
	if err != nil {
		p.logProofFetchError(height, nodeHeight, err)
		return
	}

	signedSet := buildSignedSet(proof)
	missedValidators := p.updateValidatorsFromProof(height, blockTime, signedSet)

	if p.broadcaster != nil {
		if len(missedValidators) > 0 {
			allValidators := p.registry.GetValidators()
			logger.Warn("MISS", "Block #%d Missed by %d/%d validators: %s",
				height, len(missedValidators), len(allValidators), strings.Join(missedValidators, ", "))
		}
		p.broadcaster.BroadcastUpdate()
	}

	if p.exporter != nil {
		p.exporter.Update()
	}
}

func (p *Processor) logProofFetchError(height, nodeHeight uint64, err error) {
	if strings.Contains(err.Error(), "block is not available") {
		logger.Warn("PROC", "Block proof not yet available (height: %d)", height)
	} else {
		logger.Error("PROC", "Block proof not available (height: %d)", height)
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

	var blockTime uint64
	for attempt := 0; attempt < blockFetchMaxAttempts; attempt++ {
		if attempt > 0 {
			time.Sleep(blockFetchRetryDelay)
		}
		ctxWithTimeout, cancel := context.WithTimeout(ctx, 10*time.Second)
		block, err := node.RPC.BlockByNumber(ctxWithTimeout, new(big.Int).SetUint64(height))
		cancel()
		if err == nil && block != nil {
			blockTime = block.Time()
			break
		}
		if err != nil {
			if attempt == blockFetchMaxAttempts-1 {
				if strings.Contains(err.Error(), "block is not available") || strings.Contains(err.Error(), "not found") {
					logger.Debug("PROC", "Block not available for timestamp (height=%d): %v", height, err)
				} else {
					logger.Warn("PROC", "Failed to fetch block for timestamp (height=%d): %v", height, err)
				}
			}
		}
		// "block is not available"(geth) / "not found" (etc node) → retry, other errors return immediately
		if err != nil && !strings.Contains(err.Error(), "block is not available") && !strings.Contains(err.Error(), "not found") {
			return 0
		}
	}
	if blockTime == 0 {
		return 0
	}

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
