package metrics

import (
	"context"
	"math/big"

	"github.com/prometheus/client_golang/prometheus"
	"lecca.io/pharos-watchtower/internal/config"
	"lecca.io/pharos-watchtower/internal/rpc"
	"lecca.io/pharos-watchtower/internal/validators"
)

type Exporter struct {
	cfg           config.ChainConfig
	metricsPrefix string
	registry      *validators.Registry
	nodeMgr       *rpc.Manager
	blockTime     *prometheus.GaugeVec
	avgBlock100   *prometheus.GaugeVec
	uptime        *prometheus.GaugeVec
	missed        *prometheus.GaugeVec
	total         *prometheus.GaugeVec
	down          *prometheus.GaugeVec
	staking       *prometheus.GaugeVec
	height        *prometheus.GaugeVec
	status        *prometheus.GaugeVec
	lastSeen      *prometheus.GaugeVec
	lastSigned    *prometheus.GaugeVec
	lastMissed    *prometheus.GaugeVec
	lastSignedH   *prometheus.GaugeVec
	lastMissedH   *prometheus.GaugeVec
	nodeHeight    *prometheus.GaugeVec
	nodeUp        *prometheus.GaugeVec
	nodeSyncing   *prometheus.GaugeVec
	nodeLastCheck *prometheus.GaugeVec
}

func NewExporter(cfg config.ChainConfig, reg *validators.Registry, nodeMgr *rpc.Manager) *Exporter {
	prefix := cfg.MetricsPrefix
	if prefix == "" {
		prefix = "pharos"
	}

	e := &Exporter{
		cfg:           cfg,
		metricsPrefix: prefix,
		registry:      reg,
		nodeMgr:       nodeMgr,
		blockTime: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: prefix + "_block_time_seconds",
			Help: "Last block interval in seconds",
		}, []string{"chain_id"}),
		avgBlock100: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: prefix + "_block_time_avg_100_seconds",
			Help: "Average block time over last 100 blocks in seconds",
		}, []string{"chain_id"}),
		uptime: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: prefix + "_validator_uptime_ratio",
			Help: "Validator uptime ratio (0-1)",
		}, []string{"chain_id", "validator", "moniker"}),
		missed: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: prefix + "_validator_missed_blocks_total",
			Help: "Total missed blocks in current window",
		}, []string{"chain_id", "validator", "moniker"}),
		total: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: prefix + "_validator_total_blocks_window",
			Help: "Total blocks in current window",
		}, []string{"chain_id", "validator", "moniker"}),
		down: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: prefix + "_validator_down",
			Help: "Validator down status (1=down, 0=up)",
		}, []string{"chain_id", "validator", "moniker"}),
		staking: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: prefix + "_validator_staking",
			Help: "Validator staking amount",
		}, []string{"chain_id", "validator", "moniker"}),
		height: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: prefix + "_validator_last_height",
			Help: "Last block height where validator participated",
		}, []string{"chain_id", "validator", "moniker"}),
		status: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: prefix + "_validator_status",
			Help: "Validator status code",
		}, []string{"chain_id", "validator", "moniker"}),
		lastSeen: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: prefix + "_validator_last_seen_timestamp",
			Help: "Unix timestamp when validator last signed",
		}, []string{"chain_id", "validator", "moniker"}),
		lastSigned: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: prefix + "_validator_last_signed",
			Help: "Whether validator signed the last block (1=yes, 0=no)",
		}, []string{"chain_id", "validator", "moniker"}),
		lastMissed: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: prefix + "_validator_last_missed",
			Help: "Whether validator missed the last block (1=yes, 0=no)",
		}, []string{"chain_id", "validator", "moniker"}),
		lastSignedH: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: prefix + "_validator_last_signed_block",
			Help: "Last block height where validator signed",
		}, []string{"chain_id", "validator", "moniker"}),
		lastMissedH: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: prefix + "_validator_last_missed_block",
			Help: "Last block height where validator missed",
		}, []string{"chain_id", "validator", "moniker"}),
		nodeHeight: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: prefix + "_node_height",
			Help: "Current block height of the node",
		}, []string{"chain_id", "label", "rpc_url"}),
		nodeUp: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: prefix + "_node_up",
			Help: "Node up status (1=up, 0=down)",
		}, []string{"chain_id", "label", "rpc_url"}),
		// TODO: This part is not certain yet.
		nodeSyncing: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: prefix + "_node_syncing",
			Help: "Node syncing status (1=syncing, 0=synced)",
		}, []string{"chain_id", "label", "rpc_url"}),
		nodeLastCheck: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: prefix + "_node_last_check_timestamp",
			Help: "Unix timestamp of last node check",
		}, []string{"chain_id", "label", "rpc_url"}),
	}

	prometheus.MustRegister(e.uptime)
	prometheus.MustRegister(e.missed)
	prometheus.MustRegister(e.total)
	prometheus.MustRegister(e.down)
	prometheus.MustRegister(e.staking)
	prometheus.MustRegister(e.height)
	prometheus.MustRegister(e.status)
	prometheus.MustRegister(e.lastSeen)
	prometheus.MustRegister(e.lastSigned)
	prometheus.MustRegister(e.lastMissed)
	prometheus.MustRegister(e.lastSignedH)
	prometheus.MustRegister(e.lastMissedH)
	prometheus.MustRegister(e.nodeHeight)
	prometheus.MustRegister(e.nodeUp)
	prometheus.MustRegister(e.nodeSyncing)
	prometheus.MustRegister(e.nodeLastCheck)
	prometheus.MustRegister(e.blockTime)
	prometheus.MustRegister(e.avgBlock100)

	return e
}

func (e *Exporter) Start(ctx context.Context) {}

func (e *Exporter) Update() {
	e.update()
}

func (e *Exporter) update() {
	vals := e.registry.GetValidators()
	chainID := e.cfg.ChainID

	// Update block time metrics using the first validator's window as a network proxy
	blockTimeSec := 0.0
	avgBlock100Sec := 0.0
	for _, v := range vals {
		lastInterval := v.Window.GetLastBlockInterval()
		if lastInterval > 0 {
			blockTimeSec = lastInterval.Seconds()
		}
		avgInterval := v.Window.GetAvgBlockTimeLastN(100)
		if avgInterval > 0 {
			avgBlock100Sec = avgInterval.Seconds()
		}
		break
	}

	e.blockTime.With(prometheus.Labels{"chain_id": chainID}).Set(blockTimeSec)
	e.avgBlock100.With(prometheus.Labels{"chain_id": chainID}).Set(avgBlock100Sec)

	for _, v := range vals {
		missed, total, ratio := v.Window.GetStats()

		uptime := 0.0
		if total > 0 {
			uptime = 1.0 - ratio
		}

		labels := prometheus.Labels{
			"chain_id":  chainID,
			"validator": v.Meta.ValidatorID,
			"moniker":   v.Meta.Description,
		}

		e.uptime.With(labels).Set(uptime)
		e.missed.With(labels).Set(float64(missed))
		e.total.With(labels).Set(float64(total))

		downVal := 0.0
		if v.Down {
			downVal = 1.0
		}
		e.down.With(labels).Set(downVal)

		// Staking amount (convert Wei to Ether)
		stakingVal := 0.0
		if v.Meta.Staking != nil {
			// Use big.Int division to match dashboard logic (truncate decimals)
			// 1 Ether = 10^18 Wei
			etherDivisor := new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)
			ether := new(big.Int).Div(v.Meta.Staking, etherDivisor)

			// Convert to float64
			fEther := new(big.Float).SetInt(ether)
			stakingVal, _ = fEther.Float64()
		}
		e.staking.With(labels).Set(stakingVal)

		// Last height + last seen
		v.Mu.RLock()
		lastHeight := v.LastHeight
		lastSeen := v.LastSeenAt
		v.Mu.RUnlock()
		e.height.With(labels).Set(float64(lastHeight))

		// Status code
		e.status.With(labels).Set(float64(v.Meta.Status))

		// Last seen timestamp
		if !lastSeen.IsZero() {
			e.lastSeen.With(labels).Set(float64(lastSeen.Unix()))
		} else {
			e.lastSeen.With(labels).Set(0)
		}

		// Last signed/missed info
		participated, _, _, ok := v.Window.GetLastParticipation()
		if ok {
			if participated {
				e.lastSigned.With(labels).Set(1)
				e.lastMissed.With(labels).Set(0)
			} else {
				e.lastSigned.With(labels).Set(0)
				e.lastMissed.With(labels).Set(1)
			}
		} else {
			e.lastSigned.With(labels).Set(0)
			e.lastMissed.With(labels).Set(0)
		}

		lastSignedH, hasSigned, lastMissedH, hasMissed := v.Window.GetLastSignedMissedHeights()
		if hasSigned {
			e.lastSignedH.With(labels).Set(float64(lastSignedH))
		} else {
			e.lastSignedH.With(labels).Set(0)
		}
		if hasMissed {
			e.lastMissedH.With(labels).Set(float64(lastMissedH))
		} else {
			e.lastMissedH.With(labels).Set(0)
		}
	}

	// Update node metrics
	if e.nodeMgr != nil {
		nodes := e.nodeMgr.GetNodes()
		for _, n := range nodes {
			status := n.GetStatus()
			nodeLabels := prometheus.Labels{
				"chain_id": chainID,
				"label":    n.Config.Label,
				"rpc_url":  n.Config.RPC,
			}

			e.nodeHeight.With(nodeLabels).Set(float64(status.BlockHeight))

			upVal := 0.0
			if status.Healthy {
				upVal = 1.0
			}
			e.nodeUp.With(nodeLabels).Set(upVal)

			syncingVal := 0.0
			if status.Syncing {
				syncingVal = 1.0
			}
			e.nodeSyncing.With(nodeLabels).Set(syncingVal)

			lastCheck := status.LastCheck
			if !lastCheck.IsZero() {
				e.nodeLastCheck.With(nodeLabels).Set(float64(lastCheck.Unix()))
			} else {
				e.nodeLastCheck.With(nodeLabels).Set(0)
			}
		}
	}
}
