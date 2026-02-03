package main

import (
	"context"
	_ "embed"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
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

//go:embed config.example.yml
var configExample []byte

func main() {
	logger.Init()

	configFile := flag.String("config", "", "path to config file")
	dataDir := flag.String("data-dir", "", "path to data directory")
	flag.Parse()

	configPath, baseDir, err := resolveConfigPath(*configFile)
	if err != nil {
		logger.Error("INIT", "Failed to resolve config path: %v", err)
		os.Exit(1)
	}

	if err := ensureDefaultConfig(configPath, configExample); err != nil {
		logger.Error("INIT", "Failed to ensure default config: %v", err)
		os.Exit(1)
	}

	if *dataDir == "" {
		*dataDir = filepath.Join(baseDir, "data")
	}

	logger.Info("INIT", "Loading config from %s...", configPath)
	cfg, err := config.Load(configPath)
	if err != nil {
		logger.Error("INIT", "Failed to load config: %v", err)
		os.Exit(1)
	}
	applyDataDirDefaults(cfg, *dataDir)
	logger.Info("INIT", "Config loaded. ChainID: %s, Mode: %s", cfg.Chain.ChainID, cfg.Chain.Mode)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	logger.Info("INIT", "Initializing RPC Node Manager...")
	nodeMgr := rpc.NewManager(cfg.Chain.Nodes)
	nodeMgr.Start(ctx)

	logger.Info("INIT", "Initializing Validator Registry...")
	registry := validators.NewRegistry(cfg.Chain, cfg.Advanced, nodeMgr)
	registry.Start(ctx)

	blockCh := make(chan *types.Header, 100)
	dash := dashboard.NewServer(*cfg, registry, nodeMgr)
	dash.Start(ctx)

	logger.Info("INIT", "Connecting to WebSocket for real-time blocks...")
	listener := ws.NewListener(cfg.Chain, nodeMgr, blockCh, dash)
	listener.Start(ctx)

	exporter := metrics.NewExporter(*cfg, registry, nodeMgr)
	exporter.Start(ctx)

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
	logger.Info("SYS", "Saving alert state...")
	if err := alertMgr.SaveState(); err != nil {
		logger.Warn("SYS", "Failed to save alert state: %v", err)
	} else {
		logger.Info("SYS", "Alert state saved successfully")
	}

	time.Sleep(1 * time.Second)
	logger.Info("SYS", "Shutdown complete")
}

func resolveConfigPath(configFile string) (string, string, error) {
	if configFile != "" {
		abs, err := filepath.Abs(configFile)
		if err != nil {
			return "", "", err
		}
		return abs, filepath.Dir(abs), nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", err
	}
	baseDir := filepath.Join(home, ".pwt")
	return filepath.Join(baseDir, "config.yml"), baseDir, nil
}

func ensureDefaultConfig(path string, example []byte) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	if len(example) == 0 {
		return fmt.Errorf("embedded config.example.yml is empty")
	}

	return os.WriteFile(path, example, 0o644)
}

func applyDataDirDefaults(cfg *config.Config, dataDir string) {
	if cfg.Advanced.StateFile == "" {
		cfg.Advanced.StateFile = filepath.Join(dataDir, "alerts-state.json")
	}
}
