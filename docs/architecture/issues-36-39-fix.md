# GitHub #36–#39 修复与验证记录

日期：2026-09-08 至 2026-09-09。状态：本地工作区已修复，未提交、推送或发布。本次为六阶段完成后的窄范围缺陷修复，不重开阶段；会话角色新增 00047 三方言迁移，不修改冻结滑块行为、注释基线或架构门禁。

## 行为与兼容边界

| Issue | 修复后行为 | 回归证据 |
| --- | --- | --- |
| [#36](https://github.com/Christ9038/Ydisks-Xianyu-Helper/issues/36) | 按本次用户要求移除人工插入订单按钮、弹窗、上传 Hook 和前端调用。新旧导入路径认证后均返回 501 标准错误，不解析上传内容或写入订单；订单一键同步保留。 | 页面入口断言、新旧真实 handler 契约及无写入断言；OpenAPI、生成 schema 与兼容矩阵同步更新。内部历史导入解析及服务测试仍保留，HTTP 已无可执行入口。 |
| [#37](https://github.com/Christ9038/Ydisks-Xianyu-Helper/issues/37) | 会话创建时优先用当前账号的本地商品表确认卖家身份，并把 `account_role`、买卖双方、绑定商品和证据来源写入会话。旧会话安全升级为 `unknown`；所有自动回复身份判断都必须通过本地商品库，商品缺失或本地查询失败时直接不回复，不再读取平台商品发布人。消息先落库和广播，再进行角色核验及自动回复。缺少商品 ID、当前账号是买家、身份缺失、查询失败或取消时均不回复。 | 实时消息/联系人创建阶段本地识别；旧库升级默认值；商品切换失效；本地商品存在/缺失/查询失败；已知买卖角色也受本地商品门禁；买卖双方默认/API 回复隔离。 |
| [#38](https://github.com/Christ9038/Ydisks-Xianyu-Helper/issues/38) | 新版 WS 卡片以字段 2 识别会话，字段 1 不再误作旧版简化会话。付款正文可正确触发 `order_paid`，不会被卡片残留的待付款提醒遮蔽。 | Issue 中脱敏结构、买家副本拒绝、待付款/付款区分、畸形结构、旧版简化结构；自动化中心收到同一订单的新旧付款事件仍只发货一次。 |
| [#39](https://github.com/Christ9038/Ydisks-Xianyu-Helper/issues/39) | 商品级规则优先。账号级付款规则必须含明确布尔值 `allow_all_items: true` 才可兜底；缺失、字符串或坏 JSON 不予授权。列表显示未确认状态，编辑器要求明确勾选，切换账号/商品清除确认。无匹配规则时输出明确告警并停止发货。 | 规则筛选及 UI 状态测试；真实 SQLite/MySQL/PostgreSQL 下验证商品规则优先、未授权规则过滤、授权通用规则生效及商品隔离。 |

#39 的商品同步同时拒绝缺失/畸形 `cardList`、不能识别的商品、提前空页、重复页、总数变化/不一致，以及达到页数上限却未取完的数据。平台 `auto_` 占位卡计入分页条数，避免过滤后误判末页。出错时不返回可提交的部分列表，应用层回归证明原商品与规则均不变。真正完整的空列表仍可按现有语义下架本地商品。

用户已确认接受完整性所必需的少量追加分页请求：平台未返回总页数且当前原始卡片仍填满一页时，继续读取下一页，直到出现短页、明确总页数终点或既有 `max_pages` 上限。该行为只发生在原有商品全量同步操作中，不增加失败重试、Token 刷新、后台周期同步或其他 MTOP 调用；确定性测试覆盖“首页含 `auto_` 占位卡、第二页仍有真实商品”的两次请求边界。

商品再次出现在完整远端列表后，现有 `UpsertBasic/SyncFromRemote` 会清除软删除状态；本次未重复实现这段已有逻辑。三方言回归确认：同 ID 商品恢复可见，保留本地描述和多规格/多数量设置；新 ID 创建独立商品；已软删除规则不会自动复活，需要重新配置，避免误发旧内容。

升级后，已有账号级付款规则若未确认全部商品范围，将停止兜底。商品专属发货应选择具体商品；确实通用的内容可在规则编辑器中勾选确认后保存。不会根据规则名称或模板内容自动猜测授权。

自动回复身份判断只读取本地商品归属；不持凭证锁执行外部 I/O，也不缓存或输出明文凭证。旧会话、买家侧新会话及已确认角色均先经过本地商品库门禁，商品不在当前账号有效商品集合或本地查询失败时固定为不可回复；本地商品存在时，未知角色写入 `local_item` 卖家角色。商品变化会使旧结论失效，本地商品同步仍可用确定证据恢复卖家角色。商品多规格只使用列表卡片的 `isSKU` 等列表事实，付款后的规则匹配直接使用订单数据；自动回复和商品同步均不再调用 `mtop.taobao.idle.pc.detail`。没有增加轮询、重试、联系人刷新、历史查询、Token 刷新、WebSocket 注册或重连，也未增加浏览器登录或变更 CAPTCHA 恢复路径。

聊天会话 API 和打包前端同步改为 `peer_user_id`、`peer_name`、`peer_avatar_url`，明确这些字段始终表示当前账号之外的对端；`buyer_user_id` 与 `seller_user_id` 只保存已经确认的业务角色。买家备注入口仅在当前账号角色为卖家且买家 ID 已确认时显示。数据库历史物理列 `buyer_id/buyer_name/buyer_avatar_url` 继续承载对端展示值，避免旧库复制重建大表；该历史列名不会再暴露到聊天应用模型或 HTTP/前端会话契约。

## 验证

| 命令/场景 | 结果 |
| --- | --- |
| `make check` | 通过：格式、架构、OpenAPI 生成漂移、真实路由/operation/响应契约、vet、lint、Go 全库测试及中文 AST 注释门禁。lint 为 0 issues。 |
| `npm run typecheck --prefix frontend` | 通过。 |
| `make cover-frontend` | 89 个文件、504 个测试通过；V8 statement 78.74%，branch 57.26%，function 67.06%，line 81.94%。未使用 `RUN_BROWSER_INTEGRATION=1`，它是 Go 浏览器测试开关。 |
| `make cover` | 通过；未设置 `RUN_BROWSER_INTEGRATION`，Go statement 81.2%。 |
| `make cover-browser` | 通过；命令实际设置 `RUN_BROWSER_INTEGRATION=1`，本地 Chromium 测试完成，浏览器包 statement 63.9%。 |
| `make test-server-race` | 通过。 |
| `go test -race ./internal/chat ./internal/engine ./internal/adapter ./internal/application/chat -count=1` | 通过，覆盖会话角色创建与持久化、身份查询、消息先落库广播的时序、生命周期及买卖双方回复隔离。 |
| `go test ./internal/db -run 'TestConfirmedDeliveryRules\|TestMultiDB_Issue39' -count=1 -v`，提供临时 MySQL/PostgreSQL 测试 URL | SQLite、MySQL 8.4、PostgreSQL 17 全部通过。本轮仅运行该三方言专项，不将其表述为完整 `make test-multidb`。 |
| `REQUIRE_MULTIDB=1 go test ./internal/db -run '^TestMultiDB(ChatSessionRolePersistence\|_ChatBuyerSuffixMatch)$' -v -count=1`，提供临时 MySQL/PostgreSQL 测试 URL | SQLite、MySQL 8.4、PostgreSQL 17 全部通过，验证 00047 最终结构、角色持久化、商品切换失效及买卖方订单匹配隔离。 |
| `npm run build --prefix frontend` | 通过；已更新 `internal/webui/static` 嵌入资源。 |
| 服务构建与启动 smoke | 使用临时数据库、随机 loopback 端口启动新二进制，未添加 `-no-browser`；`/health` 返回数据库及服务正常，首页包含构建资源，SIGTERM 正常退出。 |
| `git diff --check` | 通过；冻结浏览器源码、注释基线及覆盖率配置无改动。 |

本地完整日志保存在 `/tmp/issue36-39-check-complete.log`、`/tmp/issue36-39-cover-verified.log`、`/tmp/issue36-39-frontend-cover.log`、`/tmp/issue36-39-browser.log`、`/tmp/issue36-39-race.log` 和 `/tmp/issue36-39-multidb.log`。这些是临时验证记录，不是长期发布附件。覆盖率报告保持生成文件，不提交。

## 覆盖边界与例外

- 确定性行为：本次新增的身份授权、通用发货授权、卡片结构分类、分页完整性、读取/网络错误和同步失败不写入均有本地回归。新增身份门禁与通用规则筛选函数语句覆盖率均为 100%。全库既有未覆盖确定性分支仍属于后续测试补齐工作；例如详情 Token 主动刷新成功后的赋值及部分取消等待分支，不以真实平台依赖为由跳过。
- 环境行为：本地 Chromium、三种数据库、服务健康检查和关闭均实际运行。未执行 Windows/macOS/Linux 安装包或 Docker 发布矩阵，这些没有本次代码改动。
- 真实账号/外部平台：未使用真实闲鱼账号验证最新在线商品详情响应、买卖双方真实聊天、真实付款发货或下架再上架交易。以上使用本地 HTTP、Issue 脱敏 WS 帧和合成数据库数据验证；未发送真实消息、卡密或平台确认发货请求，也未修改已部署服务和用户数据库。平台实测是剩余外部验证范围，不能以本地用例宣称已完成真实交易验证。

临时 MySQL/PostgreSQL 容器仅供本次回归使用，测试完成后清理。没有增加忽略路径、降低阈值、改写冻结测试或扩大历史注释基线。
