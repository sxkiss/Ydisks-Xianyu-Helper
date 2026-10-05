package mtop

import (
	"errors"
	"fmt"
	"testing"
)

// TestTokenFailureCannotMasqueradeAsSessionExpired 验证旧包装文案和 errors.Join 不会把明确 Token 失败升级为 Session；t 管理分类断言。
func TestTokenFailureCannotMasqueradeAsSessionExpired(t *testing.T) {
	// tokenErr 模拟 MTOP 客户端返回的结构化签名过期，使用合成平台错误码。
	tokenErr := &MTopResponseError{Kind: MTopErrorTokenExpired, Ret: []string{"FAIL_SYS_TOKEN_EXOIRED::令牌过期"}}
	// failure 覆盖原始、旧版包装和存储错误合并后的 Token 错误链。
	for _, failure := range []error{tokenErr, fmt.Errorf("token API 登录凭证已失效: %w", tokenErr), errors.Join(tokenErr, errors.New("保存 Cookie 失败"))} {
		if !IsMTopTokenExpiredErr(failure) || IsSessionExpiredErr(failure) || IsCredentialRefreshableErr(failure) {
			t.Fatalf("Token 分类不得触发账号级恢复: %v", failure)
		}
	}
	// code 覆盖没有结构化错误链的旧适配器文本，不能被包装文案升级为 Session。
	for _, code := range []string{"FAIL_SYS_TOKEN_EXOIRED", "FAIL_SYS_TOKEN_EXPIRED", "FAIL_SYS_TOKEN_EMPTY"} {
		if IsSessionExpiredErr(fmt.Errorf("登录凭证已失效: %s", code)) {
			t.Fatal("旧 Token 错误文本不得触发 Session 续期")
		}
	}
	// sessionErr 是实际 Session 失效，必须继续允许账号恢复。
	sessionErr := &SessionExpiredError{Ret: []string{"FAIL_SYS_SESSION_EXPIRED"}}
	if !IsSessionExpiredErr(fmt.Errorf("请求失败: %w", sessionErr)) || !IsCredentialRefreshableErr(sessionErr) {
		t.Fatal("明确 Session 失效必须继续允许账号恢复")
	}
}
