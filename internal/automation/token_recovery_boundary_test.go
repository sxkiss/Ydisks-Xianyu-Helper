package automation

import (
	"context"
	"fmt"
	"testing"

	"xianyu-go/internal/xianyu/mtop"
)

// TestActionsDoNotRenewAccountForTokenExpiry 验证发货和改价内部 Token 刷新耗尽后不请求账号续期；t 管理本地存储。
func TestActionsDoNotRenewAccountForTokenExpiry(t *testing.T) {
	// action 区分确认发货与订单改价这两个有远端副作用的业务入口。
	for _, action := range []string{"consign", "adjust"} {
		// t 是本次动作的隔离断言上下文。
		t.Run(action, func(t *testing.T) {
			// store、cleanup 提供账号凭证及关闭责任。
			store, cleanup := newAutomationTestStore(t)
			defer cleanup()
			// tokenErr 保留旧版包装文案，验证其不能把结构化 Token 错误升级为 Session。
			tokenErr := fmt.Errorf("token API 登录凭证已失效: %w", &mtop.MTopResponseError{Kind: mtop.MTopErrorTokenExpired})
			// client 模拟已经完成内部刷新但仍失败的 MTOP 调用。
			client := &fakeMTop{consignErr: tokenErr, adjustErr: tokenErr}
			// recoverer 记录本次动作是否错误地升级到账号续期。
			recoverer := &fakeCredentialRecoverer{store: store}
			// center 装配真实业务动作执行器，所有平台副作用使用本地替身。
			center := NewWithDependencies(store, nil, nil, CenterDependencies{MTop: client, OrderDetailFetcher: recoverer})
			// task 是本地合成订单身份，不能请求真实闲鱼订单。
			task := Task{AccountID: "cid", OrderID: "local-order"}
			// err 保留动作执行结果，失败不能伪装为已完成。
			var err error
			if action == "consign" {
				err = center.actions.confirmShipmentAttempt(context.Background(), task, shipmentDeliveryProof{}, true)
			} else {
				err = center.actions.adjustOrderPriceAttempt(context.Background(), task, 100, true)
			}
			if err == nil || !mtop.IsMTopTokenExpiredErr(err) || recoverer.calls != 0 || client.consignCalls+client.adjustCalls != 1 {
				t.Fatalf("Token 失败不得续期账号或重放动作: err=%v recover=%d consign=%d adjust=%d", err, recoverer.calls, client.consignCalls, client.adjustCalls)
			}
		})
	}
}
