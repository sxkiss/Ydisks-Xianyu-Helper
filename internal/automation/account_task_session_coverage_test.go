package automation

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"xianyu-go/internal/xianyu/cookierefresh"
	"xianyu-go/internal/xianyu/mtop"
)

// TestAccountTaskSessionAndCookiePersistence 验证账号任务会话阻断指纹和响应 Cookie 收口分支。
func TestAccountTaskSessionAndCookiePersistence(t *testing.T) {
	// store、cleanup 保存本测试使用的 SQLite 存储及清理函数。
	store, cleanup := newAutomationTestStore(t)
	defer cleanup()
	// ctx 是本测试所有数据库调用共用的非取消上下文。
	ctx := context.Background()
	// sender 保存响应 Cookie 同步到在线运行时的测试发送器。
	sender := &testSender{}
	// coordinator 是绑定任务仓储和测试发送器的账号任务协调器。
	coordinator := &accountTaskCoordinator{repository: newStoreAccountTaskRepository(store), senders: testSenderProvider{sender: sender}, logger: slog.Default()}
	// unchangedValue、unchangedErr 保存空响应或相同 Cookie 的幂等结果。
	unchangedValue, unchangedErr := coordinator.persistTaskCookies(ctx, "cid", "sid=old", " sid=old ")
	if unchangedErr != nil || unchangedValue != "sid=old" || len(sender.cookieUpdates) != 0 {
		t.Fatalf("unchanged value=%q err=%v updates=%v", unchangedValue, unchangedErr, sender.cookieUpdates)
	}
	// updatedValue、updatedErr 保存新 Cookie 的本地持久化和运行时同步结果。
	updatedValue, updatedErr := coordinator.persistTaskCookies(ctx, "cid", "sid=old", " sid=new ")
	if updatedErr != nil || updatedValue != "sid=new" || len(sender.cookieUpdates) != 1 || sender.cookieUpdates[0] != "sid=new" {
		t.Fatalf("updated value=%q err=%v updates=%v", updatedValue, updatedErr, sender.cookieUpdates)
	}
	// initialCookie、initialSnapshot 是带完整作用域的账号凭证初始状态。
	initialCookie := "unb=1; _m_h5_tk=old_token"
	// initialSnapshot 保存测试账号的 Domain、Path 和 HttpOnly 属性。
	initialSnapshot := []cookierefresh.BrowserCookie{
		{Name: "unb", Value: "1", Domain: ".goofish.com", Path: "/"},
		{Name: "_m_h5_tk", Value: "old_token", Domain: ".goofish.com", Path: "/"},
		{Name: "scoped", Value: "old", Domain: "h5api.m.goofish.com", Path: "/h5", HTTPOnly: true},
	}
	// metadata 是保留业务字段与完整快照的账号 metadata。
	metadata := cookierefresh.MetadataWithSnapshot(`{"preserved":"yes"}`, initialSnapshot)
	// err 保存初始权威 Cookie metadata 写入错误。
	if err := store.Cookies.UpdateRenewalCookie(ctx, "cid", initialCookie, metadata, 1); err != nil {
		t.Fatal(err)
	}
	// session 保存会话更新能力，模拟平台响应先在内存中轮换签名 Cookie。
	_, session := mtop.WithCookieSnapshot(ctx, initialSnapshot)
	session.ReplaceSnapshot([]cookierefresh.BrowserCookie{
		{Name: "unb", Value: "1", Domain: ".goofish.com", Path: "/"},
		{Name: "_m_h5_tk", Value: "fresh_token", Domain: ".goofish.com", Path: "/"},
		{Name: "scoped", Value: "new", Domain: "h5api.m.goofish.com", Path: "/h5", HTTPOnly: true},
	})
	// snapshotValue、snapshotErr 保存带快照写回后的扁平 Cookie 结果。
	snapshotValue, snapshotErr := coordinator.persistTaskCookieSession(ctx, "cid", initialCookie, "", &accountTaskCredentialSession{cookieSession: session, persistedValue: initialCookie, persistedMetadata: metadata})
	if snapshotErr != nil || snapshotValue == initialCookie {
		t.Fatalf("权威 Cookie 快照未写回 value=%q err=%v", snapshotValue, snapshotErr)
	}
	// stored、storedErr 保存数据库中最终的 Cookie 运行视图。
	stored, storedErr := store.Cookies.GetCookieRuntimeData(ctx, "cid")
	if storedErr != nil {
		t.Fatal(storedErr)
	}
	// storedSnapshot、complete 检查签名 Cookie 和跨域 Cookie 的完整属性均被保留。
	storedSnapshot, complete := cookierefresh.SnapshotFromMetadataOK(stored.MetadataJSON)
	if !complete || storedSnapshot[1].Value != "fresh_token" || storedSnapshot[2].Value != "new" || !strings.Contains(stored.MetadataJSON, `"preserved":"yes"`) {
		t.Fatalf("权威 Cookie 快照写回不完整 complete=%v snapshot=%+v metadata=%s", complete, storedSnapshot, stored.MetadataJSON)
	}
	// fingerprint、fingerprintErr 保存当前平台凭证的不可逆阻断指纹。
	fingerprint, fingerprintErr := coordinator.accountCredentialFingerprint(ctx, "cid")
	if fingerprintErr != nil || fingerprint == "" {
		t.Fatalf("fingerprint=%q err=%v", fingerprint, fingerprintErr)
	}
	coordinator.sessionExpired.Store("cid", fingerprint)
	// blocked、blockedErr 保存凭证未变化时的会话阻断结果。
	blocked, blockedErr := coordinator.accountTaskSessionBlocked(ctx, "cid")
	if blockedErr != nil || !blocked {
		t.Fatalf("blocked=%v err=%v", blocked, blockedErr)
	}
	// replaceErr 保存模拟续期替换 Cookie 的错误。
	replaceErr := store.Cookies.UpdateValueExisting(ctx, "cid", "sid=renewed")
	if replaceErr != nil {
		t.Fatal(replaceErr)
	}
	// unblocked、unblockedErr 保存凭证变化后的会话解除结果。
	unblocked, unblockedErr := coordinator.accountTaskSessionBlocked(ctx, "cid")
	if unblockedErr != nil || unblocked {
		t.Fatalf("unblocked=%v err=%v", unblocked, unblockedErr)
	}
	// closedStoreCoordinator 是底层数据库关闭后的错误传播测试对象。
	closedStoreCoordinator := &accountTaskCoordinator{repository: newStoreAccountTaskRepository(store), senders: testSenderProvider{sender: sender}, logger: slog.Default()}
	// closeErr 保存关闭测试数据库的错误。
	closeErr := store.DB.Close()
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	// failedValue、failedErr 保存 Cookie 写回失败时的旧值和包装错误。
	failedValue, failedErr := closedStoreCoordinator.persistTaskCookies(ctx, "cid", "sid=renewed", "sid=failed")
	if failedValue != "sid=renewed" || failedErr == nil || errors.Is(failedErr, context.Canceled) {
		t.Fatalf("failed value=%q err=%v", failedValue, failedErr)
	}
}
