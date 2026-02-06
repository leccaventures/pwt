package alerts

import (
	"context"
	"testing"
	"time"
)

/*
TestNodeHeightStalled_FireOnce_ThenResolve validates the core behavior:

 1) First observation sets baseline (no alert).
 2) No alert before fire_after threshold.
 3) Alert fires once after fire_after.
 4) No re-fire on subsequent checks while still stalled.
 5) When height advances, resolved alert is sent and state clears.
*/

type captureNotifier struct {
	events []AlertEvent
}

func (c *captureNotifier) Notify(_ context.Context, e AlertEvent) error {
	c.events = append(c.events, e)
	return nil
}

func TestNodeHeightStalled_FireOnce_ThenResolve(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 2, 6, 0, 0, 0, 0, time.UTC)

	n := &captureNotifier{}
	m := &Manager{
		chainID:    "pharos-testnet",
		notifier:   n,
		alerts:     make(map[string]AlertStateItem),
		nodeHeight: make(map[string]nodeHeightTrack),
	}

	fireAfter := 30 * time.Second

	node := nodeStallSnapshot{
		Label:       "node-1",
		RPC:         "http://127.0.0.1:18100",
		AlertOnDown: true,
		Healthy:     true,
		Syncing:     false,
		Height:      100,
	}

	// 1) First observation -> baseline set, no alert
	m.checkNodeHeightStalledWithSnapshots(ctx, now, fireAfter, []nodeStallSnapshot{node})
	if len(n.events) != 0 {
		t.Fatalf("expected 0 events, got %d", len(n.events))
	}

	// 2) Still same height before threshold -> no alert
	m.checkNodeHeightStalledWithSnapshots(ctx, now.Add(20*time.Second), fireAfter, []nodeStallSnapshot{node})
	if len(n.events) != 0 {
		t.Fatalf("expected 0 events, got %d", len(n.events))
	}

	// 3) Past threshold -> should fire once
	m.checkNodeHeightStalledWithSnapshots(ctx, now.Add(31*time.Second), fireAfter, []nodeStallSnapshot{node})
	if len(n.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(n.events))
	}
	if n.events[0].RuleID != RuleNodeHeightStalled || n.events[0].Status != AlertFiring {
		t.Fatalf("expected firing RuleNodeHeightStalled, got rule=%s status=%s", n.events[0].RuleID, n.events[0].Status)
	}

	// 4) Keep stalled longer -> should NOT re-fire
	m.checkNodeHeightStalledWithSnapshots(ctx, now.Add(60*time.Second), fireAfter, []nodeStallSnapshot{node})
	if len(n.events) != 1 {
		t.Fatalf("expected still 1 event (no re-fire), got %d", len(n.events))
	}

	// 5) Height advances -> should resolve (second event)
	node.Height = 101
	m.checkNodeHeightStalledWithSnapshots(ctx, now.Add(61*time.Second), fireAfter, []nodeStallSnapshot{node})
	if len(n.events) != 2 {
		t.Fatalf("expected 2 events (firing + resolved), got %d", len(n.events))
	}
	if n.events[1].RuleID != RuleNodeHeightStalled || n.events[1].Status != AlertResolved {
		t.Fatalf("expected resolved RuleNodeHeightStalled, got rule=%s status=%s", n.events[1].RuleID, n.events[1].Status)
	}
}

func TestNodeHeightStalled_SkipsWhenSyncingOrUnhealthy(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 2, 6, 0, 0, 0, 0, time.UTC)

	n := &captureNotifier{}
	m := &Manager{
		chainID:    "test-chain",
		notifier:   n,
		alerts:     make(map[string]AlertStateItem),
		nodeHeight: make(map[string]nodeHeightTrack),
	}

	fireAfter := 10 * time.Second

	base := nodeStallSnapshot{
		Label:       "node-1",
		RPC:         "http://127.0.0.1:18100",
		AlertOnDown: true,
		Healthy:     true,
		Syncing:     false,
		Height:      100,
	}

	// baseline
	m.checkNodeHeightStalledWithSnapshots(ctx, now, fireAfter, []nodeStallSnapshot{base})

	// becomes syncing -> should skip (and baseline reset)
	syncing := base
	syncing.Syncing = true
	m.checkNodeHeightStalledWithSnapshots(ctx, now.Add(20*time.Second), fireAfter, []nodeStallSnapshot{syncing})

	// even if enough time passes, still syncing => no firing
	m.checkNodeHeightStalledWithSnapshots(ctx, now.Add(40*time.Second), fireAfter, []nodeStallSnapshot{syncing})
	if len(n.events) != 0 {
		t.Fatalf("expected 0 events while syncing, got %d", len(n.events))
	}

	// unhealthy -> should skip
	unhealthy := base
	unhealthy.Healthy = false
	m.checkNodeHeightStalledWithSnapshots(ctx, now.Add(60*time.Second), fireAfter, []nodeStallSnapshot{unhealthy})
	if len(n.events) != 0 {
		t.Fatalf("expected 0 events while unhealthy, got %d", len(n.events))
	}
}
