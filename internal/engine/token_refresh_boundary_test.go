package engine

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"xianyu-go/internal/xianyu/mtop"
	"xianyu-go/internal/xianyu/protocol"
)

// TestTokenRefreshStaysInsideMTop 验证真实 MTOP 客户端在 WS 获取 Token 时内部换签，耗尽后不续期账号；t 管理本地 HTTP 和 SQLite。
func TestTokenRefreshStaysInsideMTop(t *testing.T) {
	// exhaust 指定平台是否持续返回 Token 过期，覆盖成功轮换和重试耗尽两条路径。
	for _, exhaust := range []bool{false, true} {
		// t 是当前平台响应场景的隔离测试上下文。
		t.Run(fmt.Sprintf("exhaust=%v", exhaust), func(t *testing.T) {
			// requests 跨本地 HTTP 服务协程统计实际 Token 请求次数。
			var requests atomic.Int32
			// server 只模拟 Token 端点；w 写响应，r 用于校验重试确实采用新签名。
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// attempt 是本次 Token HTTP 请求的序号。
				attempt := requests.Add(1)
				if exhaust || attempt == 1 {
					if !exhaust {
						w.Header().Add("Set-Cookie", "_m_h5_tk=fresh_1; Path=/")
					}
					fmt.Fprint(w, `{"ret":["FAIL_SYS_TOKEN_EXOIRED::令牌过期"]}`)
					return
				}
				if err := r.ParseForm(); err != nil { // err 表示本地 Token 请求表单解析失败。
					t.Error(err)
				}
				if r.URL.Query().Get("sign") != protocol.GenerateSign(r.URL.Query().Get("t"), "fresh", r.PostForm.Get("data")) {
					t.Error("重试没有使用响应下发的新 MTOP Token 签名")
				}
				fmt.Fprintf(w, `{"ret":["SUCCESS::调用成功"],"data":{"accessToken":"local-access","accessTokenExpiredTime":%d}}`, time.Now().Add(time.Hour).UnixMilli())
			}))
			defer server.Close()
			// client 将引擎所有 Token 请求固定到本地 HTTP，不访问真实平台。
			client := &mtop.ClientImpl{HTTPClient: server.Client(), TokenURL: server.URL + "/token"}
			// account、handler、store、cleanup 提供持久化凭证、账号恢复计数和关闭责任。
			account, handler, store, cleanup := newRunAccount(t, client)
			defer cleanup()
			// token、_, tokenErr 是完整引擎刷新链返回的连接 Token 及失败分类。
			token, _, tokenErr := account.refreshToken(context.Background())
			if exhaust {
				if !mtop.IsMTopTokenExpiredErr(tokenErr) || mtop.IsSessionExpiredErr(tokenErr) || requests.Load() != 5 || token != "" {
					t.Fatalf("内部重试耗尽分类或请求次数异常: err=%v requests=%d", tokenErr, requests.Load())
				}
				// ctx、cancel 让失败处理器真实进入退避后被取消，避免等待生产重连间隔。
				ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
				defer cancel()
				// retry、failureErr 是 Token 重试耗尽后的 WS 连接决策。
				retry, failureErr := (&connectionCoordinator{account: account}).handleTokenAcquisitionFailure(ctx, &fakeWSConn{}, tokenErr)
				if retry || !errors.Is(failureErr, context.DeadlineExceeded) {
					t.Fatalf("Token 耗尽应退避并响应取消: retry=%v err=%v", retry, failureErr)
				}
			} else {
				if tokenErr != nil || token != "local-access" || requests.Load() != 2 {
					t.Fatalf("内部换签应在第二次请求成功: err=%v requests=%d", tokenErr, requests.Load())
				}
				// saved、readErr 检查新的签名 Cookie 已被引擎持久化，禁止在失败输出中打印凭证。
				saved, readErr := store.Cookies.GetValue(context.Background(), "cid")
				if readErr != nil || protocol.SignToken(saved) != "fresh" {
					t.Fatal("内部刷新后的签名 Cookie 未持久化")
				}
			}
			if handler.refresh != 0 {
				t.Fatalf("Token 刷新不得触发账号续期: calls=%d", handler.refresh)
			}
		})
	}
}
