package db

import (
	"context"
	"database/sql"
	"time"
)

// ClaimBargainFreeShipping 为砍价“待刀成”阶段领取一次免拼执行权。
// running、succeeded 和 needs_review 状态都不会被重复领取；明确失败的请求可以由后续同阶段 WS 重试。
func (a *AutomationRules) ClaimBargainFreeShipping(ctx context.Context, orderID, cookieID string) (bool, error) {
	// now 保存本次阶段领取的 UTC 观测时间，跨方言统一为 RFC3339 文本。
	now := time.Now().UTC().Format(time.RFC3339Nano)
	// insertResult、insertErr 保存首次插入阶段记录的数据库结果。
	insertResult, insertErr := a.DB.ExecContext(ctx, dialectInsertIgnorePrefix(a.Dialect)+` INTO bargain_free_shipping_stages(order_id,cookie_id,status,updated_at) VALUES(?,?,?,?)`+dialectInsertIgnore(a.Dialect, []string{"order_id", "cookie_id"}), orderID, cookieID, "running", now)
	if insertErr != nil {
		return false, insertErr
	}
	// inserted 表示当前 WS 事件首次获得平台调用权。
	inserted, rowsErr := insertResult.RowsAffected()
	if rowsErr != nil {
		return false, rowsErr
	}
	if inserted > 0 {
		return true, nil
	}
	// retryResult、retryErr 只允许明确业务失败的阶段重新进入 running；未知结果必须人工核对。
	retryResult, retryErr := a.DB.ExecContext(ctx, `UPDATE bargain_free_shipping_stages SET status=?,updated_at=? WHERE order_id=? AND cookie_id=? AND status='failed'`, "running", now, orderID, cookieID)
	if retryErr != nil {
		return false, retryErr
	}
	// retried 表示已有明确失败记录被安全地重新领取。
	retried, retryRowsErr := retryResult.RowsAffected()
	if retryRowsErr != nil {
		return false, retryRowsErr
	}
	return retried > 0, nil
}

// FinishBargainFreeShipping 记录已领取免拼动作的确定终态；status 只能由执行器的结果分类传入。
func (a *AutomationRules) FinishBargainFreeShipping(ctx context.Context, orderID, cookieID, status string) error {
	// now 保存本次免拼执行终态的 UTC 时间。
	now := time.Now().UTC().Format(time.RFC3339Nano)
	// result、err 保存只允许更新已领取阶段记录的写入结果。
	result, err := a.DB.ExecContext(ctx, `UPDATE bargain_free_shipping_stages SET status=?,updated_at=? WHERE order_id=? AND cookie_id=? AND status='running'`, status, now, orderID, cookieID)
	if err != nil {
		return err
	}
	// changed 表示本次任务仍拥有阶段状态的收口权；零行说明不能覆盖并发或历史终态。
	changed, rowsErr := result.RowsAffected()
	if rowsErr != nil {
		return rowsErr
	}
	if changed == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// MarkBargainReady 记录最终“成功小刀，待发货”阶段；它不执行任何外部动作，只为丢失发货卡片时提供兜底事实。
func (a *AutomationRules) MarkBargainReady(ctx context.Context, orderID, cookieID string) error {
	// now 保存最终砍价阶段的 UTC 观测时间。
	now := time.Now().UTC().Format(time.RFC3339Nano)
	// insertErr 保存首次记录最终砍价阶段的数据库错误。
	if _, insertErr := a.DB.ExecContext(ctx, dialectInsertIgnorePrefix(a.Dialect)+` INTO bargain_free_shipping_stages(order_id,cookie_id,status,updated_at) VALUES(?,?,?,?)`+dialectInsertIgnore(a.Dialect, []string{"order_id", "cookie_id"}), orderID, cookieID, "ready", now); insertErr != nil {
		return insertErr
	}
	// updateErr 只把明确失败的阶段推进为 ready；running 可能仍在执行免拼，不能被最终卡片覆盖。
	_, updateErr := a.DB.ExecContext(ctx, `UPDATE bargain_free_shipping_stages SET status=?,updated_at=? WHERE order_id=? AND cookie_id=? AND status='failed'`, "ready", now, orderID, cookieID)
	return updateErr
}
