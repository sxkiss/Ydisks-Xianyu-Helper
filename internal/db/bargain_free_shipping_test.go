package db

import (
	"context"
	"testing"
)

// TestBargainFreeShippingStageClaim 验证免拼阶段只允许一个 WS 任务执行，并且明确失败才允许后续事件重试。
func TestBargainFreeShippingStageClaim(t *testing.T) {
	// store、cleanup 保存隔离数据库及资源释放函数。
	store, cleanup := newTestDB(t)
	defer cleanup()
	// ctx 保存本测试共用的数据库上下文。
	ctx := context.Background()
	// claimed、claimErr 保存首次领取免拼阶段的结果。
	claimed, claimErr := store.Automation.ClaimBargainFreeShipping(ctx, "order-1", "account-1")
	if claimErr != nil || !claimed {
		t.Fatalf("首次领取失败: claimed=%v err=%v", claimed, claimErr)
	}
	// duplicate、duplicateErr 保存重复 WS 事件的领取结果。
	duplicate, duplicateErr := store.Automation.ClaimBargainFreeShipping(ctx, "order-1", "account-1")
	if duplicateErr != nil || duplicate {
		t.Fatalf("running 阶段不应重复领取: claimed=%v err=%v", duplicate, duplicateErr)
	}
	// finishErr 保存明确失败阶段的终态写入错误。
	if finishErr := store.Automation.FinishBargainFreeShipping(ctx, "order-1", "account-1", "failed"); finishErr != nil {
		t.Fatal(finishErr)
	}
	// retried、retryErr 保存明确失败后的安全重试领取结果。
	retried, retryErr := store.Automation.ClaimBargainFreeShipping(ctx, "order-1", "account-1")
	if retryErr != nil || !retried {
		t.Fatalf("明确失败后应允许重试: claimed=%v err=%v", retried, retryErr)
	}
	// successErr 保存成功终态写入错误。
	if successErr := store.Automation.FinishBargainFreeShipping(ctx, "order-1", "account-1", "succeeded"); successErr != nil {
		t.Fatal(successErr)
	}
	// afterSuccess、afterSuccessErr 保存成功后的重复领取结果。
	afterSuccess, afterSuccessErr := store.Automation.ClaimBargainFreeShipping(ctx, "order-1", "account-1")
	if afterSuccessErr != nil || afterSuccess {
		t.Fatalf("成功终态不应重复领取: claimed=%v err=%v", afterSuccess, afterSuccessErr)
	}
	// readyErr 保存最终成功小刀阶段的幂等事实写入错误。
	if readyErr := store.Automation.MarkBargainReady(ctx, "order-2", "account-1"); readyErr != nil {
		t.Fatal(readyErr)
	}
	// readyStatus 保存最终阶段事实，确认它不会被免拼执行权逻辑误认为已调用免拼。
	var readyStatus string
	// statusErr 保存读取最终砍价阶段事实的数据库错误。
	if statusErr := store.DB.QueryRowContext(ctx, `SELECT status FROM bargain_free_shipping_stages WHERE order_id=? AND cookie_id=?`, "order-2", "account-1").Scan(&readyStatus); statusErr != nil || readyStatus != "ready" {
		t.Fatalf("最终砍价阶段事实错误: status=%q err=%v", readyStatus, statusErr)
	}
}
