package adapter

import (
	"context"
	"log/slog"
)

// SessionRecoveryHandler 是平台凭证错误到账号恢复运行时的最小适配回调。
// 回调只传递失效错误，不向调用方暴露 Cookie 或 Token。
type SessionRecoveryHandler func(context.Context, string, error) bool

// NewSessionRecoveryHandler 创建统一的平台会话恢复回调。
// recoverFunc 只负责请求账号运行时恢复；本适配器负责识别平台错误和记录脱敏诊断。
func NewSessionRecoveryHandler(logger *slog.Logger, recoverFunc func(context.Context, string) bool) SessionRecoveryHandler {
	return func(ctx context.Context, accountID string, err error) bool {
		if !IsCredentialExpiredError(err) || recoverFunc == nil {
			return false
		}
		if logger != nil {
			logger.Warn("MTOP API 检测到凭证失效，停止业务请求并开始即时续期", "account", accountID, "err", err)
		}
		return recoverFunc(ctx, accountID)
	}
}
