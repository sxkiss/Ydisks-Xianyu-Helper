package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"xianyu-go/internal/xianyu/mtop"
)

// TestMaxFailuresRequiresExplicitSessionExpiry 使用 t 验证历史连续失败入口不会把 Token 或未知失败升级为账号续期。
func TestMaxFailuresRequiresExplicitSessionExpiry(t *testing.T) {
	// status 覆盖登录正常、Token 空、Token 耗尽和未知检查失败。
	for _, status := range []string{mtop.LoginStatusSuccess, mtop.LoginStatusTokenEmpty, mtop.LoginStatusFailed, "network"} {
		// t 管理当前登录检查结果对应的恢复边界断言。
		t.Run(status, func(t *testing.T) {
			// client 仅提供本地检查结果，不会访问平台。
			client := &statusMtop{result: &mtop.LoginStatusResult{Status: status}}
			if status == "network" {
				client.err = errors.New("网络连接失败")
			}
			// account、handler、cleanup 提供真实账号状态、续期计数器和数据库清理函数。
			account, handler, _, cleanup := newRunAccount(t, client)
			defer cleanup()
			// ctx、cancel 让退避可取消，避免测试等待实际重连间隔。
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			if err := account.handleMaxFailures(ctx); !errors.Is(err, context.DeadlineExceeded) { // err 是退避结束原因。
				t.Fatalf("连续失败应等待取消: %v", err)
			}
			if handler.refresh != 0 || account.RuntimeStatus().State == RuntimeAuthExpired {
				t.Fatal("没有明确 Session 过期却触发账号续期或判定登录失效")
			}
		})
	}
}
