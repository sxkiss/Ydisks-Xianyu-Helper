package db

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestAutomationDeliveryProofBoundaryBranches 覆盖发货凭证读取、清除、无凭证推进、租约丢失和数据库取消分支。
func TestAutomationDeliveryProofBoundaryBranches(t *testing.T) {
	t.Setenv("XIANYU_DATA_KEY", "")
	// store、cleanup 保存不启用数据密钥的本地测试数据库及清理函数。
	store, cleanup := newTestDB(t)
	defer cleanup()
	// ctx 保存本次数据库操作共用的上下文。
	ctx := context.Background()
	// userID、cookieID 保存创建自动化规则所需的本地账号归属。
	userID, cookieID := seedAccount(t, store)
	// ruleID、createErr 保存测试规则创建结果。
	ruleID, createErr := store.Automation.Create(ctx, makeAutomationRule(cookieID, userID, "proof-boundary-item", "paid", true, 1))
	if createErr != nil {
		t.Fatal(createErr)
	}
	// runID、started、startErr 保存第一条自动化运行的创建结果。
	runID, started, startErr := store.Automation.TryStartRun(ctx, AutomationRun{RuleID: ruleID, CookieID: cookieID, ItemID: "proof-boundary-item", TriggerType: "paid", TriggerKey: "proof-boundary"})
	if startErr != nil || !started {
		t.Fatalf("创建自动化运行失败 id=%d started=%v err=%v", runID, started, startErr)
	}
	// missingErr 保存读取不存在运行时的稳定错误。
	if _, missingErr := store.Automation.GetRun(ctx, runID+99999); !errors.Is(missingErr, ErrNotFound) {
		t.Fatalf("不存在运行错误=%v", missingErr)
	}
	// writeErr 保存写入非法明文凭证的数据库错误。
	if _, writeErr := store.DB.ExecContext(ctx, "UPDATE automation_runs SET delivery_proof=? WHERE id=?", "not-json", runID); writeErr != nil {
		t.Fatal(writeErr)
	}
	// invalidErr 保存凭证 JSON 无法解析时的错误。
	if _, invalidErr := store.Automation.GetRun(ctx, runID); invalidErr == nil || !strings.Contains(invalidErr.Error(), "解析自动化发货凭证失败") {
		t.Fatalf("非法凭证错误=%v", invalidErr)
	}
	// encryptedWriteErr 保存写入伪造密文的数据库错误。
	if _, encryptedWriteErr := store.DB.ExecContext(ctx, "UPDATE automation_runs SET delivery_proof=? WHERE id=?", "enc:v1:invalid", runID); encryptedWriteErr != nil {
		t.Fatal(encryptedWriteErr)
	}
	// encryptedErr 保存缺少数据密钥时的解密错误。
	if _, encryptedErr := store.Automation.GetRun(ctx, runID); encryptedErr == nil || !strings.Contains(encryptedErr.Error(), "XIANYU_DATA_KEY") {
		t.Fatalf("伪造密文错误=%v", encryptedErr)
	}
	// clearWriteErr 保存清空非法凭证字段的数据库错误。
	if _, clearWriteErr := store.DB.ExecContext(ctx, "UPDATE automation_runs SET delivery_proof='' WHERE id=?", runID); clearWriteErr != nil {
		t.Fatal(clearWriteErr)
	}
	// run、readErr 保存清空凭证后的运行检查点。
	run, readErr := store.Automation.GetRun(ctx, runID)
	if readErr != nil || run.DeliveryProof.TradeText != "" {
		t.Fatalf("空凭证读取异常 run=%+v err=%v", run, readErr)
	}
	// startedAction、actionErr 保存第一条运行的动作占用结果。
	startedAction, actionErr := store.Automation.StartRunAction(ctx, runID, run.AttemptCount, 0, time.Now().Add(time.Minute).Unix())
	if actionErr != nil || !startedAction {
		t.Fatalf("动作占用失败 started=%v err=%v", startedAction, actionErr)
	}
	// clearErr 保存不携带新凭证而清除旧凭证的检查点推进错误。
	clearErr := store.Automation.AdvanceRunAction(ctx, AutomationRunActionAdvance{RunID: runID, Attempt: run.AttemptCount, Cursor: 0, SentDelta: 1, ClearDeliveryProof: true})
	if clearErr != nil {
		t.Fatalf("清除凭证推进失败: %v", clearErr)
	}
	// secondRunID、secondStarted、secondStartErr 保存第二条自动化运行的创建结果。
	secondRunID, secondStarted, secondStartErr := store.Automation.TryStartRun(ctx, AutomationRun{RuleID: ruleID, CookieID: cookieID, ItemID: "proof-boundary-item", TriggerType: "paid", TriggerKey: "proof-boundary-second"})
	if secondStartErr != nil || !secondStarted {
		t.Fatalf("创建第二条运行失败 id=%d started=%v err=%v", secondRunID, secondStarted, secondStartErr)
	}
	// secondRun、secondReadErr 保存第二条运行的当前检查点。
	secondRun, secondReadErr := store.Automation.GetRun(ctx, secondRunID)
	if secondReadErr != nil {
		t.Fatal(secondReadErr)
	}
	// secondActionStarted、secondActionErr 保存第二条运行的动作占用结果。
	secondActionStarted, secondActionErr := store.Automation.StartRunAction(ctx, secondRunID, secondRun.AttemptCount, 0, time.Now().Add(time.Minute).Unix())
	if secondActionErr != nil || !secondActionStarted {
		t.Fatalf("第二条动作占用失败 started=%v err=%v", secondActionStarted, secondActionErr)
	}
	// noProofErr 保存不携带凭证且不要求清除时的检查点推进结果。
	noProofErr := store.Automation.AdvanceRunAction(ctx, AutomationRunActionAdvance{RunID: secondRunID, Attempt: secondRun.AttemptCount, Cursor: 0, SentDelta: 0})
	if noProofErr != nil {
		t.Fatalf("无凭证推进失败: %v", noProofErr)
	}
	// staleErr 保存重复使用旧动作游标时的租约丢失错误。
	staleErr := store.Automation.AdvanceRunAction(ctx, AutomationRunActionAdvance{RunID: runID, Attempt: run.AttemptCount, Cursor: 0})
	if !errors.Is(staleErr, ErrAutomationRunLeaseLost) {
		t.Fatalf("旧游标错误=%v", staleErr)
	}
	// canceledCtx、cancel 生成已取消的数据库上下文。
	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()
	// canceledReadErr 保存取消上下文读取运行时的数据库错误。
	if _, canceledReadErr := store.Automation.GetRun(canceledCtx, secondRunID); canceledReadErr == nil {
		t.Fatal("取消上下文读取应返回数据库错误")
	}
	// canceledAdvanceErr 保存取消上下文推进检查点时的数据库错误。
	if canceledAdvanceErr := store.Automation.AdvanceRunAction(canceledCtx, AutomationRunActionAdvance{RunID: secondRunID, Attempt: secondRun.AttemptCount, Cursor: 1}); canceledAdvanceErr == nil {
		t.Fatal("取消上下文推进应返回数据库错误")
	}
}

// TestClaimAndReleaseDeliveryReplay 验证内容补发只能由一个新代次领取，失败释放后可由该新代次继续收口。
func TestClaimAndReleaseDeliveryReplay(t *testing.T) {
	t.Setenv("XIANYU_DATA_KEY", "replay-proof-test-key")
	// store、cleanup 保存本测试独立的自动化仓储和清理函数。
	store, cleanup := newTestDB(t)
	defer cleanup()
	// ctx 保存本测试全部数据库操作使用的上下文。
	ctx := context.Background()
	// userID、cookieID 保存创建自动化规则所需的本地账号归属。
	userID, cookieID := seedAccount(t, store)
	// ruleID、createErr 保存测试付款发货规则的创建结果。
	ruleID, createErr := store.Automation.Create(ctx, makeAutomationRule(cookieID, userID, "replay-claim-item", "paid", true, 1))
	if createErr != nil {
		t.Fatal(createErr)
	}
	// runID、started、startErr 保存待补发失败运行的创建结果。
	runID, started, startErr := store.Automation.TryStartRun(ctx, AutomationRun{RuleID: ruleID, CookieID: cookieID, ItemID: "replay-claim-item", TriggerType: "paid", TriggerKey: "replay-claim"})
	if startErr != nil || !started {
		t.Fatalf("创建补发运行失败: id=%d started=%v err=%v", runID, started, startErr)
	}
	// proofWriteErr 写入非空测试快照；本测试仅验证领取条件，不读取或暴露其中内容。
	if _, proofWriteErr := store.DB.ExecContext(ctx, `UPDATE automation_runs SET delivery_proof='fixture-proof' WHERE id=?`, runID); proofWriteErr != nil {
		t.Fatal(proofWriteErr)
	}
	// finishErr 把初始运行置为失败，以模拟人工完整发货可补发的历史状态。
	if finishErr := store.Automation.FinishRun(ctx, runID, 1, "failed", 0, "fixture failure"); finishErr != nil {
		t.Fatal(finishErr)
	}
	// leaseExpiresAt 保存首次补发的有效租约截止时间。
	leaseExpiresAt := time.Now().UTC().Add(time.Minute).Unix()
	// firstAttempt、firstClaimed、firstClaimErr 保存首个请求领取补发权的结果。
	firstAttempt, firstClaimed, firstClaimErr := store.Automation.ClaimDeliveryReplay(ctx, runID, 1, leaseExpiresAt)
	if firstClaimErr != nil || !firstClaimed || firstAttempt != 2 {
		t.Fatalf("首次领取补发失败: attempt=%d claimed=%v err=%v", firstAttempt, firstClaimed, firstClaimErr)
	}
	// updateProofErr 保存当前补发代次写回新增快照的错误，确保外部补发内容可在失败后安全恢复。
	if updateProofErr := store.Automation.UpdateDeliveryReplayProof(ctx, runID, firstAttempt, AutomationDeliveryProof{TradeText: "fixture-replay", ExpectedUnits: 2, PreparedUnits: 2}); updateProofErr != nil {
		t.Fatal(updateProofErr)
	}
	// secondAttempt、secondClaimed、secondClaimErr 保存并发或重复请求使用旧代次再次领取的结果。
	secondAttempt, secondClaimed, secondClaimErr := store.Automation.ClaimDeliveryReplay(ctx, runID, 1, leaseExpiresAt)
	if secondClaimErr != nil || secondClaimed || secondAttempt != 1 {
		t.Fatalf("旧代次不应重复领取: attempt=%d claimed=%v err=%v", secondAttempt, secondClaimed, secondClaimErr)
	}
	// releaseErr 恢复第一次领取前的失败终态，模拟尚未成功发送时的补偿路径。
	releaseErr := store.Automation.ReleaseDeliveryReplay(ctx, runID, firstAttempt, "failed", "fixture send failure")
	if releaseErr != nil {
		t.Fatal(releaseErr)
	}
	// status 保存释放后的运行终态；释放必须恢复原失败状态。
	var status, reason string
	// attempt 保存释放后保留的新运行代次，后续领取必须以它作为条件。
	var attempt int
	// actionStarted 保存释放后是否仍占用外部发送动作，补偿完成后必须为零。
	var actionStarted int
	// stateReadErr 保存读取释放后数据库检查点的错误。
	stateReadErr := store.DB.QueryRowContext(ctx, `SELECT status,attempt_count,action_started,error_message FROM automation_runs WHERE id=?`, runID).Scan(&status, &attempt, &actionStarted, &reason)
	if stateReadErr != nil || status != "failed" || attempt != firstAttempt || actionStarted != 0 || reason != "fixture send failure" {
		t.Fatalf("补发释放状态异常: status=%s attempt=%d action_started=%d reason=%q err=%v", status, attempt, actionStarted, reason, stateReadErr)
	}
	// finalAttempt、finalClaimed、finalClaimErr 保存释放后再次领取的唯一补发权。
	finalAttempt, finalClaimed, finalClaimErr := store.Automation.ClaimDeliveryReplay(ctx, runID, firstAttempt, time.Now().UTC().Add(time.Minute).Unix())
	if finalClaimErr != nil || !finalClaimed || finalAttempt != 3 {
		t.Fatalf("释放后重新领取失败: attempt=%d claimed=%v err=%v", finalAttempt, finalClaimed, finalClaimErr)
	}
	// staleCompleteErr 验证已经失效的补发代次不能提前收口新的领取者。
	staleCompleteErr := store.Automation.CompleteDeliveryReplay(ctx, runID, firstAttempt)
	if !errors.Is(staleCompleteErr, ErrAutomationRunLeaseLost) {
		t.Fatalf("旧补发代次收口错误=%v", staleCompleteErr)
	}
	// reviewReleaseErr 模拟人工补发结果未知，验证释放到人工核对时仍保留动作占用保护。
	reviewReleaseErr := store.Automation.ReleaseDeliveryReplay(ctx, runID, finalAttempt, "needs_review", "人工补发结果未知")
	if reviewReleaseErr != nil {
		t.Fatal(reviewReleaseErr)
	}
	// reviewStatus 保存人工核对释放后的运行状态。
	var reviewStatus string
	// reviewActionStarted 保存人工核对释放后是否仍保留动作占用保护。
	var reviewActionStarted int
	// reviewReadErr 保存读取人工核对释放状态的数据库错误。
	reviewReadErr := store.DB.QueryRowContext(ctx, `SELECT status,action_started FROM automation_runs WHERE id=?`, runID).Scan(&reviewStatus, &reviewActionStarted)
	if reviewReadErr != nil || reviewStatus != "needs_review" || reviewActionStarted != 1 {
		t.Fatalf("人工核对释放未保留动作保护: status=%q action_started=%d err=%v", reviewStatus, reviewActionStarted, reviewReadErr)
	}
	// reviewAttempt、reviewClaimed、reviewClaimErr 保存人工核对状态再次领取补发权的结果。
	reviewAttempt, reviewClaimed, reviewClaimErr := store.Automation.ClaimDeliveryReplay(ctx, runID, finalAttempt, time.Now().Add(time.Minute).Unix())
	if reviewClaimErr != nil || !reviewClaimed || reviewAttempt != finalAttempt+1 {
		t.Fatalf("人工核对状态再次领取失败: attempt=%d claimed=%v err=%v", reviewAttempt, reviewClaimed, reviewClaimErr)
	}
	// completeErr 收口最新领取代次；成功后运行不再可被重复补发。
	completeErr := store.Automation.CompleteDeliveryReplay(ctx, runID, reviewAttempt)
	if completeErr != nil {
		t.Fatal(completeErr)
	}
}
