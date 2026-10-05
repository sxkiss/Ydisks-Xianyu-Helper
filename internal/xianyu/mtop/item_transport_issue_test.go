package mtop

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/iotest"
)

// TestItemListQueriesRejectTransportFailures 验证商品列表在请求、读取及 HTTP 失败时不返回可执行全集；t 管理本地传输夹具。
func TestItemListQueriesRejectTransportFailures(t *testing.T) {
	// mode 覆盖发送失败、响应读取失败与非成功 HTTP 状态，不请求真实平台。
	for _, mode := range []string{"network", "read", "http"} {
		t.Run(mode, func(t *testing.T) {
			// failure 是用于验证错误链的确定性本地故障。
			failure := errors.New("合成传输故障")
			// client 将所有请求交给内存传输；响应中的成功业务内容不能覆盖 HTTP 失败。
			client := &ClientImpl{HTTPClient: &http.Client{Transport: cookieSessionRoundTripFunc(func(*http.Request) (*http.Response, error) {
				if mode == "network" {
					return nil, failure
				}
				// body 保存模拟平台成功响应或读取故障。
				var body io.Reader = strings.NewReader(`{"ret":["SUCCESS::调用成功"],"data":{"cardList":[]}}`)
				// status 决定响应是否属于必须拒绝的 HTTP 失败。
				status := http.StatusServiceUnavailable
				if mode == "read" {
					body, status = iotest.ErrReader(failure), http.StatusOK
				}
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(body)}, nil
			})}}
			// items、itemsErr 是全集查询结果，失败不得提供可用于删除本地商品的空列表。
			items, itemsErr := client.FetchAllItems(context.Background(), consignCookies, 20, 3)
			if itemsErr == nil || items != nil {
				t.Fatal("传输失败仍返回了可同步全集")
			}
			if mode != "http" && !errors.Is(itemsErr, failure) {
				t.Fatal("网络或读取错误丢失底层错误链")
			}
		})
	}
}
