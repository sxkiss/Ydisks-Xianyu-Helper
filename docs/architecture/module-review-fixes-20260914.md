# 2026-09-14 模块复查问题修复

本次范围是用户确认修复的五项问题，属于六阶段完成后的窄范围稳定性修复。没有新增迁移、HTTP API 或前端契约，没有改变冻结 CAPTCHA。原有 `internal/engine/dispatch.go`、`internal/server/task_registry.go`、`internal/server/task_registry_test.go` 的功能改动保留；本轮只为通过既有注释门禁补充中文语义注释。

复核 CI/CD 时发现 Dockerfile 的 Playwright 下载阶段运行在 `node:24-trixie-slim` 中，而该基础镜像没有系统 CA。已在下载阶段安装 `ca-certificates`，避免缓存未命中时 `browser-install` 的 Go TLS 请求因 `x509: certificate signed by unknown authority` 失败。

## 修复与回归场景

| 问题 | 最终行为 | 确定性验证 |
| --- | --- | --- |
| 商品局部更新覆盖并发交付开关 | 应用传递可选字段，由仓储单条 UPDATE 原子保留省略列；不存在或已软删除时返回不存在 | 真实 SQLite 在读取标题后先提交交付开关；三方言验证空补丁、显式空字符串/false、true、账号隔离、不存在、软删除、取消；驱动 RowsAffected 错误透传 |
| 旧订单发货响应出现在新弹窗 | 弹窗关闭、换单或卸载使旧结果失效；保留全局在途忙碌保护，防止重复提交外部发货 | 迟到成功、迟到失败、卸载和在途重复提交；旧响应不得展示或刷新旧列表 |
| 旧删除影响新筛选分页 | 账号、状态、搜索或页码变化后旧删除结果失效；分页最小为 1；旧 finally 不清理新删除状态 | 使用真实 React 页码状态，覆盖切换及切回原账号、连续删除的旧失败、新成功刷新 |
| 通知测试取消后一直忙碌 | 替换测试的保存、删除、启停及关闭动作主动清理 testingId；卸载取消动作并使旧响应失效 | AbortSignal 取消后实际拒绝 Promise；覆盖五种取消方式及旧请求不能清理新测试或显示旧错误 |
| Cookie 更新使用过期启用状态重启 | 在转换锁内读取当前状态并决定重启，凭证写入锁在此前已释放 | 停用持锁期间完成 Cookie 写入，然后停用落库；更新必须看到停用且不调用 Restart；无竞争时也验证读取受转换锁保护 |

账号问题的影响仍按复查结论界定：原有 engine 会检查停用状态，修复的是不必要的运行实例重启，不将其夸大为停用后仍持续执行平台业务。

## 验证记录

| 命令 | 结果 |
| --- | --- |
| `make check` | 完整通过：fmt、architecture、API 契约、vet、lint（0 issues）、Go 全量测试和 Go/前端中文注释检查均通过 |
| `make cover` | 最终重跑通过，全库 Go statement **81.4%**；未设置 `RUN_BROWSER_INTEGRATION` |
| `make cover-browser` | 通过；命令内部设置 `RUN_BROWSER_INTEGRATION=1`，本地 Chromium 浏览器包 statement **64.1%** |
| `make cover-frontend` | 91 个测试文件、531 个测试通过；前端 V8 statement **79.05%**，不适用 Go 的 `RUN_BROWSER_INTEGRATION` |
| `REQUIRE_MULTIDB=1 go test ./internal/db -run '^TestMultiDB_ItemsPatch$' -count=1 -v` | 配置两个测试数据库 URL 后，SQLite、Docker MySQL 8.4、PostgreSQL 17 全部通过；外部库是本次创建的一次性数据库，容器已清理 |
| `go test -race ./internal/application/account -run '^TestSettingsCookieUpdateWaitsForDisable$' -count=30` | 最终 30 轮通过 |
| `go test -race ./internal/adapter -run 'TestCatalogPatch' -count=10` | 商品并发保留与读后删除回归通过 10 轮；实际初轮与账号包同命令执行，账号替身修正另行重跑，见下文 |
| `make test-server-race` | 服务生命周期与凭证并发子集通过 |
| `go test ./internal/adapter ./internal/application/account -run 'TestCatalogPatch|TestSettings' -count=1` | 包含最终补充的全字段适配测试，通过 |
| `npm run typecheck --prefix frontend` | 通过 |
| `npm run build --prefix frontend` | 通过，已同步 `internal/webui/static` 嵌入资源 |
| `go build -o /tmp/ydisks-fix-server ./cmd/server` | 通过 |
| `docker build -f Dockerfile.debian13 ...` | 使用匹配的 Linux arm64 runtime 缓存完成完整镜像构建；镜像内 headless Chromium 启动、服务迁移至版本 49、`/health` 的 `status/database` 均为 `ok`。首次无缓存构建的 CDN 下载曾因本机限速停滞，随后使用同版本缓存完成正式路径验证 |
| `node frontend/scripts/check-comments.mjs --mode check --root frontend` | 前端全部通过 |
| `git diff --check` | 通过 |

最终普通 Go 报告中，本次涉及的 `Items.Patch`、`itemPatchBool`、`ItemCatalogRepository.Patch`、`CatalogMutationService.Update`、`SettingsService.UpdateSettings` statement 均为 100%。覆盖率文件 `cover*.out`、`frontend/coverage/` 仅为本地生成产物，不纳入提交。

初次检查曾发现本次开始前已有未提交修改的三个文件存在 24 项注释缺项；已补充准确中文语义注释并通过最终门禁，没有新增 baseline 或降低规则。

首次定向 race 发现新账号测试继承的串行替身在解锁计数上发生竞争。测试已覆盖为真实凭证互斥锁，最终连续 30 轮通过。首次 `make cover` 与其他全量检查并行时，既有 `TestOnPasswordLoginRefreshConcurrentCallersShareResult` 的 5 毫秒 Promise 预算超时；没有修改该测试、增加预算或放宽断言，随后独立重跑 `make cover` 全部通过。初轮失败保留在 `/tmp/ydisks-fix-race.log` 与 `/tmp/ydisks-fix-cover.log`，最终覆盖率日志在 `/tmp/ydisks-fix-cover-final.log`。

## 范围与环境例外

本次新增业务场景全部使用确定性夹具，没有新增真实账号或外部平台依赖的跳过项。实际验证的外部进程只有隔离数据库和本地 Chromium；没有实测真实订单发货、真实通知投递、真实账号 Cookie 更换/恢复或平台风控。上述真实平台流程属于未执行的环境例外，不能由本地测试推断端到端平台成功。

首次无缓存 Docker 构建的 Chromium CDN 下载受本机网络限速影响；使用匹配的架构 runtime 缓存后完整镜像构建和健康检查均已通过。Docker CI 的正式路径会在构建前准备并缓存目标架构 runtime，再由 Dockerfile 复制并校验，不依赖构建阶段网络下载浏览器。

未覆盖的其他历史业务代码沿用总计划既有分类；本次不提高覆盖率数字而删除测试、忽略生产分支或调整阈值。没有启动或替换用户正在运行的服务，没有提交或发布本次改动。
