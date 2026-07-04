# Header pass 大小写保真可行性方案

## 背景

当前 Header Policy Configuration 已支持 `pass` 动作，用于显式透传匹配到的 header。现在希望在配置为 `pass` 时，原样拷贝 header 的 key，即输出时与输入时的大小写一致。

本文结论：

- **保留值和当前 `http.Header` map 中已有的 key 字符串：可行，当前实现基本已经做到。**
- **保留客户端或上游在线路上发送的原始 header key 大小写：在现有 `net/http` Handler / Client 架构下不可完全实现。**
- **如果业务只要求 request 侧把配置指定的 key 大小写发给上游：可以做小改造，但不能称为“原始大小写保真”。**
- **如果业务要求严格 wire-level 原样转发：需要引入 HTTP/1.1 原始报文级转发链路，改造范围较大，且不适用于 HTTP/2。**

## 当前实现梳理

### 配置模型

Header 策略配置位于 [internal/config/config_header.go](../internal/config/config_header.go)：

- `HeaderPolicyConfig.DefaultAction` 只允许 `pass` 或 `drop`。
- 简化配置中的 `pass` 会通过 `ToRules` 转换为 `HeaderRule{Action: ActionPass}`。
- 匹配方式支持 `exact`、`wildcard`、`regex`。

### 匹配逻辑

匹配逻辑位于 [internal/headerpolicy/matcher.go](../internal/headerpolicy/matcher.go)：

- `exact` 使用 `strings.EqualFold`，大小写不敏感。
- `wildcard` 会把 pattern 和 key 都转为小写后匹配。
- `regex` 对小写后的 key 匹配。

因此，现有策略匹配是大小写不敏感的，这符合 HTTP header 名称语义。

### 策略执行

执行逻辑位于 [internal/headerpolicy/engine.go](../internal/headerpolicy/engine.go)：

- `ProcessRequest` / `ProcessResponse` 先调用 `cloneHeaders` 复制 `http.Header`。
- `pass` 在 `applySingleRule` 中不做任何修改。
- `drop` 删除当前 map 中的 key。
- `set`、`rename`、`append`、`prepend`、`copy` 会调用 `headers.Set`，这会按 Go 标准库规则规范化 key。

这意味着：

- 如果 header key 在进入策略引擎时已经是 `X-Custom-Auth`，`pass` 后仍是 `X-Custom-Auth`。
- 如果 header key 在进入策略引擎时已经是 `x-custom-auth`，`pass` 后仍是 `x-custom-auth`。
- 但如果 key 是通过 `Set` / `Add` 写入，通常会被规范化为 Go canonical form，例如 `x-custom-auth` 变为 `X-Custom-Auth`。

### 应用到请求和响应

应用逻辑位于 [internal/proxy/helpers.go](../internal/proxy/helpers.go)：

```go
for k, v := range processed {
    if !preserve[strings.ToLower(k)] {
        target[k] = v
    }
}
```

这里是直接 map 赋值，不是 `Header.Set`。所以在这个函数内部，`processed` 里的 key 字符串会被原样放入目标 `http.Header`。

请求侧的所有主路径位于 [internal/proxy/forward.go](../internal/proxy/forward.go)、[internal/proxy/stream.go](../internal/proxy/stream.go) 和 [internal/channel/fanout.go](../internal/channel/fanout.go)，最终都使用 `http.NewRequestWithContext` 和 `http.Client.Do` 发送。响应侧最终写入 `http.ResponseWriter.Header()`。

## 关键限制

### 1. 进入 Handler 时，原始 key 大小写通常已经丢失

Go 标准库 `net/http` 在解析请求时会把 header key 规范化。标准库注释说明：HTTP header 名称大小写不敏感，请求解析器使用 `CanonicalHeaderKey`，会把 `fOO` 与 `foo` 合并为 `Foo`。

因此，业务 Handler 拿到的 `r.Header` 已经不是客户端线路上的原始 key 形式。当前 Header Policy Engine 从 `r.Header` 开始处理，无法恢复原始写法。

例如客户端真实发送：

```http
x-custom-auth: abc
X-Trace-ID: t1
```

进入当前代码后通常变为：

```go
http.Header{
    "X-Custom-Auth": {"abc"},
    "X-Trace-Id": {"t1"},
}
```

此时 `pass` 能保留的是 `X-Custom-Auth` / `X-Trace-Id`，不是原始的 `x-custom-auth` / `X-Trace-ID`。

### 2. 上游响应也会被 `http.Client` 规范化

上游响应经过 Go `http.Client` 解析后，`resp.Header` 的 key 也会被规范化。响应侧希望原样返回上游的 header key 大小写，同样无法在现有抽象中完整实现。

### 3. HTTP/2 不支持任意大小写 header name

HTTP/2 要求 header field name 使用小写。即便在 HTTP/1.1 中可以写出自定义大小写，只要连接协商为 HTTP/2，就不存在“按原始大小写发送”的语义。

因此，任何严格大小写保真方案都必须限定为 HTTP/1.1。

## 可行方案

### 方案 A：保持现状，明确文档语义

适用场景：只需要 `pass` 保留 header 值和当前 `http.Header` map 中已有的 key。

当前实现已经基本满足：

- `pass` 不修改 header。
- `cloneHeaders` 保留 map key。
- `applyProcessedHeaders` 使用直接 map 赋值，避免二次 `Set` 规范化。

需要补充说明：

- `pass` 的“原样”指进入策略引擎后的 `http.Header` key，不保证客户端或上游线路上的原始大小写。
- `set`、`rename`、`copy` 等生成或改写 key 的动作，建议按配置中的 key 直接 map 赋值，避免被 `Header.Set` 规范化。

优点：

- 改造成本低。
- 风险小。
- 与当前架构一致。

缺点：

- 不能满足 wire-level 原始大小写保真。

### 方案 B：新增配置型大小写策略

适用场景：希望 `pass` 时输出某种可控大小写，但不要求还原客户端真实原始大小写。

建议新增配置字段：

```yaml
headers:
  request:
    enabled: true
    default_action: pass
    key_case_policy: preserve_map_key # preserve_map_key|canonical|lower|configured
    pass:
      - "x-custom-auth"
```

字段含义：

| 值 | 行为 |
|----|------|
| `preserve_map_key` | 保留当前 `http.Header` map 中的 key，默认值 |
| `canonical` | 输出 Go canonical form，例如 `X-Custom-Auth` |
| `lower` | 输出小写，例如 `x-custom-auth` |
| `configured` | 匹配 `pass` 规则时使用配置中 pattern 的大小写 |

实现要点：

1. 在 `HeaderPolicyConfig` 增加 `KeyCasePolicy HeaderKeyCasePolicy`。
2. 在 `applySingleRule` 中让 `pass` 不再只是空操作，而是可选地重写 key。
3. 重写 key 时必须直接操作 map，不能使用 `headers.Set`。
4. 对 `rename` / `copy` / `set` 也建议统一改为直接 map 赋值，避免配置中的 target key 被标准库规范化。
5. 对 HTTP/2 场景在文档中声明 `lower` 才是协议自然结果。

优点：

- 能满足“我希望上游看到指定大小写”的大部分业务需求。
- 改造范围集中在 `config` 和 `headerpolicy`。
- 不需要替换 HTTP server / client。

缺点：

- `configured` 使用的是配置中的 key 大小写，不是客户端原始大小写。
- 对同一个 header 的多种大小写重复出现无法完整表达，因为进入 `r.Header` 前通常已被合并。
- 若底层使用 HTTP/2，最终仍会变为小写。

推荐作为本项目的务实方案。

### 方案 C：HTTP/1.1 原始报文级保真

适用场景：必须严格保留客户端请求或上游响应在线路上的 header key 大小写。

实现思路：

1. 请求入口不再只依赖 `net/http` Handler 的 `r.Header`，需要在更底层读取原始 HTTP/1.1 请求头。
2. 引入 `RawHeader` 结构，保存原始顺序、原始 key、value 和规范化索引：

```go
type RawHeaderField struct {
    KeyOriginal string
    KeyLower    string
    Values      []string
}
```

3. Header Policy Engine 改为处理 `RawHeader`，匹配使用 `KeyLower`，输出使用 `KeyOriginal`。
4. 上游请求不能完全依赖 `http.Client.Do` 的普通 `Header` 抽象；需要自定义 HTTP/1.1 写出逻辑，或在可控范围内使用 `Request.Write` 并确保 map key 已是目标大小写。
5. 响应侧如需保留上游原始大小写，也需要绕过 `http.Client` 的普通解析，直接读取上游响应原始 header 行。
6. 禁用 HTTP/2，明确只支持 HTTP/1.1。

优点：

- 可以真正实现 wire-level 原始大小写保真。

缺点：

- 改造范围大，涉及入口 server、上游 client、stream、fanout、debug log、测试工具。
- 与当前 `net/http` 抽象冲突明显。
- 需要重新处理连接复用、TLS、代理、超时、压缩、chunked、SSE 等细节。
- HTTP/2 场景无法支持任意大小写。

不建议作为当前阶段方案，除非确有必须兼容的上游系统错误地依赖 header 大小写。

## 推荐方案

建议采用 **方案 B：新增配置型大小写策略**。

推荐默认行为保持兼容：

```yaml
key_case_policy: preserve_map_key
```

当用户希望 `pass` 后按配置中的 key 大小写输出时，使用：

```yaml
headers:
  request:
    enabled: true
    default_action: pass
    key_case_policy: configured
    pass:
      - "x-api-key"
      - "X-Custom-Auth"
```

预期效果：

- 规则匹配仍大小写不敏感。
- 命中 `pass: ["x-api-key"]` 时，策略引擎把当前 map 中的 `X-Api-Key` 重写为 `x-api-key`。
- 命中 `pass: ["X-Custom-Auth"]` 时，输出 key 尽量保持为 `X-Custom-Auth`。
- 如果底层走 HTTP/2，最终 wire format 仍会变为小写，需要在文档中声明。

## 实施步骤

1. 配置层增加枚举：

```go
type HeaderKeyCasePolicy string

const (
    KeyCasePreserveMapKey HeaderKeyCasePolicy = "preserve_map_key"
    KeyCaseCanonical      HeaderKeyCasePolicy = "canonical"
    KeyCaseLower          HeaderKeyCasePolicy = "lower"
    KeyCaseConfigured     HeaderKeyCasePolicy = "configured"
)
```

2. 在 `HeaderPolicyConfig` 增加字段：

```go
KeyCasePolicy HeaderKeyCasePolicy `yaml:"key_case_policy,omitempty"`
```

3. 配置校验允许空值和上述 4 个值。
4. `ToRules` 中保留简化 `pass` 的原始 pattern，用于 `configured` 输出。
5. `applySingleRule` 支持对 `pass` 命中项执行 key 重写。
6. 增加工具函数：

```go
func rewriteHeaderKey(headers http.Header, oldKey, newKey string) {
    if oldKey == newKey {
        return
    }
    values := headers[oldKey]
    delete(headers, oldKey)
    headers[newKey] = values
}
```

7. 所有 key 重写必须直接 map 赋值，不使用 `Header.Set`。
8. 增加单元测试：

- `pass + preserve_map_key` 保留当前 map key。
- `pass + configured` 使用配置 pattern 的 key。
- `pass + lower` 输出小写 key。
- `pass + canonical` 输出 `http.CanonicalHeaderKey`。
- `default_action: pass` 下未命中规则的 header 保持当前 map key。
- HTTP/2 限制作为文档说明，不做普通单元测试断言。

## 验收标准

- 未配置 `key_case_policy` 时，现有测试全部通过。
- 配置 `key_case_policy: configured` 且 `pass: ["x-custom-auth"]` 时，策略引擎输出 map 中存在 `x-custom-auth`。
- 不改变安全规则：`Authorization`、`Cookie`、`X-Internal-*` 在 request 阶段仍强制删除。
- 不影响 `Content-Type` 和 `Authorization` 这类系统保留 header 的覆盖逻辑。
- 文档明确说明该能力不是 wire-level 原始大小写保真。

## 风险与注意事项

- Header 名称按 HTTP 语义大小写不敏感。依赖大小写的上游系统本身不符合通用 HTTP 约定。
- Go 标准库 `Header.Get` 假设 key 已是 canonical form。引入非 canonical key 后，代码中需要避免用 `Get` 判断这些 key 是否存在，必要时使用大小写不敏感遍历。
- 多个仅大小写不同的 header key 进入 Go `net/http` 后通常会合并，不能分别保留。
- HTTP/2 会把 header name 表示为小写，无法保持任意大小写。
- 如果后续支持 `configured`，debug log 应记录最终 map key，避免排查时误以为保留的是客户端原始 key。

