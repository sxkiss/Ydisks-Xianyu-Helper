package db

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestMultiDB_AutomationExecutionLease 验证三方言的执行续租、过期拒绝、陈旧扫描和有效隔离；t 管理独立测试库。
func TestMultiDB_AutomationExecutionLease(t *testing.T) {
	for _, target := range allTestTargets(t) { // target 包含当前方言的真实存储和销毁函数。
		t.Run(target.name, func(t *testing.T) { // t 负责当前方言完整状态流转的断言。
			defer target.cleanup()
			// store、ctx 是当前子测试独占的仓储和上下文。
			store, ctx := target.store, context.Background()
			// userID、cookieID 保存测试规则的合法归属。
			userID, cookieID := seedAccount(t, store)
			// ruleID、err 保存带真实外键的规则。
			ruleID, err := store.Automation.Create(ctx, makeAutomationRule(cookieID, userID, "lease-item", "paid", true, 1))
			if err != nil {
				t.Fatal(err)
			}
			// runID、started、err 保存首次运行创建结果。
			runID, started, err := store.Automation.TryStartRun(ctx, AutomationRun{RuleID: ruleID, CookieID: cookieID, TriggerType: "paid", TriggerKey: "lease-run", LeaseExpiresAt: time.Now().Add(time.Minute).Unix()})
			if err != nil || !started {
				t.Fatal("创建失败", err)
			}
			// err 表示没有动作占用时续租被拒绝。
			if err := store.Automation.RenewExecutingRunLease(ctx, runID, 1, time.Now().Add(time.Hour).Unix()); !errors.Is(err, ErrAutomationRunLeaseLost) {
				t.Fatal("未开始动作却可以续租", err)
			}
			// started、err 保存原子动作领取结果。
			if started, err := store.Automation.StartRunAction(ctx, runID, 1, 0, time.Now().Add(time.Minute).Unix()); err != nil || !started {
				t.Fatal("动作领取失败", err)
			}
			// err 验证调用者不能把续租截止时间设置到过去。
			if err := store.Automation.RenewExecutingRunLease(ctx, runID, 1, 1); !errors.Is(err, ErrAutomationRunLeaseLost) {
				t.Fatal("过去的续租截止时间未被拒绝", err)
			}
			// snapshot、err 保存尚未续租的扫描快照。
			snapshot, err := store.Automation.GetRun(ctx, runID)
			if err != nil {
				t.Fatal(err)
			}
			// err 保存有效动作续租结果。
			if err := store.Automation.RenewExecutingRunLease(ctx, runID, 1, time.Now().Add(time.Hour).Unix()); err != nil {
				t.Fatal(err)
			}
			// err 验证 MySQL 返回匹配行数且租约不会缩短。
			if err := store.Automation.RenewExecutingRunLease(ctx, runID, 1, time.Now().Add(time.Minute).Unix()); err != nil {
				t.Fatal("同秒重复续租必须成功", err)
			}
			// changed、err 保存陈旧快照正常竞争结果。
			if changed, err := store.Automation.QuarantineRecoveryRun(ctx, *snapshot, "stale"); err != nil || changed {
				t.Fatal("旧扫描覆盖了续租", err)
			}
			// current、err 保存续租后当前运行。
			current, err := store.Automation.GetRun(ctx, runID)
			if err != nil || current.LeaseExpiresAt <= snapshot.LeaseExpiresAt {
				t.Fatal("续租被缩短", err)
			}
			// err 保存跨代次续租拒绝结果。
			if err := store.Automation.RenewExecutingRunLease(ctx, runID, 2, time.Now().Add(time.Hour).Unix()); !errors.Is(err, ErrAutomationRunLeaseLost) {
				t.Fatal("错误代次被允许", err)
			}
			// err 把真实数据库推进到到期状态。
			if _, err := store.DB.ExecContext(ctx, `UPDATE automation_runs SET lease_expires_at=1 WHERE id=?`, runID); err != nil {
				t.Fatal(err)
			}
			// err 保存到期续租拒绝结果。
			if err := store.Automation.RenewExecutingRunLease(ctx, runID, 1, time.Now().Add(time.Hour).Unix()); !errors.Is(err, ErrAutomationRunLeaseLost) {
				t.Fatal("过期租约复活", err)
			}
			snapshot, err = store.Automation.GetRun(ctx, runID)
			if err != nil {
				t.Fatal(err)
			}
			// changed、err 保存真实隔离成功结果。
			if changed, err := store.Automation.QuarantineRecoveryRun(ctx, *snapshot, "expired"); err != nil || !changed {
				t.Fatal("有效到期快照无法隔离", err)
			}
			// canceled、cancel 让数据库错误路径保持确定性，不依赖故障网络。
			canceled, cancel := context.WithCancel(ctx)
			cancel()
			// err 保存取消后的租约查询失败。
			if err := store.Automation.RenewExecutingRunLease(canceled, runID, 1, time.Now().Add(time.Hour).Unix()); err == nil {
				t.Fatal("取消未传播")
			}
			// err 保存取消后的隔离查询失败。
			if _, err := store.Automation.QuarantineRecoveryRun(canceled, *snapshot, "cancel"); err == nil {
				t.Fatal("取消未传播")
			}
		})
	}
}
