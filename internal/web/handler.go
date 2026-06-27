package web

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/fangxiusun/ai-adapter/internal/channel"
	"github.com/fangxiusun/ai-adapter/internal/config"
	"github.com/fangxiusun/ai-adapter/internal/db"
)

type WebHandler struct {
	channels *channel.ChannelManager
	db       *db.DB
	config   *config.Config
}

func NewWebHandler(channels *channel.ChannelManager, database *db.DB, cfg *config.Config) *WebHandler {
	return &WebHandler{channels: channels, db: database, config: cfg}
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
}

func (h *WebHandler) handleHealth(w http.ResponseWriter, r *http.Request) {
	h.json(w, 200, map[string]interface{}{
		"ok":      true,
		"version": "1.0.0",
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

	out, err := yaml.Marshal(result)
	if err != nil {
		h.jsonError(w, 500, "marshal_failed", err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
	w.WriteHeader(200)
	w.Write(out)
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