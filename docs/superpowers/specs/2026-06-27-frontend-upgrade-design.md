# 前端升级设计规格说明

## 概述

在现有 Alpine.js 单文件架构上渐进增强，新增数据可视化（Chart.js）、WebSocket 实时监控、日志详情展开、批量 Key 操作与导入导出功能。

## 技术选型

- 前端框架：Alpine.js（保持不变）
- 图表库：Chart.js（CDN 加载）
- 实时通信：原生 WebSocket
- 构建工具：无（保持静态文件部署）

## 页面结构

导航新增「监控」页面：

```
仪表盘 | 渠道 | Keys | 日志 | 监控 | 设置
```

## 一、监控页面

### 1.1 实时指标卡片

页面顶部显示四个指标卡片：
- 活跃请求数（WebSocket `heartbeat` 更新）
- 当前 QPS（WebSocket `metrics` 更新）
- 平均延迟（WebSocket `metrics` 更新）
- 健康状态（WebSocket `health` 更新）

### 1.2 图表区域

**请求趋势折线图（Chart.js Line）：**
- X 轴：时间（每分钟一个点，最近 1 小时）
- Y 轴：请求数
- 数据系列：
  - 成功请求（2xx，绿色 #10B981）
  - 错误请求（4xx/5xx，红色 #EF4444）
- 初始化数据来源：`GET /admin/api/stats/history?minutes=60`
- 实时更新：WebSocket `metrics` 消息追加数据点

**错误分布饼图（Chart.js Doughnut）：**
- 分类及颜色：
  - 2xx（#10B981 绿色）
  - 400（#F59E0B 黄色）
  - 401（#F97316 橙色）
  - 429（#EF4444 红色）
  - 4xx（#8B5CF6 紫色）
  - 5xx（#DC2626 深红）
  - 网络错误（#6B7280 灰色）
- 交互：点击扇区跳转日志页并过滤对应状态

### 1.3 实时日志流

- 显示最近 20 条请求日志
- 自动滚动到底部
- 格式：`时间 | 渠道 | 模型 | 状态 | 延迟`
- 状态码颜色：2xx 绿色、4xx 黄色、5xx 红色

## 二、WebSocket 实时推送

### 2.1 端点

```
GET /admin/api/ws
```

### 2.2 消息格式

```json
{
  "type": "heartbeat" | "log" | "metrics" | "key_event" | "health",
  "data": { ... }
}
```

### 2.3 消息类型

**heartbeat（每 30 秒）：**
```json
{
  "type": "heartbeat",
  "data": { "active_requests": 2 }
}
```

**log（每个请求完成时）：**
```json
{
  "type": "log",
  "data": {
    "request_id": "req_xxx",
    "channel": "mimo",
    "model": "mimo-v2.5-pro",
    "status": 200,
    "latency_ms": 3605,
    "timestamp": "2026-06-27T17:28:43Z"
  }
}
```

**metrics（每 10 秒）：**
```json
{
  "type": "metrics",
  "data": {
    "qps": 12.5,
    "avg_latency_ms": 1250,
    "error_rate": 0.02,
    "total_requests": 1234,
    "timestamp": "2026-06-27T17:28:43Z"
  }
}
```

**key_event（Key 状态变化时）：**
```json
{
  "type": "key_event",
  "data": {
    "channel": "mimo",
    "key": "key-1",
    "event": "paused" | "resumed" | "rate_limited" | "error"
  }
}
```

**health（健康状态变化时）：**
```json
{
  "type": "health",
  "data": { "healthy": true }
}
```

### 2.4 前端处理逻辑

- 页面加载时建立连接
- 断线自动重连，指数退避（1s → 2s → 4s → ... → 30s）
- `metrics` 消息更新图表（保留最近 60 个数据点）
- `log` 消息追加到监控页实时日志流
- `key_event` 在 Keys 页面显示 toast 通知（3 秒自动消失）

## 三、日志详情展开

### 3.1 交互方式

- 点击日志行切换展开/折叠
- 同一时间只展开一个详情

### 3.2 展开内容

```
请求 ID:    req_1719482923000000001
上游模型:   mimo-v2.5-pro
Key:        key-1
Token 用量: Prompt 56,203 | Completion 73
总 Tokens:  56,276
错误信息:   （无 或 具体错误）
```

### 3.3 API

```
GET /admin/api/logs/:request_id
```

响应：
```json
{
  "request_id": "req_xxx",
  "channel_id": "mimo",
  "model": "mimo-v2.5-pro",
  "upstream_model": "mimo-v2.5-pro",
  "status_code": 200,
  "latency_ms": 3605,
  "key_name": "key-1",
  "prompt_tokens": 56203,
  "completion_tokens": 73,
  "total_tokens": 56276,
  "error_code": "",
  "error_message": "",
  "timestamp": "2026-06-27T17:28:43Z"
}
```

## 四、批量 Key 操作

### 4.1 UI 交互

- Keys 页面表格新增首列复选框
- 表头全选/取消全选
- 选中后显示操作栏：批量暂停 | 批量恢复 | 批量删除
- 删除前弹出确认对话框

### 4.2 API

```
POST /admin/api/channels/:id/keys/batch
Content-Type: application/json

{
  "action": "pause" | "resume" | "delete",
  "keys": ["key-1", "key-2", "key-3"]
}
```

响应：
```json
{
  "success": 3,
  "failed": 0,
  "errors": []
}
```

## 五、Key 导入导出

### 5.1 导出

```
GET /admin/api/channels/:id/keys/export
```

响应（YAML）：
```yaml
keys:
  - name: key-1
    value_prefix: "tp-crk53o"
  - name: key-2
    value_prefix: "tp-c8keyo"
```

注意：导出不含完整 Key 值，只含前 8 位用于识别。

### 5.2 导入

```
POST /admin/api/channels/:id/keys/import
Content-Type: text/yaml

keys:
  - name: key-1
    value: "tp-crk53oqlw7ey3lukvrqtjj9auv3w6ssiyakbky6pkc5nl8f4"
  - name: key-new
    value: "tp-new-key-value-here"
```

合并策略：
- 新增不存在的 Key
- 跳过已存在的 Key（按 value 匹配）

响应：
```json
{
  "added": 1,
  "skipped": 1,
  "errors": []
}
```

## 六、新增 API 汇总

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/admin/api/ws` | WebSocket 端点 |
| GET | `/admin/api/stats/history?minutes=60` | 历史统计（每分钟聚合） |
| GET | `/admin/api/logs/:request_id` | 日志详情 |
| POST | `/admin/api/channels/:id/keys/batch` | 批量 Key 操作 |
| GET | `/admin/api/channels/:id/keys/export` | 导出 Key 列表 |
| POST | `/admin/api/channels/:id/keys/import` | 导入 Key 列表 |

## 七、后端实现要点

### 7.1 WebSocket Hub

新增 `internal/websocket/hub.go`：
- 管理所有 WebSocket 连接
- 提供 `Broadcast(msgType string, data interface{})` 方法
- 在 `recordLog` 成功后广播 `log` 消息
- 定时器广播 `metrics` 和 `heartbeat`

### 7.2 统计聚合

新增 `internal/stats/stats.go`：
- 内存中维护最近 1 小时的每分钟聚合数据
- 环形缓冲区（60 个 slot）
- `IncrementMinute(channel, status)` 方法
- `GetHistory(minutes int)` 返回聚合数据

### 7.3 日志详情查询

扩展现有 `db.QueryLogs` 方法支持按 `request_id` 查询单条记录。

## 八、文件变更清单

**新增文件：**
- `internal/websocket/hub.go` — WebSocket 连接管理
- `internal/stats/stats.go` — 统计聚合

**修改文件：**
- `internal/web/handler.go` — 新增 API 端点
- `internal/web/static/index.html` — 前端页面
- `internal/web/static/style.css` — 样式更新
- `internal/proxy/helpers.go` — `recordLog` 中广播 WebSocket 消息
- `cmd/server/main.go` — 初始化 WebSocket Hub 和 Stats

## 九、依赖

- Chart.js 4.x（CDN：`https://cdn.jsdelivr.net/npm/chart.js`）
- 无新增 Go 依赖
