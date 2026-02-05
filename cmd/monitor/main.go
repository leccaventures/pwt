package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ethereum/go-ethereum/core/types"
	"lecca.io/pharos-watchtower/internal/alerts"
	"lecca.io/pharos-watchtower/internal/config"
	"lecca.io/pharos-watchtower/internal/dashboard"
	"lecca.io/pharos-watchtower/internal/logger"
	"lecca.io/pharos-watchtower/internal/metrics"
	"lecca.io/pharos-watchtower/internal/processor"
	"lecca.io/pharos-watchtower/internal/rpc"
	"lecca.io/pharos-watchtower/internal/validators"
	"lecca.io/pharos-watchtower/internal/ws"
)

func main() {
	logger.Init()

	configPath, dataDir, err := parseFlags()
	if err != nil {
		if err == flag.ErrHelp {
			os.Exit(0)
		}
		logger.Error("INIT", "Failed to resolve config path: %v", err)
		os.Exit(1)
	}

	logger.Info("INIT", "Loading config from %s...", configPath)
	cfg, err := config.Load(configPath)
	if err != nil {
		logger.Error("INIT", "Failed to load config: %v", err)
		os.Exit(1)
	}
	applyDataDirDefaults(cfg, dataDir)
	logger.Info("INIT", "Config loaded. ChainID: %s, Mode: %s", cfg.Chain.ChainID, cfg.Chain.Mode)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	nodeMgr := rpc.NewManager(cfg.Chain.Nodes)
	registry := validators.NewRegistry(cfg.Chain, cfg.Advanced, nodeMgr)
	blockCh := make(chan *types.Header, 100)

	exporter := metrics.NewExporter(*cfg, registry, nodeMgr)
	exporter.Start(ctx)

	dash := dashboard.NewServer(*cfg, registry, nodeMgr, exporter)
	dash.Start(ctx)

	logger.Info("INIT", "Initializing RPC Node Manager...")
	nodeMgr.Start(ctx)

	logger.Info("INIT", "Initializing Validator Registry...")
	registry.Start(ctx)

	logger.Info("INIT", "Connecting to WebSocket for real-time blocks...")
	listener := ws.NewListener(cfg.Chain, nodeMgr, blockCh, dash)
	listener.Start(ctx)

	logger.Info("INIT", "Starting Block Processor...")
	proc := processor.NewProcessor(cfg.Chain, cfg.Advanced, nodeMgr, registry, blockCh, dash, exporter)
	go proc.Start(ctx)

	alertMgr := alerts.NewManager(cfg.Alerts, cfg.Chain.ChainID, cfg.Advanced.StateFile, cfg.Chain.Validators, registry, nodeMgr)
	alertMgr.Start(ctx)

	logger.Info("SYS", "Pharos Watchtower started... (ChainID: %s)", cfg.Chain.ChainID)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	logger.Info("SYS", "Shutting down gracefully...")
	cancel()

	logger.Info("SYS", "Saving validator state...")
	if err := registry.SaveState(); err != nil {
		logger.Warn("SYS", "Failed to save validator state: %v", err)
	} else {
		logger.Info("SYS", "Validator state saved successfully")
	}
	time.Sleep(1 * time.Second)
	logger.Info("SYS", "Shutdown complete")
}
