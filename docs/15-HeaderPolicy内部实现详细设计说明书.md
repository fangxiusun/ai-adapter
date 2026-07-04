# Header Policy 内部实现详细设计说明书

## 1. Review 结论

本次对 `key_case_policy` 相关实现做了代码审查，结论如下：

- 未发现必须修复的阻断问题。
- 当前实现满足方案 B 的核心目标：在 `pass` 命中时，按配置型策略控制输出 header key 大小写。
- `preserve_map_key` 作为默认行为保持兼容；未配置 `key_case_policy` 时，`pass` 不改变当前 `http.Header` map 中的 key。
- `configured` 只对精确匹配（`exact`）生效；通配符和正则没有唯一的配置 key，当前实现保留原 map key，这与文档描述一致。
- `set`、`rename`、`copy` 已改为直接 map 赋值，避免 `http.Header.Set` 把配置中的 key 规范化。
- 仍需注意 Go `net/http` 和 HTTP/2 的限制：该功能控制的是进入策略引擎后的 `http.Header` map key，不表示保留客户端或上游在线路上的原始大小写。

剩余风险：

- 非 canonical key 写入 `http.Header` 后，`Header.Get` 可能查不到值。内部转发逻辑使用 map 遍历写出 header，不受影响；测试中也改为直接检查 map key。
- `set` 命中已存在 header 时会保留当前 map key，只改值；只有新增 header 时使用配置中的 `pattern` 作为 key。
- `append` / `prepend` 当前保留匹配到的现有 key，不受 `key_case_policy` 影响。

## 2. 目标与范围

目标：

1. 在 Header Policy 中新增 `key_case_policy` 配置。
2. 允许 `pass` 按以下策略控制输出 key：
   - `preserve_map_key`
   - `canonical`
   - `lower`
   - `configured`
3. 保持匹配逻辑大小写不敏感。
4. 避免 `set`、`rename`、`copy` 通过 `http.Header.Set` 触发 Go canonical 化。

非目标：

- 不实现 wire-level 原始 header key 大小写保真。
- 不绕过 Go `net/http` 的请求/响应解析。
- 不改变 HTTP/2 的 header 小写要求。

## 3. 模块结构

Header Policy 相关实现分布如下：

| 文件 | 职责 |
|------|------|
| `internal/config/config_header.go` | Header 配置结构、枚举、简化语法转换、配置校验 |
| `internal/headerpolicy/matcher.go` | exact / wildcard / regex 匹配 |
| `internal/headerpolicy/engine.go` | 策略收集、优先级排序、安全规则、规则执行 |
| `internal/proxy/helpers.go` | 把处理后的 header 应用到上游请求或客户端响应 |
| `internal/headerpolicy/engine_test.go` | Header Policy 行为测试 |
| `internal/config/config_header_test.go` | `key_case_policy` 配置解析和校验测试 |

调用关系：

```text
config.Parse
  -> Config.validate
    -> validateAllHeaderPolicies
      -> validateHeaderPolicy

proxy.processRequestHeaders / processResponseHeaders
  -> headerpolicy.Engine.ProcessRequest / ProcessResponse
    -> cloneHeaders
    -> applySafetyRules
    -> collectRequestRules / collectResponseRules
    -> applyRules
      -> applySingleRule
```

## 4. 配置设计

新增类型：

```go
// HeaderKeyCasePolicy controls how matched pass-through header keys are emitted.
type HeaderKeyCasePolicy string

const (
    KeyCasePreserveMapKey HeaderKeyCasePolicy = "preserve_map_key"
    KeyCaseCanonical      HeaderKeyCasePolicy = "canonical"
    KeyCaseLower          HeaderKeyCasePolicy = "lower"
    KeyCaseConfigured     HeaderKeyCasePolicy = "configured"
)
```

`HeaderPolicyConfig` 新增字段：

```go
type HeaderPolicyConfig struct {
    Enabled       bool                `yaml:"enabled"`
    DefaultAction HeaderAction        `yaml:"default_action"`
    KeyCasePolicy HeaderKeyCasePolicy `yaml:"key_case_policy,omitempty"`
    Rules         []HeaderRule        `yaml:"rules"`

    Drop    []string          `yaml:"drop,omitempty"`
    Pass    []string          `yaml:"pass,omitempty"`
    Set     map[string]string `yaml:"set,omitempty"`
    Rename  map[string]string `yaml:"rename,omitempty"`
    Append  map[string]string `yaml:"append,omitempty"`
    Prepend map[string]string `yaml:"prepend,omitempty"`
    Copy    map[string]string `yaml:"copy,omitempty"`
}
```

`HeaderRule` 增加内部字段，用于把 policy 级大小写策略带到执行阶段：

```go
type HeaderRule struct {
    Name          string              `yaml:"name"`
    Phase         HeaderPhase         `yaml:"phase"`
    MatchType     HeaderMatchType     `yaml:"match_type"`
    Pattern       string              `yaml:"pattern"`
    Action        HeaderAction        `yaml:"action"`
    Value         string              `yaml:"value,omitempty"`
    Target        string              `yaml:"target,omitempty"`
    KeyCasePolicy HeaderKeyCasePolicy `yaml:"-"`
}
```

说明：

- `KeyCasePolicy` 是 policy 级字段，不是单条 YAML rule 字段。
- `yaml:"-"` 表示 `HeaderRule.KeyCasePolicy` 只在内部使用，不从配置文件读取。
- 简化格式和完整规则最终都会走 `ToRules`，因此在这里统一注入策略最稳。

## 5. 简化语法转换

`ToRules` 负责把 `drop`、`pass`、`set`、`rename` 等简化配置转换为统一的 `HeaderRule`。

关键实现：

```go
// Process pass patterns
for _, pattern := range p.Pass {
    matchType, cleanPattern := DetectMatchType(pattern)
    rules = append(rules, HeaderRule{
        Name:      fmt.Sprintf("pass-%s", cleanPattern),
        Phase:     phase,
        MatchType: matchType,
        Pattern:   cleanPattern,
        Action:    ActionPass,
    })
}

// Append explicit rules
rules = append(rules, p.Rules...)

for i := range rules {
    if rules[i].KeyCasePolicy == "" {
        rules[i].KeyCasePolicy = p.KeyCasePolicy
    }
}
```

设计要点：

- 简化 `pass: ["x-custom-auth"]` 会变成 `ActionPass` 规则。
- `DetectMatchType` 会去掉正则前缀 `~`，但精确匹配会保留配置中的大小写。
- `configured` 依赖 `rule.Pattern` 作为输出 key，因此只对 `exact` 有明确含义。

## 6. 配置校验

新增校验逻辑：

```go
// Validate key_case_policy
if p.KeyCasePolicy != "" {
    switch p.KeyCasePolicy {
    case KeyCasePreserveMapKey, KeyCaseCanonical, KeyCaseLower, KeyCaseConfigured:
        // valid
    default:
        return fmt.Errorf(
            "%s: key_case_policy must be preserve_map_key/canonical/lower/configured, got: %s",
            context,
            p.KeyCasePolicy,
        )
    }
}
```

校验行为：

- 空值合法，运行时按 `preserve_map_key` 处理。
- 非法值会在 `config.Parse` 阶段报错。
- 校验范围覆盖全局、渠道、模型三级配置。

测试覆盖：

```go
func TestParseHeaderKeyCasePolicy(t *testing.T) {
    cfg, err := Parse(data)
    if err != nil {
        t.Fatalf("Parse returned error: %v", err)
    }
    if got := cfg.Headers.Request.KeyCasePolicy; got != KeyCaseConfigured {
        t.Fatalf("expected key_case_policy configured, got %q", got)
    }
}

func TestParseHeaderKeyCasePolicyRejectsInvalidValue(t *testing.T) {
    _, err := Parse(data)
    if err == nil {
        t.Fatal("expected invalid key_case_policy to fail")
    }
}
```

## 7. 规则收集与优先级

规则收集入口：

```go
func (e *Engine) collectRequestRules(channelID, model string) []collectedRule
func (e *Engine) collectResponseRules(channelID, model string) []collectedRule
```

优先级：

```text
model = 3
channel = 2
global = 1
```

排序逻辑：

```go
func sortByPriority(rules []collectedRule) {
    for i := 1; i < len(rules); i++ {
        key := rules[i]
        j := i - 1
        for j >= 0 && rules[j].priority < key.priority {
            rules[j+1] = rules[j]
            j--
        }
        rules[j+1] = key
    }
}
```

说明：

- 高优先级规则排在前面。
- 同一优先级内保持原规则顺序。
- 同一个 header 只会应用第一个命中的规则，命中后会记录到 `processed`。

## 8. 执行流程

请求处理：

```go
func (e *Engine) ProcessRequest(channelID, model string, clientHeaders http.Header) http.Header {
    result := cloneHeaders(clientHeaders)
    applySafetyRules(result, "request")
    rules := e.collectRequestRules(channelID, model)
    defaultAction := e.getRequestDefaultAction(channelID, model)
    applyRules(result, rules, defaultAction)
    return result
}
```

响应处理：

```go
func (e *Engine) ProcessResponse(channelID, model string, upstreamHeaders http.Header) http.Header {
    result := cloneHeaders(upstreamHeaders)
    applySafetyRules(result, "response")
    rules := e.collectResponseRules(channelID, model)
    defaultAction := e.getResponseDefaultAction(channelID, model)
    applyRules(result, rules, defaultAction)
    return result
}
```

`applyRules` 分三步：

1. 遍历现有 header，执行命中规则。
2. 第二轮处理 `set`，为不存在的 header 新增值。
3. 如果 `default_action: drop`，删除未处理 header。

关键代码：

```go
processed := make(map[string]bool)

for _, rule := range rules {
    for key := range headers {
        lowerKey := strings.ToLower(key)
        if processed[lowerKey] {
            continue
        }
        if Match(rule.MatchType, rule.Pattern, lowerKey) {
            applySingleRule(headers, key, rule.HeaderRule)
            processed[lowerKey] = true
        }
    }
}
```

## 9. `pass` 大小写策略实现

入口：

```go
case config.ActionPass:
    rewritePassHeaderKey(headers, key, rule)
```

核心实现：

```go
func rewritePassHeaderKey(headers http.Header, key string, rule config.HeaderRule) {
    newKey := key
    switch normalizeKeyCasePolicy(rule.KeyCasePolicy) {
    case config.KeyCasePreserveMapKey:
        return
    case config.KeyCaseCanonical:
        newKey = http.CanonicalHeaderKey(key)
    case config.KeyCaseLower:
        newKey = strings.ToLower(key)
    case config.KeyCaseConfigured:
        if rule.MatchType != config.MatchExact {
            return
        }
        newKey = rule.Pattern
    }
    rewriteHeaderKey(headers, key, newKey)
}
```

四种策略：

| 策略 | 行为 |
|------|------|
| `preserve_map_key` | 不改 key，直接返回 |
| `canonical` | 使用 `http.CanonicalHeaderKey(key)` |
| `lower` | 使用 `strings.ToLower(key)` |
| `configured` | 精确匹配时使用 `rule.Pattern` |

默认值：

```go
func normalizeKeyCasePolicy(policy config.HeaderKeyCasePolicy) config.HeaderKeyCasePolicy {
    if policy == "" {
        return config.KeyCasePreserveMapKey
    }
    return policy
}
```

重写实现：

```go
func rewriteHeaderKey(headers http.Header, oldKey, newKey string) {
    if oldKey == newKey || newKey == "" {
        return
    }
    values, ok := headers[oldKey]
    if !ok {
        return
    }
    delete(headers, oldKey)
    headers[newKey] = cloneHeaderValues(values)
}
```

这里必须直接操作 map，不能使用 `headers.Set`，否则会被 Go 标准库 canonical 化。

## 10. set / rename / copy 的 key 保留

本次实现还调整了 `set`、`rename`、`copy`，避免通过 `http.Header.Set` 写入。

当前实现：

```go
case config.ActionSet:
    headers[key] = []string{rule.Value}
case config.ActionRename:
    values := cloneHeaderValues(headers[key])
    delete(headers, key)
    headers[rule.Target] = values
case config.ActionCopy:
    headers[rule.Target] = cloneHeaderValues(headers[key])
```

说明：

- `rename` / `copy` 使用配置中的 `target` 作为输出 key。
- `set` 命中已有 header 时保留现有 key；第二轮新增 header 时使用 `rule.Pattern`。
- 直接 map 赋值能保留配置中的大小写。

第二轮新增 `set`：

```go
for _, rule := range rules {
    if rule.Action == config.ActionSet {
        targetKey := rule.Pattern
        lowerTarget := strings.ToLower(targetKey)
        if !processed[lowerTarget] && !hasHeader(headers, targetKey) {
            headers[targetKey] = []string{rule.Value}
            processed[lowerTarget] = true
        }
    }
}
```

`hasHeader` 使用大小写不敏感比较，避免同一个 header 仅因大小写不同被重复创建：

```go
func hasHeader(headers http.Header, key string) bool {
    for existing := range headers {
        if strings.EqualFold(existing, key) {
            return true
        }
    }
    return false
}
```

## 11. 安全规则

请求阶段会先执行安全规则：

```go
var safetyDropHeaders = []string{
    "authorization",
    "cookie",
    "x-api-key",
}

var safetyDropWildcardPatterns = []string{
    "x-internal-*",
}
```

执行顺序：

```go
result := cloneHeaders(clientHeaders)
applySafetyRules(result, "request")
rules := e.collectRequestRules(channelID, model)
defaultAction := e.getRequestDefaultAction(channelID, model)
applyRules(result, rules, defaultAction)
```

安全规则优先于所有用户规则。即使配置了 `pass`，这些请求头也会先被删除。

注意：

- 当前精确删除会尝试小写 key 和 Go canonical key。
- 在正常 `net/http` 请求入口中，Go 已经把常见 header 规范化为 canonical key。

## 12. 与代理层的集成

处理后的 header 通过 `applyProcessedHeaders` 写入目标：

```go
func applyProcessedHeaders(target http.Header, processed http.Header, preserveKeys ...string) {
    if processed == nil {
        return
    }
    preserve := make(map[string]bool)
    for _, k := range preserveKeys {
        preserve[strings.ToLower(k)] = true
    }
    for k, v := range processed {
        if !preserve[strings.ToLower(k)] {
            target[k] = v
        }
    }
}
```

这里同样使用直接 map 赋值：

- 不调用 `Header.Set`。
- 保留 `processed` 中的 key 字符串。
- `Content-Type`、`Authorization`、`Cache-Control`、`Connection` 等系统 header 可通过 `preserveKeys` 保护。

## 13. 测试覆盖

新增或调整的关键测试：

### 默认保留 map key

```go
func TestEngine_KeyCasePolicy_PreserveMapKeyByDefault(t *testing.T) {
    cfg := &config.Config{
        Headers: &config.HeadersConfig{
            Request: &config.HeaderPolicyConfig{
                Enabled: true,
                Pass:    []string{"X-Custom-Auth"},
            },
        },
    }

    clientHeaders := http.Header{
        "x-custom-auth": []string{"token"},
    }

    result := engine.ProcessRequest("ch1", "model1", clientHeaders)
    // result["x-custom-auth"] == {"token"}
}
```

### configured 使用配置大小写

```go
func TestEngine_KeyCasePolicy_ConfiguredUsesPassPatternCase(t *testing.T) {
    cfg := &config.Config{
        Headers: &config.HeadersConfig{
            Request: &config.HeaderPolicyConfig{
                Enabled:       true,
                KeyCasePolicy: config.KeyCaseConfigured,
                Pass:          []string{"x-custom-auth"},
            },
        },
    }

    clientHeaders := http.Header{
        "X-Custom-Auth": []string{"token"},
    }

    result := engine.ProcessRequest("ch1", "model1", clientHeaders)
    // result["x-custom-auth"] == {"token"}
}
```

### lower / canonical

```go
tests := []struct {
    name   string
    policy config.HeaderKeyCasePolicy
    want   string
}{
    {name: "lower", policy: config.KeyCaseLower, want: "x-custom-auth"},
    {name: "canonical", policy: config.KeyCaseCanonical, want: "X-Custom-Auth"},
}
```

### set / rename / copy 保留配置 target

```go
func TestEngine_SetRenameCopyUseConfiguredTargetCase(t *testing.T) {
    cfg := &config.Config{
        Headers: &config.HeadersConfig{
            Request: &config.HeaderPolicyConfig{
                Enabled: true,
                Set: map[string]string{
                    "x-set-me": "set",
                },
                Rename: map[string]string{
                    "X-Rename-Me": "x-renamed",
                },
                Copy: map[string]string{
                    "X-Copy-Me": "x-copied",
                },
            },
        },
    }
}
```

### 配置解析与非法值

```go
func TestParseHeaderKeyCasePolicy(t *testing.T)
func TestParseHeaderKeyCasePolicyRejectsInvalidValue(t *testing.T)
```

## 14. 实现步骤回顾

实际实现步骤如下：

1. 在 `internal/config/config_header.go` 增加 `HeaderKeyCasePolicy` 类型和 4 个枚举值。
2. 在 `HeaderPolicyConfig` 增加 `KeyCasePolicy` 字段。
3. 在 `HeaderRule` 增加内部 `KeyCasePolicy` 字段，用于执行阶段读取。
4. 在 `ToRules` 末尾把 policy 级 `KeyCasePolicy` 注入到每条规则。
5. 在 `validateHeaderPolicy` 中增加合法值校验。
6. 在 `internal/headerpolicy/engine.go` 中新增：
   - `rewritePassHeaderKey`
   - `normalizeKeyCasePolicy`
   - `rewriteHeaderKey`
   - `hasHeader`
   - `cloneHeaderValues`
7. 修改 `ActionPass`，从空操作变为按策略可选重写 key。
8. 修改 `ActionSet`、`ActionRename`、`ActionCopy`，使用直接 map 赋值。
9. 修改第二轮 `set` 新增逻辑，使用 `hasHeader` 判断是否已存在同名 header。
10. 增加配置解析测试和策略行为测试。
11. 更新示例配置与用户文档。
12. 运行完整测试验证。

## 15. 使用示例

配置：

```yaml
headers:
  request:
    enabled: true
    default_action: pass
    key_case_policy: configured
    pass:
      - "x-custom-auth"
```

输入（进入策略引擎后的 map）：

```go
http.Header{
    "X-Custom-Auth": []string{"token"},
}
```

输出：

```go
http.Header{
    "x-custom-auth": []string{"token"},
}
```

## 16. 限制说明

1. `configured` 只对精确匹配有唯一含义。
2. 通配符和正则 `pass` 在 `configured` 下保留当前 map key。
3. 该功能不能恢复客户端 wire-level 原始大小写。
4. 上游响应经过 Go `http.Client` 后，header key 也可能已经被规范化。
5. HTTP/2 要求 header name 为小写，无法保留任意大小写。
6. 对非 canonical key 使用 `Header.Get` 可能查不到值，应直接遍历 map 或使用大小写不敏感查找。

## 17. 验证命令

完整验证命令：

```powershell
$env:GOCACHE='G:\workspace\ai-adapter\.gocache'
$env:GOMODCACHE='G:\workspace\ai-adapter\.gomodcache'
go test ./...
```

验证目标：

- 配置解析通过。
- 非法 `key_case_policy` 被拒绝。
- `pass` 的 4 种大小写策略行为符合预期。
- 旧的 drop / set / rename / append / prepend / copy 行为不回归。
- 代理层相关包测试不受影响。

