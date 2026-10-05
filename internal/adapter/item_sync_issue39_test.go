package adapter

import (
	"context"
	"errors"
	"testing"
	"time"

	itemapp "xianyu-go/internal/application/items"
	"xianyu-go/internal/db"
	"xianyu-go/internal/xianyu/cookierefresh"
	"xianyu-go/internal/xianyu/mtop"
)

// TestItemSyncNotifiesRuntimeAfterReleasingCredentialLock 验证 Cookie 提交通知不会在账号凭证锁内重入运行时。
func TestItemSyncNotifiesRuntimeAfterReleasingCredentialLock(t *testing.T) {
	// store、cleanup 管理隔离数据库及关闭责任。
	store, cleanup := newAdapterTestStore(t)
	defer cleanup()
	// ctx 是凭证读取和同步提交共用的上下文。
	ctx := context.Background()
	// callbackDone 表示运行时通知已经成功重入同一账号凭证锁。
	callbackDone := make(chan struct{})
	// repository 注入会尝试重新获取账号凭证锁的运行时通知回调。
	repository := NewItemSyncRepository(store, nil, nil, func(context.Context, string, string) {
		// unlock 释放回调重新获取的账号凭证锁。
		unlock := store.LockAccountCredentials("cid")
		unlock()
		close(callbackDone)
	}, nil)
	// query 描述当前账号的同步请求。
	query := itemapp.SyncQuery{UserID: 1, CookieID: "cid"}
	// detail、cookieValue、session、unlock 保存远端请求前取得的凭证状态。
	detail, cookieValue, _, session, unlock, beginErr := repository.begin(ctx, query)
	if beginErr != nil {
		t.Fatal(beginErr)
	}
	unlock()
	// session 使用平台返回的新 Cookie 快照模拟一次成功轮换。
	session.ReplaceSnapshot([]cookierefresh.BrowserCookie{{Name: "sid", Value: "rotated-after-unlock", Domain: ".goofish.com", Path: "/"}})
	// finishErr 保存第一阶段提交结果；通知若在锁内执行，此调用会超过超时。
	finishDone := make(chan error, 1)
	go func() {
		// finishErr 保存后台同步提交返回的错误。
		_, _, _, finishErr := repository.finishRemote(ctx, query, detail, session, nil)
		finishDone <- finishErr
	}()
	select {
	// finishErr 表示后台同步提交完成后的错误。
	case finishErr := <-finishDone:
		if finishErr != nil {
			t.Fatal(finishErr)
		}
	case <-time.After(time.Second):
		t.Fatal("同步提交未在释放账号锁后完成")
	}
	select {
	case <-callbackDone:
	case <-time.After(time.Second):
		t.Fatal("运行时通知无法重新获取账号凭证锁")
	}
	// saved、savedErr 验证通知对应的新 Cookie 已经持久化。
	saved, savedErr := store.Cookies.GetCookiePlatformRuntimeData(ctx, query.CookieID)
	if savedErr != nil || saved.Value == cookieValue {
		t.Fatalf("Cookie 提交结果异常 saved=%+v err=%v", saved, savedErr)
	}
}

// TestItemSyncRejectsStaleCookieWrite 验证同步期间的新登录凭证不会被旧平台响应覆盖；t 管理本地 SQLite 生命周期。
func TestItemSyncRejectsStaleCookieWrite(t *testing.T) {
	// store、cleanup 管理隔离数据库及关闭责任。
	store, cleanup := newAdapterTestStore(t)
	defer cleanup()
	// ctx 是凭证读取和写入共用的上下文。
	ctx := context.Background()
	// owner、ownerErr 保存合成账号的所有者。
	owner, ownerErr := store.Users.GetByUsername(ctx, "admin")
	if ownerErr != nil {
		t.Fatal(ownerErr)
	}
	// repository 使用真实凭证仓储复现远端请求返回后的提交阶段。
	repository := NewItemSyncRepository(store, nil, nil, nil, nil)
	// query 描述该所有者对测试账号发起的同步。
	query := itemapp.SyncQuery{UserID: owner.ID, CookieID: "cid"}
	// detail、session、unlock 保存发起远端调用前取得的凭证快照和会话。
	detail, _, _, session, unlock, beginErr := repository.begin(ctx, query)
	if beginErr != nil {
		t.Fatal(beginErr)
	}
	unlock()
	// staleResponseSnapshot 模拟旧请求结束后携带的 Cookie Jar 变化。
	staleResponseSnapshot := []cookierefresh.BrowserCookie{{Name: "sid", Value: "old-response", Domain: ".goofish.com", Path: "/"}}
	session.ReplaceSnapshot(staleResponseSnapshot)
	// updateErr 模拟用户在远端请求期间完成新的登录并写入更新凭证。
	updateErr := store.Cookies.UpdateValueOwned(ctx, "cid", "unb=1; sid=newer-login", owner.ID)
	if updateErr != nil {
		t.Fatal(updateErr)
	}
	// _, _, _, finishErr 必须拒绝使用旧快照写回的同步提交。
	_, _, _, finishErr := repository.finishRemote(ctx, query, detail, session, nil)
	// typedErr 保存同步层暴露给调用方的凭证阶段错误。
	var typedErr *itemapp.SyncError
	if !errors.As(finishErr, &typedErr) || typedErr.Kind != itemapp.SyncErrorCredential {
		t.Fatalf("旧同步响应未被标记为凭证冲突 err=%v", finishErr)
	}
	// saved、savedErr 验证新登录结果仍是数据库的最终凭证。
	saved, savedErr := store.Cookies.GetCookiePlatformRuntimeData(ctx, "cid")
	if savedErr != nil || saved.Value != "unb=1; sid=newer-login" {
		t.Fatalf("旧同步响应覆盖了新登录凭证 saved=%+v err=%v", saved, savedErr)
	}
}

// TestItemSyncNotifiesRuntimeAfterCookieCommit 验证同步阶段写回平台 Cookie 后会通知运行中的账号。
func TestItemSyncNotifiesRuntimeAfterCookieCommit(t *testing.T) {
	// store、cleanup 管理隔离数据库及关闭责任。
	store, cleanup := newAdapterTestStore(t)
	defer cleanup()
	// ctx 是凭证和同步提交共用的上下文。
	ctx := context.Background()
	// notified 保存运行时 Cookie 更新通知次数。
	notified := 0
	// repository 注入只记录通知次数的运行时回调。
	repository := NewItemSyncRepository(store, nil, nil, func(context.Context, string, string) { notified++ }, nil)
	// query 描述当前账号的同步请求。
	query := itemapp.SyncQuery{UserID: 1, CookieID: "cid"}
	// detail、cookieValue、session、unlock 保存远端请求前的凭证状态。
	detail, cookieValue, _, session, unlock, beginErr := repository.begin(ctx, query)
	if beginErr != nil {
		t.Fatal(beginErr)
	}
	unlock()
	// session 使用平台返回的新 Cookie 快照模拟一次成功轮换。
	session.ReplaceSnapshot([]cookierefresh.BrowserCookie{{Name: "sid", Value: "rotated", Domain: ".goofish.com", Path: "/"}})
	// latest、finishedSession、callErr、finishErr 保存第一阶段提交结果。
	latest, finishedSession, callErr, finishErr := repository.finishRemote(ctx, query, detail, session, nil)
	if finishErr != nil || callErr != nil || latest == nil || finishedSession == nil {
		t.Fatalf("Cookie 提交异常 latest=%+v callErr=%v finishErr=%v", latest, callErr, finishErr)
	}
	// listErr 验证列表请求后的重复检查不会丢失第一次写回通知。
	listErr := repository.persistAfterListResponse(ctx, query, latest, finishedSession, cookieValue, "")
	if listErr != nil || notified != 1 {
		t.Fatalf("运行时 Cookie 通知次数异常 notified=%d err=%v", notified, listErr)
	}
}

// TestIncompleteItemSyncPreservesItemsAndRules 验证分页层报告错误时，部分列表不能删除现有商品或规则；t 管理本地 SQLite 生命周期。
func TestIncompleteItemSyncPreservesItemsAndRules(t *testing.T) {
	// store、cleanup 是隔离数据库及关闭函数，不读取用户业务数据库。
	store, cleanup := newAdapterTestStore(t)
	defer cleanup()
	// ctx 是仓储夹具与同步用例的共同上下文。
	ctx := context.Background()
	// owner、ownerErr 保存合成账号的所有者。
	owner, ownerErr := store.Users.GetByUsername(ctx, "admin")
	if ownerErr != nil {
		t.Fatal(ownerErr)
	}
	if itemErr := store.Items.Upsert(ctx, &db.ItemInfoRow{CookieID: "cid", ItemID: "keep", ItemTitle: "必须保留"}); itemErr != nil { // itemErr 是既有商品夹具写入结果。
		t.Fatal(itemErr)
	}
	// ruleID、ruleErr 创建一个在同步失败时必须保持启用的商品规则。
	ruleID, ruleErr := store.Automation.Create(ctx, db.AutomationRuleInput{UserID: owner.ID, CookieID: "cid", ItemID: "keep", Name: "保留规则", TriggerType: "order_paid", Enabled: true})
	if ruleErr != nil {
		t.Fatal(ruleErr)
	}
	// client 故意同时返回部分数据和失败，验证错误优先于部分列表。
	client := &itemSyncListClient{allResult: &mtop.ItemListResult{Items: []mtop.ItemListItem{{ID: "partial"}}}, allErr: errors.New("分页不完整")}
	// repository 使用真实事务仓储和本地平台替身。
	repository := NewItemSyncRepository(store, func() mtop.Client { return client }, nil, nil, nil)
	if _, syncErr := repository.SyncAll(ctx, itemapp.SyncQuery{UserID: owner.ID, CookieID: "cid"}); syncErr == nil { // syncErr 必须向应用和页面传播失败。
		t.Fatal("不完整同步未向用户返回失败")
	}
	// item、itemErr 验证旧商品仍可被正常管理查询读取。
	item, itemErr := store.Items.Get(ctx, "cid", "keep")
	if itemErr != nil || item.ItemTitle != "必须保留" {
		t.Fatal("同步失败错误删除或改写了既有商品")
	}
	// rule、loadErr 验证关联发货规则没有被停用或软删除。
	rule, loadErr := store.Automation.Get(ctx, ruleID)
	if loadErr != nil || !rule.Enabled {
		t.Fatal("同步失败错误停用了关联规则")
	}
	if _, partialErr := store.Items.Get(ctx, "cid", "partial"); !errors.Is(partialErr, db.ErrNotFound) { // partialErr 验证部分列表没有被提前写入。
		t.Fatal("同步失败仍写入了部分商品")
	}
}

// TestItemSyncPageReturnsPersistenceFailure 验证分页商品任一写入失败时不会伪造成功数量。
func TestItemSyncPageReturnsPersistenceFailure(t *testing.T) {
	// store、cleanup 保存分页持久化测试使用的隔离数据库及释放函数。
	store, cleanup := newAdapterTestStore(t)
	defer cleanup()
	// ctx 是分页同步和数据库断言共用的上下文。
	ctx := context.Background()
	// triggerSQL 拒绝指定商品写入，模拟数据库约束失败。
	triggerSQL := `CREATE TRIGGER deny_page_item BEFORE INSERT ON item_info WHEN NEW.item_id='blocked-page-item' BEGIN SELECT RAISE(FAIL,'page item rejected'); END`
	// triggerErr 保存数据库触发器创建错误。
	if _, triggerErr := store.DB.ExecContext(ctx, triggerSQL); triggerErr != nil {
		t.Fatal(triggerErr)
	}
	// client 返回先成功后失败的分页商品，验证整个页面事务一起回滚。
	client := &itemSyncListClient{pageResult: &mtop.ItemListResult{PageNumber: 1, PageSize: 2, Items: []mtop.ItemListItem{{ID: "valid-page-item", Title: "先写商品"}, {ID: "blocked-page-item", Title: "拒绝商品"}}}}
	// repository 使用真实事务仓储执行分页同步。
	repository := NewItemSyncRepository(store, func() mtop.Client { return client }, nil, nil, nil)
	// result、syncErr 保存分页同步结果及错误。
	result, syncErr := repository.SyncPage(ctx, itemapp.SyncQuery{UserID: 1, CookieID: "cid", PageNumber: 1, PageSize: 20})
	// typedErr 保存分页层返回的应用阶段错误。
	var typedErr *itemapp.SyncError
	if result != (itemapp.SyncPageResult{}) || !errors.As(syncErr, &typedErr) || typedErr.Kind != itemapp.SyncErrorPersistence {
		t.Fatalf("分页数据库错误未传播 result=%+v err=%v", result, syncErr)
	}
	// _, itemErr 验证事务失败后商品没有留下半条记录。
	if _, itemErr := store.Items.Get(ctx, "cid", "blocked-page-item"); !errors.Is(itemErr, db.ErrNotFound) {
		t.Fatalf("分页事务失败后不应保留商品 err=%v", itemErr)
	}
	// itemErr 保存前序商品回滚后的查询错误。
	if _, itemErr := store.Items.Get(ctx, "cid", "valid-page-item"); !errors.Is(itemErr, db.ErrNotFound) {
		t.Fatalf("分页事务失败后不应保留前序商品 err=%v", itemErr)
	}
}

// TestItemSyncCredentialLoadFailuresArePersistenceErrors 验证提交阶段读取凭证失败不会被误报为凭证冲突。
func TestItemSyncCredentialLoadFailuresArePersistenceErrors(t *testing.T) {
	// ctx 是凭证提交失败测试共用的上下文。
	ctx := context.Background()
	// finishStore、finishCleanup 保存第一阶段提交测试数据库。
	finishStore, finishCleanup := newAdapterTestStore(t)
	defer finishCleanup()
	// finishRepository 保存第一阶段凭证提交适配器。
	finishRepository := NewItemSyncRepository(finishStore, nil, nil, nil, nil)
	// finishDetail、finishSession、finishUnlock 保存关闭数据库前的凭证快照。
	finishDetail, _, _, finishSession, finishUnlock, beginErr := finishRepository.begin(ctx, itemapp.SyncQuery{UserID: 1, CookieID: "cid"})
	if beginErr != nil {
		t.Fatal(beginErr)
	}
	finishUnlock()
	// closeErr 保存第一阶段测试数据库关闭错误。
	if closeErr := finishStore.DB.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	// _, _, _, finishErr 保存第一阶段凭证读取失败的应用错误。
	_, _, _, finishErr := finishRepository.finishRemote(ctx, itemapp.SyncQuery{UserID: 1, CookieID: "cid"}, finishDetail, finishSession, nil)
	// finishTypedErr 保存第一阶段凭证读取错误的应用阶段分类。
	var finishTypedErr *itemapp.SyncError
	if !errors.As(finishErr, &finishTypedErr) || finishTypedErr.Kind != itemapp.SyncErrorPersistence {
		t.Fatalf("第一阶段凭证读取失败分类异常: %v", finishErr)
	}

	// enrichStore、enrichCleanup 保存第二阶段提交测试数据库。
	enrichStore, enrichCleanup := newAdapterTestStore(t)
	defer enrichCleanup()
	// enrichRepository 保存第二阶段凭证提交适配器。
	enrichRepository := NewItemSyncRepository(enrichStore, nil, nil, nil, nil)
	// enrichDetail、enrichSession、enrichUnlock 保存关闭数据库前的凭证快照。
	enrichDetail, enrichCookie, _, enrichSession, enrichUnlock, enrichBeginErr := enrichRepository.begin(ctx, itemapp.SyncQuery{UserID: 1, CookieID: "cid"})
	if enrichBeginErr != nil {
		t.Fatal(enrichBeginErr)
	}
	enrichUnlock()
	// closeErr 保存第二阶段测试数据库关闭错误。
	if closeErr := enrichStore.DB.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	// listErr 保存列表请求后凭证读取失败的应用错误。
	listErr := enrichRepository.persistAfterListResponse(ctx, itemapp.SyncQuery{UserID: 1, CookieID: "cid"}, enrichDetail, enrichSession, enrichCookie, "")
	// listTypedErr 保存列表提交的凭证读取错误分类。
	var listTypedErr *itemapp.SyncError
	if !errors.As(listErr, &listTypedErr) || listTypedErr.Kind != itemapp.SyncErrorPersistence {
		t.Fatalf("列表提交凭证读取失败分类异常: %v", listErr)
	}
}
