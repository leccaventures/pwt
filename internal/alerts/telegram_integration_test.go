//go:build integration

package alerts

import (
	"context"
	"os"
	"testing"
	"time"

	"lecca.io/pharos-watchtower/internal/config"
)

/*
TestTelegramNotifier_NodeStallEvent is a manual integration test.
It verifies that Telegram notifications are deliverable from the
machine running the tests.

This test is gated behind the `integration` build tag so it is NOT
executed during normal `go test ./...` or CI runs.

Run locally:
  export PWT_TELEGRAM_TOKEN="..."
  export PWT_TELEGRAM_CHAT_ID="..."
  go test -tags=integration -v ./internal/alerts -run TestTelegramNotifier_NodeStallEvent -count=1
*/

func TestTelegramNotifier_NodeStallEvent(t *testing.T) {
	token := os.Getenv("PWT_TELEGRAM_TOKEN")
	chatID := os.Getenv("PWT_TELEGRAM_CHAT_ID")
	if token == "" || chatID == "" {
		t.Skip("set PWT_TELEGRAM_TOKEN and PWT_TELEGRAM_CHAT_ID to run")
	}

	cfg := config.AlertsConfig{}
	cfg.Channels.Telegram.Enabled = true
	cfg.Channels.Telegram.Token = token
	cfg.Channels.Telegram.ChatID = chatID

	n := NewNotifier(cfg)

	event := AlertEvent{
		Key:         "node_stall:node1",
		RuleID:      RuleNodeHeightStalled,
		SubjectType: SubjectNode,
		SubjectID:   "node1",
		SubjectName: "node1",
		ChainID:     "pharos-testnet",
		Status:      AlertFiring,
		Severity:    "warning",
		Title:       "Node Height Stalled (integration test)",
		Message:     "This is a test message to verify Telegram alerts.",
		Timestamp:   time.Now(),
	}

	if err := n.Notify(context.Background(), event); err != nil {
		t.Fatalf("notify failed: %v", err)
	}
}
