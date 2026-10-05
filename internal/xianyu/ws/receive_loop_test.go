package ws

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// startWSEchoServer 启动一个本地 WS 服务：升级后发送一条同步推送消息，再读取并忽略 ACK，
// 最后关闭连接。返回服务 URL。用于驱动 ReceiveLoop 的消息分发测试。
// startWSEchoServer 封装开始WSEchoServer业务协调。
func startWSEchoServer(t *testing.T, payload string) *httptest.Server {
	t.Helper()
	// srv 用于本次流程后续判断的srv
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// c、err 用于本次流程后续判断的c、err
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close(websocket.StatusNormalClosure, "")
		// 构造同步推送帧：body.syncPushPackage.data[0].data = base64(payload)。
		b64 := base64.StdEncoding.EncodeToString([]byte(payload))
		// frame 用于本次流程后续判断的frame
		frame := map[string]any{
			"lwp":     "/s/sync",
			"headers": map[string]any{"mid": "m1", "sid": "s1"},
			"body": map[string]any{"syncPushPackage": map[string]any{
				"data": []any{map[string]any{"data": b64}},
			}},
		}
		// raw 用于本次流程后续判断的原始
		raw, _ := json.Marshal(frame)
		if // err 用于本次流程后续判断的err
		err := c.Write(r.Context(), websocket.MessageText, raw); err != nil {
			return
		}
		// 读掉 ACK 后关闭（触发客户端 Read 返回错误，结束 ReceiveLoop）。
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		_, _, _ = c.Read(ctx)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// wsURL 把 httptest 的 http:// URL 转成 ws://。
func wsURL(srv *httptest.Server) string {
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

// TestReceiveLoop_DecodesSyncPayload 验证 ReceiveLoop 收到同步推送帧后：
// 解码 base64+JSON → 调用 onMessage → 回 ACK（服务端能读到）→ 连接关闭后退出。
// TestReceiveLoop_DecodesSyncPayload 封装TestReceiveLoopDecodesSync请求载荷业务协调。
func TestReceiveLoop_DecodesSyncPayload(t *testing.T) {
	// payload 用于本次流程后续判断的请求载荷
	payload := `{"event":"paid","order_id":"o1"}`
	// srv 用于本次流程后续判断的srv
	srv := startWSEchoServer(t, payload)

	// dialCtx、dialCancel 用于本次流程后续判断的dialCtx、dial取消
	dialCtx, dialCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer dialCancel()
	// dialed、err 用于本次流程后续判断的dialed、err
	dialed, _, err := websocket.Dial(dialCtx, wsURL(srv), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer dialed.CloseNow()
	dialed.SetReadLimit(8 << 20)

	// conn 用于本次流程后续判断的conn
	conn := newConn(dialed, Config{}, nilLogger())

	// got 用于本次流程后续判断的got
	var got map[string]any
	// loopDone 用于本次流程后续判断的loopDone
	loopDone := make(chan error, 1)
	// ctx、cancel 用于本次流程后续判断的ctx、cancel
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() {
		loopDone <- conn.ReceiveLoop(ctx, func(decrypted map[string]any) {
			got = decrypted
		})
	}()

	select {
	case <-loopDone:
	case <-time.After(3 * time.Second):
		t.Fatal("ReceiveLoop 未在超时内退出")
	}
	if got == nil || got["event"] != "paid" || got["order_id"] != "o1" {
		t.Fatalf("onMessage 未收到解码结果: %#v", got)
	}
}

// TestReceiveLoop_DispatchesEverySyncPayload 验证同步帧内的后续付款卡片也会进入分发，且整帧只回复一次 ACK。
func TestReceiveLoop_DispatchesEverySyncPayload(t *testing.T) {
	// ackRead 用于确认本地服务端收到了客户端对整个同步帧的确认。
	ackRead := make(chan struct{}, 1)
	// srv 提供含两条同步数据的本地 WebSocket，模拟状态变更排在付款卡片之前的真实帧。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// serverConn 和 acceptErr 分别保存升级后的连接及升级失败原因。
		serverConn, acceptErr := websocket.Accept(w, r, nil)
		if acceptErr != nil {
			return
		}
		defer serverConn.Close(websocket.StatusNormalClosure, "")
		// firstPayload 是前置的订单状态变更，模拟同帧 data[0] 的非付款卡片。
		firstPayload := base64.StdEncoding.EncodeToString([]byte(`{"event":"state_changed","order_id":"o1"}`))
		// secondPayload 是必须继续分发的付款事件，模拟同帧 data[1] 的付款卡片。
		secondPayload := base64.StdEncoding.EncodeToString([]byte(`{"event":"paid","order_id":"o1"}`))
		// frame 是一个含多条 data 的平台同步帧，第二条模拟被旧实现静默丢弃的付款卡片，第三条验证坏条目不会中止整帧。
		frame := map[string]any{
			"lwp":     "/s/sync",
			"headers": map[string]any{"mid": "m-multi", "sid": "s-multi"},
			"body": map[string]any{"syncPushPackage": map[string]any{
				"data": []any{map[string]any{"data": firstPayload}, map[string]any{"data": secondPayload}, map[string]any{"data": "not-base64"}},
			}},
		}
		// raw 和 marshalErr 分别保存待发送帧及其序列化错误。
		raw, marshalErr := json.Marshal(frame)
		if marshalErr != nil {
			return
		}
		// writeErr 是测试服务端发送同步帧时发生的传输错误。
		if writeErr := serverConn.Write(r.Context(), websocket.MessageText, raw); writeErr != nil {
			return
		}
		// ackCtx 限制测试服务器等待帧级 ACK 的时间，避免测试因客户端异常永久阻塞。
		ackCtx, ackCancel := context.WithTimeout(r.Context(), time.Second)
		defer ackCancel()
		// readErr 是测试服务端等待客户端帧级 ACK 时发生的读取错误。
		if _, _, readErr := serverConn.Read(ackCtx); readErr == nil {
			ackRead <- struct{}{}
		}
	}))
	defer srv.Close()

	// dialCtx 和 dialCancel 限制本地测试 WebSocket 的建连时间。
	dialCtx, dialCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer dialCancel()
	// dialed 和 dialErr 分别保存测试客户端连接及建连错误。
	dialed, _, dialErr := websocket.Dial(dialCtx, wsURL(srv), nil)
	if dialErr != nil {
		t.Fatalf("dial: %v", dialErr)
	}
	defer dialed.CloseNow()
	dialed.SetReadLimit(8 << 20)

	// ackCount 记录客户端发出的 ACK 数量，以确保多条 data 不会生成重复确认。
	ackCount := 0
	// decryptFailureCount 记录当前帧中不能解码的条目，证明其不会阻断后续的帧级确认或其他业务条目。
	decryptFailureCount := 0
	// conn 使用记录器统计协议输出，不暴露或保存任何平台原始密文。
	conn := newConn(dialed, Config{Recorder: func(direction, rawText, parsedJSON, parseStatus, errMsg string) {
		if direction == "out" && parseStatus == "json" {
			ackCount++
		}
		if direction == "in" && parseStatus == "decrypt_failed" {
			decryptFailureCount++
		}
	}}, nilLogger())
	// received 保存回调按帧内顺序接收的业务事件名。
	received := make([]string, 0, 2)
	// loopDone 接收 ReceiveLoop 关闭连接后的最终结果。
	loopDone := make(chan error, 1)
	// receiveCtx 和 receiveCancel 限制整个分发测试的生命周期。
	receiveCtx, receiveCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer receiveCancel()
	go func() {
		loopDone <- conn.ReceiveLoop(receiveCtx, func(decrypted map[string]any) {
			// event 保存当前已解码同步消息的业务事件名。
			event, _ := decrypted["event"].(string)
			received = append(received, event)
		})
	}()

	select {
	case <-loopDone:
	case <-time.After(3 * time.Second):
		t.Fatal("ReceiveLoop 未在超时内退出")
	}
	select {
	case <-ackRead:
	case <-time.After(time.Second):
		t.Fatal("服务端未收到同步帧 ACK")
	}
	if ackCount != 1 {
		t.Fatalf("ACK 数量 = %d, 期望 1", ackCount)
	}
	if decryptFailureCount != 1 {
		t.Fatalf("解密失败条目数量 = %d, 期望 1", decryptFailureCount)
	}
	if len(received) != 2 || received[0] != "state_changed" || received[1] != "paid" {
		t.Fatalf("同步消息分发顺序 = %#v", received)
	}
}

// TestReceiveLoop_NonJSONSkipped 非 JSON 消息应被跳过，不回调 onMessage，循环继续。
func TestReceiveLoop_NonJSONSkipped(t *testing.T) {
	// srv 用于本次流程后续判断的srv
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// c、err 用于本次流程后续判断的c、err
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close(websocket.StatusNormalClosure, "")
		// 先发一条非 JSON，再发一条合法同步推送。
		c.Write(r.Context(), websocket.MessageText, []byte("not-json"))
		// b64 用于本次流程后续判断的b64
		b64 := base64.StdEncoding.EncodeToString([]byte(`{"ok":true}`))
		// frame 用于本次流程后续判断的frame
		frame := map[string]any{
			"lwp":     "/s/sync",
			"headers": map[string]any{"mid": "m2"},
			"body":    map[string]any{"syncPushPackage": map[string]any{"data": []any{map[string]any{"data": b64}}}},
		}
		// raw 用于本次流程后续判断的原始
		raw, _ := json.Marshal(frame)
		c.Write(r.Context(), websocket.MessageText, raw)
		// ctx、cancel 用于本次流程后续判断的ctx、cancel
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		_, _, _ = c.Read(ctx) // ACK
	}))
	defer srv.Close()

	// dialCtx、dialCancel 用于本次流程后续判断的dialCtx、dial取消
	dialCtx, dialCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer dialCancel()
	// dialed、err 用于本次流程后续判断的dialed、err
	dialed, _, err := websocket.Dial(dialCtx, wsURL(srv), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer dialed.CloseNow()
	dialed.SetReadLimit(8 << 20)

	// conn 用于本次流程后续判断的conn
	conn := newConn(dialed, Config{}, nilLogger())
	// got 用于本次流程后续判断的got
	var got map[string]any
	// loopDone 用于本次流程后续判断的loopDone
	loopDone := make(chan error, 1)
	// ctx、cancel 用于本次流程后续判断的ctx、cancel
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() {
		loopDone <- conn.ReceiveLoop(ctx, func(decrypted map[string]any) { got = decrypted })
	}()
	<-loopDone
	if got == nil || got["ok"] != true {
		t.Fatalf("应跳过非 JSON 并处理合法帧: %#v", got)
	}
}

// TestHeartbeatLoop_ContextCancel HeartbeatLoop 应在 ctx 取消时及时退出。
func TestHeartbeatLoop_ContextCancel(t *testing.T) {
	// srv 用于本次流程后续判断的srv
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// c、err 用于本次流程后续判断的c、err
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close(websocket.StatusNormalClosure, "")
		// 持续读，忽略心跳。
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		for {
			if // err 用于本次流程后续判断的err
			_, _, err := c.Read(ctx); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	// dialCtx、dialCancel 用于本次流程后续判断的dialCtx、dial取消
	dialCtx, dialCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer dialCancel()
	// dialed、err 用于本次流程后续判断的dialed、err
	dialed, _, err := websocket.Dial(dialCtx, wsURL(srv), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer dialed.CloseNow()
	dialed.SetReadLimit(8 << 20)

	// conn 用于本次流程后续判断的conn
	conn := newConn(dialed, Config{}, nilLogger())
	// ctx、cancel 用于本次流程后续判断的ctx、cancel
	ctx, cancel := context.WithCancel(context.Background())
	// loopDone 用于本次流程后续判断的loopDone
	loopDone := make(chan error, 1)
	go func() {
		loopDone <- conn.HeartbeatLoop(ctx, 50*time.Millisecond)
	}()
	// 让心跳发几次再取消。
	time.Sleep(200 * time.Millisecond)
	cancel()
	select {
	case // err 用于本次流程后续判断的err
	err := <-loopDone:
		if err != nil && err != context.Canceled {
			t.Fatalf("HeartbeatLoop 退出 err=%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("HeartbeatLoop 未在取消后退出")
	}
}

// nilLogger 返回一个丢弃所有输出的 slog.Logger，避免测试输出刷屏。
func nilLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
