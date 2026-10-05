package adapter

import (
	"context"
	"testing"
	"xianyu-go/internal/xianyu/cookierefresh"
	"xianyu-go/internal/xianyu/renew"
)

// TestProtocolRenewalRejectsScopedConflict 验证仅完整快照内的同名凭证变化也必须阻止旧响应覆盖。
func TestProtocolRenewalRejectsScopedConflict(t *testing.T) {
	// store 与 cleanup 管理只含合成凭证的本地测试库。
	store, cleanup := newAdapterTestStore(t)
	defer cleanup()
	// ctx 是本次数据库操作上下文。
	ctx := context.Background()
	// initialSnapshot 的 passport HostOnly Cookie 不会出现在 www 文档扁平视图内。
	initialSnapshot := []cookierefresh.BrowserCookie{{Name: "unb", Value: "fixture-user", Domain: ".goofish.com", Path: "/", Secure: true}, {Name: "session", Value: "old", Domain: "passport.goofish.com", Path: "/", Secure: true, HTTPOnly: true}}
	// err 表示测试凭证快照写入失败，不输出合成凭证值。
	if err := store.Cookies.UpdateRenewalCookie(ctx, "cid", "unb=fixture-user", cookierefresh.MetadataWithSnapshot("", initialSnapshot), 1); err != nil {
		t.Fatal(err)
	}
	// initial 固定请求发起前的视图。
	initial, err := store.Cookies.GetCookiePlatformRuntimeData(ctx, "cid")
	if err != nil {
		t.Fatal(err)
	}
	initialSnapshot[1].Value = "new-login"
	// err 表示测试凭证快照写入失败，不输出合成凭证值。
	if err := store.Cookies.UpdateRenewalCookie(ctx, "cid", "unb=fixture-user", cookierefresh.MetadataWithSnapshot("", initialSnapshot), 2); err != nil {
		t.Fatal(err)
	}
	// adapter 提交基于旧会话返回的同作用域响应。
	adapter := New(store, nil, nil)
	// persistErr 应拒绝旧响应覆盖仅存在于完整 Jar 中的新凭证。
	persistErr := adapter.persistProtocolRenewalResponse(ctx, &initial, &renew.Result{SetCookies: []string{"session=stale-response; Path=/; Secure; HttpOnly"}})
	if persistErr == nil {
		t.Fatal("未拒绝完整作用域凭证冲突")
	}
	// saved 与 snapshot 只用于核对合成凭证，不输出凭证明文。
	saved, err := store.Cookies.GetCookiePlatformRuntimeData(ctx, "cid")
	if err != nil {
		t.Fatal(err)
	}
	// snapshot 是写回拒绝后用于核对的新登录快照。
	snapshot, _ := cookierefresh.SnapshotFromMetadataOK(saved.MetadataJSON)
	// cookie 是最终快照中的一条凭证，不得输出到失败日志。
	for _, cookie := range snapshot {
		if cookie.Name == "session" && cookie.Value == "stale-response" {
			t.Fatalf("旧响应覆盖完整 Jar 中的新登录凭证，persistErr=%v", persistErr)
		}
	}
}
