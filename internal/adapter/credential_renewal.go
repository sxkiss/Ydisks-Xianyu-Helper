package adapter

import (
	"context"
	"fmt"
	"time"

	"xianyu-go/internal/db"
	"xianyu-go/internal/xianyu/renew"
)

// persistProtocolRenewalResponse 将 result 的响应 Cookie 重放到 d 对应账号的最新 Jar。
// a 只在本地读取、重放、写回期间持有账号凭证锁；外部续期不在锁内。ctx 控制存储操作。
// d 是本次调用独占的敏感内存视图，成功后同步更新；返回存储错误，不输出 Cookie 明文。
func (a *Adapter) persistProtocolRenewalResponse(ctx context.Context, d *db.CookiePlatformRuntimeData, result *renew.Result) error {
	if len(result.SetCookies) == 0 {
		return nil
	}
	// unlock 保护重读和写回的一致性，避免覆盖外部请求期间其他流程更新的凭证。
	unlock := a.store.LockAccountCredentials(d.ID)
	defer unlock()
	// latest、loadErr 只加载平台 Cookie 和 metadata，不读取密码登录秘密。
	latest, loadErr := a.store.Cookies.GetCookiePlatformRuntimeData(ctx, d.ID)
	if loadErr != nil {
		return loadErr
	}
	if renew.ResponseCookiesConflict(d.Value, d.MetadataJSON, latest.Value, latest.MetadataJSON, result) {
		return fmt.Errorf("账号凭证同名 Cookie 已在续期期间更新，已拒绝旧响应写回")
	}
	// cookies、metadata、changed 按响应作用域和头部时间更新最新 Jar，保留并发写入的无关 Cookie。
	cookies, metadata, changed := renew.RebaseResponseCookies(latest.Value, latest.MetadataJSON, result)
	if changed {
		// saveErr 表示原子写回最新 Cookie 及完整 metadata 失败，失败时不更新调用方视图。
		if saveErr := a.store.Cookies.UpdateRenewalCookie(ctx, d.ID, cookies, metadata, time.Now().Unix()); saveErr != nil {
			return saveErr
		}
	}
	if cookies != latest.Value && a.store.Tokens != nil {
		// clearErr 仅影响运行期 Token 缓存；Cookie 已提交后不能将缓存清理失败误报为续期回滚。
		if clearErr := a.store.Tokens.Clear(ctx, d.ID); clearErr != nil {
			a.logger.Warn("轻量续期清理旧 Token 缓存失败", "account", d.ID, "err", clearErr)
		}
	}
	d.Value, d.MetadataJSON = cookies, metadata
	return nil
}
