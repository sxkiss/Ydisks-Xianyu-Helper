package renewal

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"xianyu-go/internal/xianyu/mtop"
)

// TestLoginTokenUpdatesDoNotRestartOrRenewAccount 使用 t 验证 Token 成功和耗尽都不会间接触发启动 Session 续期。
func TestLoginTokenUpdatesDoNotRestartOrRenewAccount(t *testing.T) {
	// outcome 覆盖直接换签、刷新耗尽以及 Token 刷新期间确认真实 Session 失效。
	for _, outcome := range []string{"rotated", "exhausted", "session"} {
		// t 管理当前登录态路径对应的持久化与续期断言。
		t.Run(outcome, func(t *testing.T) {
			// store、cleanup 提供隔离数据库及其释放函数。
			store, cleanup := newSchedulerTestStore(t)
			defer cleanup()
			// account 保存启用的测试账号，凭证全部为合成值。
			account := createSchedulerAccount(t, store, "token-boundary", "unb=1; _m_h5_tk=old_1")
			// server 只接受登录检查与 Token 请求；w 写模拟响应，r 区分端点。
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if outcome == "rotated" {
					http.SetCookie(w, &http.Cookie{Name: "_m_h5_tk", Value: "new_2"})
				}
				if outcome == "session" && r.URL.Path == "/token" {
					fmt.Fprint(w, `{"ret":["FAIL_SYS_SESSION_EXPIRED::Session过期"]}`)
					return
				}
				fmt.Fprint(w, `{"ret":["FAIL_SYS_TOKEN_EMPTY::令牌为空"]}`)
			}))
			defer server.Close()
			// starter、refresher 记录账号重启和 Session 恢复调用。
			starter, refresher := &schedulerFakeStarter{}, &schedulerFakePasswordRefresher{}
			// scheduler 使用真实 MTOP 客户端处理登录状态和 Token 刷新。
			scheduler := NewScheduler(store, starter, refresher, nil)
			scheduler.mtop = &mtop.ClientImpl{HTTPClient: server.Client(), LoginUserURL: server.URL + "/login", TokenURL: server.URL + "/token"}
			scheduler.loginRenewOne(context.Background(), "boundary", account)
			if outcome == "session" {
				if refresher.calls.Load() != 1 || !scheduler.isSessionCooled(account.ID) {
					t.Fatal("Token 请求确认 Session 过期后未执行恢复")
				}
			} else if starter.restarts.Load() != 0 || refresher.calls.Load() != 0 || scheduler.isSessionCooled(account.ID) {
				t.Fatal("Token 处理不应重启、续期或进入 Session 冷却")
			}
		})
	}
}

// TestSessionRenewalCadenceMatchesV1010 使用 t 固定 v1.0.10 的默认四小时间隔、旧登录检查默认关闭和设置兼容优先级。
func TestSessionRenewalCadenceMatchesV1010(t *testing.T) {
	// store、cleanup 提供没有历史设置的数据库，避免把常量断言误当作有效配置断言。
	store, cleanup := newSchedulerTestStore(t)
	defer cleanup()
	// ctx 用于本次设置读取；scheduler 不启动后台任务或平台请求。
	ctx := context.Background()
	// scheduler 提供与生产相同的设置解析规则。
	scheduler := NewScheduler(store, nil, nil, nil)
	if scheduler.apiRenewInterval(ctx) != 4*time.Hour || !scheduler.apiRenewEnabled(ctx) || scheduler.settingEnabled(ctx, loginRenewEnabledSetting, false) {
		t.Fatal("默认续期频率必须为四小时，旧十分钟登录检查必须默认关闭")
	}
	if err := store.Settings.Set(ctx, cookiesRefreshIntervalSetting, "14400"); err != nil { // err 保存旧配置写入错误。
		t.Fatal(err)
	}
	if scheduler.apiRenewInterval(ctx) != 4*time.Hour {
		t.Fatal("旧配置未兼容四小时间隔")
	}
	if err := store.Settings.Set(ctx, apiCookieRenewIntervalSetting, "8h"); err != nil { // err 保存显式新配置写入错误。
		t.Fatal(err)
	}
	if scheduler.apiRenewInterval(ctx) != 8*time.Hour {
		t.Fatal("显式新配置应覆盖旧配置，保持 v1.0.10 兼容语义")
	}
}
