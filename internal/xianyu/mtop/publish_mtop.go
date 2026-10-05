// Package mtop: 发布流程使用的 MTOP 请求、响应和 Token 失败重试。
package mtop

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"xianyu-go/internal/xianyu/protocol"
)

// callMTop 调用发布相关 MTOP 接口；仅签名 Token 过期会刷新 Cookie 后重试。
func (c *ClientImpl) callMTop(ctx context.Context, cookiesStr, endpoint, api, version, spmCnt, spmPre, logID string, data any) (map[string]any, string, error) {
	// currentCookies 保存本轮发布接口请求使用的 Cookie，不向日志或错误输出。
	currentCookies := cookiesStr
	// lastFailure 保存 Token 重试耗尽时最后一次失败原因。
	var lastFailure error
	for // attempt 表示含首次请求在内的重试序号。
	attempt := 0; attempt < 4; attempt++ {
		// decoded、updated、status、err 保存一次发布 MTOP 请求的结果。
		decoded, updated, status, err := c.callMTopOnce(ctx, currentCookies, endpoint, api, version, spmCnt, spmPre, logID, data)
		if err != nil {
			if !IsMTopTokenExpiredErr(err) {
				return nil, updated, err
			}
			lastFailure = err
		} else {
			// ret 保存当前响应的业务返回标记，仅失败分支参与分类。
			ret := retFromDecoded(decoded)
			if hasMTopSuccess(ret) || !isTokenExpiredRet(ret) {
				return decoded, updated, nil
			}
			lastFailure = c.mtopResponseFailure(api, status, ret, "平台 ret 表示签名 Token 过期")
		}
		if attempt == 3 {
			break
		}
		// previousCookies 用于判断响应是否已经提供新的签名 Cookie。
		previousCookies := currentCookies
		if updated != "" {
			currentCookies = updated
		}
		if // session 用于读取响应刚吸收的最新 Cookie。
		session := cookieSessionFromContext(ctx); session != nil {
			currentCookies, _, _ = session.State()
		}
		if !mtopTokenCookieChanged(previousCookies, currentCookies) {
			// refreshed、refreshErr 保存主动刷新 MTOP 签名 Token 的结果及错误。
			refreshed, refreshErr := c.RefreshTokenContext(ctx, currentCookies)
			if refreshErr != nil {
				return nil, currentCookies, fmt.Errorf("%s Token 过期且刷新失败: %w", api, refreshErr)
			}
			currentCookies = refreshed.UpdatedCookies
		}
		if // sleepErr 表示两次平台请求之间等待期间的取消错误。
		sleepErr := sleepCtx(ctx, MTopRetryGap); sleepErr != nil {
			return nil, currentCookies, sleepErr
		}
	}
	return nil, currentCookies, fmt.Errorf("%s Token 重试失败: %w", api, lastFailure)
}

// callMTopOnce 执行一次发布相关 MTOP 请求；成功响应的解析方式保持原有逻辑。
func (c *ClientImpl) callMTopOnce(ctx context.Context, cookiesStr, endpoint, api, version, spmCnt, spmPre, logID string, data any) (map[string]any, string, int, error) {
	// hc 用于本次流程后续判断的hc
	hc := c.httpClient()
	// rawData 用于本次流程后续判断的原始数据
	rawData, _ := json.Marshal(data)
	// dataVal 用于本次流程后续判断的数据Val
	dataVal := string(rawData)
	// signingCookies、requestCookies 用于本次流程后续判断的signingCookies、requestCookies
	signingCookies, requestCookies := mtopRequestCookies(ctx, cookiesStr, "https://www.goofish.com/", endpoint)
	// t 用于本次流程后续判断的t
	t := strconv.FormatInt(time.Now().UnixMilli(), 10)
	// sign 用于本次流程后续判断的sign
	sign := protocol.GenerateSign(t, protocol.SignToken(signingCookies), dataVal)
	// query 用于本次流程后续判断的查询
	query := buildMTopQuery(api, version, t, sign, spmCnt, spmPre, logID)
	// body 用于本次流程后续判断的请求体
	body := "data=" + url.QueryEscape(dataVal)
	// req、err 用于本次流程后续判断的req、err
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"?"+query, strings.NewReader(body))
	if err != nil {
		return nil, cookiesStr, 0, err
	}
	setCommonHeaders(req, requestCookies)
	// resp、err 用于本次流程后续判断的resp、err
	resp, err := hc.Do(req)
	if err != nil {
		return nil, cookiesStr, 0, fmt.Errorf("%s 请求失败: %w", api, err)
	}
	defer resp.Body.Close()
	// updated 用于本次流程后续判断的updated
	updated := absorbMTopResponseCookies(ctx, cookiesStr, resp)
	// raw、err 用于本次流程后续判断的raw、err
	raw, err := readMTopBody(resp)
	if err != nil {
		return nil, updated, resp.StatusCode, c.mtopResponseFailureWithCause(api, resp.StatusCode, nil, "读取响应失败", err)
	}
	// decoded 用于本次流程后续判断的decoded
	var decoded map[string]any
	if // err 用于本次流程后续判断的err
	err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, updated, resp.StatusCode, c.mtopResponseFailureWithCause(api, resp.StatusCode, nil, "JSON 解析失败", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, updated, resp.StatusCode, c.mtopResponseFailure(api, resp.StatusCode, retFromDecoded(decoded), "HTTP 状态异常")
	}
	return decoded, updated, resp.StatusCode, nil
}
