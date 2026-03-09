package processor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	gethrpc "github.com/ethereum/go-ethereum/rpc"
	"lecca.io/pharos-watchtower/internal/config"
	watchrpc "lecca.io/pharos-watchtower/internal/rpc"
)

type jsonRPCRequest struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  interface{}     `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

func TestProofNodeCandidates_PrioritizesHealthyFallbacks(t *testing.T) {
	mgr := watchrpc.NewManager([]config.NodeConfig{
		{Label: "primary-behind"},
		{Label: "ready-fast"},
		{Label: "unknown-height"},
		{Label: "ready-syncing"},
		{Label: "unhealthy"},
	})

	nodes := mgr.GetNodes()
	for _, node := range nodes {
		node.RawRPC = &gethrpc.Client{}
	}

	nodes[0].Status = watchrpc.NodeStatus{Healthy: true, BlockHeight: 90, Latency: 20 * time.Millisecond}
	nodes[1].Status = watchrpc.NodeStatus{Healthy: true, BlockHeight: 120, Latency: 5 * time.Millisecond}
	nodes[2].Status = watchrpc.NodeStatus{Healthy: true, BlockHeight: 0, Latency: 10 * time.Millisecond}
	nodes[3].Status = watchrpc.NodeStatus{Healthy: true, BlockHeight: 120, Syncing: true, Latency: 1 * time.Millisecond}
	nodes[4].Status = watchrpc.NodeStatus{Healthy: false, BlockHeight: 999, Latency: 1 * time.Millisecond}

	p := NewProcessor(config.ChainConfig{}, config.AdvancedConfig{}, mgr, nil, nil, nil, nil)
	candidates := p.proofNodeCandidates(100, nodes[0])

	if len(candidates) != 4 {
		t.Fatalf("expected 4 candidates, got %d", len(candidates))
	}

	labels := []string{
		candidates[0].Config.Label,
		candidates[1].Config.Label,
		candidates[2].Config.Label,
		candidates[3].Config.Label,
	}
	// atHeight (height>=100) nodes come first sorted by score, then others
	// All nodes have neutral score (0.5), so atHeight group: ready-fast(120), ready-syncing(120)
	// then non-atHeight: primary-behind(90), unknown-height(0)
	want := []string{"ready-fast", "ready-syncing", "primary-behind", "unknown-height"}
	for i := range want {
		if labels[i] != want[i] {
			t.Fatalf("unexpected candidate order: got %v want %v", labels, want)
		}
	}
}

func TestFetchBlockProofWithRetry_FallsBackToHealthyNode(t *testing.T) {
	var badCalls atomic.Int32
	badServer := newJSONRPCServer(t, func(w http.ResponseWriter, req jsonRPCRequest) {
		badCalls.Add(1)
		writeJSONRPC(w, jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &jsonRPCError{
				Code:    -32000,
				Message: "temporary proof failure",
			},
		})
	})
	defer badServer.Close()

	var goodCalls atomic.Int32
	goodServer := newJSONRPCServer(t, func(w http.ResponseWriter, req jsonRPCRequest) {
		goodCalls.Add(1)
		writeJSONRPC(w, jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: BlockProofResponse{
				BlockNumber:            "0x64",
				BlockProofHash:         "0xabc",
				BlsAggregatedSignature: "0xsig",
				SignedBlsKeys:          []string{"0x01"},
			},
		})
	})
	defer goodServer.Close()

	badClient, err := gethrpc.Dial(badServer.URL)
	if err != nil {
		t.Fatalf("dial bad server: %v", err)
	}
	defer badClient.Close()

	goodClient, err := gethrpc.Dial(goodServer.URL)
	if err != nil {
		t.Fatalf("dial good server: %v", err)
	}
	defer goodClient.Close()

	mgr := watchrpc.NewManager([]config.NodeConfig{
		{Label: "bad", RPC: badServer.URL},
		{Label: "good", RPC: goodServer.URL},
	})
	nodes := mgr.GetNodes()
	nodes[0].RawRPC = badClient
	nodes[0].Status = watchrpc.NodeStatus{Healthy: true, BlockHeight: 100, Latency: 20 * time.Millisecond}
	nodes[1].RawRPC = goodClient
	nodes[1].Status = watchrpc.NodeStatus{Healthy: true, BlockHeight: 100, Latency: 5 * time.Millisecond}

	p := NewProcessor(config.ChainConfig{}, config.AdvancedConfig{}, mgr, nil, nil, nil, nil)
	proof, err := p.fetchBlockProofWithRetry(context.Background(), nodes[0], 100)
	if err != nil {
		t.Fatalf("fetchBlockProofWithRetry returned error: %v", err)
	}
	if proof.BlockNumber != "0x64" {
		t.Fatalf("unexpected proof block number: %s", proof.BlockNumber)
	}
	if badCalls.Load() != 1 {
		t.Fatalf("expected 1 call to bad node, got %d", badCalls.Load())
	}
	if goodCalls.Load() != 1 {
		t.Fatalf("expected 1 call to good node, got %d", goodCalls.Load())
	}
}

func newJSONRPCServer(t *testing.T, handler func(http.ResponseWriter, jsonRPCRequest)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}

		var req jsonRPCRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("unmarshal request: %v", err)
		}
		if req.Method != "debug_getBlockProof" {
			t.Fatalf("unexpected method: %s", req.Method)
		}

		handler(w, req)
	}))
}

func writeJSONRPC(w http.ResponseWriter, resp jsonRPCResponse) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
