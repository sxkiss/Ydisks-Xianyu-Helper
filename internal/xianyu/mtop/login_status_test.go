package mtop

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestRiskVerificationClassification 封装TestRiskVerificationClassification业务协调。
func TestRiskVerificationClassification(t *testing.T) {
	// ret 用于本次流程后续判断的ret
	ret := []string{"FAIL_SYS_USER_VALIDATE::用户校验失败"}
	if !isRiskVerificationRet(ret) {
		t.Fatal("FAIL_SYS_USER_VALIDATE 应识别为风控")
	}
	if isTokenExpiredRet(ret) {
		t.Fatal("FAIL_SYS_USER_VALIDATE 不应再被当作普通 token 过期")
	}
	// err 用于本次流程后续判断的err
	err := &RiskVerificationError{Ret: ret, VerificationURL: "https://passport.goofish.com/punish?x5secdata=1"}
	if !IsRiskVerificationErr(err) {
		t.Fatal("RiskVerificationError 应被识别")
	}
}

// TestRefreshTokenReturnsRiskVerificationError 封装TestRefresh令牌ReturnsRiskVerification错误业务协调。
func TestRefreshTokenReturnsRiskVerificationError(t *testing.T) {
	// srv 用于本次流程后续判断的srv
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "_m_h5_tk", Value: "newtk_1"})
		fmt.Fprint(w, `{"ret":["FAIL_SYS_USER_VALIDATE::用户校验失败"],"data":{"url":"https://passport.goofish.com/punish?x5secdata=1"}}`)
	}))
	defer srv.Close()

	// c 用于本次流程后续判断的c
	c := &ClientImpl{HTTPClient: srv.Client(), TokenURL: srv.URL}
	// res、err 用于本次流程后续判断的res、err
	res, err := c.RefreshTokenWithDeviceIDContext(context.Background(), "unb=123; _m_h5_tk=old_1", "device-1")
	if err == nil || !IsRiskVerificationErr(err) {
		t.Fatalf("应返回风控错误: res=%#v err=%v", res, err)
	}
	if res == nil || !strings.Contains(res.UpdatedCookies, "_m_h5_tk=newtk_1") {
		t.Fatalf("风控时仍应保留 Set-Cookie: %#v", res)
	}
	// riskErr 用于本次流程后续判断的riskErr
	var riskErr *RiskVerificationError
	if !strings.Contains(err.Error(), "x5secdata") {
		t.Fatalf("风控 URL 未进入错误信息: %v", riskErr)
	}
}

// TestCheckLoginStatusTokenRefreshed 封装TestCheck登录状态令牌Refreshed业务协调。
func TestCheckLoginStatusTokenRefreshed(t *testing.T) {
	// srv 用于本次流程后续判断的srv
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "_m_h5_tk", Value: "newtk_1"})
		fmt.Fprint(w, `{"ret":["FAIL_SYS_TOKEN_EXOIRED::令牌过期"],"data":{}}`)
	}))
	defer srv.Close()

	// c 用于本次流程后续判断的c
	c := &ClientImpl{HTTPClient: srv.Client(), LoginUserURL: srv.URL}
	// res、err 用于本次流程后续判断的res、err
	res, err := c.CheckLoginStatusContext(context.Background(), "unb=123; _m_h5_tk=old_1")
	if err != nil {
		t.Fatalf("CheckLoginStatusContext: %v", err)
	}
	if res.Status != LoginStatusTokenRefreshed {
		t.Fatalf("status=%s want %s", res.Status, LoginStatusTokenRefreshed)
	}
	if !strings.Contains(res.UpdatedCookies, "_m_h5_tk=newtk_1") {
		t.Fatalf("UpdatedCookies=%q", res.UpdatedCookies)
	}
}

// TestCheckLoginStatusNon2xxSessionStillDrivesRecovery 验证非 2xx 的 Session ret 仍进入登录状态机。
func TestCheckLoginStatusNon2xxSessionStillDrivesRecovery(t *testing.T) {
	// server 返回可操作的 Session 失效 ret，但使用非 2xx HTTP 状态模拟平台响应。
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"ret":["FAIL_SYS_SESSION_EXPIRED::Session过期"],"data":{}}`)
	}))
	defer server.Close()

	// client 保存注入本地测试端点的 MTOP 客户端。
	client := &ClientImpl{HTTPClient: server.Client(), LoginUserURL: server.URL}
	// result、err 保存登录状态检查结果及调用错误。
	result, err := client.CheckLoginStatusContext(context.Background(), consignCookies)
	if err != nil || result == nil || result.Status != LoginStatusSessionExpired {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

// TestCheckLoginStatusNon2xxGenericFailureRemainsHTTPError 验证没有可操作 ret 的非 2xx 仍保持 HTTP 错误。
func TestCheckLoginStatusNon2xxGenericFailureRemainsHTTPError(t *testing.T) {
	// server 返回无法驱动登录恢复的普通失败 ret。
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprint(w, `{"ret":["FAIL_SYS_GATEWAY::网关错误"],"data":{}}`)
	}))
	defer server.Close()

	// client 保存注入本地测试端点的 MTOP 客户端。
	client := &ClientImpl{HTTPClient: server.Client(), LoginUserURL: server.URL}
	// err 保存非 2xx 普通失败。
	_, err := client.CheckLoginStatusContext(context.Background(), consignCookies)
	if err == nil || !strings.Contains(err.Error(), "HTTP 502") {
		t.Fatalf("err=%v", err)
	}
}

// TestCheckLoginStatusFailureMessageIncludesPlatformReason 验证登录态失败状态保留平台错误码和原因，供前端与日志排障。
func TestCheckLoginStatusFailureMessageIncludesPlatformReason(t *testing.T) {
	// srv 返回普通业务失败，不触发 Token 自动恢复状态。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"ret":["FAIL_BIZ_LOGIN_BLOCKED::账号暂不可用"],"data":{}}`)
	}))
	defer srv.Close()
	// client 仅请求本地测试端点。
	client := &ClientImpl{HTTPClient: srv.Client(), LoginUserURL: srv.URL}
	// result、err 保存登录态检查结果及调用错误。
	result, err := client.CheckLoginStatusContext(context.Background(), "unb=123; _m_h5_tk=token_1")
	if err != nil || result == nil || result.Status != LoginStatusFailed {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if !strings.Contains(result.Message, "FAIL_BIZ_LOGIN_BLOCKED") || !strings.Contains(result.Message, "账号暂不可用") {
		t.Fatalf("message=%q 未包含平台失败原因", result.Message)
	}
}

// TestClassifyLoginStatusRisk 封装TestClassify登录状态Risk业务协调。
func TestClassifyLoginStatusRisk(t *testing.T) {
	// status、msg 用于本次流程后续判断的status、msg
	status, msg := classifyLoginStatus([]string{"RGV587_ERROR::哎哟喂，被挤爆啦"}, false)
	if status != LoginStatusRiskRequired || msg == "" {
		t.Fatalf("status=%s msg=%q", status, msg)
	}
}

// TestClassifyLoginStatusCoversTokenAndFallbackBranches 验证登录态分类器对令牌为空、Session 失效、令牌过期和未知返回的完整映射。
func TestClassifyLoginStatusCoversTokenAndFallbackBranches(t *testing.T) {
	// cases 保存平台返回、Cookie 更新状态和期望分类，覆盖不触发网络的纯业务分支。
	cases := []struct {
		name          string
		ret           []string
		cookieUpdated bool
		want          string
	}{
		{name: "token empty", ret: []string{"TOKEN_EMPTY::令牌为空"}, want: LoginStatusTokenEmpty},
		{name: "session expired", ret: []string{"FAIL_SYS_SESSION_EXPIRED::Session过期"}, want: LoginStatusSessionExpired},
		{name: "sid invalid", ret: []string{"SID_INVALID::会话无效"}, want: LoginStatusSessionExpired},
		{name: "auth reject", ret: []string{"AUTH_REJECT::认证拒绝"}, want: LoginStatusSessionExpired},
		{name: "need login", ret: []string{"NEED_LOGIN::需要登录"}, want: LoginStatusSessionExpired},
		{name: "token expired without cookie", ret: []string{"FAIL_SYS_TOKEN_EXPIRED::令牌过期"}, want: LoginStatusFailed},
		{name: "unknown", ret: []string{"FAIL_UNKNOWN::未知错误"}, want: LoginStatusFailed},
		{name: "token expired with cookie", ret: []string{"FAIL_SYS_TOKEN_EXPIRED::令牌过期"}, cookieUpdated: true, want: LoginStatusTokenRefreshed},
	}
	// testCase 表示当前登录态分类场景及其平台输入。
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			// status、message 保存当前平台返回对应的分类结果。
			status, message := classifyLoginStatus(testCase.ret, testCase.cookieUpdated)
			if status != testCase.want || message == "" {
				t.Fatalf("status=%q message=%q want=%q", status, message, testCase.want)
			}
		})
	}
}
