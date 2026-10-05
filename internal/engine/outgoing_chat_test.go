package engine

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"xianyu-go/internal/automation"
	"xianyu-go/internal/xianyu/ws"
)

// TestReleaseReliabilityEchoMIDPriority 验证旧请求的迟到保护不会拦截新请求的精确 mid 回显。
func TestReleaseReliabilityEchoMIDPriority(t *testing.T) {
	// tracker 保存同一会话同一正文的并发出站等待器。
	tracker := newOutgoingEchoTracker()
	// old 保存已经超时并进入迟到保护的旧请求。
	old := tracker.register("chat", "buyer", "text", "hello", "mid-old")
	old.cancel()
	// current 保存随后登记、必须由独立 mid 精确确认的新请求。
	current := tracker.register("chat", "buyer", "text", "hello", "mid-new")
	tracker.observePlatform(OutgoingChatMessage{ChatID: "chat", BuyerID: "buyer", MessageType: "text", Text: "hello", RequestID: "mid-new", MessageKey: "pnm-new"})
	select {
	case <-current.done:
		// 精确响应已确认新请求。
	default:
		t.Fatal("新请求的精确 mid 回显被旧请求保护记录拦截")
	}
	select {
	case <-old.done:
		t.Fatal("旧请求不应被新请求回显复活")
	default:
	}
}

// TestReleaseReliabilityUnmatchedMIDDoesNotPoisonPNM 验证未匹配的旧 mid 不会占用后续有效回显的 PNM 去重位。
func TestReleaseReliabilityUnmatchedMIDDoesNotPoisonPNM(t *testing.T) {
	// tracker 保存同一会话同一正文的待确认请求。
	tracker := newOutgoingEchoTracker()
	// old 保存已经取消的旧请求；它的迟到响应不应登记为已消费 PNM。
	old := tracker.register("chat", "buyer", "text", "hello", "mid-old")
	old.cancel()
	// current 保存新请求，使用相同正文和平台消息 ID 验证去重登记时机。
	current := tracker.register("chat", "buyer", "text", "hello", "mid-new")
	// message 保存两次请求共享的脱敏平台回显摘要。
	message := OutgoingChatMessage{ChatID: "chat", BuyerID: "buyer", MessageType: "text", Text: "hello", MessageKey: "pnm-reused"}
	message.RequestID = "mid-old"
	tracker.observePlatform(message)
	message.RequestID = "mid-new"
	tracker.observePlatform(message)
	select {
	case <-current.done:
		// 新请求已由自身精确回显确认。
	default:
		t.Fatal("未匹配的旧 mid 错误占用 PNM，导致新请求无法确认")
	}
}

// TestReleaseReliabilityPushDoesNotConsumeMIDProtection 验证无 mid 推送不会消费带 mid 的旧取消保护记录。
func TestReleaseReliabilityPushDoesNotConsumeMIDProtection(t *testing.T) {
	// tracker 保存同一会话同一正文的带身份等待状态。
	tracker := newOutgoingEchoTracker()
	// old 保存带 mid 的已取消请求；无 mid 推送不能把它当作自己的迟到回显。
	old := tracker.register("chat", "buyer", "text", "hello", "mid-old")
	old.cancel()
	// current 保存新的精确请求，验证旧保护记录不会阻塞后续确认。
	current := tracker.register("chat", "buyer", "text", "hello", "mid-new")
	tracker.observePlatform(OutgoingChatMessage{ChatID: "chat", BuyerID: "buyer", MessageType: "text", Text: "hello", MessageKey: "pnm-push"})
	tracker.observePlatform(OutgoingChatMessage{ChatID: "chat", BuyerID: "buyer", MessageType: "text", Text: "hello", RequestID: "mid-new", MessageKey: "pnm-response"})
	select {
	case <-current.done:
		// 精确响应成功确认新请求。
	default:
		t.Fatal("无 mid 推送错误消费带 mid 取消保护，导致新请求无法确认")
	}
}

// outgoingObserverHandler 用于本次流程后续判断的outgoingObserverHandler
type outgoingObserverHandler struct {
	messages []OutgoingChatMessage
}

// HandleChatMessage 处理聊天消息。
func (h *outgoingObserverHandler) HandleChatMessage(context.Context, ChatMessage) error { return nil }

// HandleSystemEvent 处理系统Event。
func (h *outgoingObserverHandler) HandleSystemEvent(context.Context, automation.Task) error {
	return nil
}

// OnPasswordLoginRefresh 封装On密码登录Refresh业务协调。
func (h *outgoingObserverHandler) OnPasswordLoginRefresh(context.Context, string) bool { return false }

// OnAccountAlert 封装On账号Alert业务协调。
func (h *outgoingObserverHandler) OnAccountAlert(context.Context, string, string, string, string) {}

// HandleOutgoingChatMessage 处理Outgoing聊天消息。
func (h *outgoingObserverHandler) HandleOutgoingChatMessage(_ context.Context, message OutgoingChatMessage) error {
	h.messages = append(h.messages, message)
	return nil
}

// TestSendTextEmitsCorrelatedOutgoingObservation 封装TestSend文本EmitsCorrelatedOutgoingObservation业务协调。
func TestSendTextEmitsCorrelatedOutgoingObservation(t *testing.T) {
	// handler 用于本次流程后续判断的handler
	handler := &outgoingObserverHandler{}
	// account 用于本次流程后续判断的账号
	account := New(Config{CookieID: "account-1", CookieStr: "unb=me", Handler: handler})
	// conn 用于本次流程后续判断的conn
	conn := &fakeWSConn{}
	account.mu.Lock()
	account.conn = conn
	account.mu.Unlock()
	// ctx 用于本次流程后续判断的ctx
	ctx := WithOutgoingMessageKey(context.Background(), "local-1")
	if // err 用于本次流程后续判断的err
	err := account.SendText(ctx, "chat-1", "buyer-1", "您好"); err != nil {
		t.Fatal(err)
	}
	if len(handler.messages) != 1 {
		t.Fatalf("messages=%+v", handler.messages)
	}
	// got 用于本次流程后续判断的got
	got := handler.messages[0]
	if got.AccountID != "account-1" || got.ChatID != "chat-1" || got.BuyerID != "buyer-1" || got.Text != "您好" || got.MessageKey != "local-1" {
		t.Fatalf("observation=%+v", got)
	}
}

// TestAutomationSendTextWaitsForOwnEcho 验证自动化文本只有收到匹配自身回显后才返回成功。
func TestAutomationSendTextWaitsForOwnEcho(t *testing.T) {
	// handler 是接收本地出站旁路的测试处理器；本测试只关注发送确认，不依赖数据库。
	handler := &outgoingObserverHandler{}
	// account 是绑定回显确认器的账号运行时。
	account := New(Config{CookieID: "echo-account", CookieStr: "unb=self", Handler: handler})
	// conn 是只记录发送参数的 WebSocket 替身。
	conn := &fakeWSConn{}
	account.runtimeMu.Lock()
	account.conn = conn
	account.runtimeMu.Unlock()
	account.outgoing.echoWaitTimeout = time.Second
	// result 保存异步自动化发送等待回显的结果。
	result := make(chan error, 1)
	go func() {
		result <- account.SendText(WithOutgoingEchoConfirmation(context.Background()), "chat-echo", "buyer-echo", "赠品内容")
	}()
	// deadline 限制测试等待发送写入的最长时间，避免测试替身异常时永久阻塞。
	deadline := time.Now().Add(time.Second)
	// sentBeforeEcho 表示测试替身在注入回显前是否确实收到了一次文本发送。
	sentBeforeEcho := false
	for time.Now().Before(deadline) {
		conn.mu.Lock()
		// sent 表示测试 WebSocket 已经收到的文本数量。
		sent := len(conn.sentTexts)
		conn.mu.Unlock()
		if sent == 1 {
			sentBeforeEcho = true
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !sentBeforeEcho {
		t.Fatal("自动化文本未写入 WebSocket")
	}
	// requestID 读取当前等待器绑定的请求 mid，模拟协议层将同一 mid 带回发送响应。
	account.outgoing.echoTracker.mu.Lock()
	// requestID 保存当前自动化等待器绑定的请求 mid。
	var requestID string
	// waiter 保存当前正文键下的请求等待器列表；只有首个非空请求 mid 可用于本测试回显。
	for _, waiter := range account.outgoing.echoTracker.pending {
		if len(waiter) > 0 {
			requestID = waiter[0].requestID
			break
		}
	}
	account.outgoing.echoTracker.mu.Unlock()
	if requestID == "" {
		t.Fatal("自动化发送未登记请求 mid")
	}
	// echo 是与发送参数一致的账号自身回显摘要。
	account.outgoing.echoTracker.observe(OutgoingChatMessage{ChatID: "chat-echo@goofish", BuyerID: "buyer-echo@goofish", RequestID: requestID, MessageType: "text", Text: "赠品内容"})
	select {
	// err 是自动化发送在收到自身回显后的最终结果。
	case err := <-result:
		if err != nil {
			t.Fatalf("收到自身回显后发送仍失败: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("收到自身回显后发送未结束")
	}
}

// TestAutomationSendTextEchoTimeoutIsUncertain 验证回显超时不会返回可安全重试的确定未发送错误。
func TestAutomationSendTextEchoTimeoutIsUncertain(t *testing.T) {
	// account 是配置极短确认预算的测试账号。
	account := New(Config{CookieID: "echo-timeout", CookieStr: "unb=self"})
	// conn 是记录文本写入但不产生回显的 WebSocket 替身。
	conn := &fakeWSConn{}
	account.runtimeMu.Lock()
	account.conn = conn
	account.runtimeMu.Unlock()
	account.outgoing.echoWaitTimeout = 10 * time.Millisecond
	// err 是发送已写入 WebSocket 但未观察到自身回显的结果。
	err := account.SendText(WithOutgoingEchoConfirmation(context.Background()), "chat-timeout", "buyer-timeout", "待确认赠品")
	if err == nil || !errors.Is(err, errOutgoingEchoUnconfirmed) {
		t.Fatalf("回显超时错误=%v，期望不确定错误", err)
	}
	conn.mu.Lock()
	defer conn.mu.Unlock()
	if len(conn.sentTexts) != 1 {
		t.Fatalf("回显超时不应重复写入，发送次数=%d", len(conn.sentTexts))
	}
}

// TestPlatformSendResponseWakesOutgoingEchoWaiter 验证已由协议层核验的发送响应会唤醒自动化确认等待项。
func TestPlatformSendResponseWakesOutgoingEchoWaiter(t *testing.T) {
	// account 是带有账号级出站回显跟踪器的测试运行时。
	account := New(Config{CookieID: "response-echo", CookieStr: "unb=self"})
	// waiter 是在发送开始前登记的文本确认等待项。
	waiter := account.outgoing.echoTracker.register("chat-response", "buyer-response", "text", "赠品内容")
	// responseEcho 是 ws 层已经完成正文、发送者和消息 ID 核验的非敏感响应摘要。
	responseEcho := ws.OutgoingEcho{ChatID: "chat-response@goofish", BuyerID: "buyer-response@goofish", MessageKey: "response-echo.PNM", MessageType: "text", Text: "赠品内容"}
	account.outgoing.observePlatformSendResponse(responseEcho)
	// waitErr 是响应观察唤醒后等待项的结果；它必须在有限预算内成功。
	waitErr := waiter.wait(context.Background(), 100*time.Millisecond)
	if waitErr != nil {
		t.Fatalf("发送响应未唤醒回显等待项：%v", waitErr)
	}
}

// TestMessageDispatcherWakesOutgoingEchoWaiter 验证消息分发器先把自身回显交给确认器，再执行本地旁路。
func TestMessageDispatcherWakesOutgoingEchoWaiter(t *testing.T) {
	// tracker 是账号级回显确认状态。
	tracker := newOutgoingEchoTracker()
	// waiter 是预先登记的自动化文本等待项。
	waiter := tracker.register("chat-dispatch", "buyer-dispatch", "text", "回显文本")
	// dispatcher 是只保留回显观察能力的消息分发器。
	dispatcher := newMessageDispatcher(messageDispatcherConfig{
		CookieID:        "echo-dispatch-account",
		CurrentCookie:   func() string { return "unb=self-dispatch" },
		ObserveOutgoing: tracker.observe,
	})
	// raw 是账号自身普通文本回显的最小协议夹具。
	raw := map[string]any{"1": map[string]any{
		"2": "chat-dispatch@goofish",
		"10": map[string]any{
			"reminderContent": "回显文本",
			"senderUserId":    "self-dispatch",
			"reminderUrl":     "fleamarket://message_chat?peerUserId=buyer-dispatch",
		},
	}}
	dispatcher.handleMessageContext(context.Background(), raw)
	// waitErr 是分发器处理回显后等待项的结果。
	waitErr := waiter.wait(context.Background(), 100*time.Millisecond)
	if waitErr != nil {
		t.Fatalf("分发器未唤醒自身回显等待项: %v", waitErr)
	}
}

// TestPlatformEchoIDCannotConfirmSecondSameContentSend 验证同一平台消息回显不能确认后续相同正文发送。
func TestPlatformEchoIDCannotConfirmSecondSameContentSend(t *testing.T) {
	// tracker 保存隔离的账号级发送确认状态。
	tracker := newOutgoingEchoTracker()
	// first 是第一条相同正文消息的等待项。
	first := tracker.register("chat-id", "buyer-id", "text", "相同正文")
	tracker.observeMessage(OutgoingChatMessage{ChatID: "chat-id", BuyerID: "buyer-id", MessageType: "text", Text: "相同正文", MessageKey: "pnm-1"})
	// err 保存第一条消息等待平台回显的结果。
	if err := first.wait(context.Background(), 20*time.Millisecond); err != nil {
		t.Fatalf("首条平台回显未确认: %v", err)
	}
	// second 是第一条确认完成后重新登记的相同正文消息。
	second := tracker.register("chat-id", "buyer-id", "text", "相同正文")
	tracker.observeMessage(OutgoingChatMessage{ChatID: "chat-id", BuyerID: "buyer-id", MessageType: "text", Text: "相同正文", MessageKey: "pnm-1"})
	// err 保存第二条消息等待重复平台 ID 的结果；它必须超时而不是成功。
	if err := second.wait(context.Background(), 20*time.Millisecond); err == nil {
		t.Fatal("重复平台消息 ID 错误确认了第二条发送")
	}
}

// TestConcurrentSameContentResponsesUseRequestID 验证两个相同正文并发发送时，乱序平台响应只能唤醒对应请求。
func TestConcurrentSameContentResponsesUseRequestID(t *testing.T) {
	// tracker 保存隔离的账号级出站回显等待状态。
	tracker := newOutgoingEchoTracker()
	// first、second 保存携带不同请求 mid 的两条同文等待项。
	first := tracker.register("chat-concurrent", "buyer-concurrent", "text", "同一份卡密", "mid-first")
	// second 保存第二个并发同文请求的等待器。
	second := tracker.register("chat-concurrent", "buyer-concurrent", "text", "同一份卡密", "mid-second")
	// 无请求 mid 的异步推送无法判断归属，不能用正文 FIFO 误确认任一并发等待项。
	tracker.observePlatform(OutgoingChatMessage{ChatID: "chat-concurrent", BuyerID: "buyer-concurrent", MessageKey: "pnm-push", MessageType: "text", Text: "同一份卡密"})
	select {
	case <-first.done:
		t.Fatal("无请求标识的异步推送错误确认了第一个同文等待项")
	case <-second.done:
		t.Fatal("无请求标识的异步推送错误确认了第二个同文等待项")
	default:
	}
	// 先注入第二个请求的响应，验证正文 FIFO 不会错误消费第一个等待项。
	tracker.observePlatform(OutgoingChatMessage{ChatID: "chat-concurrent", BuyerID: "buyer-concurrent", RequestID: "mid-second", MessageKey: "pnm-push", MessageType: "text", Text: "同一份卡密"})
	select {
	case <-first.done:
		t.Fatal("第二个请求响应错误确认了第一个同文等待项")
	default:
	}
	select {
	case <-second.done:
	default:
		t.Fatal("第二个请求响应未确认对应同文等待项")
	}
	// 再注入第一个请求的响应，确认剩余等待项仍能按请求 mid 收口。
	tracker.observePlatform(OutgoingChatMessage{ChatID: "chat-concurrent", BuyerID: "buyer-concurrent", RequestID: "mid-first", MessageKey: "pnm-first", MessageType: "text", Text: "同一份卡密"})
	// err 保存第一个请求收到精确回显后的等待结果。
	if err := first.wait(context.Background(), 20*time.Millisecond); err != nil {
		t.Fatalf("第一个请求响应未确认对应同文等待项: %v", err)
	}
}

// TestLateEchoAfterCanceledSendCannotConfirmNextSameContentSend 验证首条发送取消后的新平台回显不会确认第二条同文发送。
func TestLateEchoAfterCanceledSendCannotConfirmNextSameContentSend(t *testing.T) {
	// tracker 保存本账号并发发送的回显确认状态。
	tracker := newOutgoingEchoTracker()
	// first 是已经超时或取消、但平台回显可能仍会迟到的首条发送。
	first := tracker.register("chat-late", "buyer-late", "text", "相同正文")
	first.cancel()
	// second 是取消后重新登记的同一会话同一正文发送。
	second := tracker.register("chat-late", "buyer-late", "text", "相同正文")
	tracker.observeMessage(OutgoingChatMessage{ChatID: "chat-late", BuyerID: "buyer-late", MessageType: "text", Text: "相同正文", MessageKey: "late-first-pnm"})
	// err 保存第二条发送等待回显的结果；首条迟到回显只能被保护窗口消费，不能确认第二条。
	if err := second.wait(context.Background(), 20*time.Millisecond); err == nil {
		t.Fatal("首条发送的迟到回显错误确认了第二条发送")
	}
}

// TestDefiniteSendFailureDoesNotProtectNextSameContentSend 验证确定未发送时不会吞掉下一条同文消息的合法回显。
func TestDefiniteSendFailureDoesNotProtectNextSameContentSend(t *testing.T) {
	// tracker 保存隔离的账号级回显确认状态。
	tracker := newOutgoingEchoTracker()
	// first 是平台明确拒绝后被移除的首条发送。
	first := tracker.register("chat-definite", "buyer-definite", "text", "同文内容")
	first.cancelWithoutLateProtection()
	// second 是确定失败后重新登记的同一会话同一正文发送。
	second := tracker.register("chat-definite", "buyer-definite", "text", "同文内容")
	tracker.observeMessage(OutgoingChatMessage{ChatID: "chat-definite", BuyerID: "buyer-definite", MessageType: "text", Text: "同文内容", MessageKey: "pnm-second"})
	// err 保存第二条发送的回显确认结果；合法回显必须直接唤醒它。
	if err := second.wait(context.Background(), 20*time.Millisecond); err != nil {
		t.Fatalf("确定未发送后的合法回显未确认: %v", err)
	}
}

// TestAutomationSendTextDefiniteFailureDoesNotCreateLateProtection 验证运行时明确未发送时不会留下迟到回显保护记录。
func TestAutomationSendTextDefiniteFailureDoesNotCreateLateProtection(t *testing.T) {
	// account 是绑定自动化回显确认器的测试账号。
	account := New(Config{CookieID: "definite-send", CookieStr: "unb=self"})
	// conn 是返回确定未发送错误并记录调用的 WebSocket 替身。
	conn := &fakeWSConn{sendErr: automation.ErrMessageNotSent}
	account.runtimeMu.Lock()
	account.conn = conn
	account.runtimeMu.Unlock()
	// err 保存明确失败的自动化发送结果。
	err := account.SendText(WithOutgoingEchoConfirmation(context.Background()), "chat-definite", "buyer-definite", "同文内容")
	if !errors.Is(err, automation.ErrMessageNotSent) {
		t.Fatalf("明确未发送错误未保留: %v", err)
	}
	account.outgoing.echoTracker.mu.Lock()
	// canceledCount 保存明确失败后迟到回显保护记录的剩余数量。
	canceledCount := len(account.outgoing.echoTracker.canceledPending)
	// pendingCount 保存明确失败后待确认回显记录的剩余数量。
	pendingCount := len(account.outgoing.echoTracker.pending)
	account.outgoing.echoTracker.mu.Unlock()
	if canceledCount != 0 || pendingCount != 0 {
		t.Fatalf("明确失败不应留下回显状态 canceled=%d pending=%d", canceledCount, pendingCount)
	}
}

// TestCanceledEchoProtectionPrunesExpiredKeys 验证新增发送或观察其他会话时会回收过期保护记录。
func TestCanceledEchoProtectionPrunesExpiredKeys(t *testing.T) {
	// tracker 保存可直接检查内部回收状态的回显跟踪器。
	tracker := newOutgoingEchoTracker()
	// expiredKey 是已经超过保护窗口的历史发送键。
	expiredKey := outgoingEchoKey{chatID: "chat-expired", messageType: "text", content: "旧内容"}
	tracker.mu.Lock()
	tracker.canceledPending[expiredKey] = []canceledEchoRecord{{at: time.Now().Add(-canceledEchoRetentionWindow - time.Second)}}
	tracker.mu.Unlock()
	tracker.register("chat-new", "buyer-new", "text", "新内容")
	tracker.mu.Lock()
	// remaining 保存主动回收后仍存在的取消保护记录数量。
	remaining := len(tracker.canceledPending)
	tracker.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("过期取消保护未回收，remaining=%d", remaining)
	}
}

// TestCanceledEchoProtectionBoundsSingleKey 验证同一正文键的取消保护记录保持有界。
func TestCanceledEchoProtectionBoundsSingleKey(t *testing.T) {
	// tracker 保存反复取消同一正文的账号级保护状态。
	tracker := newOutgoingEchoTracker()
	// index 表示当前模拟发送的序号，用于构造独立请求身份。
	for index := 0; index < maxCanceledEchoRecordsPerKey*2; index++ {
		// requestID 为每次模拟发送生成独立的请求身份。
		requestID := fmt.Sprintf("mid-%d", index)
		// waiter 保存当前需要进入迟到保护的发送等待项。
		waiter := tracker.register("chat-bounded", "buyer-bounded", "text", "重复正文", requestID)
		waiter.cancel()
	}
	// key、recordCount 保存同一正文键及其保护记录数量。
	key := outgoingEchoKey{chatID: "chat-bounded", messageType: "text", content: "重复正文"}
	tracker.mu.Lock()
	// recordCount 表示限流后保留的取消保护记录数。
	recordCount := len(tracker.canceledPending[key])
	tracker.mu.Unlock()
	if recordCount > maxCanceledEchoRecordsPerKey {
		t.Fatalf("单键取消保护记录未限流: count=%d limit=%d", recordCount, maxCanceledEchoRecordsPerKey)
	}
}

// TestExtractOwnWebSocketEchoExtractsImageContent 验证图片自身回显可以提取媒体 URL 用于确认。
func TestExtractOwnWebSocketEchoExtractsImageContent(t *testing.T) {
	// raw 是包含 contentType=2 图片正文的自身回显夹具。
	raw := map[string]any{"1": map[string]any{
		"2": "chat-image@goofish",
		"10": map[string]any{
			"reminderContent": "[图片]",
			"senderUserId":    "self-image",
			"extJson":         `{"contentType":"2"}`,
		},
		"6": map[string]any{"3": map[string]any{
			"4": 2,
			"5": `{"contentType":2,"image":{"pics":[{"url":"https://cdn.example/gift.png"}]}}`,
		}},
	}}
	// echo 是解析后的非敏感图片回显摘要。
	echo := extractOwnWebSocketEcho(raw, "image-account", "unb=self-image")
	if echo == nil || echo.MessageType != "image" || echo.Content != "https://cdn.example/gift.png" {
		t.Fatalf("图片回显解析错误: %+v", echo)
	}
}
