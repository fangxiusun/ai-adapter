# 2026-07-02 项目 Code Review 意见

## 审查范围

- 仓库：`ai-adapter`
- 审查方式：基于 `superpowers` 的 review 流程，结合项目当前主干代码做静态审查
- 重点模块：`cmd/server`、`internal/web`、`internal/proxy`、`internal/channel`、`internal/db`
- 审查目标：安全性、可维护性、可观测性、生产可用性

## 优点

- 管理接口对 key 展示做了脱敏处理，避免在大多数列表接口中直接泄露明文 key，见 `internal/web/handler.go:65`、`internal/web/handler.go:122`
- 代理入口统一增加了请求体大小限制与 BOM 清理，基础防护意识较好，见 `internal/proxy/handler.go:38`、`internal/proxy/handler.go:49`
- SQLite 查询基本采用参数化方式，常规查询不存在明显 SQL 注入风险，见 `internal/db/db.go:151`、`internal/db/db.go:367`
- 渠道路由、failover、fanout、统计、Web 管理面拆分相对清晰，模块边界总体可读
- 管理接口请求体大小在若干高风险入口已有上限，见 `internal/web/handler.go:382`、`internal/web/handler.go:459`

## 问题

### [必须修复] 1. 管理端配置接口直接返回完整服务端配置，存在敏感信息泄露风险

- 位置：`internal/web/handler.go:242`
- 问题：`handleConfig` 直接返回 `h.config.Server` 和 `h.config.Logging`。如果 `Server` 结构里包含 `api_token`、`admin_token` 等字段，管理端接口会把敏感配置原样暴露给前端。
- 佐证：前端页面明确读取 `configData.server?.api_token`，见 `internal/web/static/index.html:374`。
- 影响：一旦管理端 token 配置错误、被旁路访问、或前端响应被抓取，可能导致 API token / 管理 token 暴露，属于高严重度安全问题。
- 建议：管理端配置接口只返回脱敏后的 DTO；对 token 类字段仅返回“是否已配置”的布尔值，绝不返回原值。

### [必须修复] 2. 全局 CORS 允许任意来源，管理 API 暴露面过大

- 位置：`cmd/server/main.go:209`
- 问题：`corsMiddleware` 对所有路由统一设置 `Access-Control-Allow-Origin: *`，同时管理端也走同一套中间件。
- 影响：虽然浏览器不会自动附带自定义 `Authorization`，但这会显著放大管理端被第三方页面调用、探测、联调和误暴露的风险；同时也不利于后续基于 Cookie 或浏览器凭据扩展。
- 建议：至少对 `/admin/api/` 与普通代理接口分开控制 CORS；默认关闭管理端跨域，或仅允许显式配置的来源。

### [必须修复] 3. fanout 快速返回分支没有正确回收失败结果，导致 key 状态统计失真

- 位置：`internal/channel/fanout.go:62`
- 问题：`FanoutWaitAll == false` 时，代码在 `for result := range results` 中一旦拿到首个成功结果即 `return`。这样其它并发请求即使已经返回失败，也不会进入后续 `ReportError` 路径。
- 影响：失败 key 的错误计数、暂停策略、健康状态会被低估，长期运行会让坏 key 持续被选中，削弱 key 轮换与健康治理效果。
- 建议：快速返回前至少异步 drain 结果并更新状态，或改为专门的汇总 goroutine 统一上报成功/失败。

### [必须修复] 4. 管理页面 HTML 存在明显模板污染，脚本标签被插入字面量 `` `n ``

- 位置：`internal/web/static/index.html:7`
- 问题：两个 `<script>` 标签之间出现了字面量 `` `n ``，这不是合法 HTML 换行，说明静态资源生成或拷贝过程中发生了内容污染。
- 影响：不同浏览器的容错行为不一致，轻则资源加载异常，重则页面初始化失败；这类问题也说明静态资源发布流程缺少基本校验。
- 建议：立即修复该行，并增加最小化的静态资源冒烟校验，例如检查首页是否包含合法脚本标签序列。

### [建议修改] 5. 日志查询接口未限制 `limit` 上界，存在重查询与内存放大风险

- 位置：`internal/web/handler.go:191`、`internal/web/handler.go:194`、`internal/db/db.go:177`
- 问题：`handleLogs` 只在 `limit == 0` 时设为 100，没有限制最大值；数据库层再把 `limit` 直接拼到 SQL 中。
- 影响：虽然这里是 `Atoi` 后的整数，不构成直接 SQL 注入，但大 `limit` 仍可能导致单次读取大量日志，占用内存并拖慢管理端。
- 建议：在 handler 层限制 `limit` 最大值，例如 500 或 1000；同时对 `offset` 做非负校验。

### [建议修改] 6. 管理接口的单 key 暂停/恢复缺少请求体大小限制与输入校验

- 位置：`internal/web/handler.go:156`、`internal/web/handler.go:161`
- 问题：`handleChannelKeys` 的 POST 分支直接解码 `r.Body`，未使用 `http.MaxBytesReader`，也未校验 `key` 是否为空。
- 影响：虽然接口本身简单，但仍会形成与批量接口防护强度不一致的问题；空 key 输入也会让调用方误以为操作成功。
- 建议：补齐请求体上限、`DisallowUnknownFields`、空值校验，并在 key 不存在时返回明确错误。

### [建议修改] 7. key 导入/导出能力不对称，导出文件无法直接用于导入

- 位置：`internal/web/handler.go:430`、`internal/web/handler.go:442`、`internal/web/handler.go:462`
- 问题：导出接口输出的是 `value_prefix`，导入接口要求的是完整 `value`。
- 影响：从产品语义看，“导出 keys” 通常默认应可回灌；当前实现更接近“导出摘要”，容易误导运维人员，造成恢复失败。
- 建议：二选一：要么把导出接口明确改名为“导出 key 摘要”，要么提供受限但可恢复的加密导出格式。

### [建议修改] 8. 配置与运行时数据源分离，新增/删除 key 不会同步回配置视图

- 位置：`internal/channel/keypool.go:516`、`internal/channel/keypool.go:529`、`internal/web/handler.go:242`
- 问题：运行时 `AddKey` / `RemoveKey` 只修改内存池，不修改 `h.config`；而 `/admin/api/config` 返回的是启动时配置快照。
- 影响：管理界面中的“配置视图”和“运行时实际状态”会逐步漂移，给排障和运维带来混淆。
- 建议：显式区分“运行时状态”和“静态配置”；如果不打算热持久化，就不要让配置接口承载运行时事实。

### [建议修改] 9. 请求/上游错误日志记录原始请求体，敏感数据落日志风险高

- 位置：`internal/proxy/forward.go:178`、`internal/proxy/forward.go:329`、`internal/proxy/stream.go:187`、`internal/proxy/stream.go:321`
- 问题：上游错误日志直接记录 `request_body` 与 `upstream_body`，而请求体可能包含用户 prompt、附件 URL、业务敏感字段。
- 影响：日志系统将成为高敏感数据聚合点；若日志文件权限、采集链路或备份策略不完善，风险较大。
- 建议：默认仅记录摘要字段；完整 body 仅在显式 debug 模式下保留，并增加字段级脱敏与大小截断。

### [建议修改] 10. 数据库 schema 已包含 `request_body` / `response_body`，但查询对象未暴露，设计意图不清晰

- 位置：`internal/db/db.go:66`、`internal/db/db.go:67`、`internal/db/db.go:130`
- 问题：表结构中有 `request_body`、`response_body`，但 `LogEntry` 未包含这两个字段，`QueryLogByRequestID` 也未读取它们。
- 影响：这会造成“是否保存了敏感原文、在哪里可见、是否会膨胀数据库”三方面的不确定性，维护者难以判断真实行为。
- 建议：明确产品策略：若不需要持久化原文，就移除字段；若需要，就补齐结构体、脱敏策略和查询访问控制。

### [仅供参考] 11. 管理端前端依赖外部 CDN，离线或受限网络环境下可用性较弱

- 位置：`internal/web/static/index.html:7`
- 问题：Chart.js 与 Alpine.js 直接使用 `cdn.jsdelivr.net`。
- 影响：在内网、离线环境或 CDN 被拦截场景下，管理页面会退化甚至不可用。
- 建议：如果此项目面向自托管场景，优先考虑本地静态资源打包。

## 其他观察

- `handleChannelTest` 只是从池中取一个 key 并返回“available”，并没有进行真实连通性验证，见 `internal/web/handler.go:138`。如果这是“测试渠道”能力，建议文案与行为保持一致。
- `QueryLogs` 中 `LIMIT/OFFSET` 通过 `fmt.Sprintf` 拼接，当前因入参为整数解析值，实际风险可控，但建议保持风格一致，避免未来复制粘贴到不安全场景，见 `internal/db/db.go:177`、`internal/db/db.go:180`。

## 总体评估

- 结论：**修完再合 / 修完再继续扩展管理端能力**
- 理由：项目主体结构清晰，近期也能看出持续修复安全与路由问题的意识；但当前管理端仍存在较明显的敏感信息暴露面，fanout 统计也有逻辑缺口。这两类问题一个影响安全边界，一个影响运行时健康治理，优先级都比较高。

## 建议修复顺序

1. 先修复 `internal/web/handler.go:242` 的配置泄露问题
2. 再收紧 `cmd/server/main.go:209` 的管理端 CORS 策略
3. 修复 `internal/channel/fanout.go:62` 的结果回收与状态上报逻辑
4. 修复 `internal/web/static/index.html:7` 的静态资源污染
5. 最后补齐日志查询、管理接口输入校验与导入导出语义
