package account

import (
	"context"
	"sync"
	"testing"
	"time"
)

// transitionSettingsRepository 用通道通知 Cookie 已完成写入，状态由转换锁串行保护。
type transitionSettingsRepository struct {
	*fakeSettingsRepository
	// credentialMu 模拟真实凭证锁，避免复用仅适合串行测试的计数替身。
	credentialMu sync.Mutex
	// cookieSaved 在 ClearTokens 完成时关闭，停用请求等待此信号以构造确定性交错。
	cookieSaved chan struct{}
	// beforeStatus 检查启用状态读取受转换锁保护。
	beforeStatus func()
}

// LockCredentials 为本地测试账号提供真实互斥锁；返回函数由调用方负责释放，参数不访问外部账号。
func (r *transitionSettingsRepository) LockCredentials(string) func() {
	r.credentialMu.Lock()
	return r.credentialMu.Unlock
}

// ClearTokens 模拟 Cookie 写入的最后一步；只发送完成信号，不读取真实凭证。
func (r *transitionSettingsRepository) ClearTokens(context.Context, string) error {
	close(r.cookieSaved)
	return nil
}

// StatusOwned 在返回账号状态前检查锁约束；参数沿用本地账号身份，返回启用状态和错误。
func (r *transitionSettingsRepository) StatusOwned(context.Context, int64, string) (bool, error) {
	r.beforeStatus()
	return r.status, nil
}

// SetStatusOwned 保存 enabled 作为测试账号最新启用状态；其他参数沿用应用输入，无外部副作用。
func (r *transitionSettingsRepository) SetStatusOwned(_ context.Context, _ int64, _ string, enabled bool, _ string) error {
	r.status = enabled
	return nil
}

// transitionSettingsRuntime 在停用持有转换锁时等待 Cookie 写入，随后允许停用完成。
type transitionSettingsRuntime struct {
	*fakeSettingsRuntime
	// stopStarted 通知测试已经持有转换锁并进入停用。
	stopStarted chan struct{}
	// cookieSaved 是设置仓储负责关闭的 Cookie 写入信号。
	cookieSaved <-chan struct{}
}

// StopContext 在 ctx 取消或 Cookie 写入完成时结束；accountID 只代表当前测试账号。
func (r *transitionSettingsRuntime) StopContext(ctx context.Context, accountID string) error {
	close(r.stopStarted)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-r.cookieSaved:
		return nil
	}
}

// TestSettingsCookieUpdateWaitsForDisable 验证更新在转换锁内重读最新状态，不越过已完成的停用；t 管理断言和并发收束。
func TestSettingsCookieUpdateWaitsForDisable(t *testing.T) {
	// ctx、cancel 保证测试异常时也会取消等待，所有 goroutine 在返回前收束。
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// repository、runtime 构造初始启用的账号与可控停用信号。
	repository := &transitionSettingsRepository{fakeSettingsRepository: &fakeSettingsRepository{status: true}, cookieSaved: make(chan struct{})}
	// runtime 负责阻塞停用以等待并发 Cookie 写入。
	runtime := &transitionSettingsRuntime{fakeSettingsRuntime: &fakeSettingsRuntime{}, stopStarted: make(chan struct{}), cookieSaved: repository.cookieSaved}
	// service、err 是共享转换锁的真实设置应用服务。
	service, err := NewSettingsService(repository, runtime)
	if err != nil {
		t.Fatal(err)
	}
	repository.beforeStatus = func() {
		// lock 必须由当前状态转换持有；无其他转换竞争时也不允许在锁外读取。
		lock := service.transitionLock("cid")
		if lock.TryLock() {
			lock.Unlock()
			t.Error("启用状态必须在转换锁内读取")
		}
	}
	// stopped 保存停用请求错误；接收后保证其状态写入已结束。
	stopped := make(chan error, 1)
	go func() {
		// result、stopErr 保存停用持久化与运行时结果。
		result, stopErr := service.SetStatus(ctx, 1, "cid", false)
		if stopErr == nil {
			stopErr = result.RuntimeError
		}
		stopped <- stopErr
	}()
	select {
	case <-runtime.stopStarted:
	case <-ctx.Done():
		t.Fatal("停用未进入运行时")
	}
	// cookie 仅为测试占位输入；真实凭证不会写入日志。
	cookie := "test-placeholder"
	// updated 保存更新请求完成结果，避免主协程阻塞停用信号。
	updated := make(chan error, 1)
	go func() {
		// result、updateErr 保存 Cookie 更新持久化与运行时结果。
		result, updateErr := service.UpdateSettings(ctx, SettingsUpdateInput{UserID: 1, AccountID: "cid", Cookie: &cookie})
		if updateErr == nil {
			updateErr = result.RuntimeError
		}
		updated <- updateErr
	}()
	// stopErr、updateErr 在两个请求都完成后统一断言，避免遗留协程。
	stopErr, updateErr := <-stopped, <-updated
	if stopErr != nil || updateErr != nil {
		t.Fatalf("转换失败：停用=%v 更新=%v", stopErr, updateErr)
	}
	// 无竞争时再次更新，确保状态读取本身也持有转换锁，而不是碰巧由另一请求持有。
	repository.cookieSaved = make(chan struct{})
	// repeatResult、repeatErr 检查停用状态下重复 Cookie 更新不触发运行时。
	repeatResult, repeatErr := service.UpdateSettings(ctx, SettingsUpdateInput{UserID: 1, AccountID: "cid", Cookie: &cookie})
	if repeatErr != nil || repeatResult.RuntimeError != nil {
		t.Fatal("重复更新失败")
	}
	if repository.status || runtime.restartCalls != 0 {
		t.Fatalf("停用后不应重启：enabled=%v restart=%d", repository.status, runtime.restartCalls)
	}
}
