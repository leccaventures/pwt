package config

import (
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ============================================================
// MAIN CONFIG
// ============================================================

type Config struct {
	Chain    ChainConfig    `yaml:"chain"`
	Alerts   AlertsConfig   `yaml:"alerts"`
	Advanced AdvancedConfig `yaml:"advanced"`
}

// ============================================================
// CHAIN CONFIG
// ============================================================

type ChainConfig struct {
	ChainID    string       `yaml:"chain_id"`
	Mode       string       `yaml:"mode"`
	Validators []string     `yaml:"validators"`
	Nodes      []NodeConfig `yaml:"nodes"`
}

type NodeConfig struct {
	Label       string `yaml:"label"`
	RPC         string `yaml:"rpc"`
	WS          string `yaml:"ws"`
	AlertOnDown bool   `yaml:"alert_on_down"`
}

// ============================================================
// ALERTS CONFIG
// ============================================================

type AlertsConfig struct {
	Channels AlertChannels `yaml:"channels"`
	Rules    AlertRules    `yaml:"rules"`
}

type AlertChannels struct {
	Discord   DiscordConfig   `yaml:"discord"`
	Telegram  TelegramConfig  `yaml:"telegram"`
	Slack     SlackConfig     `yaml:"slack"`
	PagerDuty PagerDutyConfig `yaml:"pagerduty"`
}

type DiscordConfig struct {
	Enabled bool   `yaml:"enabled"`
	Webhook string `yaml:"webhook"`
}

type TelegramConfig struct {
	Enabled bool   `yaml:"enabled"`
	Token   string `yaml:"token"`
	ChatID  string `yaml:"chat_id"`
}

type SlackConfig struct {
	Enabled bool   `yaml:"enabled"`
	Webhook string `yaml:"webhook"`
}

type PagerDutyConfig struct {
	Enabled  bool   `yaml:"enabled"`
	APIKey   string `yaml:"api_key"`
	Severity string `yaml:"severity"`
}

type AlertRules struct {
	NodeDown        AlertRule       `yaml:"node_down"`
	NodeStall       AlertRule       `yaml:"node_stall"`
	WsDown          AlertRule       `yaml:"ws_down"`
	ValidatorDown   AlertRule       `yaml:"validator_down"`
	ValidatorStall  AlertRule       `yaml:"validator_stall"`
	ValidatorUptime AlertUptimeRule `yaml:"validator_uptime"`
}

type AlertRule struct {
	FireAfter    string `yaml:"fire_after"`
	ResolveAfter string `yaml:"resolve_after"`
}

type AlertUptimeRule struct {
	Threshold string `yaml:"threshold"`
}

// ============================================================
// ADVANCED CONFIG
// ============================================================

type AdvancedConfig struct {
	Window         string           `yaml:"window"`
	ReloadInterval string           `yaml:"reload_interval"`
	RPCTimeout     string           `yaml:"rpc_timeout"`
	WSTimeout      string           `yaml:"ws_timeout"`
	DashboardPort  int              `yaml:"dashboard_port"`
	Prometheus     PrometheusConfig `yaml:"prometheus"`
	HideLogs       bool             `yaml:"hide_logs"`
	StateFile      string           `yaml:"state_file"`
	ContractAddr   string           `yaml:"contract_addr"`
}

type PrometheusConfig struct {
	MetricsPrefix string `yaml:"metrics_prefix"`
	Port          int    `yaml:"port"`
}

// ============================================================
// HELPER FUNCTIONS
// ============================================================

// ParseDuration parses duration strings like "1m", "5m", "30s"
func ParseDuration(s string) time.Duration {
	if s == "" {
		return 0
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0
	}
	return d
}

// ParsePercent parses percent strings like "90%", "60%"
func ParsePercent(s string) int {
	if s == "" {
		return 0
	}
	s = strings.TrimSuffix(s, "%")
	val, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return val
}

// Enabled returns true if the alert rule is enabled (has fire_after set)
func (r AlertRule) Enabled() bool {
	return r.FireAfter != ""
}

// FireDuration returns the fire_after duration
func (r AlertRule) FireDuration() time.Duration {
	return ParseDuration(r.FireAfter)
}

// ResolveDuration returns the resolve_after duration
func (r AlertRule) ResolveDuration() time.Duration {
	return ParseDuration(r.ResolveAfter)
}

// Enabled returns true if the uptime rule is enabled (has threshold set)
func (r AlertUptimeRule) Enabled() bool {
	return r.Threshold != ""
}

// ThresholdPercent returns the threshold as an integer percentage
func (r AlertUptimeRule) ThresholdPercent() int {
	return ParsePercent(r.Threshold)
}

// ============================================================
// LOAD FUNCTION
// ============================================================

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	// Apply defaults for Advanced section
	if cfg.Advanced.Window == "" {
		cfg.Advanced.Window = "60m"
	}
	if cfg.Advanced.ReloadInterval == "" {
		cfg.Advanced.ReloadInterval = "5m"
	}
	if cfg.Advanced.RPCTimeout == "" {
		cfg.Advanced.RPCTimeout = "5s"
	}
	if cfg.Advanced.WSTimeout == "" {
		cfg.Advanced.WSTimeout = "30s"
	}
	if cfg.Advanced.DashboardPort == 0 {
		cfg.Advanced.DashboardPort = 8888
	}
	if cfg.Advanced.Prometheus.Port == 0 {
		cfg.Advanced.Prometheus.Port = 9999
	}
	if cfg.Advanced.Prometheus.MetricsPrefix == "" {
		cfg.Advanced.Prometheus.MetricsPrefix = "pharos"
	}
	if cfg.Advanced.ContractAddr == "" {
		cfg.Advanced.ContractAddr = "0x4100000000000000000000000000000000000000"
	}

	return &cfg, nil
}
