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

// FreeShippingContext 调用砍价订单的免拼发货接口；orderID 为订单号，itemID 与 buyerID 必须是平台数字标识。
// 返回平台是否明确成功、原始 ret、可能轮换后的 Cookie 与请求错误；Token 过期仅在本 MTOP 调用内刷新重试，不触发账号级凭证恢复。
func (c *ClientImpl) FreeShippingContext(ctx context.Context, cookiesStr, orderID, itemID, buyerID string) (ok bool, ret []string, updatedCookies string, err error) {
	// currentCookies 保存本次免拼请求实际使用的明文凭证，只在请求与响应 Cookie 合并期间存在。
	currentCookies := cookiesStr
	if // session 用于优先读取调用方维护的权威 Cookie 会话。
	session := cookieSessionFromContext(ctx); session != nil {
		currentCookies, _, _ = session.State()
	}
	// lastRet 保存 Token 重试耗尽时最后一次平台返回，便于上层保持业务错误分类。
	var lastRet []string
	// transientSyncFailure 标记最近一次失败是否只是团购实例尚未完成平台侧同步。
	transientSyncFailure := false
	for // attempt 是包含首次请求在内的免拼发货尝试序号。
	attempt := 0; attempt < 4; attempt++ {
		// transientSyncFailure 只描述当前这一轮响应，不能把上一轮的同步错误带入 Token 分支。
		transientSyncFailure = false
		// previousCookies 保存本次请求前签名 Cookie，用于判断响应是否已轮换 Token。
		previousCookies := currentCookies
		// ok、ret、updated、requestErr 保存单次免拼请求的业务结果、Cookie 更新与传输错误。
		ok, ret, updated, requestErr := c.freeShippingOnce(ctx, currentCookies, orderID, itemID, buyerID)
		if requestErr != nil {
			if !IsMTopTokenExpiredErr(requestErr) {
				return false, ret, currentCookies, requestErr
			}
			lastRet = mtopErrorRet(requestErr)
		} else {
			lastRet = ret
			if updated != "" {
				currentCookies = updated
			}
			if ok {
				if attempt > 0 {
					c.logInfo("免拼发货重试成功", "order_id", orderID, "attempt", attempt+1)
				} else {
					c.logInfo("免拼发货成功", "order_id", orderID, "attempt", attempt+1)
				}
				return true, ret, currentCookies, nil
			}
			if isFreeShippingTransientSyncRet(ret) {
				// 暂不构造并记录最终错误；平台团购实例同步完成后，下一次原请求仍可安全重试。
				transientSyncFailure = true
				c.logInfo("免拼发货遇到团购实例暂未同步，等待后重试", "order_id", orderID, "attempt", attempt+1, "next_attempt", attempt+2, "retry_limit", 4, "ret", formatMTopRet(ret))
			} else {
				transientSyncFailure = false
				requestErr = c.mtopResponseFailure("免拼发货接口", http.StatusOK, ret, "平台 ret 未包含 SUCCESS")
				// kind、classified 保存普通业务拒绝的分类结果；业务拒绝是确定结果，不能升级为外部动作未知。
				if kind, classified := MTopErrorKindOf(requestErr); classified && kind == MTopErrorBusiness {
					return false, ret, currentCookies, nil
				}
				if !IsMTopTokenExpiredErr(requestErr) {
					return false, ret, currentCookies, requestErr
				}
			}
		}
		if updated != "" {
			currentCookies = updated
		}
		if attempt == 3 {
			break
		}
		if transientSyncFailure {
			// sleepErr 保存团购实例同步重试等待期间的取消错误。
			if sleepErr := sleepCtx(ctx, MTopRetryGap); sleepErr != nil {
				return false, ret, currentCookies, sleepErr
			}
			continue
		}
		// MTop 通常会在 Token 过期响应中下发新签名 Cookie；未下发时才主动刷新一次。
		if !mtopTokenCookieChanged(previousCookies, currentCookies) {
			// refreshed、refreshErr 保存主动刷新签名 Token 的响应及失败原因。
			refreshed, refreshErr := c.RefreshTokenContext(ctx, currentCookies)
			if refreshErr != nil {
				return false, ret, currentCookies, fmt.Errorf("免拼发货 Token 过期且刷新失败: %w", refreshErr)
			}
			if refreshed.UpdatedCookies != "" {
				currentCookies = refreshed.UpdatedCookies
			}
		}
		// sleepErr 保存下一次请求前等待被取消的原因；调用方取消时不得继续发起平台请求。
		if sleepErr := sleepCtx(ctx, MTopRetryGap); sleepErr != nil {
			return false, ret, currentCookies, sleepErr
		}
	}
	if transientSyncFailure {
		c.logInfo("免拼发货团购实例同步重试耗尽，保留系统失败结果", "order_id", orderID, "attempts", 4, "ret", formatMTopRet(lastRet))
		return false, lastRet, currentCookies, fmt.Errorf("免拼发货接口团购实例同步重试失败: %w", c.mtopResponseFailure("免拼发货接口", http.StatusOK, lastRet, "团购实例同步重试次数已耗尽"))
	}
	return false, lastRet, currentCookies, fmt.Errorf("免拼发货接口 Token 重试失败: %w", c.mtopResponseFailure("免拼发货接口", http.StatusOK, lastRet, "重试次数已耗尽"))
}

// isFreeShippingTransientSyncRet 判断免拼接口是否返回了可等待团购实例同步完成后重试的错误。
func isFreeShippingTransientSyncRet(ret []string) bool {
	// value 表示当前待判断的平台 ret 条目。
	for _, value := range ret {
		// code 是去除大小写差异后的平台错误码，允许带标准的原因分隔符。
		code := strings.ToUpper(strings.TrimSpace(value))
		if code == "GROUPON_INSTANCE_QUERY_ERROR" || strings.HasPrefix(code, "GROUPON_INSTANCE_QUERY_ERROR::") {
			return true
		}
	}
	return false
}

// freeShippingOnce 发送一次砍价订单免拼发货请求；调用方负责 Token 过期时的 Cookie 刷新与重试。
func (c *ClientImpl) freeShippingOnce(ctx context.Context, cookiesStr, orderID, itemID, buyerID string) (ok bool, ret []string, updatedCookies string, err error) {
	// itemNumber、itemErr 把商品标识限制为平台接口要求的无符号数字，避免把未经验证的值拼入 JSON。
	itemNumber, itemErr := strconv.ParseUint(strings.TrimSpace(itemID), 10, 64)
	if itemErr != nil {
		return false, nil, cookiesStr, fmt.Errorf("免拼发货商品ID不是数字: %w", itemErr)
	}
	// buyerNumber、buyerErr 把买家标识限制为平台接口要求的无符号数字。
	buyerNumber, buyerErr := strconv.ParseUint(strings.TrimSpace(buyerID), 10, 64)
	if buyerErr != nil {
		return false, nil, cookiesStr, fmt.Errorf("免拼发货买家ID不是数字: %w", buyerErr)
	}
	// freeShippingURL 保存当前请求的免拼端点；生产环境空值固定使用官方 MTOP 地址。
	freeShippingURL := c.FreeShippingURL
	if freeShippingURL == "" {
		freeShippingURL = FreeShippingAPI
	}
	// signingCookies、requestCookies 分别是参与签名与按请求 URL 作用域筛选后的 Cookie，均不得记录到日志。
	signingCookies, requestCookies := mtopRequestCookies(ctx, cookiesStr, "https://www.goofish.com/", freeShippingURL)
	// token 是仅在当前请求签名中使用的明文 Token，禁止传播到错误或日志。
	token := protocol.SignToken(signingCookies)
	// timestamp 是 MTOP 签名使用的 Unix 毫秒时间。
	timestamp := strconv.FormatInt(time.Now().UnixMilli(), 10)
	// payload 保持平台免拼接口的精确字段形状：订单号是字符串，商品和买家是数字标识。
	payload := struct {
		// OrderID 是砍价订单的业务订单号。
		OrderID string `json:"bizOrderId"`
		// ItemID 是砍价活动对应的数字商品标识。
		ItemID uint64 `json:"itemId"`
		// BuyerID 是参与砍价活动的数字买家标识。
		BuyerID uint64 `json:"buyerId"`
	}{OrderID: strings.TrimSpace(orderID), ItemID: itemNumber, BuyerID: buyerNumber}
	// dataBytes、marshalErr 保存参与签名和表单提交的 JSON 负载及其编码失败。
	dataBytes, marshalErr := json.Marshal(payload)
	if marshalErr != nil {
		return false, nil, cookiesStr, fmt.Errorf("构造免拼发货请求数据失败: %w", marshalErr)
	}
	// dataVal 是参与签名并作为表单 data 字段提交的 JSON 文本。
	dataVal := string(dataBytes)
	// sign 是当前时间、签名 Token 与免拼 JSON 负载生成的 MTOP 签名，禁止记录。
	sign := protocol.GenerateSign(timestamp, token, dataVal)
	// query 保存免拼 MTOP 请求所需的固定元信息与动态签名。
	query := buildFreeShippingQuery(timestamp, sign)
	// body 保存 application/x-www-form-urlencoded 形式的 data 字段。
	body := "data=" + url.QueryEscape(dataVal)
	// req、requestErr 保存受调用方 Context 取消控制的 HTTP 请求及构造失败。
	req, requestErr := http.NewRequestWithContext(ctx, http.MethodPost, freeShippingURL+"?"+query, strings.NewReader(body))
	if requestErr != nil {
		return false, nil, cookiesStr, requestErr
	}
	setCommonHeaders(req, requestCookies)
	// hc 是带统一安全日志的 HTTP 客户端副本，保留调用方注入的传输与超时。
	hc := c.httpClient()
	// resp、requestErr 保存平台响应与网络错误；网络层不能假定远端没有执行副作用。
	resp, requestErr := hc.Do(req)
	if requestErr != nil {
		return false, nil, cookiesStr, fmt.Errorf("免拼发货请求失败: %w", requestErr)
	}
	defer resp.Body.Close()
	// updated 保存平台响应 Set-Cookie 合并后的扁平 Cookie，并同步更新 Context 中的权威会话。
	updated := absorbMTopResponseCookies(ctx, cookiesStr, resp)
	// raw、readErr 保存受长度限制的响应正文及读取错误，正文不向错误文本回显。
	raw, readErr := readMTopBody(resp)
	if readErr != nil {
		return false, nil, updated, c.mtopResponseFailureWithCause("免拼发货接口", resp.StatusCode, nil, "读取响应失败", readErr)
	}
	// response 只解析 MTOP 的 ret 字段，用于统一错误分类与幂等判断。
	var response struct {
		// Ret 是平台成功、已发货或业务失败的安全代码列表。
		Ret []string `json:"ret"`
	}
	// decodeErr 保存响应 JSON 解码失败，底层正文不会泄露到调用链。
	if decodeErr := json.Unmarshal(raw, &response); decodeErr != nil {
		return false, nil, updated, c.mtopResponseFailureWithCause("免拼发货接口", resp.StatusCode, nil, "JSON 解析失败", decodeErr)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		// failure 保存非 2xx MTOP 响应的统一分类；确定业务 ret 仍按正常业务结果返回。
		failure := c.mtopResponseFailure("免拼发货接口", resp.StatusCode, response.Ret, "HTTP 状态异常")
		if isMTopBusinessRet(response.Ret) {
			return false, response.Ret, updated, nil
		}
		return false, response.Ret, updated, failure
	}
	return hasMTopSuccess(response.Ret), response.Ret, updated, nil
}

// buildFreeShippingQuery 构造砍价订单免拼发货的固定 MTOP 查询参数；timestamp 与 sign 由当前请求生成。
func buildFreeShippingQuery(timestamp, sign string) string {
	// values 保存由 url.Values 编码的 MTOP 查询参数，避免手工拼接遗漏编码。
	values := url.Values{
		"jsv":           {"2.7.2"},
		"appKey":        {protocol.SignAppKey},
		"t":             {timestamp},
		"sign":          {sign},
		"v":             {"1.0"},
		"type":          {"originaljson"},
		"accountSite":   {"xianyu"},
		"dataType":      {"json"},
		"timeout":       {"20000"},
		"api":           {"mtop.idle.groupon.activity.seller.freeshipping"},
		"sessionOption": {"AutoLoginOnly"},
	}
	return values.Encode()
}
