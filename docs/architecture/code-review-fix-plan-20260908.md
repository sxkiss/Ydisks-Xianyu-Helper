# 2026-09-08 深度代码审查修复记录

## 基线与范围

修复前工作区快照为 `f8f05ce281b598076d7eede85c4004a8b60464fd`，提交说明为：

> 完善聊天商品卡片与本地会话删除，调整平台错误重试语义

本轮只补全发送确认、取消、并发交错、认证撤销和本地恢复边界。闲鱼 Cookie、Token、Cookie Jar、MTOP 参数与错误重试、WebSocket 注册/心跳/退避、平台联系人和历史分页预算、风控恢复及冻结 CAPTCHA 均以快照为基线，未增加探活、预刷新、补拉、重发或凭证恢复入口。

## `refresh=true` 调用清单

保留的平台同步入口只有四类：聊天页首次选中账号的一次刷新、用户点击刷新会话、平台联系人仍有更多时的加载更多、普通实时消息对应会话不在已加载联系人列表时的既有补齐入口。它们继续使用原联系人分页、身份补全和请求预算。

页面初始化、应用壳未读红点、只剩本地游标的加载更多、删除成功或失败后的列表校正、管理 WebSocket 重连/订阅恢复、缓冲区溢出恢复、发送状态保存失败和发送结果未知均使用固定 `refresh=false` 的 `reloadLocalSessions` 或原本地读取，不会触发平台联系人、身份补全、历史、已读、Token 或 Cookie 操作。恢复列表不会自动选中会话，因此不会间接触发历史查询和已读上报。

## 十项修复落地

1. WebSocket 聊天发送沿用既有单请求和 mid pending 关联，严格按 `code=200` 确认；参数/未发送、明确拒绝、未知结果分别分类。未知结果写入 `uncertain`，HTTP 502 返回 `chat_send_uncertain`、`chat_image_send_uncertain` 或 `chat_item_card_send_uncertain`，前端提示先到闲鱼核对，不提供普通失败重试。
2. 管理 WebSocket 在 ready、每个业务帧和五秒空闲 tick 前校验本地 session，事件同时校验账号归属；校验只读本地数据库，失效关闭为 `1008/session_invalid`，数据库错误关闭为 `1011`，前端收到失效原因后停止重连并派发 `auth:logout`。
3. 新增 00046 三方言迁移和 `auth_version`。生产登录使用密码校验快照加事务内用户锁和认证代次确认后签发 session；改密、改用户名、管理员重置递增代次并撤销 session。旧 SHA-256 到 bcrypt 的 CAS 升级不改变代次。
4. 新增 00045 三方言 `local_messages_cleared_at`，删除事务分离可见性、本地实时截止和平台历史截止；实时/历史消息均严格大于各自截止线。
5. 删除期间本地列表请求代次和 AbortController 隔离，目标会话实时事件暂缓；成功/失败只做本地读取，不触发平台刷新或自动选中。
6. 新增 `internal/money.ParseYuanToCents`，拒绝符号、指数、货币符号、逗号、空小数和两位以上小数，在乘法前检测溢出，改价平台调用入口不变。
7. 通知外部发送成功后的 outbox 确认使用 `WithoutCancel` 的独立五秒上下文；确认失败再使用另一独立上下文进入不确定隔离，保留租约 CAS，不启动下一条发送。
8. 管理订阅保存 userID，发布时在锁外按 `cookies.user_id` 查询归属；查询失败关闭订阅，队列满关闭对应订阅，新账号加入无需管理端重连，查询只读归属字段。
9. 正式发布 job 固定并发组 `production-release-publish`，审批后重新抓取 tags、复核最高版本和标签 SHA，旧版本在任何远端写入前退出；离线校验脚本位于 `tools/release/check-version.sh`。
10. OpenAPI、生成 TypeScript 类型和嵌入式前端资源同步更新。

## 验收记录

- `npm run typecheck --prefix frontend`：通过。
- `npm run build --prefix frontend`：通过并更新 `internal/webui/static`。
- `go test ./internal/db -count=1`：通过，包含 00045/00046 回滚与升级测试。
- `go test` 重点包：`internal/chat`、`internal/server`、`internal/notify`、`internal/application/chat`、`internal/automation`、`internal/engine`、`internal/xianyu/ws` 通过。
- `go test ./... -run '^$'`：全仓编译通过。
- `make comments`、`make check`：架构、API 契约、vet、lint、全量 Go 测试和中英文注释门禁通过。
- `make test-server-race`：通过；认证与删除专项 `go test -race ./internal/db -run 'TestCreateVerifiedRejectsStaleAuthVersion|TestHideAndClearSession|TestMultiDBChatSessionDeletionMigration' -count=1` 通过。全量 `internal/db` race 还包含既有 `TestOrdersBatch3000Timing`，在十分钟测试上限内超时，未发现本轮修改的 race 报告。
- `make cover`：Go statements 81.1%；`make cover-browser`（`RUN_BROWSER_INTEGRATION=1`）：browser statements 64.1%；`make cover-frontend`：frontend statements 78.82%。未连接真实平台，真实账号/外部平台路径属于环境例外。
- 平台访问基线：本轮未新增 MTOP、联系人、历史、已读、Cookie 写回、Token 获取/续期、闲鱼重连或风控恢复调用；发送确认仍为每条消息一个既有 WebSocket 业务请求，未知结果不查询、不补拉、不重发。
- 未连接真实闲鱼账号、未执行真实发送、交易、风控或线上发布；浏览器和多数据库完整门禁需在隔离环境执行。

## 交付提交

修复完成且全部门禁通过后创建中文提交：

```text
修复消息投递确认、认证撤销与聊天删除一致性等十项问题
```
