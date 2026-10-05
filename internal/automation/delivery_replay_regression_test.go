package automation

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"xianyu-go/internal/db"
)

// saveManualReplayTestPlan 为 t 的历史运行夹具补齐生产保存的原始计划；ctx/store/runID 定位运行，units 指定原单卡动作单位数。
func saveManualReplayTestPlan(t *testing.T, ctx context.Context, store *db.Store, runID int64, units int) {
	t.Helper()
	// run、err 保存测试历史运行及读取失败原因。
	run, err := store.Automation.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	// rule、err 保存原始规则，夹具在规则编辑前固定其动作。
	rule, err := store.Automation.Get(ctx, run.RuleID)
	if err != nil {
		t.Fatal(err)
	}
	// task 使用固定单件订单，units 控制原始计划要求的卡密份数。
	task := Task{AccountID: run.CookieID, OrderID: run.OrderID, ItemID: run.ItemID, TriggerType: TriggerOrderPaid, Quantity: "1"}
	task.ActionPlan = (actionPlanner{}).plan(task, rule.Actions)
	for index := range task.ActionPlan { // index 定位夹具原规则中唯一的卡密动作。
		if task.ActionPlan[index].ActionType == ActionSendCard {
			task.ActionPlan[index].DeliveryCount = units
		}
	}
	// raw、err 保存与生产 Task 相同的 JSON 形状，避免构造不可能由生产产生的缺失计划。
	raw, err := json.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB.ExecContext(ctx, `UPDATE automation_runs SET raw_event_json=? WHERE id=?`, string(raw), runID); err != nil { // err 保存夹具计划写入失败原因。
		t.Fatal(err)
	}
}

// seedPartialReplay 为 t 创建两单位计划中已完成一单位的人工核对运行，ctx/store/order 来自同一隔离夹具；返回运行标识。
func seedPartialReplay(t *testing.T, ctx context.Context, store *db.Store, order *db.Order) int64 {
	t.Helper()
	// ruleID 是夹具的唯一付款发货规则标识。
	var ruleID int64
	if err := store.DB.QueryRowContext(ctx, `SELECT id FROM automation_rules WHERE cookie_id=? AND item_id=?`, order.CookieID, order.ItemID).Scan(&ruleID); err != nil { // err 表示规则夹具读取失败。
		t.Fatal(err)
	}
	// runID、started、err 保存生产幂等运行创建结果。
	runID, started, err := store.Automation.TryStartRun(ctx, db.AutomationRun{RuleID: ruleID, CookieID: order.CookieID, ItemID: order.ItemID, OrderID: order.OrderID, TriggerType: TriggerOrderPaid, TriggerKey: buildManualDeliveryTriggerKey(Task{OrderID: order.OrderID}), LeaseExpiresAt: time.Now().Add(time.Minute).Unix()})
	if err != nil || !started {
		t.Fatalf("创建历史运行失败: started=%v err=%v", started, err)
	}
	saveManualReplayTestPlan(t, ctx, store, runID, 2)
	// run、err 保存刚创建运行的执行代次。
	run, err := store.Automation.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if started, err := store.Automation.StartRunAction(ctx, runID, run.AttemptCount, 0, time.Now().Add(time.Minute).Unix()); err != nil || !started { // started、err 表示夹具是否取得生产动作占用。
		t.Fatalf("领取历史动作失败: started=%v err=%v", started, err)
	}
	// proof 是已经发送的第一单位内容，第二单位尚未取得。
	proof := db.AutomationDeliveryProof{TradeText: "SAVED", Messages: []db.AutomationDeliveryMessage{{Kind: "text", Content: "SAVED"}}, ExpectedUnits: 2, PreparedUnits: 1}
	if err := store.Automation.QuarantineRunResultWithProof(ctx, runID, run.AttemptCount, 1, "部分发货", &proof); err != nil { // err 保存历史部分发货的隔离结果。
		t.Fatal(err)
	}
	return runID
}

// TestDeliveryReplayRejectsUnexecutedAction 验证第二个卡组恢复后仍不得用第一份快照确认整单；t 管理真实 SQLite 和发送替身。
func TestDeliveryReplayRejectsUnexecutedAction(t *testing.T) {
	// ctx、store、center、sender、platform、order、cleanup 提供原始规则执行和平台确认调用的隔离夹具。
	ctx, store, center, sender, platform, order, cleanup := newManualDeliveryFixture(t, "multiple-actions-regression")
	defer cleanup()
	// admin、err 保存创建第二个卡组的用户身份。
	admin, err := store.Users.GetByUsername(ctx, "admin")
	if err != nil {
		t.Fatal(err)
	}
	// cardID、err 创建首次执行时不可用的第二份权益。
	cardID, err := store.Cards.Create(ctx, &db.CardFull{Name: "second", Type: "text", TextContent: "SECOND", Enabled: false, UserID: admin.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB.ExecContext(ctx, `INSERT INTO automation_rule_actions(rule_id,action_type,card_id,delivery_count,config_json,enabled,sort_order) SELECT id,'send_card',?,1,'{}',1,2 FROM automation_rules WHERE cookie_id=? AND item_id=?`, cardID, order.CookieID, order.ItemID); err != nil { // err 保存第二个计划动作的夹具写入错误。
		t.Fatal(err)
	}
	if _, err := center.ManualFullDelivery(ctx, order); err == nil { // err 应由第二个卡组停用产生，第一份权益已经发送。
		t.Fatal("第二个卡组停用时不应成功")
	}
	if _, err := store.DB.ExecContext(ctx, `UPDATE cards SET enabled=1 WHERE id=?`, cardID); err != nil { // err 保存恢复卡组可用状态的结果。
		t.Fatal(err)
	}
	// sent、replayErr 保存后续人工补发结果；必须在发送旧快照前拒绝，不允许假成功。
	sent, replayErr := center.ManualFullDelivery(ctx, order)
	if replayErr == nil || !strings.Contains(replayErr.Error(), "未完成") || sent != 0 || len(sender.texts) != 1 || platform.consignCalls != 0 {
		t.Fatalf("遗漏动作未阻止整单发货: sent=%d messages=%d confirms=%d err=%v", sent, len(sender.texts), platform.consignCalls, replayErr)
	}
}

// TestManualDeliveryCannotRaceWithAutomaticDelivery 验证人工预检查通过后若自动发货先取得运行权，人工请求不会再次发送卡密。
func TestManualDeliveryCannotRaceWithAutomaticDelivery(t *testing.T) {
	// ctx、store、center、sender、order、cleanup 提供隔离的自动与人工发货夹具。
	ctx, store, center, sender, _, order, cleanup := newManualDeliveryFixture(t, "manual-auto-interleave-regression")
	defer cleanup()
	// delayErr 将夹具卡密改为立即动作，确保自动运行在本测试中完成外部发送后再让人工请求竞争。
	if _, delayErr := store.DB.ExecContext(ctx, `UPDATE cards SET delay_seconds=0`); delayErr != nil {
		t.Fatalf("设置立即发货卡密失败: %v", delayErr)
	}
	// task、prepareErr 保存人工预检查已经完成的订单任务及失败原因。
	task, prepareErr := center.prepareManualDeliveryTask(ctx, order)
	if prepareErr != nil {
		t.Fatalf("准备人工发货任务失败: %v", prepareErr)
	}
	// manualTriggerKey 保存人工请求的独立幂等键；此时只完成预检查，不创建人工运行。
	manualTriggerKey := buildManualDeliveryTriggerKey(task)
	// handled、priorErr 保存人工预检查是否已处理及其错误；发送数量在本断言中无需读取。
	handled, _, priorErr := center.handlePriorManualDelivery(ctx, task, manualTriggerKey)
	if priorErr != nil || handled {
		t.Fatalf("人工预检查不应提前结束: handled=%v err=%v", handled, priorErr)
	}
	// rules、matchErr 保存随后抢先执行的自动付款发货规则及匹配错误。
	rules, matchErr := center.rules.match(ctx, task)
	if matchErr != nil || len(rules) == 0 {
		t.Fatalf("匹配自动发货规则失败: rules=%d err=%v", len(rules), matchErr)
	}
	// executeErr 保存自动付款发货规则的执行错误。
	executeErr := center.executeRule(ctx, task, rules[0])
	if executeErr != nil {
		t.Fatalf("自动发货执行失败: %v", executeErr)
	}
	// sent、manualErr 保存自动运行已完成后人工请求的结果；跨触发器幂等保护应拒绝第二次外部发送。
	sent, manualErr := center.executeManualDeliveryRules(ctx, task, manualTriggerKey)
	if manualErr == nil || sent != 0 || len(sender.texts) != 1 {
		t.Fatalf("自动与人工交错时不应重复发货: sent=%d texts=%v err=%v", sent, sender.texts, manualErr)
	}
	if !strings.Contains(manualErr.Error(), "另一条付款发货运行") {
		t.Fatalf("人工请求应报告跨触发器重复保护: %v", manualErr)
	}
	// runCount 用于确认订单最终只有一条自动付款发货运行，没有额外创建人工运行。
	var runCount int
	// queryErr 保存运行记录计数查询错误。
	queryErr := store.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM automation_runs WHERE order_id=?`, order.OrderID).Scan(&runCount)
	if queryErr != nil {
		t.Fatal(queryErr)
	}
	if runCount != 1 {
		t.Fatalf("交错发货应只保留一条运行记录: count=%d", runCount)
	}
}

// TestDeliveryReplayProofFailureBlocksFurtherConsumption 验证补取结果写入失败后保留跨请求占用；t 检查真实库存不会再次消耗。
func TestDeliveryReplayProofFailureBlocksFurtherConsumption(t *testing.T) {
	// ctx、store、center、sender、platform、order、cleanup 提供真实库存和补发运行。
	ctx, store, center, sender, platform, order, cleanup := newManualDeliveryFixture(t, "proof-save-regression")
	defer cleanup()
	if _, err := store.DB.ExecContext(ctx, `UPDATE cards SET type='data',data_content=?`, "REFILL-ONE\nREFILL-TWO"); err != nil { // err 保存将原卡组转换为两条可观测库存的夹具结果。
		t.Fatal(err)
	}
	// runID 标识已保存第一单位快照的历史运行。
	runID := seedPartialReplay(t, ctx, store, order)
	// 此触发器在库存已消费后才拒绝快照更新，允许先写入取卡前占用。
	if _, err := store.DB.ExecContext(ctx, `CREATE TRIGGER reject_refill_result BEFORE UPDATE OF delivery_proof ON automation_runs WHEN (SELECT data_content FROM cards LIMIT 1)='REFILL-TWO' BEGIN SELECT RAISE(FAIL,'refill result failure'); END`); err != nil { // err 表示失败注入建立失败。
		t.Fatal(err)
	}
	if _, err := center.ManualFullDelivery(ctx, order); err == nil { // err 必须报告外部发送后的快照持久化故障。
		t.Fatal("未报告补取结果保存失败")
	}
	if _, err := store.DB.ExecContext(ctx, `DROP TRIGGER reject_refill_result`); err != nil { // err 保存解除一次性故障注入的结果。
		t.Fatal(err)
	}
	// saved、err 验证占用与人工核对状态已经持久化，而非依赖进程内存。
	saved, err := store.Automation.GetRun(ctx, runID)
	if err != nil || !saved.DeliveryProof.RefillPending || saved.Status != "needs_review" {
		t.Fatalf("未保留补取占用: err=%v", err)
	}
	if _, err := center.ManualFullDelivery(ctx, order); err == nil || !strings.Contains(err.Error(), "禁止再次取卡") { // err 应明确拒绝再次取卡。
		t.Fatalf("允许重复补取: %v", err)
	}
	// remaining 保存真实数据库库存，第二条必须仍可供其他订单使用。
	var remaining string
	if err := store.DB.QueryRowContext(ctx, `SELECT data_content FROM cards LIMIT 1`).Scan(&remaining); err != nil { // err 保存最终库存读取失败原因。
		t.Fatal(err)
	}
	if remaining != "REFILL-TWO" || len(sender.texts) != 2 || platform.consignCalls != 0 {
		t.Fatal("失败后重复消耗库存或错误确认发货")
	}
}

// TestDeliveryReplayMarkFailureDoesNotConsume 验证取卡前占用写入失败时不访问库存，故障恢复后仍可正常完成；t 管理 SQLite 触发器。
func TestDeliveryReplayMarkFailureDoesNotConsume(t *testing.T) {
	// ctx、store、center、sender、platform、order、cleanup 提供部分发货恢复夹具。
	ctx, store, center, sender, platform, order, cleanup := newManualDeliveryFixture(t, "mark-save-regression")
	defer cleanup()
	seedPartialReplay(t, ctx, store, order)
	if _, err := store.DB.ExecContext(ctx, `CREATE TRIGGER reject_refill_mark BEFORE UPDATE OF delivery_proof ON automation_runs BEGIN SELECT RAISE(FAIL,'mark failure'); END`); err != nil { // err 保存占用失败注入建立结果。
		t.Fatal(err)
	}
	if _, err := center.ManualFullDelivery(ctx, order); err == nil { // err 应来自取卡之前的持久化保护。
		t.Fatal("未报告占用保存失败")
	}
	if len(sender.texts) != 1 || platform.consignCalls != 0 {
		t.Fatal("占用失败后仍然补取或确认")
	}
	if _, err := store.DB.ExecContext(ctx, `DROP TRIGGER reject_refill_mark`); err != nil { // err 保存解除失败注入的结果。
		t.Fatal(err)
	}
	if _, err := center.ManualFullDelivery(ctx, order); err != nil { // err 应为空，取卡前失败不应永久禁止安全重试。
		t.Fatal(err)
	}
}

// TestDeliveryReplayPendingCannotResumeActions 验证调度恢复也不能绕过人工补取保护；t 拥有运行与发送替身。
func TestDeliveryReplayPendingCannotResumeActions(t *testing.T) {
	// ctx、store、center、sender、platform、order、cleanup 提供可直接调用运行协调器的隔离夹具。
	ctx, store, center, sender, platform, order, cleanup := newManualDeliveryFixture(t, "pending-resume-regression")
	defer cleanup()
	// runID 标识具有原始快照的运行。
	runID := seedPartialReplay(t, ctx, store, order)
	// run、err 保存恢复用的当前代次与内容快照。
	run, err := store.Automation.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	run.DeliveryProof.RefillPending = true
	// sent、deferred、runErr 应表示已隔离且没有任何外部动作。
	sent, deferred, runErr := center.executeRunActions(ctx, Task{}, run.RuleID, run, nil, false)
	if !errors.Is(runErr, errAutomationNeedsReview) || deferred || sent != run.SentCount || len(sender.texts) != 0 || platform.consignCalls != 0 {
		t.Fatalf("恢复绕过补取占用: %v", runErr)
	}
}

// TestDeliveryReplaySendFailurePreservesReservation 验证确定未发送、未知投递及库存恢复失败具有不同重试边界；t 管理每个独立库存夹具。
func TestDeliveryReplaySendFailurePreservesReservation(t *testing.T) {
	// scenario 区分是否可以安全恢复库存，未知结果只能重发已保存内容。
	for _, scenario := range []string{"not-sent", "uncertain", "restore-failed"} {
		t.Run(scenario, func(t *testing.T) { // t 拥有当前发送失败场景的数据库和发送替身。
			// ctx、store、center、sender、platform、order、cleanup 提供两条库存与一条历史快照。
			ctx, store, center, sender, platform, order, cleanup := newManualDeliveryFixture(t, "send-failure-"+scenario)
			defer cleanup()
			if _, err := store.DB.ExecContext(ctx, `UPDATE cards SET type='data',data_content=?`, "REFILL-ONE\nREFILL-TWO"); err != nil { // err 保存库存初始化失败原因。
				t.Fatal(err)
			}
			// runID 定位保存占用和最终快照的运行。
			runID := seedPartialReplay(t, ctx, store, order)
			sender.failAfter = 1
			sender.err = ErrMessageNotSent
			if scenario == "uncertain" {
				sender.err = errors.New("fixture uncertain send")
			}
			if scenario == "restore-failed" {
				if _, err := store.DB.ExecContext(ctx, `CREATE TRIGGER reject_inventory_restore BEFORE UPDATE OF data_content ON cards WHEN NEW.data_content LIKE 'REFILL-ONE%' BEGIN SELECT RAISE(FAIL,'restore failure'); END`); err != nil { // err 保存仅拒绝库存恢复的触发器创建结果。
					t.Fatal(err)
				}
			}
			if _, err := center.ManualFullDelivery(ctx, order); err == nil { // err 应反映第二条消息的投递或恢复失败。
				t.Fatal("未返回补发失败")
			}
			// saved、err 核对失败后的持久化占用和未知内容，而非运行内存。
			saved, err := store.Automation.GetRun(ctx, runID)
			if err != nil {
				t.Fatal(err)
			}
			if saved.DeliveryProof.RefillPending != (scenario == "restore-failed") {
				t.Fatal("补取占用状态与失败性质不符")
			}
			if scenario == "uncertain" && (saved.DeliveryProof.UnknownUnits != 1 || len(saved.DeliveryProof.Messages) != 2) {
				t.Fatal("未保存已消费但投递未知的内容")
			}
			sender.err = nil
			// retryErr 区分安全重试与永久保留人工核对的恢复失败。
			_, retryErr := center.ManualFullDelivery(ctx, order)
			if scenario == "restore-failed" {
				if retryErr == nil || platform.consignCalls != 0 {
					t.Fatal("库存恢复失败后不应再次取卡")
				}
				return
			}
			if retryErr != nil || platform.consignCalls != 1 {
				t.Fatalf("可恢复补发未完成: %v", retryErr)
			}
			// remaining 验证原样重发和安全恢复重试均只消耗一份库存。
			var remaining string
			if err := store.DB.QueryRowContext(ctx, `SELECT data_content FROM cards LIMIT 1`).Scan(&remaining); err != nil { // err 保存最终库存读取结果。
				t.Fatal(err)
			}
			if remaining != "REFILL-TWO" {
				t.Fatal("发送失败后重复消耗库存")
			}
		})
	}
}

// TestInitialInventoryRestoreFailureBlocksLaterRefill 验证首次自动发货恢复库存失败后会持久化补取占用，禁止人工补发再次扣库存。
func TestInitialInventoryRestoreFailureBlocksLaterRefill(t *testing.T) {
	// ctx、store、center、sender、platform、order、cleanup 提供首次自动发货的真实库存和运行夹具。
	ctx, store, center, sender, platform, order, cleanup := newManualDeliveryFixture(t, "initial-restore-failure-regression")
	defer cleanup()
	// updateErr 将原始文本卡密改为两条数据库存，并开启自动确认发货入口。
	if _, updateErr := store.DB.ExecContext(ctx, `UPDATE cards SET type='data',data_content=?,delay_seconds=0`, "INITIAL-LOST\nSECOND"); updateErr != nil {
		t.Fatal(updateErr)
	}
	// updateErr 保存开启自动确认发货入口的数据库更新结果。
	if _, updateErr := store.DB.ExecContext(ctx, `UPDATE cookies SET auto_confirm=1 WHERE id='cid'`); updateErr != nil {
		t.Fatal(updateErr)
	}
	// triggerErr 只拒绝把 INITIAL-LOST 放回库存的恢复 UPDATE，消费第一条库存仍必须成功。
	if _, triggerErr := store.DB.ExecContext(ctx, `CREATE TRIGGER reject_initial_inventory_restore BEFORE UPDATE OF data_content ON cards WHEN NEW.data_content LIKE 'INITIAL-LOST%' BEGIN SELECT RAISE(FAIL,'initial restore failure'); END`); triggerErr != nil {
		t.Fatal(triggerErr)
	}
	// sender 返回确定未发送，迫使执行器先消费库存再进入恢复分支。
	sender.err = ErrMessageNotSent
	// task 保存完整订单上下文，避免测试依赖简化付款事件的订单回填。
	task := Task{AccountID: order.CookieID, ItemID: order.ItemID, OrderID: order.OrderID, BuyerID: order.BuyerID, ChatID: order.ChatID, Quantity: "1", TriggerType: TriggerOrderPaid}
	// runErr 保存首次自动发货执行结果；恢复失败必须进入人工核对。
	if runErr := center.HandleTask(ctx, task); runErr == nil {
		t.Fatal("首次库存恢复失败应进入人工核对")
	}
	// run、runErr 保存首次自动运行的持久化快照及读取错误。
	run, runErr := store.Automation.GetLatestOrderDeliveryRun(ctx, order.CookieID, order.OrderID)
	if runErr != nil {
		t.Fatal(runErr)
	}
	if run.Status != "needs_review" || !run.DeliveryProof.RefillPending {
		t.Fatalf("首次恢复失败未锁定补取占用: status=%q proof=%+v", run.Status, run.DeliveryProof)
	}
	// secondErr 验证人工补发在真正发送或取卡前就被保护标记拦截。
	sender.err = nil
	// secondErr 保存人工补发被补取占用保护拒绝的结果。
	if _, secondErr := center.ManualFullDelivery(ctx, order); secondErr == nil || !strings.Contains(secondErr.Error(), "禁止再次取卡") {
		t.Fatalf("首次恢复失败后仍允许补取: %v", secondErr)
	}
	if platform.consignCalls != 0 || len(sender.texts) != 0 {
		t.Fatalf("补取保护失败后不应发送或确认: sends=%d confirms=%d", len(sender.texts), platform.consignCalls)
	}
}

// replayCancelSender 在缺失卡密成功发送后取消请求，验证结果保存不会继承已经取消的上下文。
type replayCancelSender struct {
	// testSender 记录发送内容并提供运行时 Cookie 接口，仅由单个测试调用。
	*testSender
	// cancel 取消当前人工请求；测试结束时仍由测试持有者再次调用以确保清理。
	cancel context.CancelFunc
}

// SendText 向 s 的记录器保存 chatID/buyerID/text，并在第二条消息投递后取消 ctx 对应请求；返回原发送结果。
func (s *replayCancelSender) SendText(ctx context.Context, chatID, buyerID, text string) error {
	if err := s.testSender.SendText(ctx, chatID, buyerID, text); err != nil { // err 保存确定的模拟发送结果。
		return err
	}
	if len(s.texts) == 2 {
		s.cancel()
	}
	return nil
}

// replayCancelProvider 为测试中心固定提供可取消请求的发送器。
type replayCancelProvider struct {
	// sender 只由当前测试请求调用，没有后台协程。
	sender *replayCancelSender
}

// Sender 忽略夹具账号定位并返回 p 固定的非空发送器，bool 表示测试账号在线。
func (p replayCancelProvider) Sender(string) (MessageSender, bool) { return p.sender, true }

// TestDeliveryReplayCancellationPersistsConsumedContent 验证外部消息发送后的请求取消不丢失新快照；t 管理独立取消上下文。
func TestDeliveryReplayCancellationPersistsConsumedContent(t *testing.T) {
	// ctx、store、sender、platform、order、cleanup 提供真实 SQLite 和观测依赖。
	ctx, store, _, sender, platform, order, cleanup := newManualDeliveryFixture(t, "cancel-refill-regression")
	defer cleanup()
	// runID 是已完成一单位的历史运行。
	runID := seedPartialReplay(t, ctx, store, order)
	// requestCtx、cancel 只取消当前人工请求，不取消后续数据库验证。
	requestCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	// center 使用构造期注入的可取消发送器，平台和订单详情均沿用确定性替身。
	center := NewWithDependencies(store, replayCancelProvider{sender: &replayCancelSender{testSender: sender, cancel: cancel}}, nil, CenterDependencies{
		MTop: platform, OrderDetailFetcher: testFetcher{detail: &OrderDetail{Quantity: "1", OrderStatus: "pending_ship"}},
	})
	if _, err := center.ManualFullDelivery(requestCtx, order); err == nil { // err 应由取消后的确认步骤返回。
		t.Fatal("取消请求不应完成整单")
	}
	// saved、err 通过未取消上下文确认新内容已经可靠持久化。
	saved, err := store.Automation.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.DeliveryProof.RefillPending || saved.DeliveryProof.PreparedUnits != 2 || len(saved.DeliveryProof.Messages) != 2 {
		t.Fatal("取消导致已消费内容快照丢失")
	}
}

// TestDeliveryReplayUsesOriginalCardAfterRuleEdit 验证后续规则改卡不影响历史订单补齐来源；t 管理原始与新卡组。
func TestDeliveryReplayUsesOriginalCardAfterRuleEdit(t *testing.T) {
	// ctx、store、center、sender、platform、order、cleanup 提供已固定原始动作计划的历史运行。
	ctx, store, center, sender, platform, order, cleanup := newManualDeliveryFixture(t, "edited-rule-regression")
	defer cleanup()
	seedPartialReplay(t, ctx, store, order)
	// admin、err 保存第二卡组的所属用户。
	admin, err := store.Users.GetByUsername(ctx, "admin")
	if err != nil {
		t.Fatal(err)
	}
	// replacement、err 创建修改后规则使用的不同权益，历史订单不得取得它。
	replacement, err := store.Cards.Create(ctx, &db.CardFull{Name: "replacement", Type: "text", TextContent: "WRONG-CARD", Enabled: true, UserID: admin.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB.ExecContext(ctx, `UPDATE automation_rule_actions SET card_id=? WHERE action_type='send_card'`, replacement); err != nil { // err 保存原始运行创建后的规则编辑结果。
		t.Fatal(err)
	}
	if _, err := center.ManualFullDelivery(ctx, order); err != nil { // err 应为空，原卡组仍然可用且有唯一来源。
		t.Fatal(err)
	}
	if len(sender.texts) != 2 || sender.texts[1] != "MANUAL-CARD" || platform.consignCalls != 1 {
		t.Fatal("规则编辑改变了历史补发内容")
	}
}

// TestDeliveryReplayDoesNotConfirmSkippedRefill 验证详情规格变化导致补齐动作跳过时不能确认整单；t 使用规格切换的详情替身。
func TestDeliveryReplayDoesNotConfirmSkippedRefill(t *testing.T) {
	// ctx、store、sender、platform、order、cleanup 提供普通规格的历史快照。
	ctx, store, _, sender, platform, order, cleanup := newManualDeliveryFixture(t, "changed-spec-regression")
	defer cleanup()
	seedPartialReplay(t, ctx, store, order)
	// center 模拟本次详情返回不同规格，原动作因此不会生成新的内容。
	center := NewWithDependencies(store, testSenderProvider{sender: sender}, nil, CenterDependencies{
		MTop: platform, OrderDetailFetcher: testFetcher{detail: &OrderDetail{Quantity: "1", OrderStatus: "pending_ship", SpecName: "规格", SpecValue: "变更"}},
	})
	// sent、err 保存补齐被跳过后的人工请求结果；确认发货必须被最终完整性检查阻止。
	sent, err := center.ManualFullDelivery(ctx, order)
	if err == nil || sent != 1 || platform.consignCalls != 0 {
		t.Fatalf("缺失内容仍被确认: sent=%d err=%v", sent, err)
	}
}
