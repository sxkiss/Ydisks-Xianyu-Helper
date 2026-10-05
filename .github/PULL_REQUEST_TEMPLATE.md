## 改动说明

<!-- 说明背景、解决的问题、主要实现和用户可见变化。 -->

## 关联 Issue

<!-- 例如：Closes #123；没有关联 Issue 时说明原因。 -->

## 验证结果

- [ ] `make check`
- [ ] `npm --prefix frontend run typecheck`（涉及前端时）
- [ ] `npm --prefix frontend test`（涉及前端时）
- [ ] `npm --prefix frontend run build`（涉及前端时）
- [ ] SQLite 定向测试（涉及数据库时）
- [ ] `make test-multidb`（具备 MySQL/PostgreSQL 环境且涉及多数据库时）
- [ ] 浏览器或本地 Playwright 测试（涉及浏览器时）

未运行的验证请说明原因：

## 安全与兼容性检查

- [ ] 未提交真实 Cookie、Token、密码、API 密钥、Webhook、二维码、数据库、订单、卡密或浏览器数据。
- [ ] 未把敏感数据写入日志、错误响应、前端状态、测试输出或截图。
- [ ] 已考虑账号隔离、所有权校验、取消、并发、重试和失败恢复行为。
- [ ] 已同步受影响的 README、Wiki 源文件、OpenAPI 契约、生成产物或发布文档。
- [ ] 未修改冻结的滑块验证行为；如确有授权，已同时更新对应规范和测试。

## 注释与文档

- [ ] 新增或修改的 Go、TypeScript 和 TSX 声明已补充准确的中文注释。
- [ ] 用户可见行为、配置、错误处理或迁移边界发生变化时，已更新中文文档。
- [ ] 本 Pull Request 不包含与目标无关的格式化、依赖升级或文件重命名。
