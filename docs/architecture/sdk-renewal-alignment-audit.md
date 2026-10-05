# 闲鱼官方 SDK 续期逻辑复核（2026-09-10）

## 结论与取证范围

本轮直接读取生产 CDN 上的官方脚本，并沿本地实际生产调用链核对、修正和测试，不能沿用此前“已完全一致”的结论。
已修复下表列出的可复现差异；“Token/Session 失效后立即进入协议续期、绕过 sdkSilent 疲劳窗口”按用户确认保留。

官方来源：

- [goofish-auto-login/plugin.js](https://o.alicdn.com/vip/goofish-auto-login/plugin.js)
- 本轮下载内容 SHA-256：`b946a6dbbebd360907d428569f405fe56753fa421d9029a2f5da96588c20c64f`
- 当前任务先前从闲鱼 IM 页面脚本标签确认的 MTOP 版本为 `lib-mtop/2.7.3`，页面资源版本为 `xy-site/0.0.174`。

本地生产入口是 `internal/xianyu/renew.Service`，由 adapter 失效恢复、engine 连接恢复、renewal 定时任务调用。
浏览器辅助刷新不是这些调用的默认实现。本轮没有强制真实账号过期、清空登录 Cookie、执行真实登录或触发平台风控。

## 已确认并修正的差异

| 核对项 | 官方行为 | 本轮修正与验证 |
| --- | --- | --- |
| SDK 可见 Cookie | 从 `document.cookie` 读取；不可见 HttpOnly | 完整 Jar 分离脚本判断视图和 HTTP 发送视图；HttpOnly 不参与分支，但仍可随符合域和路径的请求发送。 |
| 同名 Cookie | 解析后末值覆盖首值 | 不再误用 MTOP 签名取首值的规则；只修改 auto-login 的解析器。 |
| URI 解码 | 名称和值解码；任一非法编码令整表解析失败 | 补齐百分号编码、非法 UTF-8、等号截断、加号和空串测试。 |
| 日期转换 | `Number` 后构造 `Date`；与当前时刻比较 | 补齐小数截断、指数、不同进制、JS 空白集合和 TimeClip 范围。Invalid Date 对疲劳与长登录判断产生不同结果，不能统一当作过期。 |
| 判断时序 | 立即检查疲劳；捕获 Cookie 后等待两秒，再按新时钟选择 Havana/Cookie3 | 不再在等待前固定长登录分支；验证等待期间 Havana 到期后切换 Cookie3。 |
| 请求形状 | 单次 POST；Havana 使用 `ltl=true`，Cookie3 使用 `skipSessionFilter=true&c2r=true` | 保留官方端点、appName、appEntrance、fromSite 和分支参数；不串联 hasLogin 或长登录设置接口。 |
| 查询串 | `encodeURIComponent`；documentReferer 去除页面 query | 空格改用 `%20`，保留 JS 的额外安全符号；页面 query 不进入 documentReferer 参数。 |
| 两秒超时 | `fetch` 响应头与计时器竞速；正文解析在后 | 响应头及时到达后禁用 Promise 计时器，慢正文仍可正常成功。非成功 HTTP 状态不继续读取业务正文。 |
| 迟到响应 | Promise 一旦拒绝便不会再成功；浏览器仍可能收到 Cookie | 迟到成功响应仍记本轮超时，不模拟 reload、不重启健康账号；保留并持久化实际响应 Cookie。 |
| 成功 JSON | 严格布尔 true、严格数值 100；嵌套 content 按 JS 真值优先 | 不再把 100.5 截断为 100；嵌套非空错误形状不回退为顶层成功。 |
| 成功后的 reload | 页面 `.then` 无条件 reload，不检查 Cookie 是否变化 | 定时续期正常成功后重建账号运行时，即使 Cookie 值未变；失败或迟到仅保存 Cookie。 |
| 重定向 Cookie | 每跳响应更新浏览器 Jar，下一跳立即可用 | 单次续期拥有独立传输 Jar；保留每跳 URL、Set-Cookie、响应头时刻。最终 HTTP、网络或正文失败不丢失中间跳 Cookie。 |
| Cookie 有效期 | Max-Age 从收到响应头开始计算 | 慢正文、迟到持久化与后续重放不重新起算有效期。 |
| 并发响应合并 | 响应更新当前 Jar，不恢复整个旧 Jar | adapter 恢复路径改为在短凭证锁内重读最新状态并重放响应头，避免覆盖请求期间其他流程更新的 Cookie；engine、scheduler 继续保留原有增量重放路径。 |
| MTOP Session 失效码 | 包含 SESSION_EXPIRED、SID_INVALID、AUTH_REJECT、NEED_LOGIN | 补齐裸码和非类型化错误兼容识别，测试不再依赖中文“会话过期”掩盖漏判。Token 与风控分类继续独立。 |

## 官方脚本执行证据

将下载的原始脚本直接放入隔离 Node VM，通过 `window.lib.refreshAutoLogin` 调用公共入口。
Cookie、时钟、响应和计时器均为人工夹具；fetch 替身不发送任何登录请求或遥测数据。
为加快运行，两秒计时器按比例缩短；这些结果验证 JavaScript 控制流，不声称测量了真实平台延迟。

| 场景 | 原始官方脚本结果 | 续期请求次数 |
| --- | --- | --- |
| 同名 sdkSilent 末值仍有效 | reject | 0 |
| 百分号编码的有效到期值 | resolve | 1 |
| 任意 Cookie 值含非法 UTF-8 URI 编码 | reject | 0 |
| resultCode 为 100.5 | reject | 1 |
| 到期毫秒小数经 TimeClip 后等于当前时刻 | reject | 0 |
| 响应头及时到达，正文晚于两秒窗口 | resolve | 1 |
| 响应头晚于两秒窗口 | reject | 1 |
| 嵌套 content 是 true，顶层 content 成功 | reject | 1 |

本地固化回归见 `internal/xianyu/renew/official_sdk_test.go`、`response_cookies_test.go`、
`internal/adapter/credential_renewal_test.go`，以及原有续期、adapter、scheduler 和 MTOP 测试。
原先断言“首值优先”“HttpOnly 决策”“迟到成功并重启”的测试与官方脚本相冲突，现按执行证据修正；
Cookie 保留、账号隔离、错误传播和取消断言仍保留并增加覆盖。没有修改冻结 CAPTCHA 测试或扩大门禁豁免。

## 不能扩大为“所有环境完全等价”的边界

1. **历史扁平 Cookie 缺少属性。** Domain、Path、HttpOnly、Secure、SameSite、Expires、PartitionKey 无法由 `name=value` 还原。
   本轮没有伪造完整快照；扁平凭证跨域重定向时不发送原站 Cookie，也不把外站 Cookie 混入原账号。
   精确浏览器作用域依赖登录时捕获的完整 Cookie Jar。
2. **协议运行时不是完整浏览器。** Go 不复制浏览器的 CORS、Referrer-Policy、缓存、网络栈和全局实时 Cookie Store。
   请求使用调用方提供的快照；本轮并发保证是响应不会整份回滚最新 Jar，不声称请求发出时与浏览器全局 Jar 的所有并发时序一致。
3. **服务端资源边界仍保留。** 本地底层请求有 30 秒硬上限、2 MiB 正文上限、调用方取消和 HTTP Client 重定向限制。
   这些服务生命周期与资源保护不能说成官方插件的原生行为；官方两秒 Promise 超时本身不会 abort fetch。
   本地 Linux 服务仍可执行协议续期，不复制插件面向网页 UA 的桌面平台拒绝行为。
4. **没有真实账号到期复现。** 未核验平台实际返回的完整 HttpOnly/分区 Cookie、账号灰度、风控响应和服务端长登录状态。
   本轮浏览器测试使用本地 Chromium 页面，不构成真实闲鱼续期成功的证明。
5. **CDN 地址不是不可变版本。** 本结论绑定上述 SHA-256；官方脚本更新后需要重新运行对照，不能永久保证一致。

因此，交付结论是“本轮发现的核心续期语义差异已按当前官方脚本修正并回归”，不是“所有平台、历史凭证及网络条件下逐字节完全一致”。

## 验证记录

覆盖率为仓库级验证产物，不提交 `cover*.out` 或前端 coverage 目录。

- 原始官方脚本 VM 对照：上述 8 个场景通过。
- `make cover-browser`：通过，`RUN_BROWSER_INTEGRATION=1`，浏览器包 statement **64.0%**。
- `make cover-frontend`：通过，89 个测试文件、504 个测试，statement **78.74%**。
- `go test -race ./internal/xianyu/renew ./internal/renewal ./internal/adapter ./internal/engine`：通过（本轮主体修改）。
  随后新增重定向传输和缓存失败分支后，单独运行这些末轮改动的定向 race，全部通过。
- 末轮定向 race 命令：`go test -race ./internal/xianyu/renew ./internal/adapter ./internal/renewal -run 'TestOfficial|TestRenewRedirect|TestRequestClient|TestProtocolRenewal|TestPersistProtocol|TestPendingAPI|TestAPICookieRenewSuccessWithoutCredential|TestOnPasswordLoginRefresh'`。
  缓存失败补充命令：`go test -race ./internal/adapter -run TestPersistProtocolRenewalKeepsCommittedCookieOnCacheError`。
- `make cover`：通过，未设置 `RUN_BROWSER_INTEGRATION`，Go 全库 statement **81.2%**，renew 包 **93.8%**。
  新增 SDK 语义、重定向 Cookie 模块及 adapter 增量持久化函数 statement 100%；这不是对真实平台路径的覆盖率声明。
- `make comments`、`go run ./tools/architecturecheck`、`git diff --check`：通过，无基线增量或豁免。
- `make lint`：通过，**0 issues**。
- `go vet ./internal/xianyu/renew ./internal/xianyu/mtop ./internal/adapter ./internal/renewal ./internal/engine`：通过。
- `go build -o /tmp/xianyu-sdk-audit.VA9ZpM/xianyu-server ./cmd/server`：通过；没有重启或替换正在运行的服务，没有提交或发布。

新增测试不依赖真实账号或外部服务。未覆盖的平台专属行为就是上节列明的真实登录、服务端 Cookie 属性、灰度及风控场景；
它们不计作本轮“已验证一致”。普通 Go 覆盖率不启用 RUN_BROWSER_INTEGRATION，浏览器覆盖率单独运行。
未配置外部 MySQL/PostgreSQL，本轮持久化回归使用隔离 SQLite；没有修改数据库 schema 或生产 SQL。
既有未覆盖代码仍按平台/环境边界处理，没有删减覆盖率范围或降低门槛。
