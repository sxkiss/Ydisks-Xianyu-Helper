package mtop

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"xianyu-go/internal/xianyu/protocol"
)

// LoginUserAPI 用于本次流程后续判断的登录用户API
const LoginUserAPI = "https://h5api.m.goofish.com/h5/mtop.taobao.idlemessage.pc.loginuser.get/1.0/"

// LoginStatusSuccess 用于本次流程后续判断的登录状态Success
const (
	LoginStatusSuccess        = "success"
	LoginStatusTokenRefreshed = "token_refreshed"
	LoginStatusSessionExpired = "session_expired"
	LoginStatusTokenEmpty     = "token_empty"
	LoginStatusRiskRequired   = "risk_required"
	LoginStatusFailed         = "failed"
)

// LoginStatusResult 是 loginuser.get 的登录态检查结果。
type LoginStatusResult struct {
	Status          string
	Ret             []string
	UpdatedCookies  string
	VerificationURL string
	Message         string
}

// CheckLoginStatusContext 使用 ctx 和 cookiesStr 核验登录态；Token 失效只调用 c 的 Token 刷新并重试一次。
// 返回平台状态或刷新错误，Session 续期由上层仅在明确会话失效时决定。
func (c *ClientImpl) CheckLoginStatusContext(ctx context.Context, cookiesStr string) (*LoginStatusResult, error) {
	// result、err 保存首次登录态检查结果；已有签名 Cookie 轮换时无需再发刷新请求。
	result, err := c.checkLoginStatusOnce(ctx, cookiesStr)
	if err != nil || result == nil || result.Status == LoginStatusTokenRefreshed || result.Status == LoginStatusRiskRequired || result.Status == LoginStatusSessionExpired || !isOfficialTokenRetryRet(result.Ret) {
		return result, err
	}
	// refreshed、refreshErr 保存现有 Token 刷新方法的结果，不调用账号恢复或登录接口。
	refreshed, refreshErr := c.RefreshTokenContext(ctx, result.UpdatedCookies)
	if refreshed != nil {
		result.UpdatedCookies = refreshed.UpdatedCookies
	}
	if refreshErr != nil {
		return result, refreshErr
	}
	return c.checkLoginStatusOnce(ctx, result.UpdatedCookies)
}

// checkLoginStatusOnce 使用 ctx 和 cookiesStr 执行 c 的单次登录态检查，返回状态并吸收响应 Cookie。
func (c *ClientImpl) checkLoginStatusOnce(ctx context.Context, cookiesStr string) (*LoginStatusResult, error) {
	if // session 提供调用方在本次请求链中持有的最新 Cookie 快照。
	session := cookieSessionFromContext(ctx); session != nil {
		cookiesStr, _, _ = session.State()
	}
	// hc 将单次登录态 HTTP 请求限制在二十秒内，仍接受 ctx 提前取消。
	hc := c.httpClientWithTimeout(20 * time.Second)
	// loginURL 允许本地测试注入端点，生产默认使用登录态检查接口。
	loginURL := c.LoginUserURL
	if loginURL == "" {
		loginURL = LoginUserAPI
	}
	// signingCookies、requestCookies 分别提供页面可见签名输入和请求作用域 Cookie，避免混用 HttpOnly 凭证。
	signingCookies, requestCookies := mtopRequestCookies(ctx, cookiesStr, mtopDocumentURL, loginURL)
	// t 是当前请求签名所绑定的毫秒时间戳。
	t := strconv.FormatInt(time.Now().UnixMilli(), 10)
	// dataVal 是登录态检查要求的空对象，签名与请求正文必须使用相同内容。
	dataVal := `{}`
	// query 保存签名、时间戳和平台协议参数，不能写入日志。
	query := buildLoginStatusQuery(t, protocol.GenerateSign(t, protocol.SignToken(signingCookies), dataVal))

	// req、err 保存受 ctx 控制的请求及端点构造错误。
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, loginURL+"?"+query, strings.NewReader("data=%7B%7D"))
	if err != nil {
		return nil, err
	}
	setCommonHeaders(req, requestCookies)
	req.Header.Set("Referer", mtopDocumentURL)

	// resp、err 保存平台响应与网络错误；响应体由当前函数负责关闭。
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("loginuser.get 请求失败: %w", err)
	}
	defer resp.Body.Close()
	// updated 在解析正文前吸收响应 Cookie，解析失败也不丢失平台更新。
	updated := absorbMTopResponseCookies(ctx, cookiesStr, resp)
	// raw、err 保存受限读取的响应正文及读取错误，正文不输出到诊断信息。
	raw, err := readMTopBody(resp)
	if err != nil {
		return nil, c.mtopResponseFailureWithCause("loginuser.get", resp.StatusCode, nil, "读取响应失败", err)
	}

	// payload 只解析状态码和验证地址，不读取无关账号资料。
	var payload struct {
		// Ret 保存平台业务状态，供 Token、Session 与风控分类使用。
		Ret []string `json:"ret"`
		// Data 是平台返回的验证信息容器。
		Data struct {
			// URL 保留平台验证地址，交给现有风控处理流程。
			URL string `json:"url"`
		} `json:"data"`
	}
	if // err 表示响应不满足登录态 JSON 协议，保留错误链但不泄露正文。
	err := json.Unmarshal(raw, &payload); err != nil {
		return nil, c.mtopResponseFailureWithCause("loginuser.get", resp.StatusCode, nil, "JSON 解析失败", err)
	}
	// failure 保存登录态失败响应的统一诊断；成功和已自动吸收新 Cookie 的恢复状态不改原消息。
	var failure error
	if !hasMTopSuccess(payload.Ret) {
		// failure 仅用于记录非成功登录态响应；登录态状态机仍沿用原有返回值。
		failure = c.mtopResponseFailure("loginuser.get", resp.StatusCode, payload.Ret, "平台 ret 未包含 SUCCESS")
	}
	// status、msg 保存业务状态及安全提示，只有签名 Cookie 轮换才可认定 Token 已刷新。
	status, msg := classifyLoginStatus(payload.Ret, mtopTokenCookieChanged(cookiesStr, updated))
	if (resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices) && !loginStatusCanDriveRecovery(status) && !isOfficialTokenRetryRet(payload.Ret) {
		return nil, c.mtopResponseFailure("loginuser.get", resp.StatusCode, payload.Ret, "HTTP 状态异常")
	}
	if failure != nil && status != LoginStatusTokenRefreshed {
		msg = failure.Error()
	}
	return &LoginStatusResult{
		Status:          status,
		Ret:             payload.Ret,
		UpdatedCookies:  updated,
		VerificationURL: payload.Data.URL,
		Message:         msg,
	}, nil
}

// loginStatusCanDriveRecovery 判断非 2xx 响应中的登录状态是否仍包含可执行的恢复动作。
func loginStatusCanDriveRecovery(status string) bool {
	switch status {
	case LoginStatusTokenRefreshed, LoginStatusSessionExpired, LoginStatusTokenEmpty, LoginStatusRiskRequired:
		return true
	default:
		return false
	}
}

// buildLoginStatusQuery 封装build登录状态查询业务协调。
func buildLoginStatusQuery(t, sign string) string {
	// parts 用于本次流程后续判断的parts
	parts := [][2]string{
		{"jsv", "2.7.2"},
		{"appKey", protocol.SignAppKey},
		{"t", t},
		{"sign", sign},
		{"v", "1.0"},
		{"type", "originaljson"},
		{"accountSite", "xianyu"},
		{"dataType", "json"},
		{"timeout", "20000"},
		{"api", "mtop.taobao.idlemessage.pc.loginuser.get"},
		{"sessionOption", "AutoLoginOnly"},
		{"spm_cnt", "a21ybx.im.0.0"},
		{"spm_pre", "a21ybx.item.want.1.12523da6waCtUp"},
		{"log_id", "12523da6waCtUp"},
	}
	// b 用于本次流程后续判断的b
	var b strings.Builder
	// i、p 表示当前遍历过程中的i、p
	for i, p := range parts {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(p[0])
		b.WriteByte('=')
		b.WriteString(p[1])
	}
	return b.String()
}

// classifyLoginStatus 根据 ret 与签名轮换标记 cookieUpdated 返回状态和提示，Token 空值不代表 Session 失效。
func classifyLoginStatus(ret []string, cookieUpdated bool) (string, string) {
	if hasMTopSuccess(ret) {
		return LoginStatusSuccess, "登录状态正常"
	}
	if isRiskVerificationRet(ret) {
		return LoginStatusRiskRequired, "闲鱼要求安全验证"
	}
	// retStr 合并平台返回标记，供状态判定和未知状态诊断使用。
	retStr := strings.Join(ret, " ")
	// upperRetStr 统一官方 SDK 使用的英文登录态错误码大小写，避免不同网关大小写导致分类漂移。
	upperRetStr := strings.ToUpper(retStr)
	switch {
	case strings.Contains(upperRetStr, "TOKEN_EMPTY") || strings.Contains(retStr, "令牌为空"):
		if cookieUpdated {
			return LoginStatusTokenRefreshed, "令牌已刷新"
		}
		return LoginStatusTokenEmpty, "令牌为空，需要刷新 Token"
	case strings.Contains(upperRetStr, "SESSION_EXPIRED") ||
		strings.Contains(upperRetStr, "SID_INVALID") ||
		strings.Contains(upperRetStr, "AUTH_REJECT") ||
		strings.Contains(upperRetStr, "NEED_LOGIN") ||
		strings.Contains(retStr, "Session过期"):
		return LoginStatusSessionExpired, "Session过期，需要重新登录"
	case strings.Contains(upperRetStr, "TOKEN_EXOIRED") || strings.Contains(upperRetStr, "TOKEN_EXPIRED"):
		if cookieUpdated {
			return LoginStatusTokenRefreshed, "令牌已刷新"
		}
		return LoginStatusFailed, "令牌过期但未获取到新Cookie"
	default:
		return LoginStatusFailed, retStr
	}
}
