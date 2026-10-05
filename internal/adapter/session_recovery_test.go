package adapter

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"xianyu-go/internal/xianyu/mtop"
)

// TestSessionRecoveryHandlerFiltersNonExpiredErrors 验证普通平台错误不会触发账号恢复。
func TestSessionRecoveryHandlerFiltersNonExpiredErrors(t *testing.T) {
	// calls 记录恢复端口被调用次数。
	calls := 0
	// handler 是绑定测试恢复端口的会话恢复适配器。
	handler := NewSessionRecoveryHandler(nil, func(context.Context, string) bool {
		calls++
		return true
	})
	// recovered 表示普通错误的恢复结果。
	recovered := handler(context.Background(), "acc1", errors.New("ordinary failure"))
	if recovered || calls != 0 {
		t.Fatalf("普通错误不应触发恢复: recovered=%v calls=%d", recovered, calls)
	}
}

// TestSessionRecoveryHandlerDelegatesExpiredErrors 验证 Session 失效只触发一次恢复端口。
func TestSessionRecoveryHandlerDelegatesExpiredErrors(t *testing.T) {
	// calls 记录恢复端口被调用次数。
	calls := 0
	// handler 是绑定测试恢复端口的会话恢复适配器。
	handler := NewSessionRecoveryHandler(nil, func(_ context.Context, accountID string) bool {
		if accountID != "acc1" {
			t.Fatalf("恢复账号错误: %q", accountID)
		}
		calls++
		return true
	})
	// recovered 表示已识别 Session 失效后的恢复结果。
	recovered := handler(context.Background(), "acc1", errors.New("FAIL_SYS_SESSION_EXPIRED"))
	if !recovered || calls != 1 {
		t.Fatalf("Session 失效未正确委托: recovered=%v calls=%d", recovered, calls)
	}
}

// TestSessionRecoveryHandlerRejectsMTopTokenErrors 验证 Token 内部重试失败不会调用账号恢复；t 记录回调次数断言。
func TestSessionRecoveryHandlerRejectsMTopTokenErrors(t *testing.T) {
	// calls 记录 Token 失效触发恢复端口的次数。
	calls := 0
	// handler 是绑定测试恢复端口的凭证恢复适配器。
	handler := NewSessionRecoveryHandler(nil, func(context.Context, string) bool {
		calls++
		return true
	})
	// tokenErr 是平台明确返回的仅 MTOP Token 失效错误。
	tokenErr := &mtop.MTopResponseError{API: "token", Kind: mtop.MTopErrorTokenExpired, HTTPStatus: 200}
	// recovered 表示 Token 失效后的凭证恢复结果。
	recovered := handler(context.Background(), "acc1", tokenErr)
	if recovered || calls != 0 {
		t.Fatalf("MTOP Token 失效不得委托账号恢复: recovered=%v calls=%d", recovered, calls)
	}
	// wrapped 保留旧版 Token 耗尽的包装文案，不能影响 Session 分类。
	wrapped := fmt.Errorf("token API 登录凭证已失效: %w", tokenErr)
	if handler(context.Background(), "acc1", wrapped) || calls != 0 || IsSessionExpiredError(wrapped) || (&OrderRuntime{}).IsSessionExpired(wrapped) {
		t.Fatal("旧版包装文案不得触发统一、批量发布或订单账号恢复")
	}
}
