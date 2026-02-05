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
	"lecca.io/pharos-watchtower/internal/metrics"
	"lecca.io/pharos-watchtower/internal/rpc"
	"lecca.io/pharos-watchtower/internal/utils"
	"lecca.io/pharos-watchtower/internal/validators"
)

const (
	broadcastBufferSize = 100
	writeTimeout        = 5 * time.Second
	windowBitmapLimit   = 100
)

//go:embed static/*
var staticFS embed.FS

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
	Window     []int   `json:"window,omitempty"`
}

type ValidatorWindowDTO struct {
	ID      string `json:"id"`
	Moniker string `json:"moniker"`
	Window  []int  `json:"window"`
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
	Validators   []ValidatorDTO `json:"validators"`
	Nodes        []NodeDTO      `json:"nodes"`
}

type BlockTimeDTO struct {
	AvgBlockTime float64 `json:"avg_block_time"`
}

type validatorsMessage struct {
	Type       string         `json:"type"`
	Validators []ValidatorDTO `json:"validators"`
}

type nodesMessage struct {
	Type  string    `json:"type"`
	Nodes []NodeDTO `json:"nodes"`
}

type blockTimeMessage struct {
	Type         string  `json:"type"`
	AvgBlockTime float64 `json:"avg_block_time"`
}

type Server struct {
	cfg      config.Config
	registry *validators.Registry
	nodeMgr  *rpc.Manager
	exporter *metrics.Exporter

	// WebSocket
	upgrader  websocket.Upgrader
	clients   map[*websocket.Conn]bool
	broadcast chan []byte
	logChan   chan logger.LogEntry // Channel for log streaming
	mu        sync.Mutex
}

func NewServer(cfg config.Config, reg *validators.Registry, nodeMgr *rpc.Manager, exporter *metrics.Exporter) *Server {
	s := &Server{
		cfg:      cfg,
		registry: reg,
		nodeMgr:  nodeMgr,
		exporter: exporter,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
		clients:   make(map[*websocket.Conn]bool),
		broadcast: make(chan []byte, broadcastBufferSize),
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
			mux.HandleFunc("/api/validators", s.handleValidators)
			mux.HandleFunc("/api/validators/windows", s.handleValidatorWindows)
			mux.HandleFunc("/api/nodes", s.handleNodes)
			mux.HandleFunc("/api/blocktime", s.handleBlockTime)
			mux.HandleFunc("/ws", s.handleConnections)

			fileServer := http.FileServer(http.FS(staticFS))
			mux.Handle("/static/", fileServer)

			mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
				content, _ := staticFS.ReadFile("static/index.html")
				w.Header().Set("Content-Type", "text/html")
				w.Write(content)
			})

			if s.cfg.Advanced.Prometheus.Port > 0 && s.cfg.Advanced.Prometheus.Port == s.cfg.Advanced.DashboardPort {
				mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
					if s.exporter != nil {
						s.exporter.UpdateNow()
					}
					promhttp.Handler().ServeHTTP(w, r)
				})
			}
		})
	}

	if s.cfg.Advanced.Prometheus.Port > 0 && s.cfg.Advanced.Prometheus.Port != s.cfg.Advanced.DashboardPort {
		go s.runServer(ctx, s.cfg.Advanced.Prometheus.Port, func(mux *http.ServeMux) {
			mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
				if s.exporter != nil {
					s.exporter.UpdateNow()
				}
				promhttp.Handler().ServeHTTP(w, r)
			})
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

	// Send initial state and config while holding mu so no concurrent write from
	// handleMessages/handleLogs to this connection (gorilla/websocket is not safe for concurrent write).
	s.mu.Lock()
	if err := s.writeInitialState(ws); err != nil {
		ws.Close()
		delete(s.clients, ws)
		s.mu.Unlock()
		return
	}
	configMsg := map[string]interface{}{
		"type":      "config",
		"hide_logs": s.cfg.Advanced.HideLogs,
	}
	if bytes, err := json.Marshal(configMsg); err == nil {
		_ = ws.SetWriteDeadline(time.Now().Add(writeTimeout))
		if err := ws.WriteMessage(websocket.TextMessage, bytes); err != nil {
			ws.Close()
			delete(s.clients, ws)
			s.mu.Unlock()
			return
		}
	}
	s.mu.Unlock()
}

func (s *Server) handleMessages() {
	for msg := range s.broadcast {
		s.mu.Lock()
		for client := range s.clients {
			_ = client.SetWriteDeadline(time.Now().Add(writeTimeout))
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
				_ = client.SetWriteDeadline(time.Now().Add(writeTimeout))
				if err := client.WriteMessage(websocket.TextMessage, bytes); err != nil {
					client.Close()
					delete(s.clients, client)
				}
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

	vals := s.registry.GetValidators()
	s.broadcastPayload(validatorsMessage{
		Type:       "validators",
		Validators: s.buildValidatorDTOs(vals, true),
	})
	s.broadcastPayload(nodesMessage{
		Type:  "nodes",
		Nodes: s.buildNodeDTOs(),
	})
	s.broadcastPayload(blockTimeMessage{
		Type:         "blocktime",
		AvgBlockTime: s.getAvgBlockTime(vals),
	})
}

func (s *Server) getStateJSON() ([]byte, error) {
	vals := s.registry.GetValidators()

	state := StateDTO{
		AvgBlockTime: s.getAvgBlockTime(vals),
		Validators:   s.buildValidatorDTOs(vals, true),
		Nodes:        s.buildNodeDTOs(),
	}

	return json.Marshal(state)
}

func (s *Server) buildValidatorDTOs(vals map[string]*validators.ValidatorState, includeWindow bool) []ValidatorDTO {
	validatorDTOs := make([]ValidatorDTO, 0, len(vals))
	baseHeight := s.getWindowBaseHeight(vals)
	for _, v := range vals {
		missed, total, ratio := v.Window.GetStats()
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

		dto := ValidatorDTO{
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
		}
		if includeWindow {
			dto.Window = v.Window.GetBitmapLastNByHeight(baseHeight, windowBitmapLimit)
		}
		validatorDTOs = append(validatorDTOs, dto)
	}

	sort.Slice(validatorDTOs, func(i, j int) bool {
		return validatorDTOs[i].ID < validatorDTOs[j].ID
	})

	return validatorDTOs
}

func (s *Server) buildValidatorWindows(vals map[string]*validators.ValidatorState) []ValidatorWindowDTO {
	windowDTOs := make([]ValidatorWindowDTO, 0, len(vals))
	baseHeight := s.getWindowBaseHeight(vals)
	for _, v := range vals {
		windowDTOs = append(windowDTOs, ValidatorWindowDTO{
			ID:      v.Meta.ValidatorID,
			Moniker: v.Meta.Description,
			Window:  v.Window.GetBitmapLastNByHeight(baseHeight, windowBitmapLimit),
		})
	}

	sort.Slice(windowDTOs, func(i, j int) bool {
		return windowDTOs[i].ID < windowDTOs[j].ID
	})

	return windowDTOs
}

func (s *Server) buildNodeDTOs() []NodeDTO {
	if s.nodeMgr == nil {
		return nil
	}

	nodes := s.nodeMgr.GetNodes()
	nodeDTOs := make([]NodeDTO, 0, len(nodes))
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

	return nodeDTOs
}

func (s *Server) getAvgBlockTime(vals map[string]*validators.ValidatorState) float64 {
	if s.exporter != nil {
		return s.exporter.GetAvgBlockTime100Seconds() * 1000
	}
	for _, v := range vals {
		return v.Window.GetAvgBlockTime()
	}
	return 0
}

func (s *Server) getWindowBaseHeight(vals map[string]*validators.ValidatorState) uint64 {
	if s.nodeMgr != nil {
		nodes := s.nodeMgr.GetNodes()
		var maxHeight uint64
		for _, n := range nodes {
			status := n.GetStatus()
			if status.BlockHeight > maxHeight {
				maxHeight = status.BlockHeight
			}
		}
		if maxHeight > 0 {
			return maxHeight
		}
	}

	var maxHeight uint64
	for _, v := range vals {
		v.Mu.RLock()
		if v.LastHeight > maxHeight {
			maxHeight = v.LastHeight
		}
		v.Mu.RUnlock()
	}
	return maxHeight
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

func (s *Server) writeInitialState(ws *websocket.Conn) error {
	vals := s.registry.GetValidators()
	if err := s.writeWSMessage(ws, validatorsMessage{Type: "validators", Validators: s.buildValidatorDTOs(vals, true)}); err != nil {
		return err
	}
	if err := s.writeWSMessage(ws, nodesMessage{Type: "nodes", Nodes: s.buildNodeDTOs()}); err != nil {
		return err
	}
	if err := s.writeWSMessage(ws, blockTimeMessage{Type: "blocktime", AvgBlockTime: s.getAvgBlockTime(vals)}); err != nil {
		return err
	}
	return nil
}

func (s *Server) writeWSMessage(ws *websocket.Conn, payload interface{}) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_ = ws.SetWriteDeadline(time.Now().Add(writeTimeout))
	return ws.WriteMessage(websocket.TextMessage, data)
}

func (s *Server) broadcastPayload(payload interface{}) {
	data, err := json.Marshal(payload)
	if err != nil {
		fmt.Printf("Failed to marshal broadcast payload: %v\n", err)
		return
	}
	select {
	case s.broadcast <- data:
	default:
		// Drop update if channel is full to avoid blocking.
	}
}

func (s *Server) handleValidators(w http.ResponseWriter, r *http.Request) {
	vals := s.registry.GetValidators()
	validatorDTOs := s.buildValidatorDTOs(vals, false)
	writeJSON(w, validatorDTOs)
}

func (s *Server) handleValidatorWindows(w http.ResponseWriter, r *http.Request) {
	vals := s.registry.GetValidators()
	windows := s.buildValidatorWindows(vals)
	writeJSON(w, windows)
}

func (s *Server) handleNodes(w http.ResponseWriter, r *http.Request) {
	nodes := s.buildNodeDTOs()
	writeJSON(w, nodes)
}

func (s *Server) handleBlockTime(w http.ResponseWriter, r *http.Request) {
	vals := s.registry.GetValidators()
	writeJSON(w, BlockTimeDTO{AvgBlockTime: s.getAvgBlockTime(vals)})
}

func writeJSON(w http.ResponseWriter, payload interface{}) {
	data, err := json.Marshal(payload)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}
