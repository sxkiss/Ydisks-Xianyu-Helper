package adapter

import (
	"context"
	"strings"
	"testing"
	"time"

	orderapp "xianyu-go/internal/application/orders"
	"xianyu-go/internal/db"
	"xianyu-go/internal/xianyu/cookierefresh"
	"xianyu-go/internal/xianyu/mtop"
)

// TestOrderRuntimeReleasesCredentialLockDuringConsign 验证确认发货的慢平台调用不会阻塞并发登录凭证写入。
func TestOrderRuntimeReleasesCredentialLockDuringConsign(t *testing.T) {
	// store、cleanup 保存并发确认发货测试使用的隔离数据库及释放函数。
	store, cleanup := newAdapterTestStore(t)
	defer cleanup()
	// started、release 确定化平台确认发货期间的凭证更新窗口。
	started, release := make(chan struct{}), make(chan struct{})
	// client 在平台请求中暂停，返回旧响应 Cookie 以验证提交阶段的版本复核。
	client := &orderRuntimeMTopFake{consign: func(context.Context, string, string) (bool, []string, string, error) {
		close(started)
		<-release
		return true, []string{"SUCCESS"}, "sid=stale", nil
	}}
	// runtime 保存绑定测试平台客户端的订单运行时。
	runtime := NewOrderRuntime(store, OrderRuntimeHooks{Client: func() mtop.Client { return client }, ClientAvailable: func() bool { return true }}, nil, nil)
	// resultCh 接收异步确认发货结果。
	resultCh := make(chan orderapp.ConsignResult, 1)
	go func() {
		resultCh <- runtime.ConfirmShipment(context.Background(), "cid", "order-lock", 1)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("确认发货未进入平台调用")
	}
	// updateDone 表示并发登录凭证写入已完成；若账号锁仍被占用，此处会超时。
	updateDone := make(chan error, 1)
	go func() {
		// unlock 保护模拟登录写入的账号凭证临界区。
		unlock := store.LockAccountCredentials("cid")
		defer unlock()
		updateDone <- store.Cookies.UpdateValueOwned(context.Background(), "cid", "sid=new-login", 1)
	}()
	select {
	// updateErr 保存并发登录凭证写入结果。
	case updateErr := <-updateDone:
		if updateErr != nil {
			t.Fatal(updateErr)
		}
	case <-time.After(time.Second):
		t.Fatal("平台确认发货期间并发凭证写入被账号锁阻塞")
	}
	close(release)
	// result 保存旧平台响应被拒绝后的确认发货结果。
	result := <-resultCh
	if result.Err == nil || !strings.Contains(result.Err.Error(), "账号凭证已变化") {
		t.Fatalf("旧确认发货响应未被拒绝: %+v", result)
	}
	// stored、storedErr 验证新登录凭证仍是最终状态。
	stored, storedErr := store.Cookies.GetValue(context.Background(), "cid")
	if storedErr != nil || stored != "sid=new-login" {
		t.Fatalf("旧确认发货响应覆盖了新登录凭证 stored=%q err=%v", stored, storedErr)
	}
}

// TestOrderRuntimePersistsCookieSessionBranches 覆盖订单运行时 Cookie 更新的忽略、成功和失败分支。
func TestOrderRuntimePersistsCookieSessionBranches(t *testing.T) {
	// store、cleanup 是带有固定测试账号的本地数据库及资源释放函数。
	store, cleanup := newAdapterTestStore(t)
	defer cleanup()
	// ctx 是本次订单 Cookie 持久化测试共用的上下文。
	ctx := context.Background()
	// runtime 是绑定本地 Cookie 仓储的订单运行时。
	runtime := NewOrderRuntime(store, OrderRuntimeHooks{}, nil, nil)
	// detail 是数据库中已有账号的非敏感平台运行数据。
	detail := &orderapp.PlatformRuntimeData{ID: "cid", Value: "unb=1", MetadataJSON: `{}`}

	// unchangedValue、unchanged、unchangedHandled 保存未变化更新的兼容结果。
	unchangedValue, unchanged, unchangedHandled, unchangedErr := runtime.PersistCookieSession(ctx, detail, orderapp.RefreshCookieUpdate{Value: detail.Value, Handled: true})
	if unchangedErr != nil || unchanged || !unchangedHandled || unchangedValue != detail.Value {
		t.Fatalf("未变化 Cookie 结果异常 value=%q changed=%v handled=%v err=%v", unchangedValue, unchanged, unchangedHandled, unchangedErr)
	}
	// changedValue、changed、changedHandled 保存变化 Cookie 的成功写回结果。
	changedValue, changed, changedHandled, changedErr := runtime.PersistCookieSession(ctx, detail, orderapp.RefreshCookieUpdate{Value: "unb=2", MetadataJSON: `{}`, Changed: true, Handled: true})
	if changedErr != nil || !changed || !changedHandled || changedValue != "unb=2" {
		t.Fatalf("变化 Cookie 结果异常 value=%q changed=%v handled=%v err=%v", changedValue, changed, changedHandled, changedErr)
	}
	// savedValue、savedErr 保存数据库实际写回后的 Cookie 值。
	savedValue, savedErr := store.Cookies.GetValue(ctx, "cid")
	if savedErr != nil || savedValue != "unb=2" {
		t.Fatalf("Cookie 写回异常 value=%q err=%v", savedValue, savedErr)
	}

	// nilRuntime 是用于验证存储未装配保护分支的订单运行时。
	nilRuntime := NewOrderRuntime(nil, OrderRuntimeHooks{}, nil, nil)
	// _, _, _, nilPersistErr 保存缺少存储时的持久化结果。
	if _, _, _, nilPersistErr := nilRuntime.PersistCookieSession(ctx, detail, orderapp.RefreshCookieUpdate{Value: "unb=3", Changed: true, Handled: true}); nilPersistErr == nil {
		t.Fatal("缺少 Cookie 存储时应返回持久化错误")
	}
	// _, _, _, nilDetailResult 保存缺少详情时的零值结果。
	if _, _, _, nilDetailResult := runtime.PersistCookieSession(ctx, nil, orderapp.RefreshCookieUpdate{Value: "unb=3", Changed: true, Handled: true}); nilDetailResult != nil {
		t.Fatalf("缺少详情不应写回: %v", nilDetailResult)
	}

	// _, unchangedFlatSession 保存平面 Cookie 会话未变化时的内部持久化结果。
	_, unchangedFlatSession := mtop.WithFlatCookieSession(ctx, detail.Value)
	// value、changed、handled、persistErr 保存平面会话内部持久化的返回结果。
	if value, changed, handled, persistErr := runtime.persistOrderCookieSession(ctx, dbCookieRuntimeData(detail), unchangedFlatSession, ""); value != "" || changed || handled || persistErr != nil {
		t.Fatalf("平面未变化会话异常 value=%q changed=%v handled=%v err=%v", value, changed, handled, persistErr)
	}
	// _, unchangedSnapshotSession 保存完整 Cookie 快照未变化时的内部持久化结果。
	_, unchangedSnapshotSession := mtop.WithCookieSnapshot(ctx, []cookierefresh.BrowserCookie{{Name: "sid", Value: "1", Domain: ".goofish.com", Path: "/"}})
	// value、changed、handled、persistErr 保存权威会话内部持久化的返回结果。
	if value, changed, handled, persistErr := runtime.persistOrderCookieSession(ctx, dbCookieRuntimeData(detail), unchangedSnapshotSession, ""); value != detail.Value || changed || !handled || persistErr != nil {
		t.Fatalf("权威未变化会话异常 value=%q changed=%v handled=%v err=%v", value, changed, handled, persistErr)
	}
	unchangedSnapshotSession.ReplaceSnapshot([]cookierefresh.BrowserCookie{{Name: "sid", Value: "2", Domain: ".goofish.com", Path: "/"}})
	// value、changed、handled、persistErr 保存权威快照变化后的内部持久化结果。
	if value, changed, handled, persistErr := runtime.persistOrderCookieSession(ctx, dbCookieRuntimeData(detail), unchangedSnapshotSession, ""); persistErr != nil || !changed || !handled || value == "" {
		t.Fatalf("权威变化会话异常 value=%q changed=%v handled=%v err=%v", value, changed, handled, persistErr)
	}

	// errorStore、errorCleanup 是用于验证写回错误的独立数据库及清理函数。
	errorStore, errorCleanup := newAdapterTestStore(t)
	defer errorCleanup()
	// errorRuntime 是随后主动关闭数据库的订单运行时。
	errorRuntime := NewOrderRuntime(errorStore, OrderRuntimeHooks{}, nil, nil)
	// errorSessionContext、errorSession 保存会触发写回的权威 Cookie 会话。
	errorSessionContext, errorSession := mtop.WithCookieSnapshot(ctx, []cookierefresh.BrowserCookie{{Name: "sid", Value: "old", Domain: ".goofish.com", Path: "/"}})
	_ = errorSessionContext
	errorSession.ReplaceSnapshot([]cookierefresh.BrowserCookie{{Name: "sid", Value: "new", Domain: ".goofish.com", Path: "/"}})
	// value、changed、handled、persistErr 保存数据库关闭后的内部持久化结果。
	if closeErr := errorStore.DB.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	// value、changed、handled、persistErr 保存数据库关闭后的内部持久化返回值。
	if value, changed, handled, persistErr := errorRuntime.persistOrderCookieSession(ctx, dbCookieRuntimeData(detail), errorSession, ""); persistErr == nil || !changed || !handled || value == "" {
		t.Fatalf("数据库关闭后的会话异常 value=%q changed=%v handled=%v err=%v", value, changed, handled, persistErr)
	}
}

// dbCookieRuntimeData 将订单应用层测试数据转换为内部持久化模型。
func dbCookieRuntimeData(detail *orderapp.PlatformRuntimeData) db.CookiePlatformRuntimeData {
	return db.CookiePlatformRuntimeData{ID: detail.ID, Value: detail.Value, MetadataJSON: detail.MetadataJSON}
}
