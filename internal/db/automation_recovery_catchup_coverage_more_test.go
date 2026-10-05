package db

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// seedCatchupOrder 写入一条可被待发货兜底扫描选中的订单夹具。
// 订单必须处于 pending_ship、未删除且带会话标识，才会进入兜底扫描的候选集合。
func seedCatchupOrder(t *testing.T, s *Store, orderID, cookieID, itemID, chatID, orderStatus string) {
	t.Helper()
	// upsertErr 保存订单夹具写入错误。
	upsertErr := s.Orders.Upsert(context.Background(), orderID, OrderUpsertOpts{
		ItemID: itemID, BuyerID: "buyer-" + orderID, CookieID: cookieID, ChatID: chatID,
		OrderStatus: orderStatus, Quantity: "1", Amount: "9.90", SpecName: "颜色", SpecValue: "黑",
	})
	if upsertErr != nil {
		t.Fatalf("写入订单夹具失败: %v", upsertErr)
	}
}

// seedCatchupRule 写入一条 order_paid 自动化规则并返回规则主键。
// 规则必须绑定商品：item_id 为空的规则会被当成通配规则命中所有订单，会破坏过滤矩阵断言。
func seedCatchupRule(t *testing.T, s *Store, userID int64, cookieID, itemID, name string, enabled bool, actions []AutomationActionInput) int64 {
	t.Helper()
	// ruleID、ruleErr 保存规则写入结果与错误。
	ruleID, ruleErr := s.Automation.Create(context.Background(), AutomationRuleInput{
		UserID: userID, CookieID: cookieID, Name: name, ItemID: itemID,
		TriggerType: "order_paid", Enabled: enabled, Actions: actions,
	})
	if ruleErr != nil {
		t.Fatal(ruleErr)
	}
	return ruleID
}

// seedCatchupRun 为订单写入一条 order_paid 运行记录，用于验证兜底扫描的排除边界。
// 也可以把 status/attempt_count/action_cursor 改写为待续跑形态，供续跑扫描测试复用。
// 快照里带一份冻结动作计划：续跑链路的放行判据必须基于它，而不是可被编辑的现行规则。
func seedCatchupRun(t *testing.T, s *Store, ruleID int64, cookieID, orderID string) int64 {
	t.Helper()
	// runID、started、startErr 保存运行写入结果与错误。
	runID, started, startErr := s.Automation.TryStartRun(context.Background(), AutomationRun{
		RuleID: ruleID, CookieID: cookieID, OrderID: orderID, TriggerType: "order_paid",
		TriggerKey: "catchup:" + orderID, RawEventJSON: catchupRunSnapshot(cookieID, orderID, idempotentTailPlanJSON),
		LeaseExpiresAt: time.Now().Add(time.Minute).Unix(),
	})
	if startErr != nil || !started {
		t.Fatalf("写入运行夹具失败: started=%v err=%v", started, startErr)
	}
	return runID
}

// idempotentTailPlanJSON 是「发卡 + 确认发货」冻结计划的 JSON 片段，供运行快照夹具复用。
const idempotentTailPlanJSON = `[{"ActionType":"send_card","Enabled":true},{"ActionType":"confirm_shipment","Enabled":true}]`

// catchupRunSnapshot 按 Task 的字段名拼出运行原始事件快照；续跑链路从这里恢复冻结计划。
func catchupRunSnapshot(cookieID, orderID, planJSON string) string {
	return `{"Source":"scheduler","AccountID":"` + cookieID + `","TriggerType":"order_paid",` +
		`"OrderID":"` + orderID + `","ActionPlan":` + planJSON + `}`
}

// TestPendingShipOrdersWithoutPaidRunAfterReturnsUntouchedOrders 验证兜底扫描能选中完全没有运行的待发货订单。
// 这是 P2 修复的核心：付款系统消息丢失时订单不会留下任何运行记录，只能靠订单状态补触发。
func TestPendingShipOrdersWithoutPaidRunAfterReturnsUntouchedOrders(t *testing.T) {
	// s、cleanup 保存测试数据库及关闭责任。
	s, cleanup := newTestDB(t)
	defer cleanup()
	// ctx 保存本测试共用的数据库上下文。
	ctx := context.Background()
	// userID、cookieID 保存测试账号主键与账号标识。
	userID, cookieID := seedAccount(t, s)
	seedCatchupRule(t, s, userID, cookieID, "item-1", "发货规则", true,
		[]AutomationActionInput{{ActionType: "send_card", MessageTemplate: "card", Enabled: true}})
	seedCatchupOrder(t, s, "o-100", cookieID, "item-1", "chat-100", "pending_ship") // candidates 保存本轮兜底扫描结果。
	candidates, err := s.Automation.PendingShipOrdersWithoutPaidRunAfter(ctx, "", 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Fatalf("应选中 1 条待发货订单: %d", len(candidates))
	}
	// got 保存选中的订单事实，用于校验扫描列完整回填。
	got := candidates[0]
	if got.OrderID != "o-100" || got.ItemID != "item-1" || got.ChatID != "chat-100" || got.OrderStatus != "pending_ship" || got.CookieID != cookieID {
		t.Fatalf("订单事实回填异常: %+v", got)
	}
	if got.Quantity != "1" || got.Amount != "9.90" || got.SpecName != "颜色" || got.SpecValue != "黑" {
		t.Fatalf("订单展示字段回填异常: %+v", got)
	}
	// afterPage 保存越过游标后的第二页结果，稳定游标必须排除已见订单。
	afterPage, err := s.Automation.PendingShipOrdersWithoutPaidRunAfter(ctx, "o-100", 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(afterPage) != 0 {
		t.Fatalf("游标之后不应再有候选: %d", len(afterPage))
	}
}

// TestPendingShipOrdersWithoutPaidRunAfterFilters 验证兜底扫描的全部排除边界。
// 每一条排除规则都对应一次真实线上风险：重复发货、打给无会话订单或对停用规则空转。
func TestPendingShipOrdersWithoutPaidRunAfterFilters(t *testing.T) {
	// s、cleanup 保存测试数据库及关闭责任。
	s, cleanup := newTestDB(t)
	defer cleanup()
	// ctx 保存本测试共用的数据库上下文。
	ctx := context.Background()
	// userID、cookieID 保存测试账号主键与账号标识。
	userID, cookieID := seedAccount(t, s)
	// activeRuleID 保存与 item-1 匹配且启用的发货规则主键。
	activeRuleID := seedCatchupRule(t, s, userID, cookieID, "item-1", "启用规则", true,
		[]AutomationActionInput{{ActionType: "send_card", MessageTemplate: "card", Enabled: true}})
	// disabledRuleID 保存已停用规则主键，用于验证停用规则不再触发补发。
	seedCatchupRule(t, s, userID, cookieID, "item-2", "停用规则", false,
		[]AutomationActionInput{{ActionType: "send_card", MessageTemplate: "card", Enabled: true}})
	// mismatchRuleID 保存商品不匹配但启用的发货规则主键。
	seedCatchupRule(t, s, userID, cookieID, "item-other", "不匹配规则", true,
		[]AutomationActionInput{{ActionType: "send_card", MessageTemplate: "card", Enabled: true}})
	// 无运行记录且启用规则匹配 → 必须被选中。
	seedCatchupOrder(t, s, "o-hit", cookieID, "item-1", "chat-hit", "pending_ship")
	// 订单状态不是待发货 → 平台侧已发货或已完成，不能再补发。
	seedCatchupOrder(t, s, "o-shipped", cookieID, "item-1", "chat-shipped", "shipped")
	// 缺少会话标识 → 没有会话就无法向买家发起发送。
	seedCatchupOrder(t, s, "o-nochat", cookieID, "item-1", "", "pending_ship")
	// 商品没有任何匹配的启用规则 → 没有可执行动作，不能空触发。
	seedCatchupOrder(t, s, "o-mismatch", cookieID, "item-3", "chat-mismatch", "pending_ship")
	// 已有 order_paid 运行 → 交由失败运行恢复链处理，不能重复触发。
	seedCatchupOrder(t, s, "o-hasrun", cookieID, "item-1", "chat-hasrun", "pending_ship")
	seedCatchupRun(t, s, activeRuleID, cookieID, "o-hasrun")
	// 砍价订单未记录免拼成功阶段 → 必须排除，防止兜底推断阶段并抢跑免拼/发卡。
	seedCatchupOrder(t, s, "o-bargain", cookieID, "item-1", "chat-bargain", "pending_ship")
	// bargainUpdateErr 保存砍价标记夹具写入错误。
	if _, bargainUpdateErr := s.DB.ExecContext(ctx, `UPDATE orders SET is_bargain=1 WHERE order_id='o-bargain'`); bargainUpdateErr != nil {
		t.Fatal(bargainUpdateErr)
	}
	// candidates 保存候选集合；未完成免拼阶段的砍价订单不能由订单状态兜底进入。
	candidates, err := s.Automation.PendingShipOrdersWithoutPaidRunAfter(ctx, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].OrderID != "o-hit" {
		t.Fatalf("兜底扫描过滤矩阵异常: %+v", candidates)
	}
	// activeRuleID 仅用于避免未使用变量告警；规则主键本身不参与查询断言。
	_ = activeRuleID
}

// TestPendingShipScansFallBackToDefaultBounds 验证两个兜底扫描在非法入参下回落到默认分页与代次上限。
// 调用方传入 0 时不得把 LIMIT 设为 0 或放开代次上限，否则会造成漏扫或无限重开。
func TestPendingShipScansFallBackToDefaultBounds(t *testing.T) {
	// s、cleanup 保存测试数据库及关闭责任。
	s, cleanup := newTestDB(t)
	defer cleanup()
	// ctx 保存本测试共用的数据库上下文。
	ctx := context.Background()
	// userID、cookieID 保存测试账号主键与账号标识。
	userID, cookieID := seedAccount(t, s)
	// ruleID 保存仅含确认发货动作的规则主键。
	ruleID := seedCatchupRule(t, s, userID, cookieID, "item-1", "默认边界规则", true,
		[]AutomationActionInput{{ActionType: "confirm_shipment", Enabled: true}})
	seedCatchupOrder(t, s, "o-bounds", cookieID, "item-1", "chat-bounds", "pending_ship")
	// 无运行记录的订单用于验证兜底扫描的默认分页回落，有运行的订单不进入该候选集合。
	seedCatchupOrder(t, s, "o-bounds-free", cookieID, "item-1", "chat-bounds-free", "pending_ship")
	// runID 保存待续跑运行主键。
	runID := seedCatchupRun(t, s, ruleID, cookieID, "o-bounds")
	// 把运行置为可续跑形态，使两个扫描都能命中同一条夹具。
	if _, updateErr := s.DB.ExecContext(ctx,
		`UPDATE automation_runs SET status='needs_review', attempt_count=1, action_cursor=0, next_retry_at=0, lease_expires_at=0 WHERE id=?`, runID); updateErr != nil {
		t.Fatal(updateErr)
	}
	// 非法 limit 与代次上限必须回落到默认值而不是清空结果。
	// orders 是 limit=0 时兜底扫描的结果，应命中没有运行记录的 o-bounds-free。
	orders, err := s.Automation.PendingShipOrdersWithoutPaidRunAfter(ctx, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(orders) != 1 || orders[0].OrderID != "o-bounds-free" {
		t.Fatalf("limit=0 应回落默认分页并保留候选: %+v", orders)
	}
	// resumes 是 limit 与代次上限都为 0 时的续跑扫描结果。
	resumes, err := s.Automation.PendingShipResumableRunsAfter(ctx, "", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(resumes) != 1 {
		t.Fatalf("limit=0 与代次上限=0 应回落默认值并保留候选: %d", len(resumes))
	}
}

// TestPendingShipScansReportStorageFailures 验证存储不可用时两个兜底扫描都返回错误。
// 兜底扫描漏报比多报更危险：错误必须上抛给调度器记录，而不是当成“没有候选”。
func TestPendingShipScansReportStorageFailures(t *testing.T) {
	// s、cleanup 保存测试数据库及关闭责任。
	s, cleanup := newTestDB(t)
	defer cleanup()
	// ctx 保存本测试共用的数据库上下文。
	ctx := context.Background()
	// closeErr 保存数据库关闭错误；关闭后存储层必须以错误返回。
	if closeErr := s.DB.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	// _, ordersErr 是存储关闭后的兜底扫描结果。
	if _, ordersErr := s.Automation.PendingShipOrdersWithoutPaidRunAfter(ctx, "", 200); ordersErr == nil {
		t.Fatal("存储不可用时兜底扫描应返回错误")
	}
	// _, resumesErr 是存储关闭后的续跑扫描结果。
	if _, resumesErr := s.Automation.PendingShipResumableRunsAfter(ctx, "", 5, 200); resumesErr == nil {
		t.Fatal("存储不可用时续跑扫描应返回错误")
	}
	// _, reopenErr 是存储关闭后的重开运行结果。
	if _, reopenErr := s.Automation.ReopenRunForRecovery(ctx, 1, 1, 0); reopenErr == nil {
		t.Fatal("存储不可用时重开运行应返回错误")
	}
}

// TestPendingShipResumableRunsAfterSelectsRunsOfEnabledOwnerRule 验证续跑扫描的过滤边界。
// 本查询只负责「运行自身」可判定的条件：状态、代次上限、订单仍在待发货、**归属规则仍启用**。
// 真正的放行判据（剩余动作是否只含幂等状态动作）必须由调用方依据快照里的冻结计划判定，
// 因为 automation_rule_actions 是会被管理员编辑的现行配置：用它判定等于把数字游标套用到改过的规则上。
func TestPendingShipResumableRunsAfterSelectsRunsOfEnabledOwnerRule(t *testing.T) {
	// s、cleanup 保存测试数据库及关闭责任。
	s, cleanup := newTestDB(t)
	defer cleanup()
	// ctx 保存本测试共用的数据库上下文。
	ctx := context.Background()
	// userID、cookieID 保存测试账号主键与账号标识。
	userID, cookieID := seedAccount(t, s)
	// fullRuleID 保存包含发卡与确认发货两类动作的规则主键。
	fullRuleID := seedCatchupRule(t, s, userID, cookieID, "item-1", "发货续跑规则", true,
		[]AutomationActionInput{{ActionType: "send_card", MessageTemplate: "card", Enabled: true}, {ActionType: "confirm_shipment", Enabled: true}})
	// 符合续跑条件：needs_review、代次 1、游标 1（已越过发卡动作）。
	seedCatchupOrder(t, s, "o-resume", cookieID, "item-1", "chat-resume", "pending_ship")
	// resumableRunID 保存符合续跑条件的运行主键。
	resumableRunID := seedCatchupRun(t, s, fullRuleID, cookieID, "o-resume")
	// 卡在发卡动作：游标 0 仍可能再次联系买家，本查询会照常返回，由调用方依据冻结计划排除。
	seedCatchupOrder(t, s, "o-cardpending", cookieID, "item-1", "chat-cardpending", "pending_ship")
	// cardPendingRunID 保存游标仍停在发卡动作的运行主键。
	cardPendingRunID := seedCatchupRun(t, s, fullRuleID, cookieID, "o-cardpending")
	// 已在执行中的运行不属于待恢复状态，必须排除。
	seedCatchupOrder(t, s, "o-running", cookieID, "item-1", "chat-running", "pending_ship")
	// runningRunID 保存状态为 running 的运行主键。
	runningRunID := seedCatchupRun(t, s, fullRuleID, cookieID, "o-running")
	// 代次已达上限的运行说明上游持续异常，继续自动重开会放大压力，必须排除。
	seedCatchupOrder(t, s, "o-exhausted", cookieID, "item-1", "chat-exhausted", "pending_ship")
	// exhaustedRunID 保存代次超限的运行主键。
	exhaustedRunID := seedCatchupRun(t, s, fullRuleID, cookieID, "o-exhausted")
	// 只包含确认发货动作的规则没有消息动作，游标 0 也应可续跑。
	shipOnlyRuleID := seedCatchupRule(t, s, userID, cookieID, "item-2", "仅确认发货规则", true,
		[]AutomationActionInput{{ActionType: "confirm_shipment", Enabled: true}})
	seedCatchupOrder(t, s, "o-shiponly", cookieID, "item-2", "chat-shiponly", "pending_ship")
	// shipOnlyRunID 保存仅含幂等动作规则的运行主键。
	shipOnlyRunID := seedCatchupRun(t, s, shipOnlyRuleID, cookieID, "o-shiponly")
	// 归属规则被停用：即使同账号下还有另一条启用且匹配的规则，也不得续跑该运行，
	// 否则管理员停用规则后仍需人工介入的旧运行会被自动执行完。
	disabledOwnerRuleID := seedCatchupRule(t, s, userID, cookieID, "item-1", "已停用发货规则", false,
		[]AutomationActionInput{{ActionType: "send_card", MessageTemplate: "card", Enabled: true}, {ActionType: "confirm_shipment", Enabled: true}})
	seedCatchupRule(t, s, userID, cookieID, "item-1", "同商品另一条启用规则", true,
		[]AutomationActionInput{{ActionType: "confirm_shipment", Enabled: true}})
	seedCatchupOrder(t, s, "o-ownerdisabled", cookieID, "item-1", "chat-ownerdisabled", "pending_ship")
	// ownerDisabledRunID 保存归属规则已停用的运行主键。
	ownerDisabledRunID := seedCatchupRun(t, s, disabledOwnerRuleID, cookieID, "o-ownerdisabled")
	// bargain 保存两个订单均为砍价订单的持久化事实；其中只有收到最终阶段事实的订单才可续跑。
	bargain := true
	seedCatchupOrder(t, s, "o-bargain-unconfirmed", cookieID, "item-1", "chat-bargain-unconfirmed", "pending_ship")
	// unconfirmedBargainErr 保存无最终砍价阶段订单的砍价标记写入错误。
	unconfirmedBargainErr := s.Orders.Upsert(ctx, "o-bargain-unconfirmed", OrderUpsertOpts{IsBargain: &bargain})
	if unconfirmedBargainErr != nil {
		t.Fatal(unconfirmedBargainErr)
	}
	// unconfirmedRunID 保存无最终阶段事实但游标已越过发卡动作的历史运行。
	unconfirmedRunID := seedCatchupRun(t, s, fullRuleID, cookieID, "o-bargain-unconfirmed")
	seedCatchupOrder(t, s, "o-bargain-ready", cookieID, "item-1", "chat-bargain-ready", "pending_ship")
	// readyBargainErr 保存已收到最终砍价阶段订单的砍价标记写入错误。
	readyBargainErr := s.Orders.Upsert(ctx, "o-bargain-ready", OrderUpsertOpts{IsBargain: &bargain})
	if readyBargainErr != nil {
		t.Fatal(readyBargainErr)
	}
	// readyStageErr 保存最终“成功小刀，待发货”阶段事实写入错误。
	readyStageErr := s.Automation.MarkBargainReady(ctx, "o-bargain-ready", cookieID)
	if readyStageErr != nil {
		t.Fatal(readyStageErr)
	}
	// readyRunID 保存有最终阶段事实且可安全续跑的历史运行。
	readyRunID := seedCatchupRun(t, s, fullRuleID, cookieID, "o-bargain-ready")
	// updateRun 是夹具改造函数：把运行改成指定的状态、代次与游标。
	updateRun := func(runID int64, status string, attempt, cursor int) {
		// updateErr 保存运行状态改写错误。
		if _, updateErr := s.DB.ExecContext(context.Background(),
			`UPDATE automation_runs SET status=?, attempt_count=?, action_cursor=?, action_started=0, next_retry_at=0, lease_expires_at=0 WHERE id=?`,
			status, attempt, cursor, runID); updateErr != nil {
			t.Fatalf("改写运行夹具失败: %v", updateErr)
		}
	}
	// 符合条件与各类排除条件分别落库。
	updateRun(resumableRunID, "needs_review", 1, 1)
	updateRun(cardPendingRunID, "needs_review", 1, 0)
	updateRun(runningRunID, "running", 1, 1)
	updateRun(exhaustedRunID, "needs_review", 5, 1)
	updateRun(shipOnlyRunID, "failed", 1, 0)
	updateRun(ownerDisabledRunID, "needs_review", 1, 1)
	updateRun(unconfirmedRunID, "needs_review", 1, 1)
	updateRun(readyRunID, "needs_review", 1, 1)
	// candidates 保存续跑扫描结果：running 与代次超限被排除，归属规则停用的被排除。
	candidates, err := s.Automation.PendingShipResumableRunsAfter(ctx, "", 5, 200)
	if err != nil {
		t.Fatal(err)
	}
	// gotIDs 收集候选订单，便于断言排除矩阵。
	gotIDs := map[string]PendingShipResume{}
	// candidate 是续跑候选运行，按订单号收集后用于断言排除矩阵。
	for _, candidate := range candidates {
		gotIDs[candidate.Order.OrderID] = candidate
	}
	if len(candidates) != 4 {
		t.Fatalf("应选中 4 条待判定运行: %+v", candidates)
	}
	// 下面三条断言分别锁死三类必须排除的运行：执行中、代次超限、归属规则已停用。
	// ok 保存该订单是否进入了候选集合。
	if _, ok := gotIDs["o-running"]; ok {
		t.Fatalf("running 状态不得进入续跑候选: %+v", gotIDs["o-running"])
	}
	// ok 保存该订单是否进入了候选集合。
	if _, ok := gotIDs["o-exhausted"]; ok {
		t.Fatalf("代次超限不得进入续跑候选: %+v", gotIDs["o-exhausted"])
	}
	// ok 保存该订单是否进入了候选集合。
	if _, ok := gotIDs["o-ownerdisabled"]; ok {
		t.Fatalf("归属规则已停用不得进入续跑候选: %+v", gotIDs["o-ownerdisabled"])
	}
	// ok 保存未收到最终阶段事实的砍价订单是否被错误放入续跑集合。
	if _, ok := gotIDs["o-bargain-unconfirmed"]; ok {
		t.Fatalf("无最终砍价阶段事实的订单不得进入续跑候选: %+v", gotIDs["o-bargain-unconfirmed"])
	}
	if gotIDs["o-bargain-ready"].RunID != readyRunID {
		t.Fatalf("已收到最终砍价阶段事实的订单应可续跑: %+v", gotIDs["o-bargain-ready"])
	}
	if gotIDs["o-resume"].RunID != resumableRunID || gotIDs["o-resume"].Status != "needs_review" || gotIDs["o-resume"].ActionCursor != 1 {
		t.Fatalf("needs_review 候选回填异常: %+v", gotIDs["o-resume"])
	}
	if gotIDs["o-shiponly"].RunID != shipOnlyRunID || gotIDs["o-shiponly"].Attempt != 1 {
		t.Fatalf("仅确认发货候选回填异常: %+v", gotIDs["o-shiponly"])
	}
	// 冻结计划必须随候选一起返回：调用方要用它判定放行，不能改用现行规则的动作。
	if !strings.Contains(gotIDs["o-resume"].RawEventJSON, `"ActionPlan"`) {
		t.Fatalf("候选未回填运行快照，调用方无法恢复冻结计划: %+v", gotIDs["o-resume"])
	}
	// 稳定游标必须跳过已见订单。
	// afterPage 保存越过 o-cardpending 后的后续候选，顺序仍按订单号升序。
	afterPage, err := s.Automation.PendingShipResumableRunsAfter(ctx, "o-cardpending", 5, 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(afterPage) != 2 || afterPage[0].Order.OrderID != "o-resume" || afterPage[1].Order.OrderID != "o-shiponly" {
		t.Fatalf("续跑扫描游标分页异常: %+v", afterPage)
	}
}

// TestReopenRunForRecoveryRequiresUnchangedAttempt 验证重开运行必须以原代次抢占成功。
// 代次变化说明另一个 worker 已经动过这条运行，抢占失败必须让调用方放弃，避免重复发货。
func TestReopenRunForRecoveryRequiresUnchangedAttempt(t *testing.T) {
	// s、cleanup 保存测试数据库及关闭责任。
	s, cleanup := newTestDB(t)
	defer cleanup()
	// ctx 保存本测试共用的数据库上下文。
	ctx := context.Background()
	// userID、cookieID 保存测试账号主键与账号标识。
	userID, cookieID := seedAccount(t, s)
	// ruleID 保存发货规则主键。
	ruleID := seedCatchupRule(t, s, userID, cookieID, "item-1", "重开规则", true,
		[]AutomationActionInput{{ActionType: "confirm_shipment", Enabled: true}})
	// runID 保存待重开的运行主键。
	runID := seedCatchupRun(t, s, ruleID, cookieID, "o-reopen")
	// statusErr 保存把运行置为人工核对状态的改写错误。
	if _, statusErr := s.DB.ExecContext(ctx, `UPDATE automation_runs SET status='needs_review', attempt_count=1, action_cursor=1 WHERE id=?`, runID); statusErr != nil {
		t.Fatal(statusErr)
	}
	// leaseAt 是本次恢复分配的租约到期时间。
	leaseAt := time.Now().UTC().Add(5 * time.Minute).Unix()
	// reopened、reopenErr 保存以原代次抢占的结果。
	reopened, reopenErr := s.Automation.ReopenRunForRecovery(ctx, runID, 1, leaseAt)
	if reopenErr != nil || !reopened {
		t.Fatalf("原代次抢占应成功: reopened=%v err=%v", reopened, reopenErr)
	}
	// run 保存重开后的运行记录，用于校验检查点已被重置。
	run, getErr := s.Automation.GetRun(ctx, runID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if run.Status != "running" || run.ActionStarted || run.AttemptCount != 2 || run.ErrorMessage != "" {
		t.Fatalf("重开后的运行状态异常: %+v", run)
	}
	if run.LeaseExpiresAt != leaseAt {
		t.Fatalf("重开后租约应更新为本次恢复的到期时间: %d", run.LeaseExpiresAt)
	}
	// 代次已变化，再次以旧代次抢占必须失败。
	staleReopened, staleErr := s.Automation.ReopenRunForRecovery(ctx, runID, 1, leaseAt)
	if staleErr != nil || staleReopened {
		t.Fatalf("旧代次抢占必须失败: reopened=%v err=%v", staleReopened, staleErr)
	}
	// 状态已变为 running，新代次抢占同样必须失败。
	raceReopened, raceErr := s.Automation.ReopenRunForRecovery(ctx, runID, 2, leaseAt)
	if raceErr != nil || raceReopened {
		t.Fatalf("running 状态不允许重开: reopened=%v err=%v", raceReopened, raceErr)
	}
}

// TestPendingShipResumableRunsDeduplicatesOrderAndBlocksActiveRun 验证同一订单只返回最新候选，且存在活动运行时不回退旧运行。
// 这样可以防止历史重复运行在重启或冷却过期后再次执行确认发货。
func TestPendingShipResumableRunsDeduplicatesOrderAndBlocksActiveRun(t *testing.T) {
	// s、cleanup 保存测试数据库及关闭责任。
	s, cleanup := newTestDB(t)
	defer cleanup()
	// ctx 保存本测试共用的数据库上下文。
	ctx := context.Background()
	// userID、cookieID 保存测试账号主键与账号标识。
	userID, cookieID := seedAccount(t, s)
	// ruleID 保存待续跑规则主键。
	ruleID := seedCatchupRule(t, s, userID, cookieID, "item-dedup", "重复运行规则", true,
		[]AutomationActionInput{{ActionType: "confirm_shipment", Enabled: true}})
	// orderID 保存同时存在多条付款运行的订单标识。
	orderID := "o-dedup"
	seedCatchupOrder(t, s, orderID, cookieID, "item-dedup", "chat-dedup", "pending_ship")
	// firstRunID 保存较早的人工核对运行。
	firstRunID := seedCatchupRun(t, s, ruleID, cookieID, orderID)
	// secondRunID 保存绕过正常防重入口写入的较新历史运行。
	var secondRunID int64
	// insertErr 保存写入较新重复运行夹具时的数据库错误。
	if insertErr := s.DB.QueryRowContext(ctx, `
INSERT INTO automation_runs
 (rule_id,cookie_id,item_id,order_id,buyer_id,chat_id,trigger_type,trigger_key,status,raw_event_json,attempt_count,action_cursor,action_started,next_retry_at,lease_expires_at)
VALUES (?,?,?,?,?,?,?,?,'needs_review',?,1,0,0,0,0)
RETURNING id`, ruleID, cookieID, "item-dedup", orderID, "buyer-"+orderID, "chat-dedup", "order_paid", "duplicate:"+orderID,
		catchupRunSnapshot(cookieID, orderID, idempotentTailPlanJSON)).Scan(&secondRunID); insertErr != nil {
		// SQLite 和 PostgreSQL 支持 RETURNING；测试数据库固定为 SQLite，直接保留明确失败信息。
		t.Fatalf("写入重复运行夹具失败: %v", insertErr)
	}
	if secondRunID <= firstRunID {
		t.Fatalf("重复运行主键应递增: first=%d second=%d", firstRunID, secondRunID)
	}
	// firstStateErr 将较早运行置为人工核对，确保查询只因同订单去重而选择较新运行。
	if _, firstStateErr := s.DB.ExecContext(ctx,
		`UPDATE automation_runs SET status='needs_review',action_started=0,action_cursor=0,attempt_count=1 WHERE id=?`, firstRunID); firstStateErr != nil {
		t.Fatal(firstStateErr)
	}
	// candidates 保存按订单去重后的续跑候选。
	candidates, queryErr := s.Automation.PendingShipResumableRunsAfter(ctx, "", 5, 200)
	if queryErr != nil {
		t.Fatal(queryErr)
	}
	if len(candidates) != 1 || candidates[0].RunID != secondRunID {
		t.Fatalf("同订单应只返回最新运行: %+v", candidates)
	}
	// activeErr 把最新候选改成活动运行，验证旧运行不会因最新运行不可续跑而回退。
	if _, activeErr := s.DB.ExecContext(ctx, `UPDATE automation_runs SET status='running' WHERE id=?`, secondRunID); activeErr != nil {
		t.Fatal(activeErr)
	}
	// activeCandidates、activeQueryErr 保存最新运行处于活动状态时的候选结果与查询错误。
	activeCandidates, activeQueryErr := s.Automation.PendingShipResumableRunsAfter(ctx, "", 5, 200)
	if activeQueryErr != nil {
		t.Fatal(activeQueryErr)
	}
	if len(activeCandidates) != 0 {
		t.Fatalf("存在活动运行时不得回退旧运行: %+v", activeCandidates)
	}
	// exhaustedErr 将最新运行标为超出代次，确认查询不会改选较早的低代次运行。
	if _, exhaustedErr := s.DB.ExecContext(ctx, `UPDATE automation_runs SET status='failed',attempt_count=5 WHERE id=?`, secondRunID); exhaustedErr != nil {
		t.Fatal(exhaustedErr)
	}
	// exhaustedCandidates、exhaustedQueryErr 保存最新运行达到代次上限时的候选结果与查询错误。
	exhaustedCandidates, exhaustedQueryErr := s.Automation.PendingShipResumableRunsAfter(ctx, "", 5, 200)
	if exhaustedQueryErr != nil {
		t.Fatal(exhaustedQueryErr)
	}
	if len(exhaustedCandidates) != 0 {
		t.Fatalf("最新运行达到代次上限时不得回退旧运行: %+v", exhaustedCandidates)
	}
}

// TestReopenRunForRecoverySerializesSameOrder 验证两个并发恢复调用在同一订单上只有一个能取得运行执行权。
// 订单写锁必须与正常 TryStartRun 使用相同的账号→订单顺序，否则跨进程重启仍可能产生双重确认发货。
func TestReopenRunForRecoverySerializesSameOrder(t *testing.T) {
	// s、cleanup 保存测试数据库及关闭责任。
	s, cleanup := newTestDB(t)
	defer cleanup()
	// ctx 保存本测试共用的数据库上下文。
	ctx := context.Background()
	// userID、cookieID 保存测试账号主键与账号标识。
	userID, cookieID := seedAccount(t, s)
	// ruleID 保存并发恢复规则主键。
	ruleID := seedCatchupRule(t, s, userID, cookieID, "item-race", "并发重开规则", true,
		[]AutomationActionInput{{ActionType: "confirm_shipment", Enabled: true}})
	// orderID 保存两个历史运行共同归属的订单。
	orderID := "o-reopen-race"
	seedCatchupOrder(t, s, orderID, cookieID, "item-race", "chat-race", "pending_ship")
	// firstRunID、secondRunID 保存两个待恢复运行；第二条通过直接插入绕过正常付款运行防重，仅用于模拟历史脏数据。
	firstRunID := seedCatchupRun(t, s, ruleID, cookieID, orderID)
	// secondRunID 保存直接插入的第二条待恢复运行主键。
	var secondRunID int64
	// insertErr 保存写入第二条并发运行夹具时的数据库错误。
	if insertErr := s.DB.QueryRowContext(ctx, `
INSERT INTO automation_runs
 (rule_id,cookie_id,item_id,order_id,buyer_id,chat_id,trigger_type,trigger_key,status,raw_event_json,attempt_count,action_cursor,action_started,next_retry_at,lease_expires_at)
VALUES (?,?,?,?,?,?,?,?,'needs_review',?,1,0,0,0,0)
RETURNING id`, ruleID, cookieID, "item-race", orderID, "buyer-"+orderID, "chat-race", "order_paid", "race:"+orderID,
		catchupRunSnapshot(cookieID, orderID, idempotentTailPlanJSON)).Scan(&secondRunID); insertErr != nil {
		t.Fatalf("写入并发运行夹具失败: %v", insertErr)
	}
	// firstStateErr 将首条运行置为人工核对，使两条历史运行都具备并发抢占条件。
	if _, firstStateErr := s.DB.ExecContext(ctx,
		`UPDATE automation_runs SET status='needs_review',action_started=0,action_cursor=0,attempt_count=1 WHERE id=?`, firstRunID); firstStateErr != nil {
		t.Fatal(firstStateErr)
	}
	// results 保存两个并发重开调用的返回结果。
	results := make(chan struct {
		// reopened 表示本次调用是否取得运行执行权。
		reopened bool
		// err 保存本次调用遇到的数据库错误。
		err error
	}, 2)
	// startGate 让两个调用尽量同时进入数据库抢占路径。
	startGate := make(chan struct{})
	// waitGroup 等待两个恢复调用完成，避免测试提前关闭数据库。
	var waitGroup sync.WaitGroup
	// runID 表示当前循环启动的恢复运行主键。
	for _, runID := range []int64{firstRunID, secondRunID} {
		// runID 保存当前 goroutine 负责抢占的运行主键副本。
		runID := runID
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			<-startGate
			// reopened、reopenErr 保存当前并发重开结果。
			reopened, reopenErr := s.Automation.ReopenRunForRecovery(ctx, runID, 1, time.Now().Add(5*time.Minute).Unix())
			results <- struct {
				reopened bool
				err      error
			}{reopened: reopened, err: reopenErr}
		}()
	}
	close(startGate)
	waitGroup.Wait()
	close(results)
	// successCount、failureCount 统计唯一成功抢占和明确失权的调用数量。
	successCount, failureCount := 0, 0
	// result 保存一个并发恢复调用的最终执行权结果。
	for result := range results {
		if result.err != nil {
			t.Fatalf("并发重开不应返回数据库错误: %v", result.err)
		}
		if result.reopened {
			successCount++
		} else {
			failureCount++
		}
	}
	if successCount != 1 || failureCount != 1 {
		t.Fatalf("同订单并发重开应一成一败: success=%d failure=%d", successCount, failureCount)
	}
	// runningCount 保存并发重开后同订单处于活动状态的运行数。
	var runningCount int
	// countErr 保存读取同订单活动运行数量时的数据库错误。
	if countErr := s.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM automation_runs WHERE order_id=? AND trigger_type='order_paid' AND status='running'`, orderID).Scan(&runningCount); countErr != nil {
		t.Fatal(countErr)
	}
	if runningCount != 1 {
		t.Fatalf("同订单最多只能有一条 running 运行: %d", runningCount)
	}
}
