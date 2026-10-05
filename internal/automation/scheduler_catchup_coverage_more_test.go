package automation

import (
	"context"
	"sync"
	"testing"
	"time"

	"xianyu-go/internal/db"
)

// selectiveSenderProvider 按账号决定发送器是否就绪，用于覆盖自动化发送前的账号就绪门禁。
// 真实环境里账号连接状态各不相同，映射能在一个扫描轮次里同时覆盖就绪与未就绪两侧分支。
type selectiveSenderProvider struct {
	// senders 保存账号到已创建发送器的映射；同一账号必须始终返回同一实例，测试才能读到发送记录。
	senders map[string]*readinessTestSender
	// ready 保存账号到就绪状态的映射；缺失的账号按未就绪处理。
	ready map[string]bool
}

// contextBoundSender 在外部发送入口等待 Context 取消，用于验证待发货扫描的单轮预算能中断慢任务。
type contextBoundSender struct {
	// entered 通知测试发送调用已经进入可取消的外部 I/O 模拟。
	entered chan struct{}
	// enteredOnce 保证重复动作或错误重试不会重复关闭 entered 通道。
	enteredOnce sync.Once
}

// SendText 模拟遵守 Context 取消协议的慢速文本发送。
func (s *contextBoundSender) SendText(ctx context.Context, _, _, _ string) error {
	// enteredOnce 只在首次进入发送时发出测试同步信号。
	s.enteredOnce.Do(func() { close(s.entered) })
	// ctxErr 保存扫描预算耗尽后由外部发送返回的取消错误。
	<-ctx.Done()
	return ctx.Err()
}

// SendImage 满足消息发送接口；预算测试只走文本卡密发送路径。
func (s *contextBoundSender) SendImage(context.Context, string, string, string, int64, int, int) error {
	return nil
}

// UpdateCookie 满足消息发送接口；预算测试不需要更新运行时凭证。
func (s *contextBoundSender) UpdateCookie(string) {}

// Sender 返回指定账号的测试发送器；首次访问时创建并缓存。
func (p selectiveSenderProvider) Sender(accountID string) (MessageSender, bool) {
	// sender 是该账号复用的测试发送器；映射是引用类型，值接收者也能写回缓存。
	sender, ok := p.senders[accountID]
	if !ok {
		sender = &readinessTestSender{testSender: &testSender{}, ready: p.ready[accountID]}
		p.senders[accountID] = sender
	}
	return sender, true
}

// catchupCard 写入一条含单条库存的数据卡密组并返回卡密组主键。
// order_paid 触发的动作计划必须包含匹配订单规格的发卡动作，否则运行会被判定为失败。
func catchupCard(t *testing.T, store *db.Store, userID int64) int64 {
	t.Helper()
	// cardID、cardErr 保存数据卡密组写入结果与错误。
	cardID, cardErr := store.Cards.Create(context.Background(), &db.CardFull{
		Name: "catchup-card", Type: "data", DataContent: "secret-catchup-1", Enabled: true, UserID: userID,
	})
	if cardErr != nil {
		t.Fatal(cardErr)
	}
	return cardID
}

// catchupCardAction 构造一个绑定卡密组、每次发放一条的启用发卡动作。
// 规格过滤与夹具订单的「颜色/黑」保持一致；留空规格只会匹配无规格订单。
func catchupCardAction(cardID int64) db.AutomationActionInput {
	return db.AutomationActionInput{
		ActionType: ActionSendCard, CardID: cardID, DeliveryCount: 1, Enabled: true,
		ConfigJSON: `{"spec_name":"颜色","spec_value":"黑"}`,
	}
}

// catchupAccount 写入一个可被自动化扫描选中的账号夹具。
// 账号必须有 cookies 行，否则账号状态门禁会以“账号不存在”拒绝自动化。
func catchupAccount(t *testing.T, store *db.Store, userID int64, cookieID string) {
	t.Helper()
	// saveErr 保存账号 Cookie 夹具写入错误。
	if saveErr := store.Cookies.Save(context.Background(), cookieID, "cv=1", userID); saveErr != nil {
		t.Fatalf("写入账号夹具失败: %v", saveErr)
	}
}

// catchupRule 写入一条启用的 order_paid 自动化规则并返回规则主键。
// 规则的商品匹配条件与订单 item_id 共同决定订单是否进入兜底扫描候选。
func catchupRule(t *testing.T, store *db.Store, userID int64, cookieID, itemID string, actions []db.AutomationActionInput) int64 {
	t.Helper()
	// ruleID、ruleErr 保存规则写入结果与错误；规则必须绑定商品，中心侧匹配器只按商品命中。
	ruleID, ruleErr := store.Automation.Create(context.Background(), db.AutomationRuleInput{
		UserID: userID, CookieID: cookieID, Name: "catchup-" + cookieID + "-" + itemID, ItemID: itemID,
		TriggerType: TriggerOrderPaid, Enabled: true, Actions: actions,
	})
	if ruleErr != nil {
		t.Fatal(ruleErr)
	}
	return ruleID
}

// catchupOrder 写入一条待发货订单夹具。
// 订单必须处于 pending_ship、未删除且带会话标识，才能被兜底扫描选中。
func catchupOrder(t *testing.T, store *db.Store, cookieID, orderID, itemID, chatID string) {
	t.Helper()
	// upsertErr 保存订单夹具写入错误。
	if upsertErr := store.Orders.Upsert(context.Background(), orderID, db.OrderUpsertOpts{
		ItemID: itemID, BuyerID: "buyer-" + orderID, CookieID: cookieID, ChatID: chatID,
		OrderStatus: "pending_ship", Quantity: "1", Amount: "9.90", SpecName: "颜色", SpecValue: "黑",
	}); upsertErr != nil {
		t.Fatalf("写入订单夹具失败: %v", upsertErr)
	}
	// observedAt 是模拟付款系统卡片已丢失足够久的最后观测时间，使既有兜底成功夹具跨过实时事件等待窗。
	observedAt := time.Now().UTC().Add(-defaultPendingShipSettleWindow - time.Minute).Format(time.RFC3339Nano)
	// updateErr 保存把夹具调整为可安全补触发历史订单的写入错误。
	if _, updateErr := store.DB.ExecContext(context.Background(), `UPDATE orders SET updated_at=? WHERE order_id=?`, observedAt, orderID); updateErr != nil {
		t.Fatalf("回拨订单夹具观测时间失败: %v", updateErr)
	}
}

// TestPendingShipCatchupReadyDefersFreshThenAllowsObservedOrders 验证通用兜底会等待实时卡片窗口，订单阶段资格由查询层决定。
func TestPendingShipCatchupReadyDefersFreshThenAllowsObservedOrders(t *testing.T) {
	// now 是固定的当前时刻，保证时间窗口断言不依赖测试执行速度。
	now := time.Date(2026, time.September, 15, 8, 0, 0, 0, time.UTC)
	// cases 覆盖普通订单等待/放行和无法确认观测时间三条安全边界。
	cases := []struct {
		// name 是当前安全边界的可读名称。
		name string
		// order 是输入给通用兜底的本地订单事实。
		order db.Order
		// wantReady 是该订单是否允许被通用兜底转换为付款事件。
		wantReady bool
		// wantReason 是拒绝时写入诊断日志的稳定原因。
		wantReason string
	}{
		{name: "普通订单仍在实时卡片窗口", order: db.Order{UpdatedAt: now.Add(-time.Minute).Format(time.RFC3339Nano)}, wantReason: "waiting_for_realtime_payment_event"},
		{name: "普通订单已过实时卡片窗口", order: db.Order{UpdatedAt: now.Add(-defaultPendingShipSettleWindow).Format(time.RFC3339Nano)}, wantReady: true},
		{name: "已由候选查询确认阶段资格的砍价订单在窗口后放行", order: db.Order{IsBargain: 1, UpdatedAt: now.Add(-time.Hour).Format(time.RFC3339Nano)}, wantReady: true},
		{name: "缺少可解析观测时间时安全停止", order: db.Order{UpdatedAt: "not-a-time"}, wantReason: "missing_observed_time"},
	}
	// testCase 表示当前待验证的兜底安全边界。
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			// ready、reason 保存被测订单的通用兜底放行结果及诊断原因。
			ready, reason := pendingShipCatchupReady(testCase.order, now)
			if ready != testCase.wantReady || reason != testCase.wantReason {
				t.Fatalf("兜底放行结果异常: ready=%v reason=%q wantReady=%v wantReason=%q", ready, reason, testCase.wantReady, testCase.wantReason)
			}
		})
	}
}

// TestPendingShipDeliveryScanUsesNormalConsignAfterRecordedBargainStage 验证兜底仅在免拼成功已记录后补发卡，并使用普通确认发货接口。
func TestPendingShipDeliveryScanUsesNormalConsignAfterRecordedBargainStage(t *testing.T) {
	// store、cleanup 保存隔离自动化数据库及资源释放函数。
	store, cleanup := newAutomationTestStore(t)
	defer cleanup()
	// ctx 保存本测试的数据库与扫描上下文。
	ctx := context.Background()
	// admin、adminErr 保存规则所属管理员及查询错误。
	admin, adminErr := store.Users.GetByUsername(ctx, "admin")
	if adminErr != nil {
		t.Fatal(adminErr)
	}
	// enabled 同时开启付款自动发货和平台确认发货，使测试覆盖最终普通确认动作。
	enabled := true
	// cookieID 是砍价订单所属测试账号。
	cookieID := "aged-bargain-catchup-acc"
	catchupAccount(t, store, admin.ID, cookieID)
	// settingsErr 保存账号自动化开关写入错误。
	if _, settingsErr := store.Cookies.UpdateSettings(ctx, cookieID, db.AccountSettingsUpdate{UserID: admin.ID, AutoConfirm: &enabled, AutoConsign: &enabled}); settingsErr != nil {
		t.Fatal(settingsErr)
	}
	// cardID 是付款动作完整性校验和发送路径需要的卡密组标识。
	cardID := catchupCard(t, store, admin.ID)
	catchupRule(t, store, admin.ID, cookieID, "aged-bargain-item", []db.AutomationActionInput{catchupCardAction(cardID), {ActionType: ActionConfirmShipment, Enabled: true}})
	catchupOrder(t, store, cookieID, "aged-bargain-catchup-order", "aged-bargain-item", "aged-bargain-chat")
	// bargainUpdateErr 保存把历史待发货夹具标识为砍价订单的写入错误。
	if _, bargainUpdateErr := store.DB.ExecContext(ctx, `UPDATE orders SET is_bargain=1 WHERE order_id=?`, "aged-bargain-catchup-order"); bargainUpdateErr != nil {
		t.Fatal(bargainUpdateErr)
	}
	// claimed、claimErr 保存模拟“待刀成”WS 已成功完成免拼后的阶段领取结果。
	claimed, claimErr := store.Automation.ClaimBargainFreeShipping(ctx, "aged-bargain-catchup-order", cookieID)
	if claimErr != nil || !claimed {
		t.Fatalf("领取已完成免拼阶段失败: claimed=%v err=%v", claimed, claimErr)
	}
	// finishErr 保存模拟免拼成功终态的阶段写入错误。
	if finishErr := store.Automation.FinishBargainFreeShipping(ctx, "aged-bargain-catchup-order", cookieID, "succeeded"); finishErr != nil {
		t.Fatal(finishErr)
	}
	// client 是记录最终普通确认和免拼调用次数的平台替身。
	client := &fakeMTop{consignOk: true, consignRet: []string{"SUCCESS::调用成功"}}
	// sender 是验证兜底仍先发送交付内容的在线替身。
	sender := &testSender{}
	// scheduler 是注入平台替身的待发货兜底调度器。
	scheduler := &Scheduler{center: NewWithDependencies(store, testSenderProvider{sender: sender}, nil, CenterDependencies{MTop: client}), pendingShipCooldown: map[string]time.Time{}}
	scheduler.scanPendingShipDeliveries(ctx)
	if len(sender.texts) != 1 {
		t.Fatalf("砍价兜底应在已记录免拼后发送一次交付内容: %+v", sender.texts)
	}
	if client.freeShippingCalls != 0 || client.consignCalls != 1 {
		t.Fatalf("砍价兜底错误调用免拼或遗漏普通确认: free=%d consign=%d", client.freeShippingCalls, client.consignCalls)
	}
}

// TestPendingShipDeliveryScanDefersFreshOrder 验证新进入待发货状态的普通订单不会被调度器立即伪造成付款事件。
func TestPendingShipDeliveryScanDefersFreshOrder(t *testing.T) {
	// store、cleanup 保存隔离自动化数据库及资源释放函数。
	store, cleanup := newAutomationTestStore(t)
	defer cleanup()
	// ctx 保存本测试的数据库与扫描上下文。
	ctx := context.Background()
	// admin、adminErr 保存规则所属管理员及查询错误。
	admin, adminErr := store.Users.GetByUsername(ctx, "admin")
	if adminErr != nil {
		t.Fatal(adminErr)
	}
	// cardID 是付款动作完整性校验所需的卡密组标识。
	cardID := catchupCard(t, store, admin.ID)
	// cookieID 是新订单所属并报告在线的测试账号。
	cookieID := "fresh-catchup-acc"
	catchupAccount(t, store, admin.ID, cookieID)
	catchupRule(t, store, admin.ID, cookieID, "fresh-item", []db.AutomationActionInput{catchupCardAction(cardID)})
	// upsertErr 保存新进入待发货状态订单的写入错误；这里刻意不回拨 updated_at。
	if upsertErr := store.Orders.Upsert(ctx, "fresh-catchup-order", db.OrderUpsertOpts{ItemID: "fresh-item", BuyerID: "fresh-buyer", CookieID: cookieID, ChatID: "fresh-chat", OrderStatus: "pending_ship", Quantity: "1", Amount: "9.90", SpecName: "颜色", SpecValue: "黑"}); upsertErr != nil {
		t.Fatal(upsertErr)
	}
	// sender 是记录是否发生提前发送的在线替身。
	sender := &testSender{}
	// scheduler 是待验证的通用待发货兜底调度器。
	scheduler := &Scheduler{center: New(store, testSenderProvider{sender: sender}, nil), pendingShipCooldown: map[string]time.Time{}}
	scheduler.scanPendingShipDeliveries(ctx)
	if len(sender.texts) != 0 {
		t.Fatalf("新订单不应被兜底提前发送: %+v", sender.texts)
	}
	// runCount 保存新订单对应付款运行数；等待窗内必须保持为零。
	var runCount int
	// countErr 保存读取提前付款运行数量的错误。
	if countErr := store.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM automation_runs WHERE order_id=? AND trigger_type='order_paid'`, "fresh-catchup-order").Scan(&runCount); countErr != nil {
		t.Fatal(countErr)
	}
	if runCount != 0 {
		t.Fatalf("新订单不应被兜底提前创建付款运行: %d", runCount)
	}
}

// catchupRun 为订单写入一条 order_paid 运行记录并返回运行主键。
// rawTask 是运行携带的任务快照 JSON；续跑恢复路径依赖它还原账号与订单事实。
func catchupRun(t *testing.T, store *db.Store, ruleID int64, cookieID, orderID, rawTask string) int64 {
	t.Helper()
	// runID、started、startErr 保存运行写入结果与错误。
	runID, started, startErr := store.Automation.TryStartRun(context.Background(), db.AutomationRun{
		RuleID: ruleID, CookieID: cookieID, OrderID: orderID, TriggerType: TriggerOrderPaid,
		TriggerKey: "catchup:" + orderID, RawEventJSON: rawTask, LeaseExpiresAt: time.Now().Add(time.Minute).Unix(),
	})
	if startErr != nil || !started {
		t.Fatalf("写入运行夹具失败: started=%v err=%v", started, startErr)
	}
	return runID
}

// setRunState 把运行改写成指定的状态、代次与动作游标。
// 兜底续跑扫描按状态、代次和游标筛选可续跑运行，夹具必须精确控制这三项。
func setRunState(t *testing.T, store *db.Store, runID int64, status string, attempt, cursor int) {
	t.Helper()
	// updateErr 保存运行状态改写错误。
	if _, updateErr := store.DB.ExecContext(context.Background(),
		`UPDATE automation_runs SET status=?, attempt_count=?, action_cursor=?, action_started=0, next_retry_at=0, lease_expires_at=0 WHERE id=?`,
		status, attempt, cursor, runID); updateErr != nil {
		t.Fatalf("改写运行夹具失败: %v", updateErr)
	}
}

// TestScanPendingShipDeliveriesSkipsWhenDisabledOrUninitialized 验证兜底扫描在开关关闭与状态缺失时安全返回。
// 止血开关与空状态保护是部署异常时的最后一道防线，缺少它们会造成无法回滚的重复发货。
func TestScanPendingShipDeliveriesSkipsWhenDisabledOrUninitialized(t *testing.T) {
	// store、cleanup 保存测试数据库及关闭责任。
	store, cleanup := newAutomationTestStore(t)
	defer cleanup()
	// ctx 保存本测试共用的数据库上下文。
	ctx := context.Background()
	// 显式关闭止血开关时，兜底扫描必须整体跳过。
	t.Setenv(pendingShipCatchupEnv, "0")
	// disabledCenter 是带真实存储但开关已关闭的自动化中心。
	disabledCenter := New(store, testSenderProvider{sender: &testSender{}}, nil)
	(&Scheduler{center: disabledCenter}).scanPendingShipDeliveries(ctx)
	// 恢复默认开关后，空状态保护仍必须拦截未初始化的调度器。
	t.Setenv(pendingShipCatchupEnv, "")
	// nilScheduler 是未初始化的调度器接收者。
	var nilScheduler *Scheduler
	nilScheduler.scanPendingShipDeliveries(ctx)
	// missingCenter 是缺少自动化中心的调度器。
	(&Scheduler{}).scanPendingShipDeliveries(ctx)
	// missingAutomation 是自动化存储未注入的调度器。
	(&Scheduler{center: &Center{store: &db.Store{}}}).scanPendingShipDeliveries(ctx)
	// enabledCenter 是开关开启且存储完整的中心，用于确认空库时直接返回。
	enabledCenter := New(store, testSenderProvider{sender: &testSender{}}, nil)
	(&Scheduler{center: enabledCenter}).scanPendingShipDeliveries(ctx)
}

// TestScanPendingShipDeliveriesCoversGatesCooldownAndSuccess 验证兜底扫描的账号门禁、冷却与补触发主链路。
// 这条链路就是 09-13 线上“卡密已发出但发货状态未更新”问题的兜底修复，必须覆盖每一类跳过原因。
func TestScanPendingShipDeliveriesCoversGatesCooldownAndSuccess(t *testing.T) {
	// store、cleanup 保存测试数据库及关闭责任。
	store, cleanup := newAutomationTestStore(t)
	defer cleanup()
	// ctx 保存本测试共用的数据库上下文。
	ctx := context.Background()
	// admin 保存规则所属管理员账号。
	admin, adminErr := store.Users.GetByUsername(ctx, "admin")
	if adminErr != nil {
		t.Fatal(adminErr)
	}
	// cardID 是三类账号共用的数据卡密组，order_paid 动作计划必须含发卡动作。
	cardID := catchupCard(t, store, admin.ID)
	// 就绪、未就绪与暂停三类账号各配一条发货规则和一个待发货订单。
	// 注：订单的 cookie_id 受外键约束必须指向真实账号，因此“账号在扫描后被删除”
	// 这类账号状态读取失败分支无法用一致夹具构造，已在核心链路清单登记为例外。
	for _, cookieID := range []string{"ready-acc", "notready-acc", "paused-acc"} {
		catchupAccount(t, store, admin.ID, cookieID)
		catchupRule(t, store, admin.ID, cookieID, "item-"+cookieID, []db.AutomationActionInput{catchupCardAction(cardID)})
		catchupOrder(t, store, cookieID, "o-"+cookieID, "item-"+cookieID, "chat-"+cookieID)
	}
	// 暂停账号被用户临时停用，自动化必须跳过而不是继续发送。
	if _, pauseErr := store.DB.ExecContext(ctx,
		`UPDATE cookies SET paused_until=? WHERE id=?`, time.Now().Add(time.Hour).Unix(), "paused-acc"); pauseErr != nil {
		t.Fatal(pauseErr)
	}
	// provider 只对 ready-acc 报告发送器就绪，并缓存各账号发送器实例。
	provider := selectiveSenderProvider{
		senders: map[string]*readinessTestSender{},
		ready:   map[string]bool{"ready-acc": true},
	}
	// scheduler 是使用选择性发送器的调度器。
	scheduler := &Scheduler{center: New(store, provider, nil), pendingShipCooldown: map[string]time.Time{}}
	// 首轮扫描应补触发就绪账号，并对其余账号分别给出跳过原因。
	scheduler.scanPendingShipDeliveries(ctx)
	// readySender 是就绪账号的缓存发送器，用于确认补触发确实执行了发送。
	readySender := provider.senders["ready-acc"]
	if readySender == nil {
		t.Fatal("兜底扫描没有为就绪账号创建发送器")
	}
	// 若就绪账号没有收到文本，说明补触发主链路没有走通。
	if len(readySender.texts) == 0 {
		t.Fatal("待发货兜底扫描未为就绪账号补触发发送")
	}
	// runs 保存补触发产生的运行数量，用于确认运行已落库。
	var runs int
	// countErr 保存统计补触发运行条数的查询错误。
	if countErr := store.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM automation_runs WHERE order_id='o-ready-acc' AND trigger_type='order_paid'`).Scan(&runs); countErr != nil {
		t.Fatal(countErr)
	}
	if runs != 1 {
		t.Fatalf("补触发应恰好产生一条运行: %d", runs)
	}
	// sentBeforeCooldown 保存冷却前已发送的文本数量。
	sentBeforeCooldown := len(readySender.texts)
	// 第二轮扫描时同一订单仍处于待发货，但必须被冷却窗口拦住，避免高频重试。
	scheduler.scanPendingShipDeliveries(ctx)
	// got 保存冷却窗口内再次扫描后已发送的文本数量，应与冷却前持平。
	if got := len(readySender.texts); got != sentBeforeCooldown {
		t.Fatalf("冷却窗口内不应重复补触发: before=%d after=%d", sentBeforeCooldown, got)
	}
	// 补触发产生的运行让订单退出“完全没有运行”的候选集合，后续交由失败恢复链处理。
	afterRuns, runErr := store.Automation.PendingShipOrdersWithoutPaidRunAfter(ctx, "", 200)
	if runErr != nil {
		t.Fatal(runErr)
	}
	// candidate 是兜底候选订单，已有 order_paid 运行的订单不应出现在其中。
	for _, candidate := range afterRuns {
		if candidate.OrderID == "o-ready-acc" {
			t.Fatalf("已有 order_paid 运行的订单不应再进入兜底候选: %+v", candidate)
		}
	}
}

// TestPendingShipDeliveryScanHonorsBudget 验证单个慢速外部动作不能把待发货扫描拖过本轮预算。
// 发送器遵守 Context 取消后，扫描必须及时结束并把尚未处理的订单留给下一轮。
func TestPendingShipDeliveryScanHonorsBudget(t *testing.T) {
	// store、cleanup 保存测试数据库及关闭责任。
	store, cleanup := newAutomationTestStore(t)
	defer cleanup()
	// ctx 保存本测试共用的数据库上下文。
	ctx := context.Background()
	// admin 保存规则所属管理员账号。
	admin, adminErr := store.Users.GetByUsername(ctx, "admin")
	if adminErr != nil {
		t.Fatal(adminErr)
	}
	// cardID 保存满足付款规则发卡动作检查的卡密组。
	cardID, cardErr := store.Cards.Create(ctx, &db.CardFull{
		Name: "budget-card", Type: "data", DataContent: "budget-secret", Enabled: true, UserID: admin.ID,
	})
	if cardErr != nil {
		t.Fatal(cardErr)
	}
	// budgetAccount 是慢速发送订单所属账号；它必须有启用规则和在线发送器才能进入外部动作。
	budgetAccount := "budget-acc"
	catchupAccount(t, store, admin.ID, budgetAccount)
	catchupRule(t, store, admin.ID, budgetAccount, "budget-item", []db.AutomationActionInput{catchupCardAction(cardID)})
	catchupOrder(t, store, budgetAccount, "o-budget-1", "budget-item", "chat-budget-1")
	// sender 保存遵守 Context 取消的慢速发送替身。
	sender := &contextBoundSender{entered: make(chan struct{})}
	// scheduler 保存短预算配置，便于在测试中毫秒级验证扫描退出。
	scheduler := &Scheduler{
		center:              New(store, blockingSenderProvider{sender: sender}, nil),
		pendingShipCooldown: map[string]time.Time{},
		// 250ms 给 race 检测下的数据库领取和 goroutine 调度留下进入发送器的余量，同时仍远小于测试超时阈值。
		pendingShipScanBudget:   250 * time.Millisecond,
		pendingShipScanMaxTasks: 20,
	}
	// startedAt 记录扫描开始时间，用于检测慢动作是否被预算取消。
	startedAt := time.Now()
	scheduler.scanPendingShipDeliveries(ctx)
	// elapsed 保存扫描从进入到返回的墙钟耗时。
	elapsed := time.Since(startedAt)
	if elapsed > 2*time.Second {
		t.Fatalf("待发货扫描超过预算后仍未及时返回: elapsed=%s", elapsed)
	}
	// 发送入口必须实际被触发过，否则测试只验证了空扫描而没有覆盖慢动作路径。
	select {
	case <-sender.entered:
	default:
		t.Fatal("慢速发送器未进入外部动作")
	}
	// runCount 保存预算耗尽前已经创建的付款运行数；扫描至少应为首个订单留下运行记录。
	var runCount int
	// countErr 保存读取预算测试运行数量时的数据库错误。
	if countErr := store.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM automation_runs WHERE order_id='o-budget-1' AND trigger_type='order_paid'`).Scan(&runCount); countErr != nil {
		t.Fatal(countErr)
	}
	if runCount != 1 {
		t.Fatalf("预算测试应创建一条运行记录: %d", runCount)
	}
	// run、runErr 保存预算取消后的运行状态及读取错误，结果不确定时必须隔离为人工核对，不能遗留 running。
	run, runErr := store.Automation.GetRun(ctx, 1)
	if runErr != nil {
		t.Fatal(runErr)
	}
	if run.Status != "needs_review" || !run.ActionStarted {
		t.Fatalf("预算取消后的不确定运行必须进入人工核对并保留动作保护: %+v", run)
	}
}

// TestPendingShipDeliveryScanHonorsTaskLimit 验证单轮任务上限生效，余下订单仍保留在无运行候选集合。
// 该上限与时间预算共同保证订单量增长不会让分钟级调度循环失去响应。
func TestPendingShipDeliveryScanHonorsTaskLimit(t *testing.T) {
	// store、cleanup 保存测试数据库及关闭责任。
	store, cleanup := newAutomationTestStore(t)
	defer cleanup()
	// ctx 保存本测试共用的数据库上下文。
	ctx := context.Background()
	// admin 保存规则所属管理员账号。
	admin, adminErr := store.Users.GetByUsername(ctx, "admin")
	if adminErr != nil {
		t.Fatal(adminErr)
	}
	// limitAccount 是任务上限测试使用的账号。
	limitAccount := "limit-acc"
	catchupAccount(t, store, admin.ID, limitAccount)
	catchupRule(t, store, admin.ID, limitAccount, "limit-item", []db.AutomationActionInput{{ActionType: ActionConfirmShipment, Enabled: true}})
	// 创建三个候选订单；确认发货规则会在动作计划校验阶段快速失败，但仍会为实际触发建立运行记录。
	for _, orderID := range []string{"o-limit-1", "o-limit-2", "o-limit-3"} {
		// orderID 表示当前任务上限测试订单。
		catchupOrder(t, store, limitAccount, orderID, "limit-item", "chat-"+orderID)
	}
	// sender 保存快速发送替身；本用例在发卡动作校验前结束，不会执行消息发送。
	sender := &testSender{}
	// scheduler 保存只允许本轮触发两个订单的配置。
	scheduler := &Scheduler{
		center:                  New(store, testSenderProvider{sender: sender}, nil),
		pendingShipCooldown:     map[string]time.Time{},
		pendingShipScanBudget:   time.Second,
		pendingShipScanMaxTasks: 2,
	}
	scheduler.scanPendingShipDeliveries(ctx)
	// runCount 保存本轮实际创建的付款运行数。
	var runCount int
	// countErr 保存读取本轮付款运行数量时的数据库错误。
	if countErr := store.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM automation_runs WHERE cookie_id=? AND trigger_type='order_paid'`, limitAccount).Scan(&runCount); countErr != nil {
		t.Fatal(countErr)
	}
	if runCount != 2 {
		t.Fatalf("单轮任务上限为 2 时应只创建两条运行: %d", runCount)
	}
	// remaining 保存第三个订单是否仍在无运行候选集合中。
	remaining, remainingErr := store.Automation.PendingShipOrdersWithoutPaidRunAfter(ctx, "", 200)
	if remainingErr != nil {
		t.Fatal(remainingErr)
	}
	if len(remaining) != 1 || remaining[0].OrderID != "o-limit-3" {
		t.Fatalf("任务上限后应留下一个未处理订单: %+v", remaining)
	}
}

// TestScanPendingShipDeliveriesReportsStorageFailures 验证存储不可用时兜底扫描返回而不是静默跳过。
// 兜底扫描漏报比多报更危险：付款消息丢失的订单会永久停在待发货。
func TestScanPendingShipDeliveriesReportsStorageFailures(t *testing.T) {
	// store、cleanup 保存测试数据库及关闭责任。
	store, cleanup := newAutomationTestStore(t)
	defer cleanup()
	// center 是仍引用已关闭数据库的自动化中心。
	center := New(store, testSenderProvider{sender: &testSender{}}, nil)
	// closeErr 保存数据库关闭错误。
	if closeErr := store.DB.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	// scheduler 是带失效存储的调度器；扫描必须安全返回。
	scheduler := &Scheduler{center: center, pendingShipCooldown: map[string]time.Time{}}
	scheduler.scanPendingShipDeliveries(context.Background())
}

// TestClaimPendingShipAttemptCooldownsAndCleansExpiredEntries 验证冷却窗口与过期清理逻辑。
func TestClaimPendingShipAttemptCooldownsAndCleansExpiredEntries(t *testing.T) {
	// nilScheduler 用于验证空接收者保护。
	var nilScheduler *Scheduler
	if nilScheduler.claimPendingShipAttempt("o-nil") {
		t.Fatal("空调度器应拒绝领取兜底尝试")
	}
	// store、cleanup 保存测试数据库及关闭责任。
	store, cleanup := newAutomationTestStore(t)
	defer cleanup()
	// center 是带真实存储的自动化中心。
	center := New(store, testSenderProvider{sender: &testSender{}}, nil)
	// scheduler 是被测调度器。
	scheduler := &Scheduler{center: center, pendingShipCooldown: map[string]time.Time{}}
	if !scheduler.claimPendingShipAttempt("o-first") {
		t.Fatal("首次领取应成功")
	}
	if scheduler.claimPendingShipAttempt("o-first") {
		t.Fatal("冷却窗口内的重复领取应失败")
	}
	// 预填大量已过期条目，验证映射超过阈值时触发清理而不是无界增长。
	// expiredKeys 保存预填的过期订单键。
	expiredKeys := make([]string, 0, 4096)
	// index 标识当前预填条目的序号。
	for index := 0; index < 4096; index++ {
		// key 是预填的过期订单键。
		key := "o-expired-" + string(rune('a'+index%26)) + "-" + time.Now().Add(time.Duration(index)*time.Microsecond).Format("150405.000000000")
		scheduler.pendingShipCooldown[key] = time.Now().Add(-defaultPendingShipCooldown - time.Minute)
		expiredKeys = append(expiredKeys, key)
	}
	if len(scheduler.pendingShipCooldown) <= 4096 {
		t.Fatalf("预填条目数应超过清理阈值: %d", len(scheduler.pendingShipCooldown))
	}
	if !scheduler.claimPendingShipAttempt("o-cleanup") {
		t.Fatal("超过阈值后的新订单领取应成功")
	}
	if len(scheduler.pendingShipCooldown) > 4097 {
		t.Fatalf("清理后映射应显著收缩: %d", len(scheduler.pendingShipCooldown))
	}
	// 清理必须只移除过期条目，本次领取的新条目必须保留。
	if _, ok := scheduler.pendingShipCooldown["o-cleanup"]; !ok {
		t.Fatal("本次领取的订单不应被清理")
	}
	// expiredKeys 仅用于说明预填集合，清理结果以映射长度断言为准。
	_ = expiredKeys
}

// TestScanPendingShipResumesContinuesIdempotentTails 验证续跑扫描能从检查点继续执行幂等收尾动作。
// 运行被隔离后剩余动作全是确认发货时，续跑不会再次联系买家，是安全的自动恢复路径。
func TestScanPendingShipResumesContinuesIdempotentTails(t *testing.T) {
	// store、cleanup 保存测试数据库及关闭责任。
	store, cleanup := newAutomationTestStore(t)
	defer cleanup()
	// ctx 保存本测试共用的数据库上下文。
	ctx := context.Background()
	// admin 保存规则所属管理员账号。
	admin, adminErr := store.Users.GetByUsername(ctx, "admin")
	if adminErr != nil {
		t.Fatal(adminErr)
	}
	// catchupAccount 写入待续跑账号夹具。
	catchupAccount(t, store, admin.ID, "resume-acc")
	// ruleID 保存包含发卡与确认发货两类动作的发货规则主键。
	ruleID := catchupRule(t, store, admin.ID, "resume-acc", "item-resume",
		[]db.AutomationActionInput{catchupCardAction(catchupCard(t, store, admin.ID)), {ActionType: ActionConfirmShipment, Enabled: true}})
	catchupOrder(t, store, "resume-acc", "o-resume", "item-resume", "chat-resume")
	// rawTask 是运行携带的冻结快照：续跑链路必须从它恢复动作计划，而不是读取可被编辑的现行规则。
	rawTask := `{"Source":"scheduler","AccountID":"resume-acc","TriggerType":"order_paid","ChatID":"chat-resume","OrderID":"o-resume","ItemID":"item-resume","BuyerID":"buyer-o-resume","Text":"待发货运行未完成，按检查点续跑",` +
		`"ActionPlan":[{"ActionType":"send_card","Enabled":true},{"ActionType":"confirm_shipment","Enabled":true}]}`
	// runID 保存被隔离的运行主键。
	runID := catchupRun(t, store, ruleID, "resume-acc", "o-resume", rawTask)
	// 运行已进入人工核对且游标越过发卡动作，属于可安全续跑候选。
	setRunState(t, store, runID, "needs_review", 1, 1)
	// provider 对待续跑账号报告发送器就绪。
	provider := selectiveSenderProvider{ready: map[string]bool{"resume-acc": true}}
	// scheduler 是使用选择性发送器的调度器。
	scheduler := &Scheduler{center: New(store, provider, nil), pendingShipCooldown: map[string]time.Time{}}
	scheduler.scanPendingShipResumes(ctx)
	// run 保存续跑后的运行记录，用于确认重开与续跑已经生效。
	run, getErr := store.Automation.GetRun(ctx, runID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if run.AttemptCount != 2 {
		t.Fatalf("续跑应把代次递增到 2: %d", run.AttemptCount)
	}
	if run.Status == "needs_review" {
		t.Fatalf("续跑后运行不应仍停留在人工核对: %+v", run)
	}
	if run.ActionCursor == 0 && run.Status == "running" {
		t.Fatalf("续跑应推进动作游标或收口运行状态: %+v", run)
	}
	// 冷却窗口必须拦住同一订单的立即二次续跑，避免上游异常时每分钟重开一次。
	// 第二轮扫描后代次不得继续递增。
	scheduler.scanPendingShipResumes(ctx)
	// runAfterCooldown 保存冷却窗口内的运行记录，代次应保持不变。
	runAfterCooldown, cooldownErr := store.Automation.GetRun(ctx, runID)
	if cooldownErr != nil {
		t.Fatal(cooldownErr)
	}
	if runAfterCooldown.AttemptCount != run.AttemptCount {
		t.Fatalf("冷却窗口内不应重复续跑: before=%d after=%d", run.AttemptCount, runAfterCooldown.AttemptCount)
	}
}

// TestScanPendingShipResumesSkipsWhenDisabledOrUninitialized 验证续跑扫描在开关关闭与状态缺失时安全返回。
func TestScanPendingShipResumesSkipsWhenDisabledOrUninitialized(t *testing.T) {
	// store、cleanup 保存测试数据库及关闭责任。
	store, cleanup := newAutomationTestStore(t)
	defer cleanup()
	// ctx 保存本测试共用的数据库上下文。
	ctx := context.Background()
	// 关闭共用止血开关时，续跑扫描必须整体跳过。
	t.Setenv(pendingShipCatchupEnv, "0")
	(&Scheduler{center: New(store, testSenderProvider{sender: &testSender{}}, nil)}).scanPendingShipResumes(ctx)
	// 恢复开关后，空状态保护仍必须拦截未初始化的调度器。
	t.Setenv(pendingShipCatchupEnv, "")
	// nilScheduler 是未初始化的调度器接收者。
	var nilScheduler *Scheduler
	nilScheduler.scanPendingShipResumes(ctx)
	// missingCenter 是缺少自动化中心的调度器。
	(&Scheduler{}).scanPendingShipResumes(ctx)
	// missingAutomation 是自动化存储未注入的调度器。
	(&Scheduler{center: &Center{store: &db.Store{}}}).scanPendingShipResumes(ctx)
}

// TestScanPendingShipResumesReportsStorageFailures 验证存储不可用时续跑扫描返回而不是静默跳过。
func TestScanPendingShipResumesReportsStorageFailures(t *testing.T) {
	// store、cleanup 保存测试数据库及关闭责任。
	store, cleanup := newAutomationTestStore(t)
	defer cleanup()
	// center 是仍引用已关闭数据库的自动化中心。
	center := New(store, testSenderProvider{sender: &testSender{}}, nil)
	// closeErr 保存数据库关闭错误。
	if closeErr := store.DB.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	// scheduler 是带失效存储的调度器；扫描必须安全返回。
	scheduler := &Scheduler{center: center, pendingShipCooldown: map[string]time.Time{}}
	scheduler.scanPendingShipResumes(context.Background())
}

// TestPendingShipResumeFrozenPlanBoundaries 验证续跑的放行判据只依据运行快照里的冻结动作计划。
// 判据一旦退回「读现行规则」，管理员编辑规则就会改变历史订单的发货义务（例如删掉发卡动作后
// 直接补一次确认发货），因此必须逐分支锁死。
func TestPendingShipResumeFrozenPlanBoundaries(t *testing.T) {
	// ownerOrder 保存归属校验通过的订单事实。
	ownerOrder := db.Order{OrderID: "o-frozen", CookieID: "frozen-acc"}
	// snapshot 按 Task 的字段名拼出快照，便于逐用例替换动作计划。
	snapshot := func(cookieID, orderID, planJSON string) string {
		return `{"AccountID":"` + cookieID + `","OrderID":"` + orderID + `","ActionPlan":` + planJSON + `}`
	}
	// tailPlan 是「发卡 + 确认发货」的冻结计划。
	tailPlan := `[{"ActionType":"send_card","Enabled":true},{"ActionType":"confirm_shipment","Enabled":true}]`
	// cases 覆盖放行、剩余动作仍会联系买家、快照缺失或损坏、归属不符与游标越界五类边界。
	cases := []struct {
		name    string
		raw     string
		cursor  int
		order   db.Order
		want    bool
		wantErr bool
	}{
		{name: "游标已越过发卡动作", raw: snapshot("frozen-acc", "o-frozen", tailPlan), cursor: 1, order: ownerOrder, want: true},
		{name: "仅确认发货计划", raw: snapshot("frozen-acc", "o-frozen", `[{"ActionType":"confirm_shipment","Enabled":true}]`), cursor: 0, order: ownerOrder, want: true},
		{name: "剩余动作仍含发卡", raw: snapshot("frozen-acc", "o-frozen", tailPlan), cursor: 0, order: ownerOrder, want: false},
		{name: "剩余动作仍含文本", raw: snapshot("frozen-acc", "o-frozen", `[{"ActionType":"send_text","Enabled":true},{"ActionType":"confirm_shipment","Enabled":true}]`), cursor: 0, order: ownerOrder, want: false},
		{name: "游标已在计划末尾", raw: snapshot("frozen-acc", "o-frozen", `[{"ActionType":"confirm_shipment","Enabled":true}]`), cursor: 1, order: ownerOrder, want: false},
		{name: "快照无法解析", raw: `{`, cursor: 0, order: ownerOrder, wantErr: true},
		{name: "快照缺少计划", raw: `{"AccountID":"frozen-acc","OrderID":"o-frozen"}`, cursor: 0, order: ownerOrder, wantErr: true},
		{name: "快照归属不符", raw: snapshot("other-acc", "o-frozen", tailPlan), cursor: 1, order: ownerOrder, wantErr: true},
		{name: "游标越界", raw: snapshot("frozen-acc", "o-frozen", tailPlan), cursor: 9, order: ownerOrder, wantErr: true},
	}
	// tc 是当前待验证的用例。
	for _, tc := range cases {
		// candidate 保存本用例的候选运行。
		candidate := db.PendingShipResume{Order: tc.order, RawEventJSON: tc.raw, ActionCursor: tc.cursor}
		// plan、eligible、err 保存恢复出的冻结计划、是否放行与不可续跑原因。
		plan, eligible, err := pendingShipResumeFrozenPlan(candidate)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("%s: 期望返回错误，实际 eligible=%v", tc.name, eligible)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: 非预期错误: %v", tc.name, err)
		}
		if eligible != tc.want {
			t.Fatalf("%s: eligible=%v want %v", tc.name, eligible, tc.want)
		}
		if tc.want && len(plan) == 0 {
			t.Fatalf("%s: 放行时必须把冻结计划交回调用方", tc.name)
		}
	}
}

// TestScanPendingShipResumesSkipsWhenAutoConfirmDisabled 验证续跑前必须过账号级「自动确认发货」闸门。
// 闸门关闭时 HandleTask 会直接返回而不收口运行；若先重开运行，它会留在 running 并继续持有租约，
// 租约到期后失败运行恢复链路直接执行 executeRule，从而绕过账号开关。
func TestScanPendingShipResumesSkipsWhenAutoConfirmDisabled(t *testing.T) {
	// store、cleanup 保存测试数据库及关闭责任。
	store, cleanup := newAutomationTestStore(t)
	defer cleanup()
	// ctx 保存本测试共用的数据库上下文。
	ctx := context.Background()
	// admin 保存规则所属管理员账号。
	admin, adminErr := store.Users.GetByUsername(ctx, "admin")
	if adminErr != nil {
		t.Fatal(adminErr)
	}
	// catchupAccount 写入账号夹具。
	catchupAccount(t, store, admin.ID, "gate-acc")
	// 显式关闭账号的自动确认发货开关，构造闸门关闭场景。
	if _, updateErr := store.DB.ExecContext(ctx, `UPDATE cookies SET auto_confirm=0 WHERE id='gate-acc'`); updateErr != nil {
		t.Fatal(updateErr)
	}
	// autoConfirm、confirmErr 保存闸门开关读数，确保本用例的前提成立。
	autoConfirm, confirmErr := store.Cookies.GetAutoConfirm(ctx, "gate-acc")
	if confirmErr != nil {
		t.Fatal(confirmErr)
	}
	if autoConfirm {
		t.Fatal("用例前提失败：自动确认发货应为关闭")
	}
	// ruleID 保存仅含确认发货动作的发货规则主键。
	ruleID := catchupRule(t, store, admin.ID, "gate-acc", "item-gate",
		[]db.AutomationActionInput{{ActionType: ActionConfirmShipment, Enabled: true}})
	catchupOrder(t, store, "gate-acc", "o-gate", "item-gate", "chat-gate")
	// rawTask 是冻结快照：游标 0 的剩余动作已全部是幂等状态动作，具备自动续跑条件。
	rawTask := `{"AccountID":"gate-acc","OrderID":"o-gate","ActionPlan":[{"ActionType":"confirm_shipment","Enabled":true}]}`
	// runID 保存被隔离且可续跑的运行主键。
	runID := catchupRun(t, store, ruleID, "gate-acc", "o-gate", rawTask)
	setRunState(t, store, runID, "needs_review", 1, 0)
	// provider 报告发送器就绪，确保拦截来自账号开关而不是连接状态。
	provider := selectiveSenderProvider{senders: map[string]*readinessTestSender{}, ready: map[string]bool{"gate-acc": true}}
	// scheduler 是使用选择性发送器的调度器。
	scheduler := &Scheduler{center: New(store, provider, nil), pendingShipCooldown: map[string]time.Time{}}
	scheduler.scanPendingShipResumes(ctx)
	// run 保存扫描后的运行记录，必须保持人工核对且代次不变。
	run, getErr := store.Automation.GetRun(ctx, runID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if run.AttemptCount != 1 || run.Status != "needs_review" {
		t.Fatalf("自动确认发货关闭时不得重开运行: %+v", run)
	}
}

// TestScanPendingShipDeliveriesDoesNotClaimCooldownWhenGatesBlock 验证闸门拦下时不占用冷却窗口。
// 冷却窗口代表「已经尝试过一次兜底」；若在发送器未就绪时提前领取，订单会被白白压制十分钟，
// 与日志里「等待下次扫描」的语义矛盾。
func TestScanPendingShipDeliveriesDoesNotClaimCooldownWhenGatesBlock(t *testing.T) {
	// store、cleanup 保存测试数据库及关闭责任。
	store, cleanup := newAutomationTestStore(t)
	defer cleanup()
	// ctx 保存本测试共用的数据库上下文。
	ctx := context.Background()
	// admin 保存规则所属管理员账号。
	admin, adminErr := store.Users.GetByUsername(ctx, "admin")
	if adminErr != nil {
		t.Fatal(adminErr)
	}
	// cardID 是发卡动作绑定的数据卡密组。
	cardID := catchupCard(t, store, admin.ID)
	catchupAccount(t, store, admin.ID, "blocked-acc")
	catchupRule(t, store, admin.ID, "blocked-acc", "item-blocked", []db.AutomationActionInput{catchupCardAction(cardID)})
	catchupOrder(t, store, "blocked-acc", "o-blocked", "item-blocked", "chat-blocked")
	// 首轮扫描：发送器未就绪，任务不触发，冷却窗口不得被占用。
	blocked := &Scheduler{center: New(store, selectiveSenderProvider{senders: map[string]*readinessTestSender{}, ready: map[string]bool{}}, nil), pendingShipCooldown: map[string]time.Time{}}
	blocked.scanPendingShipDeliveries(ctx)
	if len(blocked.pendingShipCooldown) != 0 {
		t.Fatalf("闸门拦下时不得占用冷却窗口: %+v", blocked.pendingShipCooldown)
	}
	// 发送器就绪后的下一轮扫描必须立刻补触发，不需要再等冷却。
	ready := &Scheduler{center: New(store, selectiveSenderProvider{senders: map[string]*readinessTestSender{}, ready: map[string]bool{"blocked-acc": true}}, nil), pendingShipCooldown: map[string]time.Time{}}
	ready.scanPendingShipDeliveries(ctx)
	// runs 保存补触发产生的运行数量，用于确认任务确实被触发。
	var runs int
	// countErr 保存统计补触发运行条数的查询错误。
	if countErr := store.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM automation_runs WHERE order_id='o-blocked' AND trigger_type='order_paid'`).Scan(&runs); countErr != nil {
		t.Fatal(countErr)
	}
	if runs != 1 {
		t.Fatalf("发送器就绪后应立刻补触发: runs=%d", runs)
	}
}
