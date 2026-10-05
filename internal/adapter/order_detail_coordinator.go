package adapter

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	"xianyu-go/internal/xianyu/mtop"
)

const (
	// defaultOrderDetailMinGap 是同一账号两次订单详情 MTOP 请求起点之间的最短等待时间，兼顾成交后发货时效与详情接口风控压力。
	defaultOrderDetailMinGap = 5 * time.Second
	// defaultOrderDetailJitter 是最短等待时间之上追加的随机延迟上限，避免多个账号的固定节奏形成可识别请求特征。
	defaultOrderDetailJitter = 5 * time.Second
	// defaultOrderDetailSuccessTTL 是同一订单成功响应的短暂复用窗口，覆盖重复 WS 投递和并发刷新，不长期缓存订单状态。
	defaultOrderDetailSuccessTTL = 30 * time.Second
)

// orderDetailRequest 是同一账号、同一订单正在进行的详情请求；done 仅由创建该记录的请求关闭，等待者读取 result 和 err。
type orderDetailRequest struct {
	// done 在平台调用结束后关闭，通知同订单的并发调用者复用同一结果。
	done chan struct{}
	// result 保存已完成平台调用的订单详情，仅在 done 关闭前写入。
	result *mtop.OrderDetailResult
	// err 保存已完成平台调用的错误，仅在 done 关闭前写入。
	err error
}

// orderDetailSuccess 是一条可短暂复用的成功订单详情；只缓存平台已成功解析的订单事实，不缓存错误或请求 Cookie。
type orderDetailSuccess struct {
	// result 保存平台成功响应的独立副本，读取时再次复制以隔离调用方修改。
	result *mtop.OrderDetailResult
	// expiresAt 是这条成功结果失效的墙钟时间，过期后下次请求重新访问平台。
	expiresAt time.Time
}

// OrderDetailCoordinator 统一协调订单详情 MTOP 请求。
// mu 保护 nextAllowed、requests 与 successes；锁内只管理内存状态，绝不执行定时等待或平台 I/O。
// 同一账号按最短间隔限流，同一账号同一订单的并发请求和短暂成功结果均可复用。
type OrderDetailCoordinator struct {
	// logger 记录订单详情限流、复用和平台访问结果，不记录 Cookie 或其他凭证内容。
	logger *slog.Logger
	// mu 保护每账号下一次允许时间和同订单在途请求表。
	mu sync.Mutex
	// nextAllowed 保存每个账号下一次允许开始订单详情请求的时间，不保存 Cookie 或其他敏感内容。
	nextAllowed map[string]time.Time
	// requests 保存以账号和订单组成的稳定键索引的在途请求，完成后立即删除。
	requests map[string]*orderDetailRequest
	// successes 保存尚未过期的成功详情，覆盖短时间重复事件；不保存错误避免阻断凭证续期后的重新请求。
	successes map[string]orderDetailSuccess
	// minGap 是同账号两次请求起点的基础间隔。
	minGap time.Duration
	// jitter 返回本次基础间隔之外的随机时长；测试可注入零值函数获得确定性时序。
	jitter func() time.Duration
	// successTTL 限制成功详情的短暂复用时长，零值仅供测试关闭缓存。
	successTTL time.Duration
}

// NewOrderDetailCoordinator 创建生产订单详情协调器；它不持有凭证，生命周期与装配它的运行时一致。
// logger 可选，未提供时使用默认日志器；日志内容只包含账号、订单和时序信息。
func NewOrderDetailCoordinator(loggers ...*slog.Logger) *OrderDetailCoordinator {
	// logger 保存生产运行时传入的结构化日志器，缺省时保持独立测试和旧调用方可用。
	var logger *slog.Logger
	if len(loggers) > 0 {
		logger = loggers[0]
	}
	return newOrderDetailCoordinatorWithLogger(logger, defaultOrderDetailMinGap, defaultOrderDetailJitter, func() time.Duration {
		// extra 表示本次在基础间隔之上追加的随机毫秒数，范围包含零且不超过默认抖动上限。
		extra := time.Duration(rand.Int64N(int64(defaultOrderDetailJitter/time.Millisecond)+1)) * time.Millisecond
		return extra
	})
}

// newOrderDetailCoordinator 创建可注入时间策略的协调器，仅供本包确定性测试使用。
func newOrderDetailCoordinator(minGap, maxJitter time.Duration, jitter func() time.Duration) *OrderDetailCoordinator {
	return newOrderDetailCoordinatorWithLogger(nil, minGap, maxJitter, jitter)
}

// newOrderDetailCoordinatorWithLogger 创建带日志器和可注入时间策略的订单详情协调器。
func newOrderDetailCoordinatorWithLogger(logger *slog.Logger, minGap, maxJitter time.Duration, jitter func() time.Duration) *OrderDetailCoordinator {
	if minGap < 0 {
		minGap = 0
	}
	if maxJitter < 0 {
		maxJitter = 0
	}
	if jitter == nil {
		jitter = func() time.Duration { return 0 }
	}
	if logger == nil {
		logger = slog.Default()
	}
	// boundedJitter 防止测试或未来配置把负数、过大的随机值带入请求节奏。
	boundedJitter := func() time.Duration {
		// delay 是调用方提供的本次随机等待值。
		delay := jitter()
		if delay < 0 {
			return 0
		}
		if delay > maxJitter {
			return maxJitter
		}
		return delay
	}
	return &OrderDetailCoordinator{
		logger:      logger,
		nextAllowed: make(map[string]time.Time),
		requests:    make(map[string]*orderDetailRequest),
		successes:   make(map[string]orderDetailSuccess),
		minGap:      minGap,
		jitter:      boundedJitter,
		successTTL:  defaultOrderDetailSuccessTTL,
	}
}

// Fetch 在账号级限流后调用订单详情客户端；同一账号、同一订单的并发调用等待首个调用完成并复用其结果。
// ctx 仅控制当前等待者和首个调用的平台请求；cookies 只传给当前 MTOP 调用，协调器不保存、记录或比较其内容。
func (c *OrderDetailCoordinator) Fetch(ctx context.Context, accountID, orderID, cookies string, fetcher orderDetailMTop) (*mtop.OrderDetailResult, error) {
	if c == nil {
		return nil, errors.New("订单详情协调器未初始化")
	}
	if fetcher == nil {
		return nil, errors.New("订单详情 MTOP 客户端未配置")
	}
	// normalizedAccount、normalizedOrder 是去除空白后的非敏感路由标识；为空时禁止建立会互相污染的共享键。
	normalizedAccount, normalizedOrder := strings.TrimSpace(accountID), strings.TrimSpace(orderID)
	if normalizedAccount == "" || normalizedOrder == "" {
		return nil, errors.New("订单详情请求缺少账号或订单标识")
	}
	// key 唯一标识一个账号对一笔订单的在途请求，分隔符不能出现在已规范化的业务标识中。
	key := normalizedAccount + "\x00" + normalizedOrder
	c.mu.Lock()
	// cached、cachedOK 保存尚未过期的成功结果；错误不进入缓存，确保凭证恢复可立即重新发起受限请求。
	if cached, cachedOK := c.successes[key]; cachedOK {
		if time.Now().Before(cached.expiresAt) {
			c.mu.Unlock()
			c.logger.Debug("复用短时有效的订单详情结果", "account", normalizedAccount, "order_id", normalizedOrder)
			return cloneOrderDetailResult(cached.result), nil
		}
		delete(c.successes, key)
	}
	if // existing 表示已由其他协程发起的平台调用；后到调用方不得额外访问平台。
	existing := c.requests[key]; existing != nil {
		c.mu.Unlock()
		c.logger.Debug("订单详情请求已在执行，等待复用结果", "account", normalizedAccount, "order_id", normalizedOrder)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-existing.done:
			return cloneOrderDetailResult(existing.result), existing.err
		}
	}
	// request 是当前调用拥有的唯一平台访问权，完成时负责删除并关闭 done。
	request := &orderDetailRequest{done: make(chan struct{})}
	c.requests[key] = request
	c.mu.Unlock()

	// startedAt 记录本次平台请求从协调器开始执行的时刻，用于输出可检索的耗时日志。
	startedAt := time.Now()
	// result、callErr 保存限流后唯一一次平台访问的结果；所有等待者通过 request 读取同一终态。
	result, callErr := c.fetchAfterAccountGap(ctx, normalizedAccount, normalizedOrder, cookies, fetcher)
	// elapsed 表示本次等待限流和平台访问的总耗时。
	elapsed := time.Since(startedAt).Round(time.Millisecond)
	c.mu.Lock()
	request.result, request.err = cloneOrderDetailResult(result), callErr
	if callErr == nil && result != nil && c.successTTL > 0 {
		// expiresAt 表示成功详情仍可复用的截止时刻，短 TTL 不会把订单状态长期当作缓存来源。
		expiresAt := time.Now().Add(c.successTTL)
		c.successes[key] = orderDetailSuccess{result: cloneOrderDetailResult(result), expiresAt: expiresAt}
	}
	delete(c.requests, key)
	close(request.done)
	c.mu.Unlock()
	if callErr != nil {
		c.logger.Warn("订单详情请求失败", "account", normalizedAccount, "order_id", normalizedOrder, "elapsed", elapsed, "err", callErr)
	} else {
		c.logger.Debug("订单详情请求完成", "account", normalizedAccount, "order_id", normalizedOrder, "elapsed", elapsed)
	}
	return result, callErr
}

// fetchAfterAccountGap 等待账号的下一个可用时隙后执行一次平台请求；请求起点而非完成时间决定下一次允许时间。
func (c *OrderDetailCoordinator) fetchAfterAccountGap(ctx context.Context, accountID, orderID, cookies string, fetcher orderDetailMTop) (*mtop.OrderDetailResult, error) {
	for {
		// now、allowedAt 分别表示当前观察时间和该账号最近一次请求设定的下一时隙。
		now := time.Now()
		c.mu.Lock()
		// allowedAt 是当前账号因先前请求而受到的下一次可访问平台时间。
		allowedAt := c.nextAllowed[accountID]
		if !now.Before(allowedAt) {
			// nextGap 是基础间隔加受控随机抖动，保障同账号不会形成固定的一秒重试节奏。
			nextGap := c.minGap + c.jitter()
			c.nextAllowed[accountID] = now.Add(nextGap)
			c.mu.Unlock()
			return fetcher.FetchOrderDetail(ctx, cookies, orderID)
		}
		c.mu.Unlock()
		// waitFor 表示当前账号距离下一次允许发起订单详情请求还需等待的时长。
		waitFor := time.Until(allowedAt)
		c.logger.Info("订单详情请求按账号限流等待", "account", accountID, "order_id", orderID, "wait", waitFor.Round(time.Millisecond))
		// timer 只负责当前账号的等待；Context 取消时立即停止，不占用未来请求时隙。
		timer := time.NewTimer(waitFor)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

// cloneOrderDetailResult 返回独立结果副本，避免一个等待者修改共享结果影响其他调用方。
func cloneOrderDetailResult(result *mtop.OrderDetailResult) *mtop.OrderDetailResult {
	if result == nil {
		return nil
	}
	// copied 保存只含订单事实和响应 Cookie 平面值的值副本；不复制或输出请求 Cookie。
	copied := *result
	return &copied
}
