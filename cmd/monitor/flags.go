package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"lecca.io/pharos-watchtower/internal/config"
)

func parseFlags() (string, string, error) {
	fs := flag.NewFlagSet(os.Args[0], flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configFile := fs.String("config", "", "path to config file")
	dataDir := fs.String("data-dir", "", "path to data directory")
	usage := func() {
		defaultConfig, defaultData := defaultPaths()
		fmt.Fprintln(os.Stderr, "Usage: pharos-watchtower start [flags]\n")
		fmt.Fprintf(os.Stderr, "Defaults:\n  config: %s\n  data-dir: %s\n\n", defaultConfig, defaultData)
		fs.PrintDefaults()
	}

	if len(os.Args) < 2 || os.Args[1] != "start" {
		usage()
		return "", "", flag.ErrHelp
	}

	if err := fs.Parse(os.Args[2:]); err != nil {
		usage()
		return "", "", err
	}

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

func defaultPaths() (string, string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", ""
	}
	baseDir := filepath.Join(home, ".pwt")
	return filepath.Join(baseDir, "config.yml"), filepath.Join(baseDir, "data")
}

func applyDataDirDefaults(cfg *config.Config, dataDir string) {
	if cfg.Advanced.StateFile == "" {
		cfg.Advanced.StateFile = filepath.Join(dataDir, "alerts-state.json")
	}
}
