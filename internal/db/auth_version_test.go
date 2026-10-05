package db

import (
	"context"
	"errors"
	"testing"
)

// TestCreateVerifiedRejectsStaleAuthVersion 验证改密与旧密码校验交错时不会签发旧会话。
func TestCreateVerifiedRejectsStaleAuthVersion(t *testing.T) {
	// store 和 cleanup 保存隔离数据库及其释放函数。
	store, cleanup := newTestDB(t)
	defer cleanup()
	// ctx 是本测试全部本地事务的取消边界。
	ctx := context.Background()
	// created 和 createErr 保存认证夹具创建结果。
	created, createErr := store.Users.Create(ctx, "auth-version-user", "auth-version@example.com", "old-password")
	if createErr != nil || !created {
		t.Fatalf("创建认证夹具失败：created=%v err=%v", created, createErr)
	}
	// verified、matched 和 verifyErr 保存旧密码认证快照及结果。
	verified, matched, verifyErr := store.Users.VerifyAndUpgrade(ctx, "auth-version-user", "old-password")
	if verifyErr != nil || !matched || verified == nil {
		t.Fatalf("读取认证快照失败：matched=%v err=%v", matched, verifyErr)
	}
	// updated 和 updateErr 保存改密事务结果。
	updated, updateErr := store.Users.UpdatePassword(ctx, "auth-version-user", "new-password")
	if updateErr != nil || !updated {
		t.Fatalf("更新密码失败：updated=%v err=%v", updated, updateErr)
	}
	// sessionErr 保存使用旧认证代次签发会话的拒绝结果。
	_, sessionErr := store.Sessions.CreateVerified(ctx, verified, verified.AuthVersion)
	if !errors.Is(sessionErr, ErrStaleLogin) {
		t.Fatalf("旧认证代次应拒绝签发会话，实际错误=%v", sessionErr)
	}
}
