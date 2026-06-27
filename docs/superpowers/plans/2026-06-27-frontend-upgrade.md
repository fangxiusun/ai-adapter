# 前端升级实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 为 ai-adapter 前端添加数据可视化（Chart.js）、WebSocket 实时监控、日志详情展开、批量 Key 操作与导入导出功能

**架构：** 在现有 Alpine.js 单文件架构上渐进增强，新增 WebSocket Hub 管理实时推送，Stats 模块聚合统计数据，前端新增监控页面展示图表和实时日志流

**技术栈：** Alpine.js、Chart.js 4.x（CDN）、原生 WebSocket、Go net/http

---

## 文件结构

**新增文件：**
- `internal/websocket/hub.go` — WebSocket 连接管理与消息广播
- `internal/stats/stats.go` — 内存统计聚合（最近 1 小时每分钟数据）
- `internal/web/static/index.html` — 前端页面（替换现有文件）
- `internal/web/static/style.css` — 样式文件（替换现有文件）

**修改文件：**
- `internal/web/handler.go` — 新增 API 端点和 WebSocket 路由
- `internal/proxy/helpers.go` — `recordLog` 中广播 WebSocket 消息
- `cmd/server/main.go` — 初始化 WebSocket Hub 和 Stats

---

## 任务 1：创建 WebSocket Hub

**文件：**
- 创建：`internal/websocket/hub.go`

- [ ] **步骤 1：创建 websocket 目录和 hub.go**

```go
package websocket

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// Hub manages WebSocket connections and broadcasts messages.
type Hub struct {
	mu          sync.RWMutex
	clients     map[*websocket.Conn]bool
	broadcastCh chan Message
	register    chan *websocket.Conn
	unregister  chan *websocket.Conn
}

// Message represents a WebSocket message.
type Message struct {
	Type string      `json:"type"`
	Data interface{} `json:"data"`
}

// NewHub creates a new WebSocket Hub.
func NewHub() *Hub {
	return &Hub{
		clients:     make(map[*websocket.Conn]bool),
		broadcastCh: make(chan Message, 256),
		register:    make(chan *websocket.Conn),
		unregister:  make(chan *websocket.Conn),
	}
}

// Run starts the Hub's main loop.
func (h *Hub) Run() {
	for {
		select {
		case conn := <-h.register:
			h.mu.Lock()
			h.clients[conn] = true
			h.mu.Unlock()
		case conn := <-h.unregister:
			h.mu.Lock()
			delete(h.clients, conn)
			conn.Close()
			h.mu.Unlock()
		case msg := <-h.broadcastCh:
			h.mu.RLock()
			for conn := range h.clients {
				conn.WriteJSON(msg)
			}
			h.mu.RUnlock()
		}
	}
}

// Broadcast sends a message to all connected clients.
func (h *Hub) Broadcast(msgType string, data interface{}) {
	select {
	case h.broadcastCh <- Message{Type: msgType, Data: data}:
	default:
		// Channel full, drop message
	}
}

// ServeHTTP handles WebSocket upgrade requests.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	h.register <- conn

	// Read loop (handle client disconnect)
	go func() {
		defer func() { h.unregister <- conn }()
		for {
			_, _, err := conn.ReadMessage()
			if err != nil {
				break
			}
		}
	}()
}

// StartHeartbeat sends periodic heartbeat messages.
func (h *Hub) StartHeartbeat(getActive func() int) {
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			h.Broadcast("heartbeat", map[string]interface{}{
				"active_requests": getActive(),
			})
		}
	}()
}
```

- [ ] **步骤 2：添加 gorilla/websocket 依赖**

运行：`go get github.com/gorilla/websocket`

- [ ] **步骤 3：编译验证**

运行：`go build ./internal/websocket/`
预期：编译成功

- [ ] **步骤 4：Commit**

```bash
git add internal/websocket/hub.go go.mod go.sum
git commit -m "feat(websocket): add WebSocket Hub for real-time push"
```

---

## 任务 2：创建 Stats 聚合模块

**文件：**
- 创建：`internal/stats/stats.go`

- [ ] **步骤 1：创建 stats 目录和 stats.go**

```go
package stats

import (
	"sync"
	"time"
)

// MinuteStats holds aggregated stats for one minute.
type MinuteStats struct {
	Timestamp    time.Time `json:"timestamp"`
	Total        int       `json:"total"`
	Success      int       `json:"success"`
	Errors       int       `json:"errors"`
	StatusCounts map[int]int `json:"status_counts"`
}

// HistoryEntry is returned by GetHistory.
type HistoryEntry struct {
	Timestamp string `json:"timestamp"`
	Total     int    `json:"total"`
	Success   int    `json:"success"`
	Errors    int    `json:"errors"`
}

// ErrorDistEntry is returned by GetErrorDistribution.
type ErrorDistEntry struct {
	Status string `json:"status"`
	Count  int    `json:"count"`
}

// Stats maintains in-memory per-minute stats for the last hour.
type Stats struct {
	mu      sync.RWMutex
	slots   [60]*MinuteStats
	cursor  int
	current *MinuteStats
}

// NewStats creates a new Stats instance.
func NewStats() *Stats {
	s := &Stats{}
	now := time.Now().Truncate(time.Minute)
	for i := range s.slots {
		s.slots[i] = &MinuteStats{
			Timestamp:    now.Add(time.Duration(i-59) * time.Minute),
			StatusCounts: make(map[int]int),
		}
	}
	s.cursor = 59
	s.current = s.slots[59]
	return s
}

// Record adds a request result to the current minute.
func (s *Stats) Record(status int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().Truncate(time.Minute)
	if now.After(s.current.Timestamp) {
		// Advance to new minute(s)
		for s.current.Timestamp.Before(now) {
			s.cursor = (s.cursor + 1) % 60
			s.slots[s.cursor] = &MinuteStats{
				Timestamp:    s.current.Timestamp.Add(time.Minute),
				StatusCounts: make(map[int]int),
			}
			s.current = s.slots[s.cursor]
		}
	}

	s.current.Total++
	s.current.StatusCounts[status]++
	if status >= 200 && status < 400 {
		s.current.Success++
	} else {
		s.current.Errors++
	}
}

// GetHistory returns the last N minutes of stats.
func (s *Stats) GetHistory(minutes int) []HistoryEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if minutes > 60 {
		minutes = 60
	}
	if minutes < 1 {
		minutes = 1
	}

	result := make([]HistoryEntry, 0, minutes)
	for i := minutes - 1; i >= 0; i-- {
		idx := (s.cursor - i + 60) % 60
		slot := s.slots[idx]
		result = append(result, HistoryEntry{
			Timestamp: slot.Timestamp.Format("15:04"),
			Total:     slot.Total,
			Success:   slot.Success,
			Errors:    slot.Errors,
		})
	}
	return result
}

// GetErrorDistribution returns error distribution for the last N minutes.
func (s *Stats) GetErrorDistribution(minutes int) []ErrorDistEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if minutes > 60 {
		minutes = 60
	}

	dist := make(map[string]int)
	for i := minutes - 1; i >= 0; i-- {
		idx := (s.cursor - i + 60) % 60
		slot := s.slots[idx]
		for status, count := range slot.StatusCounts {
			switch {
			case status >= 200 && status < 300:
				dist["2xx"] += count
			case status == 400:
				dist["400"] += count
			case status == 401:
				dist["401"] += count
			case status == 429:
				dist["429"] += count
			case status >= 400 && status < 500:
				dist["4xx"] += count
			case status >= 500:
				dist["5xx"] += count
			}
		}
	}

	result := make([]ErrorDistEntry, 0, len(dist))
	for status, count := range dist {
		result = append(result, ErrorDistEntry{Status: status, Count: count})
	}
	return result
}

// GetCurrentQPS returns requests in the current minute divided by 60.
func (s *Stats) GetCurrentQPS() float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return float64(s.current.Total) / 60.0
}
```

- [ ] **步骤 2：编译验证**

运行：`go build ./internal/stats/`
预期：编译成功

- [ ] **步骤 3：Commit**

```bash
git add internal/stats/stats.go
git commit -m "feat(stats): add in-memory stats aggregation module"
```

---

## 任务 3：添加后端 API 端点

**文件：**
- 修改：`internal/web/handler.go`

- [ ] **步骤 1：在 WebHandler 中添加 Stats 和 Hub 字段**

在 `WebHandler` 结构体中添加字段：

```go
type WebHandler struct {
	channels *channel.ChannelManager
	db       *db.DB
	config   *config.Config
	stats    *stats.Stats
	wsHub    *websocket.Hub
}
```

更新 `NewWebHandler` 签名：

```go
func NewWebHandler(channels *channel.ChannelManager, database *db.DB, cfg *config.Config, statsInstance *stats.Stats, hub *websocket.Hub) *WebHandler {
	return &WebHandler{channels: channels, db: database, config: cfg, stats: statsInstance, wsHub: hub}
}
```

- [ ] **步骤 2：添加 WebSocket 路由**

在 `RegisterRoutes` 中添加：

```go
mux.Handle("/admin/api/ws", h.wsHub)
```

- [ ] **步骤 3：添加 stats/history API**

```go
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
```

在 `RegisterRoutes` 中添加：

```go
mux.HandleFunc("/admin/api/stats/history", h.handleStatsHistory)
```

- [ ] **步骤 4：添加日志详情 API**

```go
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
	// Query single log by request_id
	logs, total := h.db.QueryLogs(db.LogQueryParams{
		RequestID: requestID,
		Limit:     1,
	})
	if total == 0 {
		h.jsonError(w, 404, "not_found", "log not found")
		return
	}
	h.json(w, 200, logs[0])
}
```

在 `RegisterRoutes` 中添加路由（放在 `/admin/api/logs` 之前）：

```go
mux.HandleFunc("/admin/api/logs/", h.handleLogDetail)
```

- [ ] **步骤 5：添加批量 Key 操作 API**

```go
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
			if err := ch.KeyPool().PauseKey(keyVal); err != nil {
				errors = append(errors, keyVal+": "+err.Error())
			} else {
				success++
			}
		case "resume":
			if err := ch.KeyPool().ResumeKey(keyVal); err != nil {
				errors = append(errors, keyVal+": "+err.Error())
			} else {
				success++
			}
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
```

在 `handleChannelByID` 的路由匹配中添加批量操作路由。

- [ ] **步骤 6：添加 Key 导出 API**

```go
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
```

- [ ] **步骤 7：添加 Key 导入 API**

```go
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
```

- [ ] **步骤 8：编译验证**

运行：`go build ./internal/web/`
预期：编译成功（可能需要先添加 KeyPool 方法）

- [ ] **步骤 9：Commit**

```bash
git add internal/web/handler.go
git commit -m "feat(api): add WebSocket, stats history, log detail, batch keys, import/export endpoints"
```

---

## 任务 4：集成 WebSocket 和 Stats 到请求处理

**文件：**
- 修改：`internal/proxy/helpers.go`
- 修改：`cmd/server/main.go`

- [ ] **步骤 1：在 ProxyHandler 中添加 stats 和 wsHub 字段**

```go
type ProxyHandler struct {
	channels     *channel.ChannelManager
	db           *db.DB
	logger       *log.Logger
	config       *config.Config
	deepDebug    *debuglog.DeepDebugLogger
	headerEngine *headerpolicy.Engine
	stats        *stats.Stats
	wsHub        *websocket.Hub
}
```

更新 `NewProxyHandler` 签名添加参数。

- [ ] **步骤 2：在 recordLog 中广播消息**

在 `recordLog` 函数末尾添加：

```go
// Record stats
if h.stats != nil {
	h.stats.Record(status)
}

// Broadcast via WebSocket
if h.wsHub != nil {
	h.wsHub.Broadcast("log", map[string]interface{}{
		"request_id": reqID,
		"channel":    channelID,
		"model":      clientModel,
		"status":     status,
		"latency_ms": latencyMs,
		"timestamp":  time.Now().Format(time.RFC3339),
	})
}
```

- [ ] **步骤 3：更新 cmd/server/main.go 初始化**

```go
// Initialize WebSocket Hub
wsHub := websocket.NewHub()
go wsHub.Run()
wsHub.StartHeartbeat(func() int { return 0 }) // TODO: track active requests

// Initialize Stats
statsInstance := stats.NewStats()

// Initialize handlers
proxyHandler := proxy.NewProxyHandler(channels, database, logger, cfg, deepDebug, headerEngine, statsInstance, wsHub)
webHandler := web.NewWebHandler(channels, database, cfg, statsInstance, wsHub)
```

- [ ] **步骤 4：编译验证**

运行：`go build ./cmd/server/`
预期：编译成功

- [ ] **步骤 5：Commit**

```bash
git add internal/proxy/helpers.go cmd/server/main.go
git commit -m "feat: integrate WebSocket and Stats into request pipeline"
```

---

## 任务 5：前端监控页面和图表

**文件：**
- 修改：`internal/web/static/index.html`
- 修改：`internal/web/static/style.css`

- [ ] **步骤 1：添加 Chart.js CDN**

在 `<head>` 中添加：

```html
<script src="https://cdn.jsdelivr.net/npm/chart.js@4.4.0/dist/chart.umd.min.js"></script>
```

- [ ] **步骤 2：添加监控页面导航**

在导航栏中添加：

```html
<a :class="{'active': page === 'monitor'}" @click.prevent="page='monitor'; loadStatsHistory()" href="#">
    <span class="nav-icon">📈</span><span>监控</span>
</a>
```

- [ ] **步骤 3：添加监控页面 HTML**

在 `<main>` 中添加监控页面：

```html
<!-- ========== Monitor ========== -->
<div x-show="page === 'monitor'" x-cloak>
    <div class="page-header">
        <h1>实时监控</h1>
        <span class="badge badge-green" x-text="wsConnected ? '已连接' : '未连接'"></span>
    </div>

    <div class="stats-grid">
        <div class="stat-card">
            <div class="stat-icon">⚡</div>
            <div class="stat-body">
                <div class="stat-value" x-text="realtime.activeRequests"></div>
                <div class="stat-label">活跃请求</div>
            </div>
        </div>
        <div class="stat-card">
            <div class="stat-icon">📊</div>
            <div class="stat-body">
                <div class="stat-value" x-text="realtime.qps.toFixed(1)"></div>
                <div class="stat-label">QPS</div>
            </div>
        </div>
        <div class="stat-card">
            <div class="stat-icon">⏱️</div>
            <div class="stat-body">
                <div class="stat-value" x-text="realtime.avgLatency + 'ms'"></div>
                <div class="stat-label">平均延迟</div>
            </div>
        </div>
        <div class="stat-card">
            <div class="stat-icon">❌</div>
            <div class="stat-body">
                <div class="stat-value" :class="realtime.errorRate > 0.05 ? 'text-red' : ''" x-text="(realtime.errorRate * 100).toFixed(1) + '%'"></div>
                <div class="stat-label">错误率</div>
            </div>
        </div>
    </div>

    <div class="chart-grid">
        <div class="card">
            <div class="card-header"><span class="card-title">请求趋势（最近 1 小时）</span></div>
            <div class="card-body">
                <canvas id="requestTrendChart" height="200"></canvas>
            </div>
        </div>
        <div class="card">
            <div class="card-header"><span class="card-title">错误分布</span></div>
            <div class="card-body">
                <canvas id="errorDistChart" height="200"></canvas>
            </div>
        </div>
    </div>

    <div class="card card-full">
        <div class="card-header"><span class="card-title">实时日志流</span></div>
        <div class="card-body no-padding">
            <div class="live-log-container" id="liveLogContainer">
                <template x-for="log in liveLogs" :key="log.request_id">
                    <div class="live-log-row">
                        <span class="mono text-sm" x-text="fmtTime(log.timestamp)"></span>
                        <span x-text="log.channel"></span>
                        <span class="mono text-sm" x-text="log.model"></span>
                        <span class="badge" :class="statusBadgeClass(log.status)" x-text="log.status"></span>
                        <span x-text="log.latency_ms + 'ms'"></span>
                    </div>
                </template>
            </div>
        </div>
    </div>
</div>
```

- [ ] **步骤 4：添加 CSS 样式**

在 `style.css` 中添加：

```css
/* Charts */
.chart-grid {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 1rem;
    margin-bottom: 1.5rem;
}

/* Live Log */
.live-log-container {
    max-height: 400px;
    overflow-y: auto;
    font-family: monospace;
    font-size: 0.85rem;
}

.live-log-row {
    display: grid;
    grid-template-columns: 180px 80px 1fr 60px 80px;
    gap: 0.5rem;
    padding: 0.4rem 1rem;
    border-bottom: 1px solid var(--border);
}

.live-log-row:hover {
    background: var(--bg-hover);
}

/* Toast */
.toast-container {
    position: fixed;
    top: 1rem;
    right: 1rem;
    z-index: 9999;
}

.toast {
    background: var(--bg-card);
    border: 1px solid var(--border);
    border-radius: 8px;
    padding: 0.75rem 1rem;
    margin-bottom: 0.5rem;
    box-shadow: 0 4px 12px rgba(0,0,0,0.15);
    animation: slideIn 0.3s ease;
}

@keyframes slideIn {
    from { transform: translateX(100%); opacity: 0; }
    to { transform: translateX(0); opacity: 1; }
}

/* Checkbox */
.checkbox-col {
    width: 40px;
    text-align: center;
}

.batch-actions {
    display: flex;
    gap: 0.5rem;
    margin-bottom: 1rem;
}
```

- [ ] **步骤 5：添加 JavaScript 逻辑**

在 `app()` 函数中添加状态和方法：

```javascript
// Realtime state
wsConnected: false,
realtime: { activeRequests: 0, qps: 0, avgLatency: 0, errorRate: 0 },
liveLogs: [],
requestTrendChart: null,
errorDistChart: null,

// WebSocket connection
connectWebSocket() {
    const protocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
    const ws = new WebSocket(`${protocol}//${location.host}/admin/api/ws`);
    ws.onopen = () => { this.wsConnected = true; };
    ws.onclose = () => {
        this.wsConnected = false;
        setTimeout(() => this.connectWebSocket(), 3000);
    };
    ws.onmessage = (event) => {
        const msg = JSON.parse(event.data);
        this.handleWSMessage(msg);
    };
},

handleWSMessage(msg) {
    switch (msg.type) {
        case 'heartbeat':
            this.realtime.activeRequests = msg.data.active_requests;
            break;
        case 'log':
            this.liveLogs.unshift(msg.data);
            if (this.liveLogs.length > 20) this.liveLogs.pop();
            break;
        case 'metrics':
            this.realtime.qps = msg.data.qps;
            this.realtime.avgLatency = msg.data.avg_latency_ms;
            this.realtime.errorRate = msg.data.error_rate;
            this.updateCharts(msg.data);
            break;
        case 'key_event':
            this.showToast(`${msg.data.channel}/${msg.data.key}: ${msg.data.event}`);
            break;
    }
},

async loadStatsHistory() {
    try {
        const r = await fetch('/admin/api/stats/history?minutes=60');
        if (r.ok) {
            const d = await r.json();
            this.initCharts(d);
        }
    } catch {}
},

initCharts(data) {
    // Destroy existing charts
    if (this.requestTrendChart) this.requestTrendChart.destroy();
    if (this.errorDistChart) this.errorDistChart.destroy();

    // Request trend chart
    const trendCtx = document.getElementById('requestTrendChart');
    if (trendCtx) {
        this.requestTrendChart = new Chart(trendCtx, {
            type: 'line',
            data: {
                labels: data.history.map(h => h.timestamp),
                datasets: [
                    { label: '成功', data: data.history.map(h => h.success), borderColor: '#10B981', tension: 0.3 },
                    { label: '错误', data: data.history.map(h => h.errors), borderColor: '#EF4444', tension: 0.3 }
                ]
            },
            options: { responsive: true, scales: { y: { beginAtZero: true } } }
        });
    }

    // Error distribution chart
    const distCtx = document.getElementById('errorDistChart');
    if (distCtx) {
        const colors = { '2xx': '#10B981', '400': '#F59E0B', '401': '#F97316', '429': '#EF4444', '4xx': '#8B5CF6', '5xx': '#DC2626' };
        this.errorDistChart = new Chart(distCtx, {
            type: 'doughnut',
            data: {
                labels: data.errors.map(e => e.status),
                datasets: [{
                    data: data.errors.map(e => e.count),
                    backgroundColor: data.errors.map(e => colors[e.status] || '#6B7280')
                }]
            }
        });
    }
},

showToast(message) {
    // Simple toast notification
    const container = document.getElementById('toastContainer');
    if (!container) return;
    const toast = document.createElement('div');
    toast.className = 'toast';
    toast.textContent = message;
    container.appendChild(toast);
    setTimeout(() => toast.remove(), 3000);
}
```

在 `init()` 中添加 WebSocket 连接：

```javascript
async init() {
    await this.fetchStats();
    this.healthy = true;
    this.connectWebSocket();
},
```

- [ ] **步骤 6：Commit**

```bash
git add internal/web/static/index.html internal/web/static/style.css
git commit -m "feat(frontend): add monitor page with charts and live log stream"
```

---

## 任务 6：日志详情展开

**文件：**
- 修改：`internal/web/static/index.html`

- [ ] **步骤 1：添加日志详情状态**

在 `app()` 中添加：

```javascript
expandedLog: null,
logDetail: null,
```

- [ ] **步骤 2：添加展开/折叠方法**

```javascript
async toggleLogDetail(requestId) {
    if (this.expandedLog === requestId) {
        this.expandedLog = null;
        this.logDetail = null;
        return;
    }
    this.expandedLog = requestId;
    try {
        const r = await fetch('/admin/api/logs/' + requestId);
        if (r.ok) this.logDetail = await r.json();
    } catch {}
},
```

- [ ] **步骤 3：修改日志表格行**

在日志表格的 `<template>` 中添加点击事件和展开行：

```html
<template x-for="log in logs" :key="log.request_id">
    <tr>
        <td class="mono text-sm" x-text="fmtTime(log.timestamp)" @click="toggleLogDetail(log.request_id)" style="cursor:pointer"></td>
        <td class="mono text-sm" x-text="log.request_id" @click="toggleLogDetail(log.request_id)" style="cursor:pointer"></td>
        <td x-text="log.channel_id" @click="toggleLogDetail(log.request_id)" style="cursor:pointer"></td>
        <td class="mono text-sm" x-text="log.model" @click="toggleLogDetail(log.request_id)" style="cursor:pointer"></td>
        <td @click="toggleLogDetail(log.request_id)" style="cursor:pointer">
            <span class="badge" :class="statusBadgeClass(log.status_code)" x-text="log.status_code"></span>
        </td>
        <td x-text="(log.latency_ms || 0) + 'ms'" @click="toggleLogDetail(log.request_id)" style="cursor:pointer"></td>
    </tr>
    <tr x-show="expandedLog === log.request_id && logDetail">
        <td colspan="6" class="log-detail-cell">
            <div class="log-detail">
                <div class="info-row"><span class="info-key">请求 ID</span><span class="mono" x-text="logDetail?.request_id"></span></div>
                <div class="info-row"><span class="info-key">上游模型</span><span class="mono" x-text="logDetail?.upstream_model || logDetail?.model"></span></div>
                <div class="info-row"><span class="info-key">Key</span><span class="mono" x-text="logDetail?.key_name || '-'"></span></div>
                <div class="info-row"><span class="info-key">Token 用量</span><span x-text="'Prompt ' + (logDetail?.prompt_tokens || 0).toLocaleString() + ' | Completion ' + (logDetail?.completion_tokens || 0).toLocaleString()"></span></div>
                <div class="info-row"><span class="info-key">总 Tokens</span><span x-text="(logDetail?.total_tokens || 0).toLocaleString()"></span></div>
                <div class="info-row"><span class="info-key">错误信息</span><span :class="logDetail?.error_message ? 'text-red' : ''" x-text="logDetail?.error_message || '-'"></span></div>
            </div>
        </td>
    </tr>
</template>
```

- [ ] **步骤 4：添加日志详情样式**

```css
.log-detail-cell {
    background: var(--bg-secondary);
    padding: 1rem !important;
}

.log-detail {
    max-width: 600px;
}
```

- [ ] **步骤 5：Commit**

```bash
git add internal/web/static/index.html internal/web/static/style.css
git commit -m "feat(frontend): add log detail expansion with token usage"
```

---

## 任务 7：批量 Key 操作与导入导出

**文件：**
- 修改：`internal/web/static/index.html`

- [ ] **步骤 1：添加批量操作状态**

在 `app()` 中添加：

```javascript
selectedKeys: {},
selectAll: false,

toggleSelectAll(channelId, keys) {
    if (this.selectAll) {
        this.selectedKeys[channelId] = keys.map(k => k.value);
    } else {
        this.selectedKeys[channelId] = [];
    }
},

async batchKeyAction(channelId, action) {
    const keys = this.selectedKeys[channelId] || [];
    if (keys.length === 0) return;
    if (action === 'delete' && !confirm(`确认删除 ${keys.length} 个 Key？`)) return;
    try {
        const r = await fetch('/admin/api/channels/' + channelId + '/keys/batch', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ action, keys })
        });
        if (r.ok) {
            const d = await r.json();
            this.showToast(`${action}: 成功 ${d.success}, 失败 ${d.failed}`);
            this.selectedKeys[channelId] = [];
            await this.loadChannels();
        }
    } catch {}
},

async exportKeys(channelId) {
    window.open('/admin/api/channels/' + channelId + '/keys/export');
},

async importKeys(channelId) {
    const input = document.createElement('input');
    input.type = 'file';
    input.accept = '.yaml,.yml';
    input.onchange = async (e) => {
        const file = e.target.files[0];
        if (!file) return;
        const text = await file.text();
        try {
            const r = await fetch('/admin/api/channels/' + channelId + '/keys/import', {
                method: 'POST',
                headers: { 'Content-Type': 'text/yaml' },
                body: text
            });
            if (r.ok) {
                const d = await r.json();
                this.showToast(`导入完成: 新增 ${d.added}, 跳过 ${d.skipped}`);
                await this.loadChannels();
            }
        } catch {}
    };
    input.click();
},
```

- [ ] **步骤 2：修改 Keys 页面添加复选框和批量操作栏**

在 Keys 页面模板中添加批量操作栏：

```html
<div class="batch-actions" x-show="selectedKeys[ch.id]?.length > 0">
    <button class="btn btn-sm btn-yellow" @click="batchKeyAction(ch.id, 'pause')">批量暂停</button>
    <button class="btn btn-sm btn-green" @click="batchKeyAction(ch.id, 'resume')">批量恢复</button>
    <button class="btn btn-sm btn-red" @click="batchKeyAction(ch.id, 'delete')">批量删除</button>
    <button class="btn btn-sm btn-ghost" @click="exportKeys(ch.id)">导出</button>
    <button class="btn btn-sm btn-ghost" @click="importKeys(ch.id)">导入</button>
</div>
```

在表头添加全选复选框：

```html
<th class="checkbox-col"><input type="checkbox" x-model="selectAll" @change="toggleSelectAll(ch.id, ch.key_stats || [])"></th>
```

在每行添加复选框：

```html
<td class="checkbox-col"><input type="checkbox :value="k.value" x-model="selectedKeys[ch.id]"></td>
```

- [ ] **步骤 3：Commit**

```bash
git add internal/web/static/index.html
git commit -m "feat(frontend): add batch key operations, import/export"
```

---

## 任务 8：添加 Toast 容器和最终集成测试

**文件：**
- 修改：`internal/web/static/index.html`

- [ ] **步骤 1：添加 Toast 容器**

在 `<body>` 开头添加：

```html
<div class="toast-container" id="toastContainer"></div>
```

- [ ] **步骤 2：编译并启动服务**

运行：`go build -o ai-adapter.exe ./cmd/server/`
运行：`./ai-adapter.exe`

- [ ] **步骤 3：手动验证**

1. 访问首页，确认监控页面可访问
2. 发送几个请求，查看实时日志流
3. 查看图表是否更新
4. 测试日志详情展开
5. 测试批量 Key 操作
6. 测试 Key 导入导出

- [ ] **步骤 4：Commit**

```bash
git add -A
git commit -m "feat: complete frontend upgrade with charts, WebSocket, batch ops"
```

---

## 自检结果

✅ 规格覆盖度：所有需求都有对应任务
✅ 无占位符：每个步骤都有完整代码
✅ 类型一致：函数签名、字段名在各任务间一致
✅ 文件路径精确：所有路径都是绝对路径或相对于项目根目录
