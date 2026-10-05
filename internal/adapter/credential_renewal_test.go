package adapter

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"xianyu-go/internal/xianyu/cookierefresh"
	"xianyu-go/internal/xianyu/renew"
)

// TestProtocolRenewalPreservesConcurrentCookies 验证直接和迟到响应都在最新 Cookie 上增量合并；t 拥有本地服务与数据库。
func TestProtocolRenewalPreservesConcurrentCookies(t *testing.T) {
	// late 标记当前场景是否让响应头晚于 Promise 截止时间。
	for _, late := range []bool{false, true} {
		// name 使两种响应时序的失败可以独立定位。
		name := "on-time"
		if late {
			name = "late"
		}
		// t 是当前时序场景的测试上下文。
		t.Run(name, func(t *testing.T) {
			// store、cleanup 创建并释放仅含人工 Cookie 的 SQLite 数据库。
			store, cleanup := newAdapterTestStore(t)
			defer cleanup()
			// ctx、cancel 共同约束 HTTP 处理器及结果等待，失败时不会遗留 goroutine。
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			// entered、release 让测试在请求已发送、响应未到达的窗口更新并发 Cookie。
			entered, release := make(chan struct{}), make(chan struct{})
			// server 的处理器由 release 或请求取消唤醒，只返回合成响应 Cookie。
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				close(entered)
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				w.Header().Set("Set-Cookie", "rotated=fixture; Path=/")
				_, _ = io.WriteString(w, `{"content":{"data":{"processFinished":true,"resultCode":100}}}`)
			}))
			defer server.Close()
			// 初始值和并发值仅在不同字段上更新；响应不得丢失 concurrent。
			if err := store.Cookies.UpdateValueExisting(ctx, "cid", "havana_lgc_exp=9999999999999; concurrent=old"); err != nil {
				t.Fatal(err)
			}
			// detail、loadErr 保存请求发起前的敏感内存视图，不写入断言输出。
			detail, loadErr := store.Cookies.GetCookiePlatformRuntimeData(ctx, "cid")
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			// adapter 是生产恢复编排入口；timeout 注入是否触发迟到路径。
			adapter := New(store, nil, nil)
			// timeout 默认允许请求成功，迟到场景改为短窗口。
			timeout := time.Second
			if late {
				timeout = 5 * time.Millisecond
			}
			adapter.SetRenewService(renew.Service{HTTPClient: server.Client(), SilentHasLoginURL: server.URL, RetryDelay: -1, PromiseTimeout: timeout})
			// done 由唯一恢复协程发送，测试必须消费以 Join 当前调用。
			done := make(chan bool, 1)
			// 协程只拥有 detail 视图，测试线程不并发读写它。
			go func() {
				// success 是恢复最终状态；错误详情不含本用例需要的其他断言。
				success, _ := adapter.tryProtocolCredentialRenew(ctx, &detail)
				done <- success
			}()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("续期请求没有进入测试服务")
			}
			if late {
				time.Sleep(25 * time.Millisecond)
			}
			// updateErr 保存平台请求进行中另一路凭证更新的结果。
			updateErr := store.Cookies.UpdateValueExisting(ctx, "cid", "havana_lgc_exp=9999999999999; concurrent=new")
			close(release)
			if updateErr != nil {
				t.Fatal(updateErr)
			}
			select {
			// success 应与是否在 Promise 窗口内完成对应。
			case success := <-done:
				if success == late {
					t.Error("续期状态没有保留官方 Promise 时序")
				}
			case <-ctx.Done():
				t.Fatal("续期协程没有按时结束")
			}
			// saved、readErr 检查并发字段与响应字段共存，不输出 Cookie 明文。
			saved, readErr := store.Cookies.GetValue(ctx, "cid")
			if readErr != nil || !strings.Contains(saved, "concurrent=new") || !strings.Contains(saved, "rotated=fixture") {
				t.Fatalf("增量 Cookie 合并丢失字段，err=%v", readErr)
			}
		})
	}
}

// TestPersistProtocolRenewalResponseBoundaries 验证无响应、重复响应、账号缺失和数据库写失败；t 管理隔离数据库。
func TestPersistProtocolRenewalResponseBoundaries(t *testing.T) {
	// store、cleanup 创建并清理用于边界验证的 SQLite 仓储。
	store, cleanup := newAdapterTestStore(t)
	defer cleanup()
	// adapter 使用真实仓储，ctx 限制所有本地存储操作。
	adapter := New(store, nil, nil)
	// ctx 是本组同步操作共享的测试上下文。
	ctx := context.Background()
	// detail、err 保存账号当前平台凭证，不包含密码登录秘密。
	detail, err := store.Cookies.GetCookiePlatformRuntimeData(ctx, "cid")
	if err != nil {
		t.Fatal(err)
	}
	if err = adapter.persistProtocolRenewalResponse(ctx, &detail, &renew.Result{}); err != nil {
		t.Fatal(err)
	}
	// metadata 为权威 Jar，验证增量持久化保留 Domain、Path 和 HttpOnly。
	metadata := cookierefresh.MetadataWithSnapshot("", []cookierefresh.BrowserCookie{})
	if err = store.Cookies.UpdateRenewalCookie(ctx, "cid", "", metadata, 0); err != nil {
		t.Fatal(err)
	}
	// detail 在测试主动切换为权威空 Jar 后重新获取，与真实请求发起前固定凭证的时序一致。
	detail, err = store.Cookies.GetCookiePlatformRuntimeData(ctx, "cid")
	if err != nil {
		t.Fatal(err)
	}
	// response 是可重复重放的人工响应。
	response := &renew.Result{SetCookies: []string{"fixture=value; Domain=.goofish.com; Path=/; Secure; HttpOnly"}}
	// attempt 覆盖首次变化和再次重放不变化两条路径。
	for attempt := 0; attempt < 2; attempt++ {
		if err = adapter.persistProtocolRenewalResponse(ctx, &detail, response); err != nil {
			t.Fatal(err)
		}
	}
	// snapshot、complete 检查调用方内存同步了最新完整 Jar，而非退化为扁平 Cookie。
	snapshot, complete := cookierefresh.SnapshotFromMetadataOK(detail.MetadataJSON)
	if !complete || len(snapshot) != 1 || !snapshot[0].HTTPOnly {
		t.Fatal("完整 Jar 属性丢失")
	}
	// triggerErr 注入 SQLite 写回失败，验证错误不会被误报为续期成功。
	if _, triggerErr := store.DB.Exec(`CREATE TRIGGER reject_renewal BEFORE UPDATE ON cookies BEGIN SELECT RAISE(FAIL, 'fixture failure'); END`); triggerErr != nil {
		t.Fatal(triggerErr)
	}
	if err = adapter.persistProtocolRenewalResponse(ctx, &detail, &renew.Result{SetCookies: []string{"fixture=changed; Domain=.goofish.com; Path=/; Secure"}}); err == nil {
		t.Fatal("写入失败未传播")
	}
	detail.ID = "missing-fixture-account"
	if err = adapter.persistProtocolRenewalResponse(ctx, &detail, response); err == nil {
		t.Fatal("账号不存在时不应保存响应")
	}
}

// TestPersistProtocolRenewalKeepsCommittedCookieOnCacheError 验证缓存清理失败不会回滚或误报已提交的 Cookie；t 管理 SQLite 夹具。
func TestPersistProtocolRenewalKeepsCommittedCookieOnCacheError(t *testing.T) {
	// store、cleanup 是本测试独立的账号存储和释放函数。
	store, cleanup := newAdapterTestStore(t)
	defer cleanup()
	// ctx 约束本组同步仓储操作。
	ctx := context.Background()
	// 创建人工设备记录以便触发 UPDATE 失败，设备值不进入断言输出。
	if _, err := store.Tokens.GetOrCreateDeviceID(ctx, "cid", "fixture-device"); err != nil {
		t.Fatal(err)
	}
	// triggerErr 注入只针对 Token 缓存的存储错误，不影响 Cookie 主记录。
	if _, triggerErr := store.DB.Exec(`CREATE TRIGGER reject_token_clear BEFORE UPDATE ON account_tokens BEGIN SELECT RAISE(FAIL, 'fixture cache failure'); END`); triggerErr != nil {
		t.Fatal(triggerErr)
	}
	// detail、readErr 获取被测账号的敏感内存视图，避免重新读取密码字段。
	detail, readErr := store.Cookies.GetCookiePlatformRuntimeData(ctx, "cid")
	if readErr != nil {
		t.Fatal(readErr)
	}
	// adapter 是生产增量持久化入口，调用后即使缓存失败也应返回 Cookie 提交成功。
	adapter := New(store, nil, nil)
	// err 保存续期响应持久化错误；缓存清理失败不能改变主 Cookie 已提交的成功结果。
	if err := adapter.persistProtocolRenewalResponse(ctx, &detail, &renew.Result{SetCookies: []string{"committed=fixture; Path=/"}}); err != nil {
		t.Fatalf("缓存失败不应覆盖 Cookie 提交结果，err=%v", err)
	}
	// value、loadErr 验证数据库和调用方视图都保留已经提交的新 Cookie，不输出明文。
	value, loadErr := store.Cookies.GetValue(ctx, "cid")
	if loadErr != nil || !strings.Contains(value, "committed=fixture") || detail.Value != value {
		t.Fatal("缓存错误导致已提交的 Cookie 丢失")
	}
}

// TestPersistProtocolRenewalRejectsSameNameConcurrentLogin 验证并发登录后的同名 Cookie 不会被旧续期响应覆盖。
func TestPersistProtocolRenewalRejectsSameNameConcurrentLogin(t *testing.T) {
	// store、cleanup 管理隔离数据库及其释放函数。
	store, cleanup := newAdapterTestStore(t)
	defer cleanup()
	// ctx 控制本地凭证读写。
	ctx := context.Background()
	// initial 保存旧请求开始时的凭证快照。
	initial, err := store.Cookies.GetCookiePlatformRuntimeData(ctx, "cid")
	if err != nil {
		t.Fatal(err)
	}
	// err 保存模拟并发新登录写入失败原因。
	if err := store.Cookies.UpdateValueExisting(ctx, "cid", "_m_h5_tk=new-login"); err != nil {
		t.Fatal(err)
	}
	// adapter 使用生产续期响应收口入口。
	adapter := New(store, nil, nil)
	// persistErr 应拒绝旧请求的同名响应；不同名增量由其他测试覆盖。
	persistErr := adapter.persistProtocolRenewalResponse(ctx, &initial, &renew.Result{SetCookies: []string{"_m_h5_tk=old-response; Path=/"}})
	if persistErr == nil {
		t.Fatal("同名并发登录凭证冲突未被拒绝")
	}
	// saved、readErr 验证新登录值仍然存在，不输出凭证明文。
	saved, readErr := store.Cookies.GetValue(ctx, "cid")
	if readErr != nil || saved != "_m_h5_tk=new-login" {
		t.Fatal("旧续期响应覆盖了新登录凭证")
	}
}
