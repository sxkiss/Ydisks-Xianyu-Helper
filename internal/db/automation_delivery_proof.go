package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// GetRun 返回自动化运行及动作检查点，并在执行动作前恢复发货凭证。
func (a *AutomationRules) GetRun(ctx context.Context, id int64) (*AutomationRun, error) {
	// run 保存读取到的自动化运行。
	var run AutomationRun
	// actionStarted 保存数据库整数布尔值。
	var actionStarted int
	// deliveryProofJSON 保存数据库中的加密凭证 JSON。
	var deliveryProofJSON string
	// err 保存自动化运行读取错误。
	err := a.DB.QueryRowContext(ctx, `SELECT id,rule_id,cookie_id,item_id,order_id,buyer_id,chat_id,trigger_type,trigger_key,
		status,sent_count,error_message,raw_event_json,delivery_proof,lease_expires_at,attempt_count,next_retry_at,action_cursor,action_started
		FROM automation_runs WHERE id=?`, id).Scan(&run.ID, &run.RuleID, &run.CookieID, &run.ItemID, &run.OrderID,
		&run.BuyerID, &run.ChatID, &run.TriggerType, &run.TriggerKey, &run.Status, &run.SentCount,
		&run.ErrorMessage, &run.RawEventJSON, &deliveryProofJSON, &run.LeaseExpiresAt, &run.AttemptCount, &run.NextRetryAt,
		&run.ActionCursor, &actionStarted)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	run.ActionStarted = actionStarted != 0
	if err != nil {
		return &run, err
	}
	// proof、proofErr 保存解密和校验后的短期发货凭证。
	proof, proofErr := a.decodeDeliveryProof(run.ID, deliveryProofJSON)
	if proofErr != nil {
		return nil, proofErr
	}
	run.DeliveryProof = proof
	return &run, nil
}

// GetRunByRuleAndTrigger 读取同一规则和幂等键的运行快照；ctx 控制查询取消，未找到返回 ErrNotFound。
// 该方法只供自动化编排判断能否原样补发，返回的发货内容始终保持在加密仓储边界内。
func (a *AutomationRules) GetRunByRuleAndTrigger(ctx context.Context, ruleID int64, triggerKey string) (*AutomationRun, error) {
	// runID 保存唯一运行记录标识；readErr 表示按规则和幂等键查询失败。
	var runID int64
	// readErr 保存按规则和幂等键读取自动化运行失败的数据库错误。
	readErr := a.DB.QueryRowContext(ctx, `SELECT id FROM automation_runs WHERE rule_id=? AND trigger_key=?`, ruleID, triggerKey).Scan(&runID)
	if readErr != nil {
		if errors.Is(readErr, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, readErr
	}
	return a.GetRun(ctx, runID)
}

// GetLatestOrderDeliveryRun 读取订单最近一次付款发货运行；ctx 控制查询取消，cookieID 防止跨账号复用快照。
// 返回的快照可能来自自动或人工流程，调用方必须先根据运行终态决定补发、仅补记状态或人工核对。
func (a *AutomationRules) GetLatestOrderDeliveryRun(ctx context.Context, cookieID, orderID string) (*AutomationRun, error) {
	// runID 保存同一账号订单最近更新的付款发货运行标识；readErr 表示读取失败。
	var runID int64
	// readErr 保存读取同一订单最近付款发货运行失败的数据库错误。
	readErr := a.DB.QueryRowContext(ctx, `SELECT id FROM automation_runs
		WHERE cookie_id=? AND order_id=? AND trigger_type='order_paid'
		ORDER BY updated_at DESC,id DESC LIMIT 1`, cookieID, orderID).Scan(&runID)
	if readErr != nil {
		if errors.Is(readErr, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, readErr
	}
	return a.GetRun(ctx, runID)
}

// ClaimDeliveryReplay 原子领取失败或人工核对运行的内容补发权，避免并发人工请求重复发送同一快照。
// 返回的 attempt 是领取成功后的新代次；leaseExpiresAt 限制运行中的补发占用时间。
func (a *AutomationRules) ClaimDeliveryReplay(ctx context.Context, runID int64, expectedAttempt int, leaseExpiresAt int64) (attempt int, claimed bool, err error) {
	// now 保存当前 UTC 秒时间，用于拒绝已经过期的补发租约输入。
	now := time.Now().UTC().Unix()
	if leaseExpiresAt <= now {
		return expectedAttempt, false, fmt.Errorf("补发租约必须晚于当前时间")
	}
	// result、updateErr 保存条件领取更新及其数据库错误；状态和代次共同构成单个补发执行者的所有权。
	result, updateErr := a.DB.ExecContext(ctx, `UPDATE automation_runs
		SET status='running',attempt_count=attempt_count+1,lease_expires_at=?,action_started=1,updated_at=CURRENT_TIMESTAMP
		WHERE id=? AND attempt_count=? AND status IN ('failed','needs_review') AND delivery_proof<>''`, leaseExpiresAt, runID, expectedAttempt)
	if updateErr != nil {
		return expectedAttempt, false, updateErr
	}
	// affected、affectedErr 保存实际领取行数及读取更新结果错误；零行表示已被其他请求或状态迁移占用。
	affected, affectedErr := result.RowsAffected()
	if affectedErr != nil {
		return expectedAttempt, false, affectedErr
	}
	if affected != 1 {
		return expectedAttempt, false, nil
	}
	return expectedAttempt + 1, true, nil
}

// ReleaseDeliveryReplay 在补发尚未成功收口时恢复终态；恢复到 needs_review 会保留动作占用标志以禁止整批重试。
// runID 和 attempt 只允许当前补发领取者释放，避免旧请求覆盖新的执行代次。
func (a *AutomationRules) ReleaseDeliveryReplay(ctx context.Context, runID int64, attempt int, restoreStatus, reason string) error {
	if restoreStatus != "failed" && restoreStatus != "needs_review" {
		return fmt.Errorf("补发运行不能恢复到状态 %q", restoreStatus)
	}
	// result、updateErr 保存失败补发的条件释放更新及其数据库错误。
	result, updateErr := a.DB.ExecContext(ctx, `UPDATE automation_runs
		SET status=?,error_message=?,lease_expires_at=0,action_started=CASE WHEN ?='needs_review' THEN 1 ELSE 0 END,updated_at=CURRENT_TIMESTAMP
		WHERE id=? AND attempt_count=? AND status='running' AND action_started=1`, restoreStatus, reason, restoreStatus, runID, attempt)
	if updateErr != nil {
		return updateErr
	}
	return requireAutomationRunOwner(result)
}

// CompleteDeliveryReplay 将已经原样补发并完成确认发货的已领取运行收口为成功。
// ctx 控制数据库更新；runID 和 attempt 防止旧 worker 覆盖新代次，快照必须继续保留给后续审计。
func (a *AutomationRules) CompleteDeliveryReplay(ctx context.Context, runID int64, attempt int) error {
	// result、updateErr 保存终态收口更新及其数据库错误；attempt 和补发领取状态共同隔离旧 worker。
	result, updateErr := a.DB.ExecContext(ctx, `UPDATE automation_runs
		SET status='success',error_message='',lease_expires_at=0,next_retry_at=0,action_started=0,updated_at=CURRENT_TIMESTAMP
		WHERE id=? AND attempt_count=? AND status='running' AND action_started=1`, runID, attempt)
	if updateErr != nil {
		return updateErr
	}
	return requireAutomationRunOwner(result)
}

// UpdateDeliveryReplayProof 保存补发期间新取得的发货快照，只有当前补发代次可以写入。
// proof 必须包含已经生成的全部内容；取卡前先保存 RefillPending，结果写入失败时保留该标记禁止再次取卡。
func (a *AutomationRules) UpdateDeliveryReplayProof(ctx context.Context, runID int64, attempt int, proof AutomationDeliveryProof) error {
	// encryptedProof 保存按运行作用域加密后的补发快照。
	encryptedProof, err := a.encodeDeliveryProof(runID, proof)
	if err != nil {
		return err
	}
	// result、updateErr 保存当前补发代次的条件写回结果及数据库错误。
	result, updateErr := a.DB.ExecContext(ctx, `UPDATE automation_runs
		SET delivery_proof=?,updated_at=CURRENT_TIMESTAMP
		WHERE id=? AND attempt_count=? AND status='running' AND action_started=1`, encryptedProof, runID, attempt)
	if updateErr != nil {
		return updateErr
	}
	return requireAutomationRunOwner(result)
}

// AdvanceRunAction 在动作明确成功后原子推进游标、累计数量并保存或清除发货凭证。
func (a *AutomationRules) AdvanceRunAction(ctx context.Context, advance AutomationRunActionAdvance) error {
	return a.advanceRunAction(ctx, advance)
}

// advanceRunAction 原子推进动作游标、累计发送数并保存或清空确认发货凭证。
func (a *AutomationRules) advanceRunAction(ctx context.Context, advance AutomationRunActionAdvance) error {
	// assignments 保存本次检查点需要更新的列。
	assignments := "action_cursor=?,action_started=0,sent_count=sent_count+?,updated_at=CURRENT_TIMESTAMP"
	// args 保存检查点 SQL 的参数。
	args := []any{advance.Cursor + 1, advance.SentDelta}
	if advance.ClearDeliveryProof {
		assignments = "delivery_proof=''," + assignments
	} else if advance.DeliveryProof != nil {
		// encryptedProof 保存按运行作用域加密后的完整凭证 JSON。
		encryptedProof, err := a.encodeDeliveryProof(advance.RunID, *advance.DeliveryProof)
		if err != nil {
			return err
		}
		assignments = "delivery_proof=?," + assignments
		args = append([]any{encryptedProof}, args...)
	}
	args = append(args, advance.RunID, advance.Attempt, advance.Cursor)
	// res、err 保存检查点更新结果及数据库错误。
	res, err := a.DB.ExecContext(ctx, `UPDATE automation_runs SET `+assignments+`
		WHERE id=? AND attempt_count=? AND status='running' AND action_cursor=? AND action_started=1`, args...)
	if err != nil {
		return err
	}
	return requireAutomationRunOwner(res)
}

// encodeDeliveryProof 将短期发货凭证编码并按自动化运行作用域加密。
func (a *AutomationRules) encodeDeliveryProof(runID int64, proof AutomationDeliveryProof) (string, error) {
	if a == nil || a.codec == nil {
		return "", errors.New("自动化发货凭证缺少加密编解码器")
	}
	if a.codec.currentAEAD() == nil {
		return "", errors.New("自动化发货凭证缺少持久化数据密钥")
	}
	// payload 保存稳定的凭证 JSON 结构。
	payload, err := json.Marshal(proof)
	if err != nil {
		return "", fmt.Errorf("编码自动化发货凭证失败: %w", err)
	}
	// encrypted 保存按运行作用域加密后的凭证，禁止新凭证以明文写入数据库。
	encrypted, err := a.codec.encrypt("automation-delivery-proof", fmt.Sprint(runID), string(payload))
	if err != nil {
		return "", fmt.Errorf("加密自动化发货凭证失败: %w", err)
	}
	return encrypted, nil
}

// decodeDeliveryProof 解密并校验自动化运行中的发货凭证。
func (a *AutomationRules) decodeDeliveryProof(runID int64, raw string) (AutomationDeliveryProof, error) {
	if strings.TrimSpace(raw) == "" {
		return AutomationDeliveryProof{}, nil
	}
	// plain 保存按运行作用域解密后的凭证 JSON。
	plain, err := a.codec.decrypt("automation-delivery-proof", fmt.Sprint(runID), raw)
	if err != nil {
		return AutomationDeliveryProof{}, fmt.Errorf("读取自动化发货凭证失败: %w", err)
	}
	// proof 保存解密并解析后的确认发货凭证。
	var proof AutomationDeliveryProof
	// err 保存凭证 JSON 解析错误。
	if err := json.Unmarshal([]byte(plain), &proof); err != nil {
		return AutomationDeliveryProof{}, fmt.Errorf("解析自动化发货凭证失败: %w", err)
	}
	return proof, nil
}
