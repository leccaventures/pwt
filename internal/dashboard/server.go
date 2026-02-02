package dashboard

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"lecca.io/pharos-watchtower/internal/config"
	"lecca.io/pharos-watchtower/internal/logger"
	"lecca.io/pharos-watchtower/internal/rpc"
	"lecca.io/pharos-watchtower/internal/utils"
	"lecca.io/pharos-watchtower/internal/validators"
)

//go:embed static/*
var staticFS embed.FS

type Server struct {
	cfg      config.Config
	registry *validators.Registry
	nodeMgr  *rpc.Manager

	// WebSocket
	upgrader  websocket.Upgrader
	clients   map[*websocket.Conn]bool
	broadcast chan []byte
	logChan   chan logger.LogEntry // Channel for log streaming
	mu        sync.Mutex
}

func NewServer(cfg config.Config, reg *validators.Registry, nodeMgr *rpc.Manager) *Server {
	s := &Server{
		cfg:      cfg,
		registry: reg,
		nodeMgr:  nodeMgr,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
		clients:   make(map[*websocket.Conn]bool),
		broadcast: make(chan []byte),
		logChan:   make(chan logger.LogEntry, 100), // Buffer logs
	}

	// Connect logger to this server's log channel
	logger.SetLogChannel(s.logChan)

	return s
}

// Log is deprecated. Use logger package instead.
// Kept for interface compatibility if needed, but implementation forwards to logger.
func (s *Server) Log(format string, v ...interface{}) {
	logger.Info("DASH", format, v...)
}

func (s *Server) Start(ctx context.Context) {
	if s.cfg.Advanced.DashboardPort > 0 {
		go s.handleMessages()
		go s.handleLogs() // Start log handler
		go s.runServer(ctx, s.cfg.Advanced.DashboardPort, func(mux *http.ServeMux) {
			mux.HandleFunc("/api/state", s.handleState)
			mux.HandleFunc("/ws", s.handleConnections)

			fileServer := http.FileServer(http.FS(staticFS))
			mux.Handle("/static/", fileServer)

			mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
				content, _ := staticFS.ReadFile("static/index.html")
				w.Header().Set("Content-Type", "text/html")
				w.Write(content)
			})

			if s.cfg.Advanced.MetricsPort > 0 && s.cfg.Advanced.MetricsPort == s.cfg.Advanced.DashboardPort {

				mux.Handle("/metrics", promhttp.Handler())
			}
		})
	}

	if s.cfg.Advanced.MetricsPort > 0 && s.cfg.Advanced.MetricsPort != s.cfg.Advanced.DashboardPort {
		go s.runServer(ctx, s.cfg.Advanced.MetricsPort, func(mux *http.ServeMux) {
			mux.Handle("/metrics", promhttp.Handler())
		})
	}
}

func (s *Server) runServer(ctx context.Context, port int, setup func(*http.ServeMux)) {
	mux := http.NewServeMux()
	setup(mux)

	addr := fmt.Sprintf(":%d", port)
	server := &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	s.log("SYS", "HTTP server listening on %s", addr)

	go func() {
		<-ctx.Done()
		server.Shutdown(context.Background())
		s.log("SYS", "HTTP server shutting down")
	}()

	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		s.log("SYS", "HTTP server failed on %s: %v", addr, err)
	}
}

// Internal helper for server logs
func (s *Server) log(component, format string, v ...interface{}) {
	logger.Info(component, format, v...)
}

func (s *Server) handleConnections(w http.ResponseWriter, r *http.Request) {
	ws, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		fmt.Printf("WS upgrade failed: %v\n", err)
		return
	}

	s.mu.Lock()
	s.clients[ws] = true
	s.mu.Unlock()

	// Send initial state
	if state, err := s.getStateJSON(); err == nil {
		ws.WriteMessage(websocket.TextMessage, state)
	}

	configMsg := map[string]interface{}{
		"type":      "config",
		"hide_logs": s.cfg.Advanced.HideLogs,
	}
	if bytes, err := json.Marshal(configMsg); err == nil {
		ws.WriteMessage(websocket.TextMessage, bytes)
	}
}

func (s *Server) handleMessages() {
	for msg := range s.broadcast {
		s.mu.Lock()
		for client := range s.clients {
			err := client.WriteMessage(websocket.TextMessage, msg)
			if err != nil {
				client.Close()
				delete(s.clients, client)
			}
		}
		s.mu.Unlock()
	}
}

func (s *Server) handleLogs() {
	for entry := range s.logChan {
		// Wrap log in JSON structure
		type LogMessage struct {
			Type      string `json:"type"`
			Timestamp string `json:"timestamp"`
			Level     string `json:"level"`
			Component string `json:"component"`
			Message   string `json:"message"`
		}

		msg := LogMessage{
			Type:      "log",
			Timestamp: entry.Timestamp,
			Level:     entry.Level,
			Component: entry.Component,
			Message:   entry.Message,
		}

		bytes, err := json.Marshal(msg)
		if err == nil {
			s.mu.Lock()
			for client := range s.clients {
				// Fire and forget logging to avoid blocking main updates
				client.WriteMessage(websocket.TextMessage, bytes)
			}
			s.mu.Unlock()
		}
	}
}

// BroadcastUpdate triggers a push of the current state to all connected clients
func (s *Server) BroadcastUpdate() {
	if s.cfg.Advanced.DashboardPort == 0 {
		return
	}

	state, err := s.getStateJSON()
	if err != nil {
		fmt.Printf("Failed to marshal state for broadcast: %v\n", err)
		return
	}
	s.broadcast <- state
}

func (s *Server) getStateJSON() ([]byte, error) {
	vals := s.registry.GetValidators()

	type ValidatorDTO struct {
		ID         string  `json:"id"`
		Moniker    string  `json:"moniker"`
		Status     uint8   `json:"status"`
		Missed     int     `json:"missed"`
		Total      int     `json:"total"`
		Uptime     float64 `json:"uptime"`
		LastHeight uint64  `json:"last_height"`
		LastSeen   string  `json:"last_seen"`
		Down       bool    `json:"down"`
		Staking    string  `json:"staking"`
		Window     []bool  `json:"window"`
	}

	type NodeDTO struct {
		Label       string `json:"label"`
		RpcUrl      string `json:"rpc_url"`
		WsUrl       string `json:"ws_url"`
		Healthy     bool   `json:"healthy"`
		BlockHeight uint64 `json:"block_height"`
		Syncing     bool   `json:"syncing"`
		Latency     string `json:"latency"`
		LastError   string `json:"last_error,omitempty"`
		LastCheck   string `json:"last_check"`
	}

	type StateDTO struct {
		AvgBlockTime float64        `json:"avg_block_time"`
		Validators []ValidatorDTO `json:"validators"`
		Nodes      []NodeDTO      `json:"nodes"`
	}

	var validatorDTOs []ValidatorDTO
	for _, v := range vals {
		missed, total, ratio := v.Window.GetStats()
		window := v.Window.GetBitmap() // Get historical data
		uptime := 0.0
		if total > 0 {
			uptime = 1.0 - ratio
		}

		v.Mu.RLock()
		lastSeen := v.LastSeenAt.Format(time.RFC3339)
		lastHeight := v.LastHeight
		down := v.Down
		staking := "0"
		if v.Meta.Staking != nil {
			staking = utils.FormatStaking(v.Meta.Staking)
		}
		v.Mu.RUnlock()

		validatorDTOs = append(validatorDTOs, ValidatorDTO{
			ID:         v.Meta.ValidatorID,
			Moniker:    v.Meta.Description,
			Status:     v.Meta.Status,
			Missed:     missed,
			Total:      total,
			Uptime:     uptime,
			LastHeight: lastHeight,
			LastSeen:   lastSeen,
			Down:       down,
			Staking:    staking,
			Window:     window,
		})
	}

	sort.Slice(validatorDTOs, func(i, j int) bool {
		return validatorDTOs[i].ID < validatorDTOs[j].ID
	})

	// Get node information
	var nodeDTOs []NodeDTO
	if s.nodeMgr != nil {
		nodes := s.nodeMgr.GetNodes()
		for _, n := range nodes {
			status := n.GetStatus()
			lastError := ""
			if status.LastError != nil {
				lastError = status.LastError.Error()
			}

			nodeDTOs = append(nodeDTOs, NodeDTO{
				Label:       n.Config.Label,
				RpcUrl:      n.Config.RPC,
				WsUrl:       n.Config.WS,
				Healthy:     status.Healthy,
				BlockHeight: status.BlockHeight,
				Syncing:     status.Syncing,
				Latency:     status.Latency.String(),
				LastError:   lastError,
				LastCheck:   status.LastCheck.Format(time.RFC3339),
			})
		}
	}

	// Calculate average block time from first validator (network property)
	var avgBlockTime float64
	if len(vals) > 0 {
		for _, v := range vals {
			avgBlockTime = v.Window.GetAvgBlockTime()
			break // Just need one validator for network block time
		}
	}

	state := StateDTO{
		AvgBlockTime: avgBlockTime,
		Validators: validatorDTOs,
		Nodes:      nodeDTOs,
	}

	return json.Marshal(state)
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	state, err := s.getStateJSON()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(state)
}
