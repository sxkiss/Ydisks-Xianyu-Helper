package mtop

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"xianyu-go/internal/xianyu/protocol"
)

// TestTokenRefreshForLoginStatusAndOrderDetail 使用 t 核验两种入口的 Token 刷新、失败上限和真实 Session 错误传递。
func TestTokenRefreshForLoginStatusAndOrderDetail(t *testing.T) {
	// operation 区分登录态检查与订单详情，二者共用真实本地 MTOP 刷新夹具。
	for _, operation := range []string{"login", "order"} {
		// outcome 表示刷新端点的返回类型，覆盖成功、重复过期、Session 和网络响应解析失败。
		for _, outcome := range []string{"success", "token", "session", "decode"} {
			// t 管理当前入口与刷新结果组合的断言。
			t.Run(operation+"/"+outcome, func(t *testing.T) {
				// businessCalls、tokenCalls 由同步客户端顺序请求的服务端处理器记录调用次数。
				businessCalls, tokenCalls := 0, 0
				// server 模拟业务 Token 过期与刷新端点；w 写响应，r 用于验证换签输入。
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/token" {
						tokenCalls++
						switch outcome {
						case "success":
							http.SetCookie(w, &http.Cookie{Name: "_m_h5_tk", Value: "fresh_2", Path: "/"})
							fmt.Fprint(w, `{"ret":["SUCCESS::调用成功"],"data":{"accessToken":"fixture"}}`)
						case "token":
							fmt.Fprint(w, `{"ret":["FAIL_SYS_TOKEN_EXPIRED::令牌过期"]}`)
						case "session":
							fmt.Fprint(w, `{"ret":["FAIL_SYS_SESSION_EXPIRED::Session过期"]}`)
						case "decode":
							fmt.Fprint(w, `{`)
						}
						return
					}
					businessCalls++
					if businessCalls == 1 {
						// 普通 Cookie 变化不能被误认为签名 Token 已刷新。
						http.SetCookie(w, &http.Cookie{Name: "ordinary", Value: "changed"})
						w.WriteHeader(http.StatusUnauthorized)
						fmt.Fprint(w, `{"ret":["FAIL_SYS_TOKEN_EMPTY::令牌为空"]}`)
						return
					}
					_ = r.ParseForm()
					if r.URL.Query().Get("sign") != protocol.GenerateSign(r.URL.Query().Get("t"), "fresh", r.PostForm.Get("data")) {
						t.Error("重试未使用刷新后的 Token 签名")
					}
					fmt.Fprint(w, `{"ret":["SUCCESS::调用成功"],"data":{}}`)
				}))
				defer server.Close()
				// client 的全部端点限定在本地，不访问真实账号或平台。
				client := &ClientImpl{HTTPClient: server.Client(), LoginUserURL: server.URL + "/login", OrderDetailURL: server.URL + "/order", TokenURL: server.URL + "/token"}
				// ctx、session 保存跨业务与 Token 刷新请求共享的 Cookie 状态。
				ctx, session := WithFlatCookieSession(context.Background(), "unb=1; _m_h5_tk=old_1")
				// err 保留当前入口最终返回的错误分类。
				var err error
				if operation == "login" {
					_, err = client.CheckLoginStatusContext(ctx, "")
				} else {
					_, err = client.FetchOrderDetail(ctx, "", "order-1")
				}
				// expectedBusiness、expectedToken 定义有界请求次数，重复过期保留客户端既有五次上限。
				expectedBusiness, expectedToken := 1, 1
				if outcome == "success" {
					expectedBusiness = 2
					// cookies 保存刷新后会话状态，验证普通 Cookie 和签名 Cookie 都未丢失。
					cookies, _, _ := session.State()
					if err != nil || !strings.Contains(cookies, "_m_h5_tk=fresh_2") || !strings.Contains(cookies, "ordinary=changed") {
						t.Fatalf("刷新后的状态未保留: err=%v", err)
					}
				} else if err == nil {
					t.Fatal("刷新失败未返回错误")
				}
				if outcome == "token" {
					expectedToken = 5
					if !IsMTopTokenExpiredErr(err) {
						t.Fatal("Token 重试耗尽丢失分类")
					}
				}
				if IsSessionExpiredErr(err) != (outcome == "session") {
					t.Fatalf("Session 恢复边界错误: %v", err)
				}
				if businessCalls != expectedBusiness || tokenCalls != expectedToken {
					t.Fatalf("业务/Token 调用=%d/%d，期望 %d/%d", businessCalls, tokenCalls, expectedBusiness, expectedToken)
				}
			})
		}
	}
}
