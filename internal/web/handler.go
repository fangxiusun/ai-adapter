package web

import (
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/fangxiusun/ai-adapter/internal/channel"
	"github.com/fangxiusun/ai-adapter/internal/config"
	"github.com/fangxiusun/ai-adapter/internal/db"
	"github.com/fangxiusun/ai-adapter/internal/stats"
	"github.com/fangxiusun/ai-adapter/internal/websocket"
)

type WebHandler struct {
	channels *channel.ChannelManager
	db       *db.DB
	config   *config.Config
	stats    *stats.Stats
	wsHub    *websocket.Hub
	version  string
}
func NewWebHandler(channels *channel.ChannelManager, database *db.DB, cfg *config.Config, statsInstance *stats.Stats, hub *websocket.Hub, version string) *WebHandler {
	return &WebHandler{channels: channels, db: database, config: cfg, stats: statsInstance, wsHub: hub, version: version}
}

func (h *WebHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.Handle("/", StaticHandler())

	mux.HandleFunc("/admin/api/health", h.handleHealth)
	mux.HandleFunc("/admin/api/channels", h.handleChannels)
	mux.HandleFunc("/admin/api/channels/", h.handleChannelByID)
	mux.HandleFunc("/admin/api/logs", h.handleLogs)
	mux.HandleFunc("/admin/api/stats", h.handleStats)
	mux.HandleFunc("/admin/api/config", h.handleConfig)
	mux.HandleFunc("/admin/api/valid_keys", h.handleValidKeys)
	mux.Handle("/metrics", promhttp.Handler())
	mux.Handle("/admin/api/ws", h.wsHub)
	mux.HandleFunc("/admin/api/stats/history", h.handleStatsHistory)
	mux.HandleFunc("/admin/api/logs/", h.handleLogDetail)
}

func (h *WebHandler) handleHealth(w http.ResponseWriter, r *http.Request) {
	h.json(w, 200, map[string]interface{}{
		"ok":      true,
		"version": h.version,
	})
}

func (h *WebHandler) handleChannels(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		channels := h.channels.ListChannels()
		var list []map[string]interface{}
		for _, ch := range channels {
			stats := ch.KeyPool().GetStats()
			list = append(list, map[string]interface{}{
				"id":            ch.Config.ID,
				"name":          ch.Config.Name,
				"proxy_id":      ch.Config.ProxyID,
				"chat_url": ch.Config.ChatURL, "responses_url": ch.Config.ResponsesURL, "messages_url": ch.Config.MessagesURL, "generate_content_url": ch.Config.GenerateContentURL,
				"enabled":       ch.Config.Enabled,
				"default_model": ch.Config.DefaultModel,
				"key_count":     len(ch.Config.Keys),
				"models":        ch.Config.Models,
				"key_stats":     stats,
			})
		}
		h.json(w, 200, map[string]interface{}{"channels": list})
	default:
		h.jsonError(w, 405, "method_not_allowed", "use GET")
	}
}

func (h *WebHandler) handleChannelByID(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/admin/api/channels/")
	id = strings.TrimSuffix(id, "/keys")
	id = strings.TrimSuffix(id, "/test")

	ch, ok := h.channels.GetChannel(id)
	if !ok {
		h.jsonError(w, 404, "not_found", "channel not found")
		return
	}

	if strings.HasSuffix(r.URL.Path, "/test") {
		h.handleChannelTest(w, ch)
		return
	}

	if strings.HasSuffix(r.URL.Path, "/keys/batch") {
		h.handleBatchKeys(w, r, ch)
		return
	}

	if strings.HasSuffix(r.URL.Path, "/keys/export") {
		h.handleExportKeys(w, r, ch)
		return
	}

	if strings.HasSuffix(r.URL.Path, "/keys/import") {
		h.handleImportKeys(w, r, ch)
		return
	}

	if strings.HasSuffix(r.URL.Path, "/keys") {
		h.handleChannelKeys(w, r, ch)
		return
	}

	switch r.Method {
	case "GET":
		stats := ch.KeyPool().GetStats()
		h.json(w, 200, map[string]interface{}{
			"id":            ch.Config.ID,
			"name":          ch.Config.Name,
			"proxy_id":      ch.Config.ProxyID,
			"chat_url": ch.Config.ChatURL, "responses_url": ch.Config.ResponsesURL, "messages_url": ch.Config.MessagesURL, "generate_content_url": ch.Config.GenerateContentURL,
			"enabled":       ch.Config.Enabled,
			"default_model": ch.Config.DefaultModel,
			"models":        ch.Config.Models,
			"key_stats":     stats,
		})
	default:
		h.jsonError(w, 405, "method_not_allowed", "use GET")
	}
}

func (h *WebHandler) handleChannelTest(w http.ResponseWriter, ch *channel.Channel) {
	key := ch.GetKey()
	if key == nil {
		h.jsonError(w, 400, "no_keys", "no available keys")
		return
	}
	h.json(w, 200, map[string]interface{}{
		"ok":     true,
		"key":    key.Name,
		"status": "available",
	})
}

func (h *WebHandler) handleChannelKeys(w http.ResponseWriter, r *http.Request, ch *channel.Channel) {
	switch r.Method {
	case "GET":
		stats := ch.KeyPool().GetStats()
		h.json(w, 200, map[string]interface{}{"keys": stats})
	case "POST":
		var body struct {
			Action string `json:"action"`
			Key    string `json:"key"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			h.jsonError(w, 400, "invalid_body", err.Error())
			return
		}
		switch body.Action {
		case "pause":
			ch.KeyPool().PauseKey(body.Key)
			h.json(w, 200, map[string]interface{}{"ok": true})
		case "resume":
			ch.KeyPool().ResumeKey(body.Key)
			h.json(w, 200, map[string]interface{}{"ok": true})
		default:
			h.jsonError(w, 400, "unknown_action", "action must be pause or resume")
		}
	default:
		h.jsonError(w, 405, "method_not_allowed", "use GET or POST")
	}
}

func (h *WebHandler) handleLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		h.jsonError(w, 405, "method_not_allowed", "use GET")
		return
	}
	q := r.URL.Query()
	channelID := q.Get("channel")
	statusMin, _ := strconv.Atoi(q.Get("statusMin"))
	statusMax, _ := strconv.Atoi(q.Get("statusMax"))
	from, _ := strconv.ParseInt(q.Get("from"), 10, 64)
	to, _ := strconv.ParseInt(q.Get("to"), 10, 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))

	if limit == 0 {
		limit = 100
	}

	logs, err := h.db.QueryLogs(channelID, statusMin, statusMax, from, to, limit, offset)
	if err != nil {
		h.jsonError(w, 500, "query_failed", err.Error())
		return
	}
	h.json(w, 200, map[string]interface{}{"logs": logs})
}

func (h *WebHandler) handleStats(w http.ResponseWriter, r *http.Request) {
	count, _ := h.db.GetLogCount()
	channels := h.channels.ListChannels()
	var channelStats []map[string]interface{}
	for _, ch := range channels {
		stats := ch.KeyPool().GetStats()
		channelStats = append(channelStats, map[string]interface{}{
			"id":                   ch.Config.ID,
			"name":                 ch.Config.Name,
			"enabled":              ch.Config.Enabled,
			"chat_url":             ch.Config.ChatURL,
			"responses_url":        ch.Config.ResponsesURL,
			"messages_url":         ch.Config.MessagesURL,
			"generate_content_url": ch.Config.GenerateContentURL,
			"default_model":        ch.Config.DefaultModel,
			"key_count":            len(ch.Config.Keys),
			"key_stats":            stats,
		})
	}
	h.json(w, 200, map[string]interface{}{
		"log_count": count,
		"channels":  channelStats,
	})
}

func (h *WebHandler) handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		var proxies []map[string]interface{}
		for _, p := range h.config.Proxies {
			maskedURL := maskProxyURL(p.URL)
			proxies = append(proxies, map[string]interface{}{
				"id":   p.ID,
				"type": p.Type,
				"url":  maskedURL,
			})
		}
		h.json(w, 200, map[string]interface{}{
			"server":   h.config.Server,
			"logging":  h.config.Logging,
			"channels": len(h.config.Channels),
			"proxies":  proxies,
		})
		return
	}
	h.jsonError(w, 405, "method_not_allowed", "use GET")
}

// maskProxyURL masks the password in proxy URLs like socks5://user:pass@host:port
func maskProxyURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	if u.User != nil {
		u.User = url.UserPassword(u.User.Username(), "***")
	}
	return u.String()
}

func (h *WebHandler) handleValidKeys(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		h.jsonError(w, 405, "method_not_allowed", "use GET")
		return
	}

	channels := h.channels.ListChannels()
	type keyEntry struct {
		Value string `yaml:"value"`
		Name  string `yaml:"name"`
	}
	type channelEntry struct {
		ID    string     `yaml:"id"`
		Name  string     `yaml:"name"`
		Keys  []keyEntry `yaml:"keys"`
	}
	var result struct {
		Channels []channelEntry `yaml:"channels"`
	}

	for _, ch := range channels {
		validKeys := ch.KeyPool().GetValidKeys()
		if len(validKeys) == 0 {
			continue
		}
		ce := channelEntry{
			ID:   ch.Config.ID,
			Name: ch.Config.Name,
		}
		for _, k := range validKeys {
			ce.Keys = append(ce.Keys, keyEntry{Value: k.Value, Name: k.Name})
		}
		result.Channels = append(result.Channels, ce)
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	err := enc.Encode(&result)
	if err != nil {
		h.jsonError(w, 500, "marshal_failed", err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
	w.WriteHeader(200)
	w.Write(buf.Bytes())
}

func (h *WebHandler) json(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func (h *WebHandler) jsonError(w http.ResponseWriter, status int, code, message string) {
	h.json(w, status, map[string]interface{}{
		"error": map[string]interface{}{
			"code":    code,
			"message": message,
			"status":  status,
		},
	})
}






// handleStatsHistory returns historical stats for charts.
func (h *WebHandler) handleStatsHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		h.jsonError(w, 405, "method_not_allowed", "use GET")
		return
	}
	minutes := 60
	if m := r.URL.Query().Get("minutes"); m != "" {
		if v, err := strconv.Atoi(m); err == nil && v > 0 && v <= 60 {
			minutes = v
		}
	}
	h.json(w, 200, map[string]interface{}{
		"history": h.stats.GetHistory(minutes),
		"errors":  h.stats.GetErrorDistribution(minutes),
	})
}

// handleLogDetail returns detailed log entry by request_id.
func (h *WebHandler) handleLogDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		h.jsonError(w, 405, "method_not_allowed", "use GET")
		return
	}
	requestID := strings.TrimPrefix(r.URL.Path, "/admin/api/logs/")
	if requestID == "" {
		h.jsonError(w, 400, "missing_id", "request_id is required")
		return
	}
	entry, err := h.db.QueryLogByRequestID(requestID)
	if err != nil {
		h.jsonError(w, 404, "not_found", "log not found")
		return
	}
	h.json(w, 200, entry)
}

// handleBatchKeys handles batch key operations (pause/resume/delete).
func (h *WebHandler) handleBatchKeys(w http.ResponseWriter, r *http.Request, ch *channel.Channel) {
	if r.Method != "POST" {
		h.jsonError(w, 405, "method_not_allowed", "use POST")
		return
	}
	var req struct {
		Action string   `json:"action"`
		Keys   []string `json:"keys"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.jsonError(w, 400, "invalid_json", err.Error())
		return
	}
	if len(req.Keys) == 0 {
		h.jsonError(w, 400, "empty_keys", "keys array is required")
		return
	}
	success := 0
	var errors []string
	for _, keyVal := range req.Keys {
		switch req.Action {
		case "pause":
			ch.KeyPool().PauseKey(keyVal)
			success++
		case "resume":
			ch.KeyPool().ResumeKey(keyVal)
			success++
		case "delete":
			if err := ch.KeyPool().RemoveKey(keyVal); err != nil {
				errors = append(errors, keyVal+": "+err.Error())
			} else {
				success++
			}
		default:
			h.jsonError(w, 400, "invalid_action", "action must be pause, resume, or delete")
			return
		}
	}
	h.json(w, 200, map[string]interface{}{
		"success": success,
		"failed":  len(errors),
		"errors":  errors,
	})
}

// handleExportKeys exports channel keys in YAML format.
func (h *WebHandler) handleExportKeys(w http.ResponseWriter, r *http.Request, ch *channel.Channel) {
	if r.Method != "GET" {
		h.jsonError(w, 405, "method_not_allowed", "use GET")
		return
	}
	keys := ch.KeyPool().ListKeys()
	type exportKey struct {
		Name        string `yaml:"name"`
		ValuePrefix string `yaml:"value_prefix"`
	}
	export := struct {
		Keys []exportKey `yaml:"keys"`
	}{}
	for _, k := range keys {
		prefix := k.Value
		if len(prefix) > 8 {
			prefix = prefix[:8]
		}
		export.Keys = append(export.Keys, exportKey{Name: k.Name, ValuePrefix: prefix})
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	enc.Encode(&export)
	w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
	w.WriteHeader(200)
	w.Write(buf.Bytes())
}

// handleImportKeys imports keys from YAML format.
func (h *WebHandler) handleImportKeys(w http.ResponseWriter, r *http.Request, ch *channel.Channel) {
	if r.Method != "POST" {
		h.jsonError(w, 405, "method_not_allowed", "use POST")
		return
	}
	var req struct {
		Keys []struct {
			Name  string `yaml:"name"`
			Value string `yaml:"value"`
		} `yaml:"keys"`
	}
	if err := yaml.NewDecoder(r.Body).Decode(&req); err != nil {
		h.jsonError(w, 400, "invalid_yaml", err.Error())
		return
	}
	existingKeys := ch.KeyPool().ListKeys()
	existingValues := make(map[string]bool)
	for _, k := range existingKeys {
		existingValues[k.Value] = true
	}
	added := 0
	skipped := 0
	var errors []string
	for _, k := range req.Keys {
		if k.Value == "" {
			errors = append(errors, k.Name+": empty value")
			continue
		}
		if existingValues[k.Value] {
			skipped++
			continue
		}
		if err := ch.KeyPool().AddKey(k.Value, k.Name); err != nil {
			errors = append(errors, k.Name+": "+err.Error())
		} else {
			added++
		}
	}
	h.json(w, 200, map[string]interface{}{
		"added":   added,
		"skipped": skipped,
		"errors":  errors,
	})
}







