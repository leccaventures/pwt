package main

import (
	"flag"
	"os"
	"path/filepath"

	"lecca.io/pharos-watchtower/internal/config"
)

func parseFlags() (string, string, error) {
	configFile := flag.String("config", "", "path to config file")
	dataDir := flag.String("data-dir", "", "path to data directory")
	flag.Parse()

	configPath, baseDir, err := resolveConfigPath(*configFile)
	if err != nil {
		return "", "", err
	}

	if *dataDir == "" {
		*dataDir = filepath.Join(baseDir, "data")
	}

	return configPath, *dataDir, nil
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

func applyDataDirDefaults(cfg *config.Config, dataDir string) {
	if cfg.Advanced.StateFile == "" {
		cfg.Advanced.StateFile = filepath.Join(dataDir, "alerts-state.json")
	}
}
