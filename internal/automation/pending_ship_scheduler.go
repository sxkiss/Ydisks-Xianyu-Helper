package automation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"xianyu-go/internal/db"
)

// paidRecoveryReady 重新核对付款运行的来源、最新订单状态和自动发货总开关。
// 它只允许恢复 WebSocket 已创建或待发货兜底已创建的运行，绝不从历史运行推断砍价阶段或绕过账号开关。
func (s *Scheduler) paidRecoveryReady(ctx context.Context, task Task, run db.AutomationRun) (bool, error) {
	if task.TriggerType != TriggerOrderPaid {
		return true, nil
	}
	if task.Source != "ws" && task.Source != "scheduler" {
		// reason 说明旧版或人工来源付款运行不能被计划任务静默续跑。
		reason := "付款运行缺少可信的 WebSocket 或待发货兜底来源，已停止自动恢复"
		return false, s.quarantineRunForReview(ctx, run, reason)
	}
	// order、orderErr 保存计划任务执行前读取到的最新订单事实。
	order, orderErr := s.center.store.Orders.Get(ctx, run.OrderID)
	if errors.Is(orderErr, db.ErrNotFound) {
		// reason 说明没有订单事实时无法证明买家仍已付款且等待发货。
		reason := "本地缺少订单状态，无法确认订单仍为待发货，已停止自动恢复"
		return false, s.quarantineRunForReview(ctx, run, reason)
	}
	if orderErr != nil {
		// postponeErr 保存订单事实暂时不可读时的恢复延期结果，避免数据库瞬时错误触发外部动作。
		postponeErr := s.center.store.Automation.PostponeRecoveryRun(ctx, run.ID, run.AttemptCount, time.Now().UTC().Add(defaultReviewRequestScanInterval).Unix())
		if postponeErr != nil {
			return false, errors.Join(fmt.Errorf("读取付款恢复订单状态失败: %w", orderErr), fmt.Errorf("延期付款恢复运行失败: %w", postponeErr))
		}
		s.center.logger.Warn("读取付款恢复订单状态失败，已延期等待下次核对", "run_id", run.ID, "account", run.CookieID, "order_id", run.OrderID, "err", orderErr)
		return false, nil
	}
	if order.CookieID != run.CookieID {
		// reason 说明订单归属变化后不能继续使用原运行的账号凭证或会话。
		reason := "订单归属账号与付款运行不一致，已停止自动恢复"
		return false, s.quarantineRunForReview(ctx, run, reason)
	}
	if !isPendingShipOrder(order) || order.SystemShipped {
		// reason 记录自动取消原因；订单已取消、完成或发货时无需用户再次处理。
		reason := fmt.Sprintf("订单当前状态为 %s，不再需要自动发货恢复", firstNonEmpty(order.OrderStatus, "未知"))
		// canceled、cancelErr 保存过期运行是否仍与扫描快照一致并已安全取消。
		canceled, cancelErr := s.center.store.Automation.CancelObsoletePaidRecoveryRun(ctx, run, reason)
		if cancelErr != nil {
			return false, fmt.Errorf("取消已失效付款恢复运行: %w", cancelErr)
		}
		if canceled {
			s.center.logger.Info("订单已不再待发货，取消历史付款恢复运行", "run_id", run.ID, "account", run.CookieID, "order_id", run.OrderID, "order_status", order.OrderStatus, "system_shipped", order.SystemShipped)
		}
		return false, nil
	}
	if task.Source == "ws" && task.OrderRole != OrderRoleSeller {
		// _, sellerVerified、roleReason、roleErr 保存历史 WS 运行的未知角色核验结果；恢复路径不能沿用旧快照猜测卖家身份。
		_, sellerVerified, roleReason, roleErr := s.center.authorizeWebSocketSellerTaskWithOrder(ctx, task, order)
		if roleErr != nil {
			// postponeErr 保存本地商品归属暂时不可读时的延期结果，避免数据库瞬时故障直接执行外部发货。
			postponeErr := s.center.store.Automation.PostponeRecoveryRun(ctx, run.ID, run.AttemptCount, time.Now().UTC().Add(defaultReviewRequestScanInterval).Unix())
			if postponeErr != nil {
				return false, errors.Join(fmt.Errorf("核对未知角色付款运行失败: %w", roleErr), fmt.Errorf("延期付款恢复运行失败: %w", postponeErr))
			}
			s.center.logger.Warn("未知角色付款运行核验失败，已延期等待本地事实恢复", "run_id", run.ID, "account", run.CookieID, "order_id", run.OrderID, "err", roleErr)
			return false, nil
		}
		if !sellerVerified {
			// reason 说明历史运行无法由本地商品和订单身份证明是卖家事件，必须人工核对而不能重放发货。
			reason := "历史 WebSocket 付款运行缺少明确卖家角色且本地证据不足（" + roleReason + "），已停止自动恢复"
			return false, s.quarantineRunForReview(ctx, run, reason)
		}
	}
	// enabled、settingsErr 保存账号当前自动发货总开关及读取错误。
	enabled, settingsErr := s.center.paidDeliveryAutoConfirmEnabled(ctx, run.CookieID)
	if settingsErr != nil {
		// postponeErr 保存设置暂时不可读时的恢复延期结果，禁止失败开放。
		postponeErr := s.center.store.Automation.PostponeRecoveryRun(ctx, run.ID, run.AttemptCount, time.Now().UTC().Add(defaultReviewRequestScanInterval).Unix())
		if postponeErr != nil {
			return false, errors.Join(fmt.Errorf("读取付款恢复自动发货开关失败: %w", settingsErr), fmt.Errorf("延期付款恢复运行失败: %w", postponeErr))
		}
		s.center.logger.Warn("读取付款恢复自动发货开关失败，已延期等待下次核对", "run_id", run.ID, "account", run.CookieID, "order_id", run.OrderID, "err", settingsErr)
		return false, nil
	}
	if !enabled {
		// postponeErr 保存用户关闭自动发货后把历史运行移出当前扫描窗口的结果。
		postponeErr := s.center.store.Automation.PostponeRecoveryRun(ctx, run.ID, run.AttemptCount, time.Now().UTC().Add(10*time.Minute).Unix())
		if postponeErr != nil {
			return false, fmt.Errorf("自动发货已关闭，延期付款恢复运行失败: %w", postponeErr)
		}
		s.center.logger.Info("账号已关闭自动发货，付款恢复运行等待用户重新开启", "run_id", run.ID, "account", run.CookieID, "order_id", run.OrderID)
		return false, nil
	}
	return true, nil
}

// scanPendingShipDeliveries 兜底扫描没有付款运行的待发货订单并补触发自动发货。
func (s *Scheduler) scanPendingShipDeliveries(ctx context.Context) {
	if ctx == nil {
		return
	}
	// scanCtx、cancel 为直接调用入口提供独立预算及释放函数。
	scanCtx, cancel := context.WithTimeout(ctx, s.pendingShipScanBudgetValue())
	defer cancel()
	_ = s.scanPendingShipDeliveriesWithContextAndLimit(scanCtx, s.pendingShipScanMaxTasksValue())
}

// scanPendingShipDeliveriesWithContextAndLimit 在预算与任务额度内补触发待发货订单。
func (s *Scheduler) scanPendingShipDeliveriesWithContextAndLimit(ctx context.Context, remainingTasks int) (leftTasks int) {
	leftTasks = remainingTasks
	if strings.TrimSpace(os.Getenv(pendingShipCatchupEnv)) == "0" || s == nil || s.center == nil || s.center.store == nil || s.center.store.Automation == nil || leftTasks <= 0 {
		return
	}
	// triggeredCount 统计本轮实际领取的订单数。
	triggeredCount := 0
	// afterOrderID 保存分页扫描的稳定订单游标。
	afterOrderID := ""
	for {
		if ctx.Err() != nil {
			s.center.logger.Warn("待发货兜底扫描达到本轮时间预算", "err", ctx.Err())
			return
		}
		// orders、err 保存当前候选页及查询错误。
		orders, err := s.center.store.Automation.PendingShipOrdersWithoutPaidRunAfter(ctx, afterOrderID, pendingShipScanPageSize)
		if err != nil {
			s.center.logger.Warn("扫描待发货兜底订单失败", "err", err)
			return
		}
		if len(orders) == 0 {
			return
		}
		// order 表示当前待发货候选订单。
		for _, order := range orders {
			if ctx.Err() != nil || leftTasks <= 0 {
				if ctx.Err() != nil {
					s.center.logger.Warn("待发货兜底扫描达到本轮时间预算", "err", ctx.Err())
				} else {
					s.center.logger.Info("待发货兜底扫描达到本轮任务上限", "count", triggeredCount)
				}
				return
			}
			afterOrderID = order.OrderID
			// catchupReady、waitReason 保存当前订单是否已越过实时事件优先窗口及未放行原因。
			catchupReady, waitReason := pendingShipCatchupReady(order, time.Now().UTC())
			if !catchupReady {
				s.center.logger.Info("待发货兜底任务等待实时付款事件或人工核对", "account", order.CookieID, "order_id", order.OrderID, "reason", waitReason)
				continue
			}
			// allowed、allowErr 保存账号自动化门禁结果。
			allowed, allowErr := s.center.accountAutomationAllowed(ctx, order.CookieID)
			if allowErr != nil {
				s.center.logger.Warn("检查待发货兜底账号状态失败", "account", order.CookieID, "order_id", order.OrderID, "err", allowErr)
				continue
			}
			if !allowed {
				continue
			}
			// paid、paidErr 保存账号自动确认发货开关及读取错误。
			paid, paidErr := s.center.paidDeliveryAutoConfirmEnabled(ctx, order.CookieID)
			if paidErr != nil {
				s.center.logger.Warn("检查待发货兜底自动确认发货开关失败", "account", order.CookieID, "order_id", order.OrderID, "err", paidErr)
				continue
			}
			if !paid {
				continue
			}
			if !s.center.accountSenderReady(order.CookieID) {
				s.center.logger.Info("账号 WebSocket 尚未就绪，待发货兜底任务等待下次扫描", "account", order.CookieID, "order_id", order.OrderID)
				continue
			}
			if !s.claimPendingShipAttempt(order.OrderID) {
				continue
			}
			triggeredCount++
			leftTasks--
			s.center.logger.Info("付款系统消息缺失，按订单状态补触发自动发货", "account", order.CookieID, "order_id", order.OrderID, "item_id", order.ItemID)
			// taskCtx、cancel 为单个兜底任务提供执行预算及释放函数。
			taskCtx, cancel := context.WithTimeout(ctx, pendingShipTaskTimeout)
			// task 保存待发货订单转换后的自动化任务载荷。
			// task 是由本地待发货订单扫描构造的可信卖家任务；它不依赖 WS 卡片中的 role 字段。
			task := Task{Source: "scheduler", AccountID: order.CookieID, OrderRole: OrderRoleSeller, TriggerType: TriggerOrderPaid, ChatID: order.ChatID, OrderID: order.OrderID, ItemID: order.ItemID, BuyerID: order.BuyerID, Text: "付款系统消息缺失，按订单状态补触发自动发货", Raw: map[string]any{"source": "scheduler", "order_id": order.OrderID}}
			// err 保存兜底任务执行错误；失败只影响当前订单。
			if err := s.center.HandleTask(taskCtx, task); err != nil {
				s.center.logger.Warn("待发货兜底任务执行失败", "account", order.CookieID, "order_id", order.OrderID, "err", err)
			}
			cancel()
		}
		if len(orders) < pendingShipScanPageSize {
			return
		}
	}
}

// pendingShipCatchupReady 判断已由候选查询确认阶段资格的订单能否安全转换为付款自动化事件。
// 所有订单都必须先等待实时付款卡片窗口结束；砍价订单是否已完成免拼由查询层的阶段记录决定，兜底不得推断或调用免拼。
func pendingShipCatchupReady(order db.Order, now time.Time) (bool, string) {
	// observedAt 保存本地最后一次观测到待发货事实的时间；优先使用更新时间以覆盖订单同步和事件写入两种来源。
	observedAt := parseDBTime(firstNonEmpty(order.UpdatedAt, order.PaidAt, order.CreatedAt))
	if observedAt.IsZero() {
		return false, "missing_observed_time"
	}
	// settleDeadline 是实时付款系统卡片应优先完成处理的截止时刻。
	settleDeadline := observedAt.Add(defaultPendingShipSettleWindow)
	if now.Before(settleDeadline) {
		return false, "waiting_for_realtime_payment_event"
	}
	return true, ""
}

// pendingShipResumeFrozenPlan 从运行的原始事件快照恢复冻结的动作计划，并判定能否自动续跑。
func pendingShipResumeFrozenPlan(candidate db.PendingShipResume) ([]db.AutomationAction, bool, error) {
	// original 保存运行创建时冻结的任务事实与完整动作计划。
	var original Task
	// err 保存快照解析错误；历史快照损坏时不能猜测缺失的动作。
	if err := json.Unmarshal([]byte(candidate.RawEventJSON), &original); err != nil {
		return nil, false, fmt.Errorf("待发货续跑运行的原始计划无法解析: %w", err)
	}
	if original.AccountID != candidate.Order.CookieID || original.OrderID != candidate.Order.OrderID || len(original.ActionPlan) == 0 {
		return nil, false, fmt.Errorf("待发货续跑运行的原始计划缺失或归属不符")
	}
	if candidate.ActionCursor < 0 || candidate.ActionCursor > len(original.ActionPlan) {
		return nil, false, fmt.Errorf("待发货续跑运行的游标越界: %d", candidate.ActionCursor)
	}
	if !pendingShipOnlyIdempotentTail(original.ActionPlan[candidate.ActionCursor:]) {
		return nil, false, nil
	}
	return original.ActionPlan, true, nil
}

// pendingShipOnlyIdempotentTail 判断剩余动作是否只包含平台侧幂等的状态动作。
func pendingShipOnlyIdempotentTail(remaining []db.AutomationAction) bool {
	if len(remaining) == 0 {
		return false
	}
	// action 是剩余动作中的一个，需要确认其不会再次联系买家。
	for _, action := range remaining {
		if action.ActionType != ActionConfirmShipment {
			return false
		}
	}
	return true
}

// claimPendingShipAttempt 领取一次兜底尝试；处于冷却窗口内的订单返回 false。
func (s *Scheduler) claimPendingShipAttempt(orderID string) bool {
	if s == nil {
		return false
	}
	s.pendingShipMu.Lock()
	defer s.pendingShipMu.Unlock()
	if s.pendingShipCooldown == nil {
		// pendingShipCooldown 延迟初始化，兼容测试或历史调用方直接构造 Scheduler 的场景。
		s.pendingShipCooldown = make(map[string]time.Time)
	}
	// now 是本次冷却判断的统一时间基准，避免同一轮内多次取时。
	now := time.Now()
	// last、seen 保存该订单上次兜底触发时间以及是否存在。
	if last, seen := s.pendingShipCooldown[orderID]; seen && now.Sub(last) < defaultPendingShipCooldown {
		return false
	}
	s.pendingShipCooldown[orderID] = now
	if len(s.pendingShipCooldown) > 4096 {
		// key、ts 是过期条目的订单号与记录时间。
		for key, ts := range s.pendingShipCooldown {
			if now.Sub(ts) >= defaultPendingShipCooldown {
				delete(s.pendingShipCooldown, key)
			}
		}
	}
	return true
}

// releasePendingShipAttempt 释放已知未取得数据库运行权的冷却预约。
func (s *Scheduler) releasePendingShipAttempt(orderID string) {
	if s == nil {
		return
	}
	s.pendingShipMu.Lock()
	defer s.pendingShipMu.Unlock()
	delete(s.pendingShipCooldown, orderID)
}
