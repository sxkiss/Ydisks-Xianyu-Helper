package db

import (
	"context"
	"time"
)

// RenewExecutingRunLease 为 a 中 runID/attempt 的在途动作续租到 leaseExpiresAt（UTC 秒）；过期租约不能复活，零影响行返回失权。
func (a *AutomationRules) RenewExecutingRunLease(ctx context.Context, runID int64, attempt int, leaseExpiresAt int64) error {
	// now 保存本次续租的 UTC 秒；调用者必须提供未来截止时间。
	now := time.Now().UTC().Unix()
	if leaseExpiresAt <= now {
		return ErrAutomationRunLeaseLost
	}
	// result、err 原子校验运行代次、动作占用与未过期租约；保留更长的已有租约。
	result, err := a.DB.ExecContext(ctx, `UPDATE automation_runs SET lease_expires_at=CASE WHEN lease_expires_at>? THEN lease_expires_at ELSE ? END
 WHERE id=? AND attempt_count=? AND status='running' AND action_started=1 AND lease_expires_at>=?`, leaseExpiresAt, leaseExpiresAt, runID, attempt, now)
	if err != nil {
		return err
	}
	return requireAutomationRunOwner(result)
}

// QuarantineRecoveryRun 仅隔离 a 中仍与扫描快照 run 一致的到期运行；续租、游标推进、取消或新代次使该扫描失效。
// reason 是脱敏的人工核对原因，返回是否真正隔离及数据库错误，陈旧扫描不产生通知。
func (a *AutomationRules) QuarantineRecoveryRun(ctx context.Context, run AutomationRun, reason string) (bool, error) {
	// result、err 保存基于整个执行状态的条件更新，防止同一代次已经续租后仍被旧扫描隔离。
	result, err := a.DB.ExecContext(ctx, `UPDATE automation_runs SET status='needs_review',error_message=?,lease_expires_at=0,next_retry_at=0,updated_at=CURRENT_TIMESTAMP
 WHERE id=? AND attempt_count=? AND status=? AND action_cursor=? AND action_started=? AND lease_expires_at=?
 AND ((status='running' AND lease_expires_at<?) OR (status='failed' AND next_retry_at<=?))`, reason, run.ID, run.AttemptCount, run.Status, run.ActionCursor, boolToInt(run.ActionStarted), run.LeaseExpiresAt, time.Now().Unix(), time.Now().Unix())
	if err != nil {
		return false, err
	}
	// affected、err 表示该扫描是否仍拥有隔离权；零行是正常竞争结果。
	affected, err := result.RowsAffected()
	return affected == 1, err
}
