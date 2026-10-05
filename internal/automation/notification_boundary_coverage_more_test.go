package automation

import (
	"context"
	"strings"
	"testing"

	"xianyu-go/internal/db"
)

// triggerAwareNotificationProbe 记录自动化中心是否传递了具体触发类别。
type triggerAwareNotificationProbe struct {
	// triggerType 保存自动化中心传入的触发类别编码。
	triggerType string
	// calls 保存按触发类别通知入口被调用的次数。
	calls int
	// manualCalls 保存无运行主键的人工处理通知次数。
	manualCalls int
	// manualAction、manualReason、manualKey 保存人工处理通知的环节、原因和幂等键。
	manualAction, manualReason, manualKey string
}

// NotifyManualIntervention 记录没有运行主键的人工处理阶段通知。
func (p *triggerAwareNotificationProbe) NotifyManualIntervention(_ context.Context, _, _, _, _, _, action, reason, _ string, idempotencyKey string) {
	p.manualCalls++
	p.manualAction = action
	p.manualReason = reason
	p.manualKey = idempotencyKey
}

// NotifyAutomationRun 记录旧版统一通知入口，便于确认测试替身仍满足兼容接口。
func (p *triggerAwareNotificationProbe) NotifyAutomationRun(context.Context, int64, string, string, string, string, string, string) {
	// calls 保存兼容入口被调用次数；本测试期望新入口被选择，因此通常为零。
	p.calls++
}

// NotifyAutomationRunForTrigger 记录按触发类别通知入口收到的编码。
func (p *triggerAwareNotificationProbe) NotifyAutomationRunForTrigger(_ context.Context, triggerType string, _ int64, _ string, _ string, _ string, _ string, _ string, _ string) {
	// triggerType 保存本次自动化终态通知的细分类别。
	p.triggerType = triggerType
	// calls 保存按触发类别通知入口被调用次数。
	p.calls++
}

// TestAutomationNotifierPassesTriggerCategory 验证自动化中心把四类任务编码传给支持细分事件的通知器。
func TestAutomationNotifierPassesTriggerCategory(t *testing.T) {
	// probe 保存能够同时实现新旧通知接口的本地替身。
	probe := &triggerAwareNotificationProbe{}
	// delivery 保存使用该替身的通知协调器。
	delivery := deliveryNotifier{current: func() Notifier { return probe }}
	delivery.notifyResult(context.Background(), Task{TriggerType: TriggerOrderCreated, OrderID: "order"}, 8, "success", 1, "")
	if probe.calls != 1 || probe.triggerType != TriggerOrderCreated {
		t.Fatalf("trigger-aware notification calls=%d trigger=%q", probe.calls, probe.triggerType)
	}
}

// TestAutomationNotificationsCoverOptionalAndTerminalBranches 验证自动化通知器的可选依赖、成功空跑、失败和人工核对通知。
func TestAutomationNotificationsCoverOptionalAndTerminalBranches(t *testing.T) {
	// ctx 是通知测试共用的上下文。
	ctx := context.Background()
	// nilNotifier 验证未装配通知器时所有入口都安全跳过。
	nilNotifier := deliveryNotifier{current: func() Notifier { return nil }}
	nilNotifier.notifyResult(ctx, Task{TriggerType: TriggerBuyerReviewed}, 1, "success", 1, "")
	nilNotifier.notifyRunNeedsReview(ctx, db.AutomationRun{}, "no notifier")
	// notifier 保存记录通知调用的本地替身。
	notifier := &recordingNotifier{}
	// delivery 保存固定通知器的通知协调器。
	delivery := deliveryNotifier{current: func() Notifier { return notifier }}
	// task 保存成功通知展示所需的订单事实。
	task := Task{AccountID: "account", BuyerID: "buyer", ItemID: "item", ChatID: "chat", OrderID: "order", TriggerType: TriggerOrderPaid}
	delivery.notifyResult(ctx, task, 2, "success", 0, "empty")
	if len(notifier.messages()) != 1 || !strings.Contains(notifier.messages()[0], "已发送 0 条") {
		t.Fatalf("成功终态即使 sent 为零也应通知: %v", notifier.messages())
	}
	delivery.notifyResult(ctx, task, 2, "success", 1, "")
	delivery.notifyResult(ctx, Task{TriggerType: "unknown_trigger", OrderID: "order"}, 3, "failed", 0, "failed")
	delivery.notifyRunNeedsReview(ctx, db.AutomationRun{ID: 4, CookieID: "account", BuyerID: "buyer", ItemID: "item", ChatID: "chat", OrderID: "order", TriggerType: TriggerBuyerReviewed}, "uncertain")
	if len(notifier.messages()) != 4 {
		t.Fatalf("终态通知数量=%d want 4", len(notifier.messages()))
	}
	// nilCenter 验证 Center 兼容通知入口的空接收者保护。
	var nilCenter *Center
	nilCenter.notifyRunNeedsReview(ctx, db.AutomationRun{}, "nil center")
	// store、cleanup 保存唤醒凭证阻断任务测试数据库及关闭责任。
	store, cleanup := newAutomationTestStore(t)
	defer cleanup()
	// center 保存具备自动化存储的通知中心。
	center := New(store, nil, nil)
	center.wakeCredentialBlockedAutomation(ctx, "")
	// closeErr 保存数据库关闭错误，随后用于覆盖唤醒写入失败日志路径。
	if closeErr := store.DB.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	center.wakeCredentialBlockedAutomation(ctx, "account")
	// emptyCenter 验证缺少存储的中心不会尝试执行数据库 I/O。
	emptyCenter := &Center{}
	emptyCenter.wakeCredentialBlockedAutomation(ctx, "account")
}

// TestBargainUncertainResultSendsManualInterventionNotification 验证免拼接口结果不确定时停止重试并发送独立人工处理通知。
func TestBargainUncertainResultSendsManualInterventionNotification(t *testing.T) {
	// store、cleanup 保存免拼人工处理通知测试数据库及关闭责任。
	store, cleanup := newAutomationTestStore(t)
	defer cleanup()
	// ctx 是账号设置、免拼执行和阶段状态读取共用的上下文。
	ctx := context.Background()
	// admin、adminErr 保存账号所属管理员及查询错误。
	admin, adminErr := store.Users.GetByUsername(ctx, "admin")
	if adminErr != nil {
		t.Fatal(adminErr)
	}
	// enabled 是独立自动免拼开关的开启值。
	enabled := true
	// settingsErr 保存自动免拼开关写入错误。
	_, settingsErr := store.Cookies.UpdateSettings(ctx, "cid", db.AccountSettingsUpdate{UserID: admin.ID, AutoBargain: &enabled})
	if settingsErr != nil {
		t.Fatal(settingsErr)
	}
	// probe 记录自动化中心发送的阶段人工处理通知。
	probe := &triggerAwareNotificationProbe{}
	// client 返回传输错误，使免拼结果按未知外部结果处理。
	client := &fakeMTop{freeShippingErr: context.DeadlineExceeded}
	// center 是注入免拼客户端和通知替身的自动化中心。
	center := NewWithDependencies(store, nil, nil, CenterDependencies{MTop: client, Notifier: probe})
	// task 是“我已小刀，待刀成”WebSocket 阶段的完整平台事实。
	task := Task{Source: "ws", AccountID: "cid", OrderRole: OrderRoleSeller, TriggerType: TriggerBargainPending, OrderID: "bargain-review-order", ItemID: "item", BuyerID: "buyer", ChatID: "chat", IsBargain: true}
	// handleErr 保存免拼结果不确定错误。
	handleErr := center.HandleTask(ctx, task)
	if handleErr == nil || probe.manualCalls != 1 || probe.manualAction != "二人小刀免拼" || !strings.Contains(probe.manualReason, "deadline") || probe.manualKey != bargainManualInterventionKey(task) {
		t.Fatalf("免拼人工处理通知异常: err=%v calls=%d action=%q reason=%q key=%q", handleErr, probe.manualCalls, probe.manualAction, probe.manualReason, probe.manualKey)
	}
	// stageStatus 保存免拼阶段最终隔离状态。
	var stageStatus string
	if /* stageErr 保存免拼阶段隔离状态的读取错误。 */ stageErr := store.DB.QueryRowContext(ctx, `SELECT status FROM bargain_free_shipping_stages WHERE order_id=? AND cookie_id=?`, task.OrderID, task.AccountID).Scan(&stageStatus); stageErr != nil {
		t.Fatal(stageErr)
	}
	if stageStatus != "needs_review" || client.freeShippingCalls != 1 {
		t.Fatalf("免拼阶段状态=%q calls=%d", stageStatus, client.freeShippingCalls)
	}
}
