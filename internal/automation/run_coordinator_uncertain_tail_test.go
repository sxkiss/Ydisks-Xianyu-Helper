package automation

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"xianyu-go/internal/db"
)

// TestCanContinueAfterUncertainAction 验证只有「剩余动作全部为幂等状态动作」时才放行。
// 这条判据是放行的安全边界：只要还有会再次联系买家的动作未完成，就必须维持原有熔断。
func TestCanContinueAfterUncertainAction(t *testing.T) {
	// cases 覆盖空集合、纯状态动作、以及混入消息动作三类关键边界。
	cases := []struct {
		name      string
		remaining []db.AutomationAction
		want      bool
	}{
		{name: "没有剩余动作", remaining: nil, want: false},
		{name: "空切片", remaining: []db.AutomationAction{}, want: false},
		{name: "仅确认发货", remaining: []db.AutomationAction{{ActionType: ActionConfirmShipment}}, want: true},
		{name: "多个确认发货", remaining: []db.AutomationAction{{ActionType: ActionConfirmShipment}, {ActionType: ActionConfirmShipment}}, want: true},
		{name: "含发卡动作", remaining: []db.AutomationAction{{ActionType: ActionSendCard}}, want: false},
		{name: "含模板动作", remaining: []db.AutomationAction{{ActionType: ActionSendTemplate}}, want: false},
		{name: "含文本动作", remaining: []db.AutomationAction{{ActionType: ActionSendText}}, want: false},
		{name: "状态动作后仍有发卡", remaining: []db.AutomationAction{{ActionType: ActionConfirmShipment}, {ActionType: ActionSendCard}}, want: false},
		{name: "含未知动作", remaining: []db.AutomationAction{{ActionType: "unknown_action"}}, want: false},
	}
	// tc 是当前待验证的用例，含剩余动作列表与期望的放行判定。
	for _, tc := range cases {
		// got 保存本次放行判定的实际结果，用于与用例期望值比对。
		if got := canContinueAfterUncertainAction(tc.remaining); got != tc.want {
			t.Errorf("%s: canContinueAfterUncertainAction=%v want %v", tc.name, got, tc.want)
		}
	}
}

// TestContinueAfterUncertainActionSwitch 验证止血开关默认开启、显式置 0 时关闭。
func TestContinueAfterUncertainActionSwitch(t *testing.T) {
	// 未设置时必须开启，否则本次修复在部署后不会生效。
	t.Setenv(continueUncertainActionEnv, "")
	if !continueAfterUncertainActionEnabled() {
		t.Fatal("默认应开启放行")
	}
	t.Setenv(continueUncertainActionEnv, "1")
	if !continueAfterUncertainActionEnabled() {
		t.Fatal("非 0 值应保持开启")
	}
	t.Setenv(continueUncertainActionEnv, "0")
	if continueAfterUncertainActionEnabled() {
		t.Fatal("显式设为 0 时应关闭放行")
	}
}

// TestExecuteRunActionsContinuesIdempotentTailAfterUncertainMessage 验证放行主链路：
// 消息动作结果不确定但剩余动作全是幂等状态动作时，运行推进游标继续执行，最后仍收口为人工核对。
// 这是「卡密已发出但发货状态未更新」问题的核心修复语义，必须逐分支锁死。
func TestExecuteRunActionsContinuesIdempotentTailAfterUncertainMessage(t *testing.T) {
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
	// ruleID 保存包含发卡与确认发货两类动作的规则主键。
	ruleID := uncertainTailRule(t, store, admin.ID, "uncertain-acc",
		[]db.AutomationActionInput{
			{ActionType: ActionSendCard, DeliveryCount: 1, Enabled: true, ConfigJSON: `{"spec_name":"颜色","spec_value":"黑"}`},
			{ActionType: ActionConfirmShipment, Enabled: true},
		})
	// runID 保存待执行运行主键。
	runID := uncertainTailRun(t, store, ruleID, "uncertain-acc", "o-uncertain")
	// running 保存重新读取的运行状态，游标与代次必须与夹具一致。
	running := uncertainTailRunningRun(t, store, runID)
	// executed 收集实际执行过的动作类型，用于证明确认发货确实被放行执行。
	executed := []string{}
	// coordinator 是注入不确定动作的运行协调器。
	coordinator := newUncertainTailCoordinator(store, func(_ context.Context, _ Task, action db.AutomationAction) (actionExecutionResult, error) {
		executed = append(executed, action.ActionType)
		if action.ActionType != ActionSendCard {
			return actionExecutionResult{}, nil
		}
		// 卡密内容可能已经发出但平台响应中断：这是结果不确定，而不是确定未发送。
		return actionExecutionResult{reviewProof: shipmentDeliveryProof{messages: []db.AutomationDeliveryMessage{{Kind: "text", Content: "卡密内容"}}}},
			uncertainAction(errors.New("平台发送响应中断"))
	})
	// task 是与动作规格匹配的付款发货任务。
	task := Task{TriggerType: TriggerOrderPaid, AccountID: "uncertain-acc", OrderID: "o-uncertain",
		ChatID: "chat-uncertain", BuyerID: "buyer-uncertain", ItemID: "item-uncertain",
		SpecName: "颜色", SpecValue: "黑"}
	// actions 是运行要执行的动作计划。
	actions := []db.AutomationAction{
		{ActionType: ActionSendCard, Enabled: true, CardID: 1, DeliveryCount: 1, ConfigJSON: `{"spec_name":"颜色","spec_value":"黑"}`},
		{ActionType: ActionConfirmShipment, Enabled: true},
	}
	// sent、deferred、runErr 是运行编排的返回结果。
	sent, deferred, runErr := coordinator.executeRunActions(ctx, task, ruleID, running, actions, false)
	if deferred {
		t.Fatal("结果不确定时不得进入延迟队列")
	}
	if !errors.Is(runErr, errAutomationNeedsReview) {
		t.Fatalf("放行后仍必须收口为人工核对: %v", runErr)
	}
	if sent != 0 {
		t.Fatalf("不确定动作不得计入已确认数量: %d", sent)
	}
	if len(executed) != 2 || executed[0] != ActionSendCard || executed[1] != ActionConfirmShipment {
		t.Fatalf("确认发货未被放行执行: %v", executed)
	}
	// resumed 保存落库后的运行状态，游标必须推进到计划末尾且状态为人工核对。
	resumed, resumedErr := store.Automation.GetRun(ctx, runID)
	if resumedErr != nil {
		t.Fatal(resumedErr)
	}
	if resumed.ActionCursor != len(actions) {
		t.Fatalf("放行后游标必须推进到计划末尾: %d", resumed.ActionCursor)
	}
	if resumed.Status != "needs_review" {
		t.Fatalf("放行链路的运行必须收口为人工核对: %+v", resumed)
	}
	if resumed.SentCount != 0 {
		t.Fatalf("不确定动作不得落库为已发送: %d", resumed.SentCount)
	}
	if resumed.DeliveryProof.TradeText == "" && len(resumed.DeliveryProof.Messages) == 0 && len(resumed.DeliveryProof.PicList) == 0 {
		t.Fatalf("结果不确定的卡密内容必须保留为发货凭证: %+v", resumed.DeliveryProof)
	}
}

// TestExecuteRunActionsQuarantinesUncertainMessageWithoutIdempotentTail 验证剩余动作仍会联系买家时维持熔断。
// 只要还有会再次联系买家的动作未完成，结果不确定就必须整体隔离，不能放行。
func TestExecuteRunActionsQuarantinesUncertainMessageWithoutIdempotentTail(t *testing.T) {
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
	// ruleID 保存包含两个消息动作的规则主键。
	ruleID := uncertainTailRule(t, store, admin.ID, "quarantine-acc",
		[]db.AutomationActionInput{
			{ActionType: ActionSendCard, DeliveryCount: 1, Enabled: true, ConfigJSON: `{"spec_name":"颜色","spec_value":"黑"}`},
			{ActionType: ActionSendText, MessageTemplate: "后续通知", Enabled: true},
		})
	// runID 保存待执行运行主键。
	runID := uncertainTailRun(t, store, ruleID, "quarantine-acc", "o-quarantine")
	// running 保存重新读取的运行状态。
	running := uncertainTailRunningRun(t, store, runID)
	// coordinator 是注入不确定动作的运行协调器。
	coordinator := newUncertainTailCoordinator(store, func(context.Context, Task, db.AutomationAction) (actionExecutionResult, error) {
		return actionExecutionResult{}, uncertainAction(errors.New("平台发送响应中断"))
	})
	// task 是付款发货任务。
	task := Task{TriggerType: TriggerOrderPaid, AccountID: "quarantine-acc", OrderID: "o-quarantine", ChatID: "chat", BuyerID: "buyer"}
	// actions 是仍包含消息动作的计划。
	actions := []db.AutomationAction{
		{ActionType: ActionSendCard, Enabled: true, CardID: 1, ConfigJSON: `{"spec_name":"颜色","spec_value":"黑"}`},
		{ActionType: ActionSendText, Enabled: true},
	}
	// sent、deferred、runErr 是运行编排的返回结果。
	sent, deferred, runErr := coordinator.executeRunActions(ctx, task, ruleID, running, actions, false)
	if deferred || sent != 0 {
		t.Fatalf("熔断路径不得延迟或计入发送: sent=%d deferred=%v", sent, deferred)
	}
	if !errors.Is(runErr, errAutomationNeedsReview) {
		t.Fatalf("结果不确定必须收口为人工核对: %v", runErr)
	}
	// quarantined 保存落库后的运行状态；熔断时游标不得推进。
	quarantined, quarantinedErr := store.Automation.GetRun(ctx, runID)
	if quarantinedErr != nil {
		t.Fatal(quarantinedErr)
	}
	if quarantined.Status != "needs_review" || quarantined.ActionCursor != 0 {
		t.Fatalf("熔断路径游标不得推进: %+v", quarantined)
	}
}

// TestExecuteRunActionsQuarantinesCertainNotSentTail 验证「确定未发送」时即便剩余动作全是幂等状态动作也不放行。
// 买家没有收到任何内容时把订单改判为已发货只会掩盖本地不一致，必须维持熔断。
func TestExecuteRunActionsQuarantinesCertainNotSentTail(t *testing.T) {
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
	// ruleID 保存包含发卡与确认发货两类动作的规则主键。
	ruleID := uncertainTailRule(t, store, admin.ID, "notsent-acc",
		[]db.AutomationActionInput{
			{ActionType: ActionSendCard, DeliveryCount: 1, Enabled: true, ConfigJSON: `{"spec_name":"颜色","spec_value":"黑"}`},
			{ActionType: ActionConfirmShipment, Enabled: true},
		})
	// runID 保存待执行运行主键。
	runID := uncertainTailRun(t, store, ruleID, "notsent-acc", "o-notsent")
	// running 保存重新读取的运行状态。
	running := uncertainTailRunningRun(t, store, runID)
	// executed 收集实际执行过的动作类型，用于断言确认发货未被放行。
	executed := []string{}
	// coordinator 是注入「确定未发送且本地库存恢复失败」的运行协调器。
	coordinator := newUncertainTailCoordinator(store, func(_ context.Context, _ Task, action db.AutomationAction) (actionExecutionResult, error) {
		executed = append(executed, action.ActionType)
		// 与卡密发货一致：确定未发送后恢复库存失败，错误链里同时带有「确定未发送」与本地失败。
		return actionExecutionResult{}, uncertainAction(errors.Join(ErrMessageNotSent, errors.New("恢复未发送卡密库存: 约束失败")))
	})
	// task 是与动作规格匹配的付款发货任务。
	task := Task{TriggerType: TriggerOrderPaid, AccountID: "notsent-acc", OrderID: "o-notsent",
		ChatID: "chat-notsent", BuyerID: "buyer-notsent", ItemID: "item-uncertain",
		SpecName: "颜色", SpecValue: "黑"}
	// actions 是运行要执行的动作计划。
	actions := []db.AutomationAction{
		{ActionType: ActionSendCard, Enabled: true, CardID: 1, DeliveryCount: 1, ConfigJSON: `{"spec_name":"颜色","spec_value":"黑"}`},
		{ActionType: ActionConfirmShipment, Enabled: true},
	}
	// sent、deferred、runErr 是运行编排的返回结果。
	sent, deferred, runErr := coordinator.executeRunActions(ctx, task, ruleID, running, actions, false)
	if deferred || sent != 0 {
		t.Fatalf("确定未发送不得延迟或计入发送: sent=%d deferred=%v", sent, deferred)
	}
	if !errors.Is(runErr, errAutomationNeedsReview) {
		t.Fatalf("确定未发送必须收口为人工核对: %v", runErr)
	}
	if len(executed) != 1 || executed[0] != ActionSendCard {
		t.Fatalf("确定未发送时不得放行后续状态动作: %v", executed)
	}
	// quarantined 保存落库后的运行状态；熔断时游标不得推进。
	quarantined, quarantinedErr := store.Automation.GetRun(ctx, runID)
	if quarantinedErr != nil {
		t.Fatal(quarantinedErr)
	}
	if quarantined.Status != "needs_review" || quarantined.ActionCursor != 0 {
		t.Fatalf("确定未发送时游标不得推进: %+v", quarantined)
	}
}

// uncertainTailAccount 写入一个供运行外键引用的账号夹具；动作计划不需要账号状态门禁。
func uncertainTailAccount(t *testing.T, store *db.Store, userID int64, cookieID string) {
	t.Helper()
	// saveErr 保存账号 Cookie 夹具写入错误。
	if saveErr := store.Cookies.Save(context.Background(), cookieID, "cv=1", userID); saveErr != nil {
		t.Fatalf("写入账号夹具失败: %v", saveErr)
	}
}

// uncertainTailRule 写入账号与一条绑定指定动作的 order_paid 规则并返回规则主键。
// 动作规格与夹具任务的「颜色/黑」一致，保证动作计划能通过订单规格匹配闸。
func uncertainTailRule(t *testing.T, store *db.Store, userID int64, cookieID string, actions []db.AutomationActionInput) int64 {
	t.Helper()
	uncertainTailAccount(t, store, userID, cookieID)
	// ruleID、ruleErr 保存规则写入结果与错误。
	ruleID, ruleErr := store.Automation.Create(context.Background(), db.AutomationRuleInput{
		UserID: userID, CookieID: cookieID, Name: "uncertain-" + cookieID, ItemID: "item-uncertain",
		TriggerType: TriggerOrderPaid, Enabled: true, Actions: actions,
	})
	if ruleErr != nil {
		t.Fatal(ruleErr)
	}
	return ruleID
}

// uncertainTailRun 通过真实存储创建一条 order_paid 运行并返回运行主键。
func uncertainTailRun(t *testing.T, store *db.Store, ruleID int64, cookieID, orderID string) int64 {
	t.Helper()
	// runID、started、startErr 保存运行写入结果与错误。
	runID, started, startErr := store.Automation.TryStartRun(context.Background(), db.AutomationRun{
		RuleID: ruleID, CookieID: cookieID, OrderID: orderID, ItemID: "item-uncertain",
		TriggerType: TriggerOrderPaid, TriggerKey: "uncertain:" + orderID,
		RawEventJSON: "{}", LeaseExpiresAt: time.Now().Add(10 * time.Minute).Unix(),
	})
	if startErr != nil || !started {
		t.Fatalf("写入运行夹具失败: started=%v err=%v", started, startErr)
	}
	return runID
}

// uncertainTailRunningRun 把运行改写成 running 并返回重新读取的快照。
// 租约必须有效，否则动作检查点的归属锁会拒绝领取。
func uncertainTailRunningRun(t *testing.T, store *db.Store, runID int64) *db.AutomationRun {
	t.Helper()
	// updateErr 保存运行状态改写错误；租约延长到十分钟后保证检查点可领取。
	if _, updateErr := store.DB.ExecContext(context.Background(),
		`UPDATE automation_runs SET status='running', attempt_count=1, action_cursor=0, action_started=0, next_retry_at=0, lease_expires_at=? WHERE id=?`,
		time.Now().Add(10*time.Minute).Unix(), runID); updateErr != nil {
		t.Fatalf("改写运行夹具失败: %v", updateErr)
	}
	// run、getErr 保存重新读取的运行快照与错误。
	run, getErr := store.Automation.GetRun(context.Background(), runID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	return run
}

// newUncertainTailCoordinator 构造一个注入全部依赖的运行协调器，供直连测试运行编排分支。
// 依赖全部用测试替身，隔离订单补全、账号门禁与外部动作，只保留真实存储检查点链路。
func newUncertainTailCoordinator(store *db.Store, executeAction func(context.Context, Task, db.AutomationAction) (actionExecutionResult, error)) automationRunCoordinator {
	return automationRunCoordinator{
		store:   store,
		planner: actionPlanner{},
		logger:  slog.Default(),
		prepareTask: func(_ context.Context, task Task) (Task, error) {
			return task, nil
		},
		actionDelaySeconds: func(context.Context, db.AutomationAction) (int, error) {
			return 0, nil
		},
		accountAutomationAllowed: func(context.Context, string) (bool, error) {
			return true, nil
		},
		accountSenderReady: func(string) bool {
			return true
		},
		deferTask: func(context.Context, Task, int64) error {
			return nil
		},
		executeAction: func(ctx context.Context, task Task, action db.AutomationAction, _ shipmentDeliveryProof) (actionExecutionResult, error) {
			return executeAction(ctx, task, action)
		},
		hasNotifier: func() bool {
			return false
		},
		notifyResult: func(context.Context, Task, int64, string, int, string) {},
	}
}

// newUncertainTailCoordinatorWithDelay 在基础协调器上再注入延迟与延迟任务回调：
// delaySeconds 按动作返回等待秒数，onDeferred 捕获被持久化的延迟任务，供恢复用例重放。
func newUncertainTailCoordinatorWithDelay(
	store *db.Store,
	executeAction func(context.Context, Task, db.AutomationAction) (actionExecutionResult, error),
	delaySeconds func(db.AutomationAction) int,
	onDeferred func(Task),
) automationRunCoordinator {
	// coordinator 是在基础协调器之上注入延迟行为的运行协调器。
	coordinator := newUncertainTailCoordinator(store, executeAction)
	// actionDelaySeconds 用测试配置替换默认的零延迟。
	coordinator.actionDelaySeconds = func(_ context.Context, action db.AutomationAction) (int, error) {
		return delaySeconds(action), nil
	}
	// deferTask 记录延迟任务并返回成功，模拟延迟任务已持久化。
	coordinator.deferTask = func(_ context.Context, task Task, _ int64) error {
		if onDeferred != nil {
			onDeferred(task)
		}
		return nil
	}
	return coordinator
}

// TestExecuteRunActionsKeepsReviewAcrossDelayedTailAction 验证收尾动作带延迟时人工核对收口不会丢失。
// 放行后若收尾动作带延迟，本次调用只持久化延迟任务就返回；恢复时内存状态已经丢失，
// 必须靠任务快照继续收口为 needs_review，否则「消息送达结果不确定」会被记成成功。
func TestExecuteRunActionsKeepsReviewAcrossDelayedTailAction(t *testing.T) {
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
	// ruleID 保存包含发卡与确认发货两类动作的规则主键。
	ruleID := uncertainTailRule(t, store, admin.ID, "delay-acc",
		[]db.AutomationActionInput{
			{ActionType: ActionSendCard, DeliveryCount: 1, Enabled: true, ConfigJSON: `{"spec_name":"颜色","spec_value":"黑"}`},
			{ActionType: ActionConfirmShipment, Enabled: true},
		})
	// runID 保存待执行运行主键。
	runID := uncertainTailRun(t, store, ruleID, "delay-acc", "o-delay")
	// running 保存重新读取的运行状态。
	running := uncertainTailRunningRun(t, store, runID)
	// executed 收集实际执行过的动作类型。
	executed := []string{}
	// deferredTask 保存被持久化的延迟任务，用于模拟延迟到点后的重放。
	var deferredTask Task
	// coordinator 是「只有确认发货动作带延迟」的运行协调器。
	coordinator := newUncertainTailCoordinatorWithDelay(store, func(_ context.Context, _ Task, action db.AutomationAction) (actionExecutionResult, error) {
		executed = append(executed, action.ActionType)
		if action.ActionType != ActionSendCard {
			return actionExecutionResult{}, nil
		}
		// 卡密内容可能已经发出但平台响应中断：结果不确定，而不是确定未发送。
		return actionExecutionResult{reviewProof: shipmentDeliveryProof{messages: []db.AutomationDeliveryMessage{{Kind: "text", Content: "卡密内容"}}}},
			uncertainAction(errors.New("平台发送响应中断"))
	}, func(action db.AutomationAction) int {
		if action.ActionType == ActionConfirmShipment {
			return 600
		}
		return 0
	}, func(task Task) {
		deferredTask = task
	})
	// task 是与动作规格匹配的付款发货任务。
	task := Task{TriggerType: TriggerOrderPaid, AccountID: "delay-acc", OrderID: "o-delay",
		ChatID: "chat-delay", BuyerID: "buyer-delay", ItemID: "item-uncertain",
		SpecName: "颜色", SpecValue: "黑"}
	// actions 是运行要执行的动作计划。
	actions := []db.AutomationAction{
		{ActionType: ActionSendCard, Enabled: true, CardID: 1, DeliveryCount: 1, ConfigJSON: `{"spec_name":"颜色","spec_value":"黑"}`},
		{ActionType: ActionConfirmShipment, Enabled: true},
	}
	// sent、deferred、runErr 是首轮执行的返回结果：带延迟的收尾动作应让本轮进入延迟队列。
	sent, deferred, runErr := coordinator.executeRunActions(ctx, task, ruleID, running, actions, false)
	if !deferred || runErr != nil || sent != 0 {
		t.Fatalf("带延迟的收尾动作应进入延迟队列: sent=%d deferred=%v err=%v", sent, deferred, runErr)
	}
	if len(executed) != 1 || executed[0] != ActionSendCard {
		t.Fatalf("首轮只应执行发卡动作: %v", executed)
	}
	// 待收口标记必须随延迟任务一起持久化，否则恢复后无法继续收口。
	if deferredTask.Raw[pendingUncertaintyReasonKey] == nil || deferredTask.Raw[pendingUncertaintyErrorKey] == nil {
		t.Fatalf("延迟任务未携带待人工核对标记: %+v", deferredTask.Raw)
	}
	// resumed 保存恢复时的运行快照；游标已由放行逻辑推进到收尾动作。
	resumed, resumedErr := store.Automation.GetRun(ctx, runID)
	if resumedErr != nil {
		t.Fatal(resumedErr)
	}
	// sentLater、deferredLater、laterErr 是延迟任务重放的返回结果。
	sentLater, deferredLater, laterErr := coordinator.executeRunActions(ctx, deferredTask, ruleID, resumed, actions, false)
	if deferredLater {
		t.Fatal("延迟重放不应再次进入延迟队列")
	}
	if !errors.Is(laterErr, errAutomationNeedsReview) {
		t.Fatalf("延迟恢复后仍必须收口为人工核对: %v", laterErr)
	}
	if sentLater != 0 {
		t.Fatalf("不确定动作不得计入已确认数量: %d", sentLater)
	}
	// final 保存落库后的运行状态，必须收口为人工核对。
	final, finalErr := store.Automation.GetRun(ctx, runID)
	if finalErr != nil {
		t.Fatal(finalErr)
	}
	if final.Status != "needs_review" {
		t.Fatalf("延迟恢复后运行必须收口为人工核对: %+v", final)
	}
}
