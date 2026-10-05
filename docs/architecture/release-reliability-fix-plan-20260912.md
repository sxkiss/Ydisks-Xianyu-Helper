# 发布前消息确认与凭证恢复修复计划（2026-09-12）

状态：**本轮 P1 修复已实施，代码检查通过；真实平台与三方数据库验证仍未执行**。

实施结果（2026-09-12）：消息确认已收口为单锁内的匹配与 PNM 登记，取消保护记录保留请求 `mid` 并限制单键记录数量；带 `mid` 的旧/错配回显不会回退 FIFO，也不会污染后续合法 PNM，无 `mid` 的历史路径继续按正文保护；MTOP Token 恢复现在只接受 `_m_h5_tk` 非空轮换作为签名变化证据，数据库已配置但读取失败或仅更新 `sdkSilent` 时进入可取消退避，并将即时刷新限制为连续失败期间一次；账号任务恢复在续期前或续期后无法读取签名 Cookie 时均不会报告假恢复。新增 `TestReleaseReliabilityEchoMIDPriority`、`TestReleaseReliabilityUnmatchedMIDDoesNotPoisonPNM`、`TestReleaseReliabilityPushDoesNotConsumeMIDProtection`、`TestCanceledEchoProtectionBoundsSingleKey`、`TestReleaseReliabilityTokenRequiresSigningRotation`、`TestReleaseReliabilityTokenRefreshIsBounded`、`TestReleaseReliabilityCredentialReadFailureMustBackoff`、账号任务凭证恢复边界和签名比较纯函数测试；既有同文并发、重复 PNM、迟到回显、WS mid 透传测试继续保留并通过。

本地验收证据：`go test ./... -count=1`、`go test -race ./internal/engine -run '^TestReleaseReliability' -count=20`、`make test-server-race`、`make architecture`、`make api-check`、`make vet`、`make lint`、`make comments`、前端 typecheck/505 项测试、`make cover` 均通过；Go statement 覆盖率 81.3%。前端源码和嵌入资源未因本轮修改变化，沿用此前 `make cover-frontend` 78.76% 与浏览器覆盖率 64.2% 记录。未调用真实账号，不宣称 MTOP 实际过期恢复、平台消息回显、MySQL/PostgreSQL 或桌面包验证通过。

本计划处理本轮 Code Review 已复现的两项 P1，以及同一状态机内可能绕过修复的相邻路径。作为一个完整修复任务、一个评审单元交付；下文的执行顺序不是新的重构阶段、独立子任务或中间提交。六个正式阶段仍保持已完成，以 `refactoring-master-plan.md` 为唯一状态依据。

## 1. 修复依据与重复遗漏的原因

审查基线是 `v1.0.10` 到当前工作区，HEAD 为 `9f21ec0`，包含当前未提交及未跟踪文件。实施前保存 HEAD、工作区差异和新增文件清单；仅保存非敏感源代码摘要，不读取生产凭证。后续验收绑定最终源代码快照，不能只引用 HEAD，因为本轮大量变更尚未提交。

| 已确认问题 | 当前行为 | 必须实现的业务结果 |
| --- | --- | --- |
| A：旧取消记录吞掉新 mid 的回显 | `outgoing_echo_confirmation.go` 在匹配 mid 前消费同正文的取消记录；有效响应的 PNM 已登记，后续重复通知也可能无法补救 | A 超时后，B 的有效精确响应只能确认 B；A 的旧响应和保护记录不能改变 B 的结果 |
| B：无关 Cookie 更新被认作签名恢复 | `connection_coordinator.go` 用完整 Cookie/metadata 指纹变化决定立即重连并重置失败计数 | 仅 sdkSilent/无关属性变化不能绕过 Token 失败退避；真实连接 Token 获取成功才重置对应失败计数 |

两个最小复现已在审查中失败：`TestReviewCanceledEchoDoesNotConsumeDifferentMID`、`TestReviewSDKOnlyRefreshMustBackoff`。审查临时文件位于 `/tmp/xianyu-review-probes/`，但正式回归必须进入仓库，不能依赖临时文件长期存在。

前几次修复分别验证了同文 FIFO、mid 透传、取消保护、PNM 去重和凭证整体变化，却缺少这些机制的组合验证。具体遗漏是：

1. 消息确认存在多个修改状态的入口，去重登记与等待器匹配还跨越两段锁；局部正确不等于整个事件处理原子正确。
2. 取消记录丢失请求身份，只保留正文和时间；引入 mid 后没有同步重新定义该记录的适用范围。
3. “响应已收到”“协议续期成功”“凭证已落库”“签名可重新尝试”“WS 已在线”被不同调用方混用。
4. 既有成功断言偏重返回值与计数，没有证明其他请求不被唤醒、失败计数不被提前清零、正常退避实际发生。
5. 大规模测试通过和覆盖率数字不能证明新保护措施之间没有冲突。必须先建立业务不变量，再用交错场景和调用链测试验证。

## 2. 修改边界与责任

| 范围 | 主要位置 | 工作要求 |
| --- | --- | --- |
| 消息确认状态所有权 | `internal/engine/outgoing_echo_confirmation.go` | 统一精确匹配、兼容匹配、取消、PNM 去重和清理的状态转换 |
| 生产发送接线 | `internal/engine/outgoing_message_coordinator.go`、`account.go`、`message_dispatcher.go`、`internal/xianyu/ws/client.go`、`sync.go` | 核对实际 mid 从登记到发送响应全链路一致；仅为必要接线和生命周期修正修改 |
| Token 恢复判断 | `internal/engine/connection_coordinator.go`、`credential_tokens.go`、`token_cache.go` | 区分签名恢复证据、缓存绑定指纹和实际连接成功；禁止互相替代 |
| 共享签名语义 | `internal/xianyu/mtop/client.go`、`cookie_session.go` | 在协议层复用现有签名选择语义，提供无 I/O 的窄比较能力，避免 engine/automation 各写一套解析 |
| 账号任务恢复 | `internal/automation/account_tasks.go` | 与引擎共享签名证据规则；补齐空值、读取失败和权威 Jar 场景，保留任务停止后下一轮重试语义 |
| 上下游复核 | `internal/adapter/adapter_events.go`、`credential_renewal.go`、`internal/renewal/scheduler.go`、MTOP 各调用方 | 逐入口标注返回值含义、失败后的重试预算；发现同一根因才修改，不能泛化重构 |
| 业务效果验证 | engine/ws/automation/adapter/renewal 的正式测试 | 使用本地 WS/HTTP、SQLite、可控发送器和同步屏障验证真实调用链 |

不新增数据库迁移、HTTP 字段、OpenAPI 变化、前端流程或第三方依赖。完整凭证指纹仍用于缓存绑定与并发冲突检测，不得全局改成仅比较一个 Token。保留原有未提交修复，不能回退 `RefillPending`、补发领取、原始动作计划、Cookie Jar 冲突检查和 uncertain DTO。

遵守 `dependency-rules.md`、`comment-standard.md`、`slider-captcha-frozen-spec.md`。不改变冻结 CAPTCHA、浏览器指纹、平台端点和请求参数，也不增加探活、自动重发、平台重试次数或后台扫描。成功商品响应省略 `cardList` 仍按空列表处理。

## 3. 消息确认的统一规则

### 3.1 状态与身份

生产自动化等待器必须在平台写入之前关联唯一 mid。等待器至少有“等待、已确认、已取消/超时”三个明确终态；结束后不能重新进入等待。`done` 仅代表已确认，取消与超时不能通过关闭同一成功信号伪装成功。

账号拥有 tracker；其互斥锁保护 pending、取消记录和 PNM 去重状态。在一个锁内完成一次观察事件的分类、身份验证、去重判断和状态变更。锁内不执行发送、数据库、日志或等待。生产发送和接收入口最终进入同一状态转换实现，不保留两套各自维护规则的 observe 路径。

取消保护必须保留足够身份：有 mid 的请求按 mid 隔离，历史无 mid 路径保留正文保护。既有正文保护不得先于精确 mid 匹配。具体 map/索引可以沿用当前结构或用命名记录替代时间切片，但不得出现多个独立、可不一致的权威状态。

### 3.2 事件决策表

| 输入 | 必须执行 | 明确禁止 |
| --- | --- | --- |
| 带 mid，命中当前等待器且正文/会话/已知对端相符 | 原子确认该等待器，并登记已消费 PNM | 被其他 mid 的取消记录阻挡；确认另一等待器 |
| 带 mid，但属于已取消请求或没有对应等待器 | 丢弃；必要时仅更新该请求的去重/保护状态 | 回退正文 FIFO；消费其他请求的保护或等待项 |
| 带 mid，但正文、会话、类型或已知对端不符 | 不确认，不污染合法响应的确认机会 | 仅凭 mid 或 PNM 就算成功 |
| 无 mid，存在同正文的关联等待器 | 不确认关联等待器，保留其精确响应机会 | 先登记 PNM 导致后来的精确响应失效 |
| 无 mid，仅存在历史无关联等待器 | 沿用有界的正文、对端和迟到保护语义，最多确认一个 | 消费关联请求的身份状态；一条推送确认多次 |
| 已消费 PNM 再次到达，包括不同入口重复通知 | 幂等，不再确认任何等待器 | 同一个平台消息对应多个成功发送 |
| 明确未发送/明确拒绝 | 移除等待器，不生成不确定投递的迟到保护 | 阻挡随后同文的正常发送 |
| 超时、取消、未知发送结果 | 终结本等待器并保留适用的迟到保护 | 由迟到回显改写已返回的业务结果；自动重新取卡或发送 |

PNM 去重表示该事件已经获得明确的处理归属，不是“见过任何带这个字符串的输入”。对未匹配输入是否保留记录，要在实现注释和测试中给出理由；不得让低可信输入抢占后续合法精确响应。

成功观察与超时/取消竞争时，以锁内完成的终态转换决定结果；不得让 `select` 随机分支造成“已确认却返回超时”或“已取消却成功”。发送函数自身返回错误与观察事件交错时，保留原有确定未发送/未知结果分类，不能单凭 tracker 绕过传输结果。

### 3.3 资源与生命周期

保留五秒确认预算、三十秒迟到保护和既有去重容量语义；测试使用可控时间，不延长生产等待掩盖丢回显。验证单键多次取消的记录也有界，不能只限制 map 键数。若实现需要补充上限，须明确淘汰后的兼容安全语义并增加回归。

同一账号断连、重连和停止后，旧连接事件不能确认新的 mid。等待器在所有返回分支均清理；账号关闭必须能取消并等待已有任务，不增加独立清理 goroutine。清理前后不得双重关闭 channel、泄漏等待项或丢失新请求。

## 4. 凭证恢复的证据与退避规则

### 4.1 分开五种事实

| 事实 | 合法用途 | 不能据此推导 |
| --- | --- | --- |
| 收到 Set-Cookie | 按当前 Jar 和并发规则持久化 | 登录恢复或签名有效 |
| 静默续期业务成功 | 记录协议调用成功；定时路径执行既有成功语义 | 失效 `_m_h5_tk` 已修好 |
| 完整凭证指纹变化 | Token 缓存失效、凭证版本/并发校验 | 立即重连或清零失败计数 |
| 有效签名输入变化 | 在已有预算内允许一次使用新凭证的尝试 | 平台已接受 Token、账号已在线 |
| 连接 Token 获取成功 / WS 注册成功 | 分别复位 Token 获取失败计数 / 进入在线并结束离线周期 | 在更早的续期回调中提前报告在线 |

不把所有调用方的 `Success` 改为“Cookie 必须变化”。定时续期的正常成功仍执行既有 reload/重建语义；迟到或失败只保存 Cookie。Session 失效、风控、普通网络错误保留各自已有处理，不套用 Token 专属恢复判据。

### 4.2 唯一签名选择语义

协议层提供纯函数或窄快照能力，使用与实际 MTOP 请求一致的 document URL、顶层站点、域/路径/分区、过期与 HttpOnly 过滤、同名 Cookie 顺序。已有 `mtopRequestCookies` 是对照依据，不能用 auto-login 插件的“同名末值”解析规则替代 MTOP 首值规则。

完整 Jar 存在时为权威来源；权威空 Jar 不能回退旧扁平 Cookie。只有无完整快照的历史账号使用原有扁平兼容语义。返回值仅限敏感内存中的比较，不记录明文、摘要或完整 metadata 到日志/HTTP。

分别识别“有效值未变”“有效值变化且非空”“无法建立证据”。sdkSilent、last_refresh_at、无关 metadata、未命中当前作用域的同名 Cookie、已失效或 HttpOnly 候选变化，都不能被算作有效签名恢复。完整 `_m_h5_tk` 值及实际参与签名的部分应明确区分：只改变后缀而未改变有效签名输入，不能重置失败计数或无限获得立即重试资格。

先核对失败请求实际采用的快照与响应内 Token 轮换：不能只在错误处理函数开始时读取数据库作为“原始 Token”。若失败请求仍用旧值而另一个流程已写入新值，必须识别当前最新凭证已有可尝试变化，避免重复续期；若失败响应自身已轮换，要保留该信息。并发更新后的下一请求仍经过原有最新凭证读取及 Token 绑定检查。

### 4.3 引擎与账号任务

引擎不再以完整指纹变化分支调用 `resetFailures()`。有可用签名变化时，仅允许已有恢复流程中的一次立即尝试；若新尝试仍失败，必须回到正常、可取消的退避，直到真实 Token 获取成功后才重置对应失败状态。即使每轮产生不同但被平台拒绝的签名，也不得形成无限零退避循环。恢复状态由账号/连接协调器独占，写明生命周期和复位点。

生产仓储读取失败、账号不存在、权威快照无有效签名、续期写回失败、并发冲突或上下文取消时，不能沿 `canCheck=false` 兼容分支当作恢复成功。测试替身必须显式提供所需凭证事实，不能用“缺少数据库就算成功”保留生产旁路；保留必要兼容接口，不新增运行时必需依赖 setter。

账号自动任务共用同一证据判断。读取前/后凭证失败、从非空变为空都不返回“已恢复”；真实变化时结束本轮并沿既有扫描/任务重试预算继续。保留已完成动作记录，不重置 attempt_count，不重放已成功评价/擦亮；Token 失效不能误写 Session 永久阻断状态。

## 5. 必须落地的测试矩阵

测试先加入正式仓库并在旧实现上失败，再实施修复。新增测试使用统一前缀 `TestReleaseReliability`，原测试继续保留，以便下面的验收命令准确选中。不能把历史失败改为成功、删除断言或改夹具以迁就实现。

### 5.1 消息确认

| 编号 | 场景 | 关键断言 |
| --- | --- | --- |
| E01 | A 超时，B 同文，B 精确响应先到，A 迟到 | 只有 B 成功；B 不等待到超时；A 不复活 |
| E02 | 与 E01 相同，A 迟到先于 B 成功 | A 不确认 B；B 仍能成功 |
| E03 | A/B/C 同文并发，任意响应顺序，部分取消 | 每个 mid 只影响自己，成功数与合法响应数相符 |
| E04 | 无 mid 推送先到，随后同 PNM 精确响应；反向顺序 | 都只确认正确请求一次，不抢占 PNM |
| E05 | 错 mid、错正文、错类型、错会话、错对端、重复 PNM | 不串单、不误确认、不污染随后合法响应 |
| E06 | 历史无 mid 等待器与关联等待器混合 | 正文兼容保护不影响精确请求，也不把不明确推送归给关联请求 |
| E07 | 明确拒绝、确定未发送、未知错误、Context 取消 | 分类正确；只在适用路径保留保护，所有路径无残留等待项 |
| E08 | 观察与超时/取消同时就绪；重复取消 | 终态稳定、无双 close、无成功/错误自相矛盾 |
| E09 | 保护过期、4096 容量边界、单键多取消 | 时间边界、淘汰后行为和内存上界明确 |
| E10 | 旧连接事件晚到、账号停止并重启、跨账号相同正文 | 旧事件不确认新请求，取消/Join 可收束 |
| E11 | 实际 WS 请求 mid → 响应摘要 → engine 等待器 | 使用本地 WebSocket 服务；不由测试手工调用 tracker 冒充全链路 |
| E12 | 自动发货超时后人工补发相同内容 | SQLite 中原始快照、库存与占用正确；新精确响应可收口；不额外取卡、不重复确认发货 |

E01–E08 覆盖文本与图片。人工文本/图片/商品卡片既有状态机继续回归；不能让自动化新等待逻辑改变人工聊天的响应契约。事件排列测试用有限模型、同步屏障和明确的预期终态，随机调度/短 sleep 只能作为补充，不能是主要证据。

### 5.2 凭证与重连

| 编号 | 场景 | 关键断言 |
| --- | --- | --- |
| T01 | 续期只改变 sdkSilent、无关 Cookie、metadata 或修订时间 | 不清零失败计数，不绕过退避，不宣布签名恢复 |
| T02 | 有效签名确实变化，下一次 Token 成功 | 使用新值发起既有请求；成功后在正确位置复位计数 |
| T03 | 每轮无关 Cookie 都变化，Token 连续失败 | 完整 Account.Run 中实际拨号/续期次数有界，退避可观察且可取消 |
| T04 | 每轮有效签名变化但平台持续拒绝 | 立即尝试预算不被反复刷新；失败仍进入退避 |
| T05 | 前/后仓储读取失败、删除账号、写回失败、冲突、取消 | 无假恢复，错误可定位，无额外平台动作 |
| T06 | 空值、权威空 Jar、完整 Jar 与扁平值不一致 | 权威来源优先，不能复用旧扁平签名 |
| T07 | 同名多域/路径/分区、HttpOnly、Secure、过期、无关作用域变化 | 与真实请求选取同一个签名输入 |
| T08 | 仅 Token 后缀变化、字符串重排、相同值重复写回 | 不错误刷新无退避预算；缓存绑定规则仍保留 |
| T09 | 请求在途时另一路已完成续期、响应自身轮换 Token | 以实际失败请求为起点识别新证据；不覆盖最新 Jar、不多续期 |
| T10 | 自动评价/擦亮遇到 Token 失效，包含部分成功 | 不重做已完成动作，不绕过累计重试上限，不误入 Session 阻断 |
| T11 | Session 失效、风控、普通网络失败 | 保留既有分类和恢复/终止行为，冻结 CAPTCHA 不受影响 |
| T12 | 定时续期正常成功但 Cookie 不变，或 Promise 超时后迟到 | 正常成功沿原有重建语义；迟到仅保存 Cookie，不伪装成功 |

T03/T04 必须走 Account.Run + 本地/可控 Token 客户端 + WS 替身，统计实际拨号、续期、Token 获取及退避调用。不得只测试 helper 返回布尔值。可使用私有构造期时间/等待依赖；不得新增生产配置开关或可变 setter 来缩短测试。

## 6. 同类路径审查与防止再次遗漏

实现者提交一份调用方核对表，逐项填写“证据来源、允许动作、失败动作、对应测试”。至少覆盖：

- engine 的两种观察入口、文本/图片发送、所有取消方法、连接轮换和停止。
- Token 获取错误处理、成功计数复位、历史 `handleMaxFailures`、登录态检查、缓存绑定与注册前后复核。
- automation 账号任务、确认发货与改价；adapter 的订单/商品/批量发布恢复入口。
- MTOP 的账号任务、商品列表、详情、聊天商品、订单详情、已售订单、发布、确认发货、改价和资料查询中的 Token 重试。
- renewal 定时正常与迟到响应，以及 adapter 即时恢复共享结果。

这不是要求所有入口具有相同业务策略，而是共享事实解释：业务失败回调成功不等于签名有效，凭证变化不等于平台接受，同 PNM 不等于同请求。保持端点原有尝试次数和操作幂等语义。只有确认同一根因的入口进入本次修改；其他发现单独记录证据，不能借机扩大重构。

修复后进行定向反向验证：在隔离副本/overlay 中分别恢复“取消记录优先于 mid”“先占用 PNM 再判归属”“任意 Cookie 变化即恢复”“读取失败仍算成功”“续期回调即清零失败计数”。对应正式测试必须失败；逐项记录测试名和失败断言。此验证证明测试能拦住错误逻辑，不能代替全部回归，也不能留下临时反向修改。

最终审查逐条对照事件决策表和 E/T 矩阵，不以“总测试数增加”“覆盖率变高”或注释声称正确作为通过依据。

## 7. 执行顺序与验收

以下工作在同一个修复任务中连续完成，不单独交付中间状态。

1. 固定源代码基线；确认既有未提交内容和冻结范围。把两个临时复现转为正式测试，记录旧实现失败证据。
2. 完成事件决策和签名证据纯逻辑测试，补齐 E/T 关键交错。先定义终态、数据来源及预算，再改实现。
3. 实施 tracker 原子状态转换与 Token 恢复判断；同步必要调用方及中文语义注释。清理实质重构单元的历史注释债务，不扩大 baseline。
4. 加入本地 WS、Account.Run 和 SQLite 自动化链路回归，完成所有矩阵行及调用方核对表。
5. 执行定向反向验证，再执行最终源代码快照上的全部门禁。后续代码变更使受影响证据失效，重跑相关验证。
6. 一次性记录最终结果与外部验证缺口。全部通过后才把本计划改为完成，并在总计划追加最终证据；若后续提交，采用一条中文最终提交，不做中间提交。提交/发布不由本计划自动执行。

正式验收命令：

```bash
go test ./internal/engine ./internal/xianyu/ws ./internal/xianyu/mtop ./internal/automation ./internal/adapter ./internal/renewal -run '^TestReleaseReliability' -count=1
go test -race ./internal/engine ./internal/xianyu/ws ./internal/xianyu/mtop ./internal/automation ./internal/adapter ./internal/renewal -run '^TestReleaseReliability' -count=20
go test -race ./internal/engine ./internal/automation ./internal/adapter ./internal/renewal -count=1
go test ./... -count=1
make check
go test -race ./internal/server -run 'TestRun_|TestPublishWorkerTrackingWaitsForCompletion|TestPublishRecoveryLifecycleStopsBeforeWorkerWait|TestUpdateRunningCookieWakesCredentialBlockedAutomationWithoutManager|TestSetCookieStatusWaitsForCredentialTransition|TestDeleteCookieRechecksOwnershipInsideCredentialLock' -count=1
npm run typecheck --prefix frontend
npm test --prefix frontend
make cover
make cover-frontend
make cover-browser
RUN_BROWSER_INTEGRATION=1 go test ./internal/browser -count=1
git diff --check
```

命令通过前还必须核对测试列表和运行日志：新测试不能被正则漏选，出现 `[no tests to run]` 不算验收。重复 race 只用于本次并发状态机压力验证，不可替代确定性的事件排列。

`make check` 含全库 fmt：先仅格式化本次修改文件，确认不会改动冻结实现或无关工作区。若预检发现既有文件会被重写，逐一运行 architecture/api-check/vet/lint/test/comments 等等价检查并记录原因，不以门禁为名批量格式化无关内容。

构建 server 到临时路径，使用临时空 SQLite、回环地址和隔离端口启动默认浏览器；必须看到 Chromium 就绪、`/health` 200、SIGTERM 后正常退出，无遗留子进程。用前端临时输出目录重新构建并逐文件核对内嵌资源；若实际修改前端，按仓库要求重建并提交内嵌产物。

覆盖率报告包含命令、是否启用 RUN_BROWSER_INTEGRATION、Go 与前端 statement 百分比、此次修改函数/分支覆盖情况。审查时参考值为 Go 81.3%、前端 78.76%、本地浏览器包 64.2%，这些是历史观察值而非本次完成证据。所有新改确定性路径必须有测试，未覆盖业务路径必须分类，不能降阈值、排除文件或把确定性分支写成平台例外。覆盖率产物不提交。

## 8. 完成判据与发版边界

只有下面全部满足，才可声称本轮修复完成：

- 两个原始复现先失败后通过，正式用例进入仓库；所有 E01–E12、T01–T12 有测试名、断言和执行结果对应。
- 有精确响应的新消息不再被旧保护误伤，旧/未知响应仍不能确认其他请求；取消、清理和去重具备一致终态。
- sdkSilent 和无关凭证变化不能触发零退避重连；有效签名变化也不能无限刷新立即尝试预算；真实成功在正确节点复位计数。
- 阅览完整生产调用链的测试证明实际消息、取卡、拨号、续期、持久化数量正确，而非只证明 helper 工作。
- 调用方核对表和反向验证记录齐全；既有库存保护、补发互斥、Cookie 并发保护、HTTP 契约、冻结行为回归全部通过。
- 最终源代码快照上的检查、构建和启动验证完成，所有未通过项明确列出；没有“先标完成、随后补测”。

真实账号验证单独列示：MTOP Token 实际过期与恢复、真实消息回显、真实确认发货与擦亮、平台灰度 Cookie 行为。不得为了测试主动破坏现有账号凭证或重复给买家发卡。无账号条件时明确写未验证，不能声称平台全场景通过。

MySQL/PostgreSQL 未配置连接时只能声明 SQLite 验证通过；若实现最终涉及 SQL/事务，必须补齐可用三方言回归。桌面/Docker 包仍走原发布工作流，在每个架构完成既有测试、Chromium 启动和打包服务健康检查后才能发布 manifest。代码修复完成与新版本发布就绪是两项不同结论。

本计划的目标是通过明确不变量、组合场景、生产接线和反向验证消除已经反复暴露的遗漏；不以“保证永不返工”替代证据。
