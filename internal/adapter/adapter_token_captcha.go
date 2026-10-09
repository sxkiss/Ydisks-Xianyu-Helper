package adapter

import (
	"context"
	"fmt"
	"time"

	"xianyu-go/internal/db"
	"xianyu-go/internal/engine"
	"xianyu-go/internal/xianyu/cookierefresh"
	"xianyu-go/internal/xianyu/mtop"
)

// openTokenCaptchaRiskLog 创建一次 token 滑块风控日志，返回可后续更新的日志 ID。
func (a *Adapter) openTokenCaptchaRiskLog(ctx context.Context, cookieID, verificationURL string) int64 {
	// logID 是新增风控日志的标识；写入失败时保持 0 并继续，不能阻断滑块恢复。
	var logID int64
	if a.store != nil && a.store.RiskLogs != nil {
		if // id、err 用于本次流程后续判断的id、err
		id, err := a.store.RiskLogs.Add(ctx, db.RiskControlLog{
			CookieID:         cookieID,
			EventType:        "slider_captcha",
			EventDescription: "触发场景: Token刷新, URL: " + verificationURL,
			ProcessingStatus: "processing",
		}); err == nil {
			logID = id
		} else {
			a.logger.Warn("记录风控日志失败", "account", cookieID, "err", err)
		}
	}
	return logID
}

// updateTokenCaptchaRiskLog 用统一耗时字段更新一次 token 滑块风控日志。
func (a *Adapter) updateTokenCaptchaRiskLog(ctx context.Context, logID int64, start time.Time, engineName string, patch db.RiskControlLog, result string) {
	if a.store == nil || a.store.RiskLogs == nil {
		return
	}
	// record 是合并处理结果与耗时后的最终日志。
	record := patch
	record.ProcessingResult = result
	record.CaptchaEngine = engineName
	record.DurationMS = time.Since(start).Milliseconds()
	_ = a.store.RiskLogs.Update(ctx, logID, record)
}

// tokenCaptchaPersistInput 是 token 滑块成功后写回凭证所需的入参集合。
type tokenCaptchaPersistInput struct {
	CookieID      string
	CookieStr     string
	NewCookies    string
	MetadataJSON  string
	CaptchaEngine string
	Headless      bool
	RemoteHandled bool
	Start         time.Time
	LogID         int64
}

// persistTokenCaptchaResult 读取滑块后的完整 Cookie Jar、更新登录凭证并收口风控日志。
func (a *Adapter) persistTokenCaptchaResult(ctx context.Context, in tokenCaptchaPersistInput) (*mtop.RefreshResult, bool) {
	// cookieSnapshot 用于本次流程后续判断的登录凭证Snapshot
	var cookieSnapshot []cookierefresh.BrowserCookie
	// snapshotComplete 用于本次流程后续判断的snapshotComplete
	snapshotComplete := false
	// newCookies 可能在读取浏览器 profile 快照后被替换为完整 Cookie 字符串。
	newCookies := in.NewCookies
	if !in.RemoteHandled {
		if // reader、ok 用于本次流程后续判断的reader、ok
		reader, ok := a.browser.(browserTokenCaptchaSnapshotReader); ok {
			// profileCookies、profileSnapshot、readErr 用于本次流程后续判断的profileCookies、profileSnapshot、readErr
			profileCookies, profileSnapshot, readErr := reader.TokenCaptchaCookieSnapshot(ctx, in.CookieID, in.Headless)
			if readErr != nil {
				a.logger.Warn("读取滑块验证后完整 Cookie Jar 失败，回退 Go 快照合并", "account", in.CookieID, "err", readErr)
			} else {
				cookieSnapshot = cookierefresh.NormalizeSnapshot(profileSnapshot)
				if cookieSnapshot == nil {
					cookieSnapshot = []cookierefresh.BrowserCookie{}
				}
				snapshotComplete = true
				newCookies = profileCookies
			}
		}
	}
	if !snapshotComplete {
		if // existing、complete 用于本次流程后续判断的existing、complete
		existing, complete := cookierefresh.SnapshotFromMetadataOK(in.MetadataJSON); complete {
			cookieSnapshot = cookierefresh.ReconcileSnapshotWithCookieString(existing, newCookies)
			snapshotComplete = true
		}
	}
	// updatedMetadata 用于本次流程后续判断的updatedMetadata
	updatedMetadata := cookierefresh.MetadataWithoutSnapshot(in.MetadataJSON)
	if snapshotComplete {
		updatedMetadata = cookierefresh.MetadataWithSnapshot(in.MetadataJSON, cookieSnapshot)
	}
	if // err 用于本次流程后续判断的err
	err := a.store.Cookies.UpdateRenewalCookie(ctx, in.CookieID, newCookies, updatedMetadata, time.Now().Unix()); err != nil {
		a.logger.Warn("保存 token 风控恢复 Cookie 失败", "account", in.CookieID, "err", err)
		a.updateTokenCaptchaRiskLog(ctx, in.LogID, in.Start, in.CaptchaEngine, db.RiskControlLog{
			ProcessingStatus: "error",
			ErrorMessage:     err.Error(),
		}, "滑块完成但保存 Cookie 失败")
		return nil, false
	}
	if a.store.Tokens != nil {
		_ = a.store.Tokens.Clear(ctx, in.CookieID)
	}
	a.updateTokenCaptchaRiskLog(ctx, in.LogID, in.Start, in.CaptchaEngine, db.RiskControlLog{
		ProcessingStatus: "success",
	}, fmt.Sprintf("token 风控滑块验证成功（%s），已更新登录凭证，耗时: %.2f秒", in.CaptchaEngine, time.Since(in.Start).Seconds()))
	a.OnAccountEvent(ctx, in.CookieID, engine.EventSecurityVerification, engine.AlertLevelInfo,
		"token 风控验证已自动恢复", "系统已完成验证并更新登录凭证。")
	return &mtop.RefreshResult{
		UpdatedCookies:         newCookies,
		CookieSnapshot:         cookieSnapshot,
		CookieSnapshotComplete: snapshotComplete,
		CookieStateChanged:     newCookies != in.CookieStr || snapshotComplete,
	}, true
}

// solveTokenCaptcha 优先用远程服务过滑块，不可用时回退本机浏览器自动化。
// 返回更新后的 Cookie 串、实际使用的引擎名与失败原因。
func (a *Adapter) solveTokenCaptcha(ctx context.Context, cookieID, cookieStr, verificationURL, deviceID string, headless bool) (string, string, error) {
	// provider 向验证码请求器索取新的验证 URL，供滑块重试使用。
	provider := func(runCtx context.Context, currentCookies string) (string, bool, string, error) {
		if a.captchaReq == nil {
			return "", false, "", nil
		}
		// res、err 用于本次流程后续判断的res、err
		res, err := a.captchaReq.RequestFreshCaptchaURLContext(runCtx, currentCookies, deviceID)
		if err != nil || res == nil {
			return "", false, "", err
		}
		return res.VerificationURL, res.TokenOK, res.UpdatedCookies, nil
	}
	// captchaEngine 用于本次流程后续判断的captchaEngine
	captchaEngine := "playwright"
	if // remoteConfig 用于本次流程后续判断的remote配置
	remoteConfig := a.loadRemoteCaptchaConfig(ctx, cookieID); remoteConfig != nil {
		newCookies, remoteHandled, err := solveRemoteCaptcha(
			ctx, newRemoteCaptchaHTTPClient(), *remoteConfig,
			cookieID, verificationURL, cookieStr, deviceID, provider,
		)
		if remoteHandled {
			return newCookies, "remote", nil
		}
		if err != nil {
			a.logger.Warn("远程过滑块不可用，回退本机逻辑", "account", cookieID, "err", err)
		}
	}
	// br、ok 用于本次流程后续判断的br、ok
	br, ok := a.browser.(browserTokenCaptchaRecoverer)
	if a.browser == nil || !ok {
		a.OnAccountEvent(ctx, cookieID, engine.EventSecurityVerification, engine.AlertLevelWarn,
			"token 风控验证无法自动处理", "远程服务不可用且浏览器自动化未启用，无法自动完成 token 滑块验证。")
		return "", captchaEngine, nil
	}
	if // withEngine、engineOK 用于本次流程后续判断的withEngine、engineOK
	withEngine, engineOK := a.browser.(browserTokenCaptchaEngineRecoverer); engineOK {
		newCookies, actualEngine, err := withEngine.TokenCaptchaRecoverWithEngine(
			ctx, cookieID, cookieStr, verificationURL, headless, provider,
		)
		return newCookies, actualEngine, err
	}
	newCookies, err := br.TokenCaptchaRecover(ctx, cookieID, cookieStr, verificationURL, headless, provider)
	return newCookies, captchaEngine, err
}
