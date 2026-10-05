package automation

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"xianyu-go/internal/db"
)

// seedRecoverableSchedulerRun 创建一条未进入外部动作、可由恢复调度器领取的运行记录。
func seedRecoverableSchedulerRun(t *testing.T, store *db.Store, ctx context.Context, suffix string, raw any) int64 {
	// admin、adminErr 保存恢复规则所属管理员及查询错误。
	admin, adminErr := store.Users.GetByUsername(ctx, "admin")
	if adminErr != nil {
		t.Fatal(adminErr)
	}
	// ruleID、ruleErr 保存恢复运行引用的规则及创建错误。
	ruleID, ruleErr := store.Automation.Create(ctx, db.AutomationRuleInput{
		UserID: admin.ID, CookieID: "cid", Name: "recovery-" + suffix, TriggerType: TriggerBuyerReviewed, Enabled: true,
		Actions: []db.AutomationActionInput{{ActionType: ActionSendText, MessageTemplate: "recovery", Enabled: true}},
	})
	if ruleErr != nil {
		t.Fatal(ruleErr)
	}
	// rawJSON、marshalErr 保存恢复任务快照及序列化错误。
	rawJSON, marshalErr := json.Marshal(raw)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	// runID、started、startErr 保存初始运行记录及抢占结果。
	runID, started, startErr := store.Automation.TryStartRun(ctx, db.AutomationRun{
		RuleID: ruleID, CookieID: "cid", OrderID: "recovery-" + suffix, TriggerType: TriggerBuyerReviewed,
		TriggerKey: "recovery:" + suffix, RawEventJSON: string(rawJSON), LeaseExpiresAt: time.Now().Add(time.Minute).Unix(),
	})
	if startErr != nil || !started {
		t.Fatalf("创建恢复运行失败: started=%v err=%v", started, startErr)
	}
	// updateErr 保存清除租约和设置立即恢复时间的错误。
	if _, updateErr := store.DB.ExecContext(ctx, `UPDATE automation_runs SET lease_expires_at=0,next_retry_at=0 WHERE id=?`, runID); updateErr != nil {
		t.Fatal(updateErr)
	}
	return runID
}

// TestSchedulerRecoveryCoversInvalidSnapshotAndAccountPostpone 验证恢复调度器对非法快照和账号阻断的收口分支。
func TestSchedulerRecoveryCoversInvalidSnapshotAndAccountPostpone(t *testing.T) {
	// store、cleanup 保存恢复调度器测试数据库及关闭责任。
	store, cleanup := newAutomationTestStore(t)
	defer cleanup()
	// ctx 是恢复运行数据库操作共用的上下文。
	ctx := context.Background()
	// invalidRunID 保存非法 JSON 快照对应的运行主键。
	invalidRunID := seedRecoverableSchedulerRun(t, store, ctx, "invalid", "not-an-object")
	// invalidErr 保存把快照直接替换为非法 JSON 的更新错误。
	if _, invalidErr := store.DB.ExecContext(ctx, `UPDATE automation_runs SET raw_event_json='{"broken' WHERE id=?`, invalidRunID); invalidErr != nil {
		t.Fatal(invalidErr)
	}
	// invalidScheduler 负责把非法快照隔离到人工核对状态。
	invalidScheduler := &Scheduler{center: New(store, nil, nil)}
	// invalidResultErr 保存非法快照隔离结果的错误。
	if invalidResultErr := invalidScheduler.runRecoveryTasks(ctx); invalidResultErr != nil {
		t.Fatalf("非法快照隔离不应失败: %v", invalidResultErr)
	}
	// invalidRun、invalidGetErr 保存隔离后的运行记录。
	invalidRun, invalidGetErr := store.Automation.GetRun(ctx, invalidRunID)
	if invalidGetErr != nil || invalidRun.Status != "needs_review" {
		t.Fatalf("非法快照状态=%+v err=%v", invalidRun, invalidGetErr)
	}
	// blockedRunID 保存账号被暂停时应延期的恢复运行主键。
	blockedRunID := seedRecoverableSchedulerRun(t, store, ctx, "blocked", Task{AccountID: "cid", TriggerType: TriggerBuyerReviewed, OrderID: "recovery-blocked"})
	// pauseErr 保存暂停账号的状态写入错误。
	_, pauseErr := store.Cookies.SetPause(ctx, "cid", 60)
	if pauseErr != nil {
		t.Fatal(pauseErr)
	}
	// blockedScheduler 负责验证账号状态门禁会将任务移到恢复队列尾部。
	blockedScheduler := &Scheduler{center: New(store, nil, nil)}
	// blockedResultErr 保存账号阻断延期结果的错误。
	if blockedResultErr := blockedScheduler.runRecoveryTasks(ctx); blockedResultErr != nil {
		t.Fatalf("账号阻断延期不应失败: %v", blockedResultErr)
	}
	// blockedRun、blockedGetErr 保存延期后的运行记录。
	blockedRun, blockedGetErr := store.Automation.GetRun(ctx, blockedRunID)
	if blockedGetErr != nil || blockedRun.LeaseExpiresAt <= time.Now().UTC().Unix() {
		t.Fatalf("账号阻断延期状态=%+v err=%v", blockedRun, blockedGetErr)
	}
	// resetErr 保存把延期运行再次置为到期以验证下一次延期写入失败的错误。
	if _, resetErr := store.DB.ExecContext(ctx, `UPDATE automation_runs SET lease_expires_at=0 WHERE id=?`, blockedRunID); resetErr != nil {
		t.Fatal(resetErr)
	}
	// triggerErr 保存阻止延期写入的 SQLite 触发器创建错误。
	if _, triggerErr := store.DB.ExecContext(ctx, `CREATE TRIGGER reject_scheduler_postpone
		BEFORE UPDATE OF lease_expires_at ON automation_runs
		BEGIN SELECT RAISE(ABORT, 'forced scheduler postpone failure'); END`); triggerErr != nil {
		t.Fatal(triggerErr)
	}
	// retryErr 保存延期落库失败后的统一错误。
	retryErr := blockedScheduler.runRecoveryTasks(ctx)
	if retryErr == nil || !strings.Contains(retryErr.Error(), "forced scheduler postpone failure") {
		t.Fatalf("延期写入失败未返回错误: %v", retryErr)
	}
}

// newPaidRecoveryFixture 创建一条尚未执行外部动作的付款运行，用于验证恢复调度器的最新订单状态门禁。
// orderStatus 是本地最新订单状态，source 是运行创建来源；返回运行主键、消息替身、调度器、存储和清理函数。
func newPaidRecoveryFixture(t *testing.T, orderStatus, source string) (int64, *testSender, *Scheduler, *db.Store, func()) {
	// store、cleanup 保存隔离测试数据库及关闭责任。
	store, cleanup := newAutomationTestStore(t)
	// ctx 是测试数据创建使用的上下文。
	ctx := context.Background()
	// admin、adminErr 保存付款规则所属管理员及查询错误。
	admin, adminErr := store.Users.GetByUsername(ctx, "admin")
	if adminErr != nil {
		cleanup()
		t.Fatal(adminErr)
	}
	// cardID、cardErr 保存若门禁失效就会被发送的一次性测试卡密组。
	cardID, cardErr := store.Cards.Create(ctx, &db.CardFull{Name: "恢复门禁卡", Type: "text", TextContent: "RECOVERY-MUST-BE-GATED", Enabled: true, UserID: admin.ID})
	if cardErr != nil {
		cleanup()
		t.Fatal(cardErr)
	}
	// ruleID、ruleErr 保存付款后发卡规则及创建错误。
	ruleID, ruleErr := store.Automation.Create(ctx, db.AutomationRuleInput{UserID: admin.ID, CookieID: "cid", ItemID: "recovery-item", Name: "付款恢复门禁", TriggerType: TriggerOrderPaid, Enabled: true, Actions: []db.AutomationActionInput{{ActionType: ActionSendCard, CardID: cardID, DeliveryCount: 1, ConfigJSON: `{}`, Enabled: true}}})
	if ruleErr != nil {
		cleanup()
		t.Fatal(ruleErr)
	}
	// orderErr 保存恢复前最新订单事实的写入错误。
	orderErr := store.Orders.Upsert(ctx, "paid-recovery-order", db.OrderUpsertOpts{CookieID: "cid", ItemID: "recovery-item", BuyerID: "buyer", ChatID: "chat", Quantity: "1", OrderStatus: orderStatus})
	if orderErr != nil {
		cleanup()
		t.Fatal(orderErr)
	}
	// task 是运行创建时保存的原始付款事件快照。
	// task 是恢复夹具中已明确确认卖家角色的付款事件；未知角色恢复由独立回归覆盖。
	task := Task{Source: source, AccountID: "cid", OrderRole: OrderRoleSeller, TriggerType: TriggerOrderPaid, OrderID: "paid-recovery-order", ItemID: "recovery-item", BuyerID: "buyer", ChatID: "chat", Quantity: "1", OrderStatus: "pending_ship"}
	// rawJSON、marshalErr 保存任务快照编码及序列化错误。
	rawJSON, marshalErr := json.Marshal(task)
	if marshalErr != nil {
		cleanup()
		t.Fatal(marshalErr)
	}
	// runID、started、startErr 保存初始运行及唯一领取结果。
	runID, started, startErr := store.Automation.TryStartRun(ctx, db.AutomationRun{RuleID: ruleID, CookieID: "cid", ItemID: "recovery-item", OrderID: task.OrderID, BuyerID: "buyer", ChatID: "chat", TriggerType: TriggerOrderPaid, TriggerKey: "paid-recovery:" + source + ":" + orderStatus, RawEventJSON: string(rawJSON), LeaseExpiresAt: time.Now().Add(time.Minute).Unix()})
	if startErr != nil || !started {
		cleanup()
		t.Fatalf("创建付款恢复运行失败: started=%v err=%v", started, startErr)
	}
	// updateErr 保存把未执行运行调整为立即到期的写入错误。
	if _, updateErr := store.DB.ExecContext(ctx, `UPDATE automation_runs SET lease_expires_at=0,next_retry_at=0 WHERE id=?`, runID); updateErr != nil {
		cleanup()
		t.Fatal(updateErr)
	}
	// sender 记录恢复调度器是否越过门禁发送卡密。
	sender := &testSender{}
	// scheduler 是使用在线发送器的恢复调度器。
	scheduler := NewScheduler(New(store, testSenderProvider{sender: sender}, nil))
	return runID, sender, scheduler, store, cleanup
}

// TestPaidRecoveryRechecksCurrentOrderAndMasterSwitch 验证历史付款运行恢复不会抢跑已取消订单或绕过用户关闭的自动发货开关。
func TestPaidRecoveryRechecksCurrentOrderAndMasterSwitch(t *testing.T) {
	t.Run("订单已取消时取消旧运行", func(t *testing.T) {
		// runID、sender、scheduler、store、cleanup 保存已取消订单恢复场景的完整测试夹具。
		runID, sender, scheduler, store, cleanup := newPaidRecoveryFixture(t, "canceled", "ws")
		defer cleanup()
		if /* runErr 保存已取消订单恢复扫描的执行错误。 */ runErr := scheduler.runRecoveryTasks(context.Background()); runErr != nil {
			t.Fatalf("取消旧运行失败: %v", runErr)
		}
		// run、readErr 保存调度后的运行状态及读取错误。
		run, readErr := store.Automation.GetRun(context.Background(), runID)
		if readErr != nil || run.Status != "canceled" || len(sender.texts) != 0 {
			t.Fatalf("已取消订单仍发生恢复: run=%+v messages=%v err=%v", run, sender.texts, readErr)
		}
	})
	t.Run("自动发货关闭时延期旧运行", func(t *testing.T) {
		// runID、sender、scheduler、store、cleanup 保存关闭自动发货场景的完整测试夹具。
		runID, sender, scheduler, store, cleanup := newPaidRecoveryFixture(t, "pending_ship", "ws")
		defer cleanup()
		// settingsErr 保存关闭账号自动发货总开关的数据库错误。
		_, settingsErr := store.DB.ExecContext(context.Background(), `UPDATE cookies SET auto_confirm=0 WHERE id='cid'`)
		if settingsErr != nil {
			t.Fatal(settingsErr)
		}
		if /* runErr 保存自动发货关闭场景的恢复扫描错误。 */ runErr := scheduler.runRecoveryTasks(context.Background()); runErr != nil {
			t.Fatalf("延期旧运行失败: %v", runErr)
		}
		// run、readErr 保存调度后的运行状态及读取错误。
		run, readErr := store.Automation.GetRun(context.Background(), runID)
		if readErr != nil || run.Status != "running" || run.LeaseExpiresAt <= time.Now().UTC().Unix() || len(sender.texts) != 0 {
			t.Fatalf("关闭自动发货后仍发生恢复: run=%+v messages=%v err=%v", run, sender.texts, readErr)
		}
	})
	t.Run("人工来源付款运行转人工处理", func(t *testing.T) {
		// runID、sender、scheduler、store、cleanup 保存不可信来源付款运行的完整测试夹具。
		runID, sender, scheduler, store, cleanup := newPaidRecoveryFixture(t, "pending_ship", "manual")
		defer cleanup()
		if /* runErr 保存人工来源运行隔离扫描的执行错误。 */ runErr := scheduler.runRecoveryTasks(context.Background()); runErr != nil {
			t.Fatalf("隔离人工来源旧运行失败: %v", runErr)
		}
		// run、readErr 保存调度后的运行状态及读取错误。
		run, readErr := store.Automation.GetRun(context.Background(), runID)
		if readErr != nil || run.Status != "needs_review" || len(sender.texts) != 0 {
			t.Fatalf("人工来源付款运行未被隔离: run=%+v messages=%v err=%v", run, sender.texts, readErr)
		}
	})
}
