# Header Policy Engine 配置指南

## 概述

Header Policy Engine 允许您精细控制 HTTP header 在客户端和上游服务之间的传递行为。通过配置规则，可以实现：

- 删除敏感 header（防止泄露给上游）
- 添加自定义 header（如网关标识）
- 重命名 header（适配不同服务的命名规范）
- 过滤上游响应 header（移除不需要的内部信息）

## 核心概念

### 两阶段处理

Header 处理分为两个阶段：

| 阶段 | 方向 | 说明 |
|------|------|------|
| `request` | 客户端 → 上游 | 处理客户端发送的请求 header |
| `response` | 上游 → 客户端 | 处理上游返回的响应 header |

### 三层配置

策略支持三个层级，优先级从高到低：

```
model（模型级） > channel（渠道级） > global（全局级）
```

- **全局级**：作用于所有渠道和模型
- **渠道级**：覆盖全局配置，作用于特定渠道
- **模型级**：覆盖渠道配置，作用于特定模型

### 安全规则

以下 header 在 `request` 阶段会被**强制删除**，不可被任何规则覆盖：

| Header | 匹配方式 | 说明 |
|--------|----------|------|
| `Authorization` | 精确匹配 | 防止客户端 token 泄露给上游 |
| `Cookie` | 精确匹配 | 防止会话信息泄露 |
| `X-Internal-*` | 通配符匹配 | 防止内部标记泄露 |

> **重要**：上游请求的 `Authorization` 由系统自动设置（使用配置的 API Key），不受策略引擎影响。

---

## 配置格式

### 简化格式（推荐）

简化格式使用直观的语法，适合大多数场景：

```yaml
headers:
  request:
    enabled: true                    # 是否启用（默认 true）
    default_action: pass             # 未匹配规则的默认行为：pass|drop
    drop:                            # 要删除的 header 列表
      - "X-Debug-*"
      - "X-Remove-Me"
    pass:                            # 显式透传的 header 列表
      - "X-Custom-Auth"
    set:                             # 设置为固定值（键值对）
      "X-Gateway-ID": "ai-adapter"
      "X-Request-Source": "proxy"
    rename:                          # 重命名（原名 -> 新名）
      "X-Old-Name": "X-New-Name"
    append:                          # 在原值后追加
      "X-Tags": ",extra-tag"
    prepend:                         # 在原值前追加
      "X-Tags": "prefix,"
    copy:                            # 复制到另一个 header
      "X-Source": "X-Target"
  response:
    enabled: true
    drop:
      - "X-Upstream-*"
      - "X-Internal-*"
```

### 完整格式（高级）

完整格式提供更精细的控制，适合复杂场景：

```yaml
headers:
  request:
    enabled: true
    default_action: pass
    rules:
      - name: "rule-name"            # 规则名称（用于日志）
        phase: request               # 作用阶段：request|response|both
        match_type: exact            # 匹配方式：exact|wildcard|regex
        pattern: "X-Custom-Header"   # 匹配模式
        action: set                  # 操作类型
        value: "new-value"           # set/append/prepend 时必填
        target: "X-New-Name"         # rename/copy 时必填
```

### 混合格式

两种格式可以混合使用，简化格式会先被转换为规则，再与完整规则合并：

```yaml
headers:
  request:
    enabled: true
    # 简化格式
    drop: ["X-Debug-*"]
    set:
      "X-Gateway-ID": "ai-adapter"
    # 完整格式
    rules:
      - name: "complex-rule"
        phase: request
        match_type: regex
        pattern: "^x-(old|legacy)-.*$"
        action: drop
```

---

## 匹配方式详解

Header Policy Engine 支持三种匹配方式，通过模式前缀自动识别：

### 精确匹配（Exact）

**识别规则**：模式不以 `~` 开头且不包含 `*`

**特点**：大小写不敏感

```yaml
drop:
  - "Authorization"      # 匹配 Authorization、authorization、AUTHORIZATION
  - "X-Custom-Header"    # 匹配 x-custom-header、X-CUSTOM-HEADER
```

### 通配符匹配（Wildcard）

**识别规则**：模式包含 `*`

**特点**：`*` 可出现在任意位置，匹配任意数量的字符

```yaml
drop:
  - "X-Internal-*"       # 匹配 X-Internal-ID、X-Internal-Token、X-Internal-Any
  - "X-*-ID"             # 匹配 X-Request-ID、X-Response-ID、X-Any-ID
  - "*-Type"             # 匹配 Content-Type、Accept-Type、Any-Type
  - "X-Debug-*-Trace"    # 匹配 X-Debug-Request-Trace、X-Debug-Response-Trace
```

### 正则匹配（Regex）

**识别规则**：模式以 `~` 开头

**特点**：使用 Go 正则表达式语法，匹配时自动转为小写

```yaml
drop:
  - "~^x-(request|response)-id$"     # 匹配 X-Request-ID 或 X-Response-ID
  - "~^x-debug-.*$"                  # 匹配以 X-Debug- 开头的任意 header
  - "~^(accept|content)-type$"       # 匹配 Accept-Type 或 Content-Type
```

### 匹配优先级

当多个规则匹配同一个 header 时，按以下优先级应用：

1. **层级优先级**：model > channel > global
2. **同层内**：按规则顺序，第一个匹配的规则生效

---

## 操作类型详解

### drop — 删除

删除匹配的 header，不再传递给上游或客户端。

```yaml
# 简化格式
drop:
  - "X-Debug-*"
  - "X-Internal-Token"

# 完整格式
rules:
  - name: "drop-debug"
    pattern: "X-Debug-*"
    action: drop
```

### pass — 透传

显式标记 header 为透传，不进行任何修改。通常与 `default_action: drop` 配合使用。

```yaml
# 当 default_action 为 drop 时，显式保留某些 header
default_action: drop
pass:
  - "Content-Type"
  - "Accept"
  - "X-Custom-Auth"
```

### set — 设置值

将 header 设置为指定值。如果 header 不存在，会自动创建。

```yaml
# 简化格式
set:
  "X-Gateway-ID": "ai-adapter"
  "X-Request-Source": "proxy"
  "X-Version": "v1"

# 完整格式
rules:
  - name: "add-gateway-id"
    pattern: "x-gateway-id"
    action: set
    value: "ai-adapter"
```

### rename — 重命名

将 header 重命名后传递。原 header 会被删除。

```yaml
# 简化格式
rename:
  "X-Old-Name": "X-New-Name"
  "X-Legacy-Token": "X-Auth-Token"

# 完整格式
rules:
  - name: "rename-token"
    pattern: "x-legacy-token"
    action: rename
    target: "X-Auth-Token"
```

### append — 追加

在 header 原值后追加内容。如果 header 不存在，会创建新 header。

```yaml
# 简化格式
append:
  "X-Tags": ",extra-tag"
  "X-Forwarded-For": ",proxy-ip"

# 完整格式
rules:
  - name: "append-tag"
    pattern: "x-tags"
    action: append
    value: ",extra-tag"
```

**示例**：
- 原值：`X-Tags: original`
- 追加：`,extra-tag`
- 结果：`X-Tags: original,extra-tag`

### prepend — 前置

在 header 原值前追加内容。如果 header 不存在，会创建新 header。

```yaml
# 简化格式
prepend:
  "X-Tags": "prefix,"
  "X-Path": "/api/v1"

# 完整格式
rules:
  - name: "prepend-path"
    pattern: "x-path"
    action: prepend
    value: "/api/v1"
```

**示例**：
- 原值：`X-Path: /users`
- 前置：`/api/v1`
- 结果：`X-Path: /api/v1,/users`

### copy — 复制

将 header 值复制到另一个 header。原 header 保留。

```yaml
# 简化格式
copy:
  "X-Request-ID": "X-Correlation-ID"
  "X-User-ID": "X-Internal-User-ID"

# 完整格式
rules:
  - name: "copy-request-id"
    pattern: "x-request-id"
    action: copy
    target: "X-Correlation-ID"
```

---

## 默认行为

`default_action` 定义了未匹配任何规则的 header 的处理方式：

| 值 | 说明 |
|----|------|
| `pass` | 透传（默认值） |
| `drop` | 删除 |

**典型用法**：

```yaml
# 白名单模式：只保留明确允许的 header
headers:
  request:
    default_action: drop
    pass:
      - "Content-Type"
      - "Accept"
      - "X-Custom-Auth"

# 黑名单模式：只删除明确禁止的 header（默认行为）
headers:
  request:
    default_action: pass
    drop:
      - "X-Debug-*"
      - "X-Internal-*"
```

---

## 配置位置

### 全局配置

作用于所有渠道和模型：

```yaml
# config.yaml 顶层
headers:
  request:
    enabled: true
    drop: ["X-Debug-*"]
    set:
      "X-Gateway-ID": "ai-adapter"
  response:
    enabled: true
    drop: ["X-Upstream-*"]
```

### 渠道配置

覆盖全局配置，作用于特定渠道：

```yaml
channels:
  - id: "mimo"
    name: "MiMo"
    request_headers:
      enabled: true
      drop: ["X-Mimo-Internal-*"]
      set:
        "X-Channel-ID": "mimo"
    response_headers:
      enabled: true
      drop: ["X-Mimo-Trace-*"]
```

### 模型配置

覆盖渠道配置，作用于特定模型：

```yaml
channels:
  - id: "mimo"
    models:
      - id: "mimo-v2.5-pro"
        request_headers:
          enabled: true
          set:
            "X-Model-Tier": "pro"
            "X-Priority": "high"
      - id: "mimo-v2.5"
        request_headers:
          enabled: true
          set:
            "X-Model-Tier": "standard"
```

---

## 完整配置示例

### 示例 1：基础安全配置

防止敏感 header 泄露，添加网关标识：

```yaml
headers:
  request:
    enabled: true
    default_action: pass
    drop:
      - "X-Internal-*"
      - "X-Debug-*"
    set:
      "X-Gateway-ID": "ai-adapter"
      "X-Forwarded-By": "llm-proxy"
  response:
    enabled: true
    drop:
      - "X-Server-*"
      - "X-Internal-*"
```

### 示例 2：白名单模式

只允许特定 header 通过，其他全部删除：

```yaml
headers:
  request:
    enabled: true
    default_action: drop
    pass:
      - "Content-Type"
      - "Accept"
      - "Accept-Encoding"
      - "User-Agent"
      - "X-Custom-Auth"
  response:
    enabled: true
    default_action: drop
    pass:
      - "Content-Type"
      - "X-Request-ID"
```

### 示例 3：Header 重命名

适配不同服务的 header 命名规范：

```yaml
headers:
  request:
    enabled: true
    rename:
      "X-Auth-Token": "Authorization"
      "X-Client-Version": "X-Api-Version"
    set:
      "X-Gateway-Version": "1.0.0"
```

### 示例 4：多渠道差异化配置

不同渠道使用不同的 header 策略：

```yaml
# 全局配置
headers:
  request:
    enabled: true
    set:
      "X-Gateway-ID": "ai-adapter"
  response:
    enabled: true
    drop: ["X-Internal-*"]

channels:
  - id: "mimo"
    request_headers:
      enabled: true
      set:
        "X-Provider": "mimo"
        "X-Priority": "high"
    response_headers:
      enabled: true
      drop: ["X-Mimo-Trace-*"]

  - id: "deepseek"
    request_headers:
      enabled: true
      set:
        "X-Provider": "deepseek"
      rename:
        "X-Custom-Auth": "X-Api-Key"
```

### 示例 5：模型级别差异化

同一渠道的不同模型使用不同配置：

```yaml
channels:
  - id: "mimo"
    request_headers:
      enabled: true
      set:
        "X-Provider": "mimo"
    models:
      - id: "mimo-v2.5-pro"
        request_headers:
          enabled: true
          set:
            "X-Model-Tier": "pro"
            "X-Max-Tokens": "131072"
      - id: "mimo-v2.5"
        request_headers:
          enabled: true
          set:
            "X-Model-Tier": "standard"
            "X-Max-Tokens": "65536"
```

### 示例 6：使用正则匹配

复杂的 header 匹配需求：

```yaml
headers:
  request:
    enabled: true
    drop:
      - "~^x-(debug|trace|internal)-.*$"    # 删除所有 debug/trace/internal header
      - "~^x-old-.*$"                        # 删除所有旧版 header
    set:
      "X-Request-ID": "generated-id"
  response:
    enabled: true
    drop:
      - "~^x-(server|runtime)-.*$"           # 删除服务器运行时 header
```

### 示例 7：混合格式

简化格式和完整格式混合使用：

```yaml
headers:
  request:
    enabled: true
    default_action: pass
    # 简化格式处理常见场景
    drop: ["X-Debug-*", "X-Internal-*"]
    set:
      "X-Gateway-ID": "ai-adapter"
    # 完整格式处理复杂场景
    rules:
      - name: "conditional-keep"
        phase: request
        match_type: regex
        pattern: "^x-important-.*$"
        action: pass
      - name: "complex-rename"
        phase: request
        match_type: wildcard
        pattern: "X-Legacy-*-Token"
        action: rename
        target: "X-Modern-Token"
```

---

## 高级配置

### 完整规则格式字段说明

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `name` | string | 否 | 规则名称，用于日志和调试 |
| `phase` | string | 是 | 作用阶段：`request`、`response`、`both` |
| `match_type` | string | 是 | 匹配方式：`exact`、`wildcard`、`regex` |
| `pattern` | string | 是 | 匹配模式 |
| `action` | string | 是 | 操作类型 |
| `value` | string | 条件 | `set`/`append`/`prepend` 时必填 |
| `target` | string | 条件 | `rename`/`copy` 时必填 |

### both 阶段

当 `phase` 设置为 `both` 时，规则同时作用于 request 和 response 阶段：

```yaml
rules:
  - name: "both-phases"
    phase: both
    match_type: exact
    pattern: "X-Trace-ID"
    action: pass
```

> **注意**：`both` 只在完整规则格式中有效。简化格式中，`request_headers` 的规则只作用于 request，`response_headers` 的规则只作用于 response。

### 配置验证

系统会在启动时验证配置，常见错误：

| 错误 | 说明 |
|------|------|
| `phase must be request/response/both` | phase 值无效 |
| `match_type must be exact/wildcard/regex` | match_type 值无效 |
| `action must be drop/pass/set/rename/append/prepend/copy` | action 值无效 |
| `pattern is required` | 模式为空 |
| `invalid regex pattern` | 正则表达式语法错误 |
| `value is required for action set` | set 操作缺少 value |
| `target is required for action rename` | rename 操作缺少 target |

---

## 调试技巧

### 启用深度调试

使用 `--deep-debug` 参数启动，可以查看每个请求的 header 处理详情：

```bash
./ai-adapter --deep-debug --config config.yaml
```

调试日志会记录：
- 客户端原始 request header
- 处理后的 upstream request header
- 上游返回的 response header
- 处理后返回客户端的 response header

### 查看生效规则

系统启动时会日志输出所有生效的 header 策略规则：

```
time=... level=INFO msg="header policy engine initialized" 
  global_request_rules=3 
  global_response_rules=2 
  channels=2
```

### 常见问题排查

**Q: Header 没有被删除？**
- 检查是否被安全规则保护（如 Authorization）
- 检查是否有更高优先级的 pass 规则
- 检查 `enabled` 是否为 `true`

**Q: Header 值没有被修改？**
- 检查模式是否匹配（注意大小写）
- 检查是否有更高优先级的其他规则先匹配

**Q: 配置不生效？**
- 检查 YAML 语法是否正确
- 检查 `enabled` 字段是否为 `true`
- 查看启动日志是否有配置错误

---

## 参考

### 匹配模式速查

| 模式 | 匹配类型 | 匹配范围 |
|------|----------|----------|
| `Authorization` | exact | 仅 Authorization |
| `X-Custom-*` | wildcard | X-Custom- 开头的任意 header |
| `*-Type` | wildcard | 以 -Type 结尾的任意 header |
| `X-*-ID` | wildcard | X-...-ID 格式的任意 header |
| `~^x-.*$` | regex | 以 x- 开头的任意 header |
| `~^(GET\|POST)$` | regex | 仅 GET 或 POST |

### 操作类型速查

| 操作 | 效果 | 需要字段 |
|------|------|----------|
| `drop` | 删除 header | - |
| `pass` | 透传 header | - |
| `set` | 设置为固定值 | `value` |
| `rename` | 重命名 | `target` |
| `append` | 追加到末尾 | `value` |
| `prepend` | 追加到开头 | `value` |
| `copy` | 复制到另一个 header | `target` |
