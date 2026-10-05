package db

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// TestDefaultReplyClaimReclaimsExpiredPendingLease 封装TestDefault回复ClaimReclaimsExpiredPendingLease业务协调。
func TestDefaultReplyClaimReclaimsExpiredPendingLease(t *testing.T) {
	// ctx 用于本次流程后续判断的ctx
	ctx := context.Background()
	// database、dialect、err 用于本次流程后续判断的database、dialect、err
	database, dialect, err := Open(ctx, filepath.Join(t.TempDir(), "reply-state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()
	// store 用于本次流程后续判断的store
	store := NewStore(database, dialect)

	// result、err 用于本次流程后续判断的result、err
	result, err := database.ExecContext(ctx, `INSERT INTO users (username,email,password_hash) VALUES (?,?,?)`, "reply-test", "reply-test@example.com", "hash")
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	// userID、err 用于本次流程后续判断的用户ID、err
	userID, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("LastInsertId: %v", err)
	}
	if // err 用于本次流程后续判断的err
	err := store.Cookies.Save(ctx, "cookie-1", "unb=1", userID); err != nil {
		t.Fatalf("save cookie: %v", err)
	}

	// initial、claimed、err 用于本次流程后续判断的initial、claimed、err
	initial, claimed, err := store.DefaultReps.ClaimRecord(ctx, "cookie-1", "chat-1", true, true)
	if err != nil || !claimed || initial.Status != defaultReplyStatusSending {
		t.Fatalf("initial claim: record=%+v claimed=%v err=%v", initial, claimed, err)
	}
	if _, claimed, err = store.DefaultReps.ClaimRecord(ctx, "cookie-1", "chat-1", true, true); err != nil || claimed {
		t.Fatalf("active sending lease must not be claimed: claimed=%v err=%v", claimed, err)
	}

	// err 将旧版本记录恢复为 pending，验证兼容的历史租约仍可接管。
	_, err = database.ExecContext(ctx, `UPDATE default_reply_records SET status='pending',lease_expires_at=? WHERE cookie_id=? AND chat_id=?`, time.Now().Add(-10*time.Minute).Unix(), "cookie-1", "chat-1")
	if err != nil {
		t.Fatalf("expire lease: %v", err)
	}
	// reclaimed、claimed、err 用于本次流程后续判断的reclaimed、claimed、err
	reclaimed, claimed, err := store.DefaultReps.ClaimRecord(ctx, "cookie-1", "chat-1", true, true)
	if err != nil || !claimed || reclaimed.Status != "pending" {
		t.Fatalf("expired pending lease should be reclaimed: record=%+v claimed=%v err=%v", reclaimed, claimed, err)
	}
}

// TestDefaultReplySendingLeaseNeverReclaims 验证新建 sending 记录即使租约过期也不会自动重发。
func TestDefaultReplySendingLeaseNeverReclaims(t *testing.T) {
	// ctx 用于本测试的数据库操作上下文。
	ctx := context.Background()
	// database、dialect、err 保存隔离数据库及迁移结果。
	database, dialect, err := Open(ctx, filepath.Join(t.TempDir(), "reply-sending.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()
	// store 保存默认回复状态仓储。
	store := NewStore(database, dialect)
	// userResult、err 保存测试账号所属用户的创建结果。
	userResult, err := database.ExecContext(ctx, `INSERT INTO users (username,email,password_hash) VALUES (?,?,?)`, "reply-sending", "reply-sending@example.com", "hash")
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	// userID、err 保存测试账号所属用户标识及读取错误。
	userID, err := userResult.LastInsertId()
	if err != nil {
		t.Fatalf("LastInsertId: %v", err)
	}
	// err 保存测试账号凭证写入错误。
	if err := store.Cookies.Save(ctx, "cookie-sending", "unb=1", userID); err != nil {
		t.Fatalf("save cookie: %v", err)
	}
	// first、claimed、claimErr 保存初次发送任务领取结果。
	first, claimed, claimErr := store.DefaultReps.ClaimRecord(ctx, "cookie-sending", "chat-sending", true, false)
	if claimErr != nil || !claimed || first.Status != defaultReplyStatusSending {
		t.Fatalf("初次领取结果异常 record=%+v claimed=%v err=%v", first, claimed, claimErr)
	}
	// expireErr 模拟发送进程在外部结果确认前退出，租约已经过期。
	if _, expireErr := database.ExecContext(ctx, `UPDATE default_reply_records SET lease_expires_at=0 WHERE cookie_id=? AND chat_id=?`, "cookie-sending", "chat-sending"); expireErr != nil {
		t.Fatalf("过期租约失败: %v", expireErr)
	}
	// record、reclaimed、reclaimErr 保存过期 sending 记录的再次领取结果。
	record, reclaimed, reclaimErr := store.DefaultReps.ClaimRecord(ctx, "cookie-sending", "chat-sending", true, false)
	if reclaimErr != nil || reclaimed || record.Status != defaultReplyStatusSending {
		t.Fatalf("sending 记录不应被自动接管 record=%+v claimed=%v err=%v", record, reclaimed, reclaimErr)
	}
}

// TestDefaultReplyUncertainFallbackBlocksExpiredReclaim 验证 uncertain 写入受数据库约束拒绝时仍会持久化不可重试隔离标记。
func TestDefaultReplyUncertainFallbackBlocksExpiredReclaim(t *testing.T) {
	// ctx 用于本测试的数据库操作上下文。
	ctx := context.Background()
	// database、dialect、err 用于创建隔离 SQLite 数据库并应用迁移。
	database, dialect, err := Open(ctx, filepath.Join(t.TempDir(), "reply-uncertain.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()
	// store 保存默认回复状态仓储。
	store := NewStore(database, dialect)
	// userResult、err 保存测试用户创建结果。
	userResult, err := database.ExecContext(ctx, `INSERT INTO users (username,email,password_hash) VALUES (?,?,?)`, "reply-uncertain", "reply-uncertain@example.com", "hash")
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	// userID、err 保存测试用户标识及读取错误。
	userID, err := userResult.LastInsertId()
	if err != nil {
		t.Fatalf("LastInsertId: %v", err)
	}
	// err 保存测试账号凭证写入错误。
	if err := store.Cookies.Save(ctx, "cookie-uncertain", "unb=1", userID); err != nil {
		t.Fatalf("save cookie: %v", err)
	}
	// _, claimed、err 保存初次领取结果；后续更新模拟消息已经可能送达。
	if _, claimed, err := store.DefaultReps.ClaimRecord(ctx, "cookie-uncertain", "chat-uncertain", true, false); err != nil || !claimed {
		t.Fatalf("claim record: claimed=%v err=%v", claimed, err)
	}
	// err 模拟数据库拒绝直接写入 uncertain 状态，但允许降级隔离更新。
	if _, err := database.ExecContext(ctx, `CREATE TRIGGER deny_uncertain_status BEFORE UPDATE OF status ON default_reply_records WHEN NEW.status='uncertain' BEGIN SELECT RAISE(FAIL,'fixture rejection'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	// err 保存未知投递状态的降级隔离结果。
	if err := store.DefaultReps.MarkRecordUncertain(ctx, "cookie-uncertain", "chat-uncertain", "发送结果未知"); err != nil {
		t.Fatalf("uncertain fallback: %v", err)
	}
	// err 保存租约到期模拟更新结果。
	if _, err := database.ExecContext(ctx, `UPDATE default_reply_records SET lease_expires_at=? WHERE cookie_id=? AND chat_id=?`, time.Now().Add(-time.Minute).Unix(), "cookie-uncertain", "chat-uncertain"); err != nil {
		t.Fatalf("expire lease: %v", err)
	}
	// record、claimed、err 验证隔离记录不会因租约到期再次领取。
	record, claimed, err := store.DefaultReps.ClaimRecord(ctx, "cookie-uncertain", "chat-uncertain", true, false)
	if err != nil || claimed || record.Status != "pending" {
		t.Fatalf("uncertain fallback was reclaimed: record=%+v claimed=%v err=%v", record, claimed, err)
	}
}
