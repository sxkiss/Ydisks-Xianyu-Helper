# 发布可靠性修复记录：发货补发与聊天分页

日期：2026-09-12
状态：已实施，未发布

## 修复目标

本记录对应发布前审查复现的四个问题：人工补发失败后可能重新整批取卡、模板后续取卡失败丢失前序凭证、合法空模板消息被错误计为缺发，以及删除会话中断联系人分页后忙碌状态无法恢复。

历史订单不做批量回填或推算。只有保存了完整原始动作计划、账号和订单归属，以及足够的加密发货凭证时才允许继续；证据不足的记录进入人工核对。

## 实施边界

- `ReleaseDeliveryReplay` 在恢复到 `needs_review` 时保留 `action_started`，人工补发发送、确认或结果收口失败始终使用未知结果提示，异常策略只允许取消，不能用 `continue` 跳过确认发货。历史记录若已有发送数量但动作占用标志缺失，发货动作只允许取消，不能普通重试；跳过、已发送和未知单位超出冻结计划时在领取前拒绝。
- 模板执行器在所有取卡、渲染、发送和库存回滚出口返回累计结果。凭证新增 `skipped_template_messages`，按原始动作和消息下标记录已恢复库存且渲染为空的消息；完整性统一按 `prepared + unknown + skipped == expected` 计算。
- 补发计划只读取运行创建时的冻结动作计划，校验跳过位置唯一、有效且属于启用模板动作；缺少位置证据、重复或越界均拒绝。现有加密 JSON 凭证承载该可选字段，不新增数据库列和迁移。
- 聊天 Hook 在账号切换、删除开始和删除收口时统一取消联系人请求并清除 `contactsLoading`；删除期间阻止目标账号新分页，代次检查继续阻止旧响应覆盖当前列表。平台游标保持不变，本地恢复只使用 `refresh=false`。

未修改冻结滑块实现、商品列表省略 `cardList` 的成功语义、HTTP/OpenAPI、账号凭证协议和数据库 schema。

## 验证证据

- `go test ./... -count=1`：通过。
- `go test -race ./internal/automation ./internal/db -run 'Test(ClaimAndReleaseDeliveryReplay|DeliveryReplay|SendTemplate|AutomationIssuePolicy|AutomationRepository)' -count=1`：通过。
- `make test-server-race`：通过。
- `npm run typecheck --prefix frontend`：通过；前端 `89` 个测试文件、`506` 项测试通过。
- `make architecture`、`make api-check`、`make vet`、`make lint`、`make comments`、`git diff --check`：通过。
- `make cover`：Go statement `81.3%`。
- `make cover-browser`（`RUN_BROWSER_INTEGRATION=1`）：浏览器包 statement `64.2%`。
- `make cover-frontend`：前端 statement `78.79%`。
- `go build -trimpath -o /tmp/xianyu-fix-server ./cmd/server` 与 `npm run build --prefix frontend`：通过，嵌入资源已由本次源码重新生成。
- 使用临时 SQLite、临时端口和同目录 `browser-install` 启动服务；Chromium 就绪，`GET /health` 返回 200，收到 SIGTERM 后生命周期正常退出。

真实账号消息投递、平台确认发货、API 卡密服务以及 MySQL/PostgreSQL 外部连接未执行，属于外部环境验证项；不得用本地夹具结果替代这些证据。覆盖率文件和临时运行目录不提交。
