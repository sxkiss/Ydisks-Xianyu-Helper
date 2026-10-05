package mtop

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// TestItemSyncTotalConsistency 验证准确总数允许完整同步，分页途中总数变化则拒绝提交；t 管理场景。
func TestItemSyncTotalConsistency(t *testing.T) {
	// changes 表示平台总数是否在第二页发生变化。
	for _, changes := range []bool{false, true} {
		// pages 统计本地平台返回的页号，避免重复 ID 遮蔽总数校验。
		var pages atomic.Int32
		// server 按序返回两页商品，w 输出带准确总数的响应。
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			// page 是本次返回页号；total 是这次响应宣告的总条数。
			page, total := pages.Add(1), 2
			if changes && page == 2 {
				total = 3
			}
			fmt.Fprintf(w, `{"ret":["SUCCESS::调用成功"],"data":{"totalCount":%d,"pageCount":2,"cardList":[{"cardData":{"id":"item-%d"}}]}}`, total, page)
		}))
		// client 只连接当前本地列表服务。
		client := &ClientImpl{HTTPClient: &http.Client{Transport: &rewriteTransport{base: server.Client().Transport, target: server.URL}}}
		// result、err 是完整性校验后的全集结果。
		result, err := client.FetchAllItems(context.Background(), consignCookies, 1, 3)
		server.Close()
		if changes {
			if err == nil || result != nil {
				t.Fatal("总数变化仍返回了可同步全集")
			}
		} else if err != nil || result == nil || len(result.Items) != 2 {
			t.Fatalf("完整分页应成功: %v", err)
		}
	}
}

// TestItemSyncRejectsMalformedPages 验证异常或不完整页不能变成远端全集，导致本地商品软删除；t 管理本地服务。
func TestItemSyncRejectsMalformedPages(t *testing.T) {
	// body 是不能安全参与全量同步的页面内容。
	for _, body := range []string{`{"cardList":{}}`, `{"cardList":null}`, `{"cardList":[null]}`, `{"cardList":[{}]}`, `{"cardList":[{"cardData":{}}]}`, `{"cardList":[],"pageCount":3}`, `{"cardList":[],"totalCount":1}`, `{"cardList":[{"cardData":{"id":"item"}}],"pageCount":1,"totalCount":2}`} {
		// server 按 body 返回平台成功状态下的异常数据，w 写入响应。
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprintf(w, `{"ret":["SUCCESS::调用成功"],"data":%s}`, body)
		}))
		// client 将固定商品 API 地址改写到本地服务，不请求真实平台。
		client := &ClientImpl{HTTPClient: &http.Client{Transport: &rewriteTransport{base: server.Client().Transport, target: server.URL}}}
		// result、err 是全量同步结果，失败时必须没有可提交的部分列表。
		result, err := client.FetchAllItems(context.Background(), consignCookies, 20, 3)
		server.Close()
		if err == nil || result != nil {
			t.Fatal("畸形或提前结束的分页不能作为完整列表")
		}
	}
}

// TestItemSyncRejectsRepeatedPages 验证远端重复返回同一页时及时失败，不能无限翻页或把重复列表用于删除；t 管理断言。
func TestItemSyncRejectsRepeatedPages(t *testing.T) {
	// server 总是返回同一个商品，w 输出固定列表。
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"ret":["SUCCESS::调用成功"],"data":{"cardList":[{"cardData":{"id":"same-item"}}],"pageCount":3}}`)
	}))
	defer server.Close()
	// client 通过本地重写传输隔离真实闲鱼网络。
	client := &ClientImpl{HTTPClient: &http.Client{Transport: &rewriteTransport{base: server.Client().Transport, target: server.URL}}}
	// result、err 是重复页的全量读取结果。
	result, err := client.FetchAllItems(context.Background(), consignCookies, 1, 3)
	if err == nil || result != nil {
		t.Fatal("重复商品分页未被拒绝")
	}
}

// TestItemSyncAcceptsOmittedCardsWithPagination 保留原 pageCount=2 夹具并按权威空列表协议断言成功；t 管理本地 HTTP 服务，显式畸形数组仍由负向测试拒绝。
func TestItemSyncAcceptsOmittedCardsWithPagination(t *testing.T) {
	// server 返回省略 cardList 且没有非零商品总数的成功响应，页数不能替代商品数量。
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { // w 只写入隔离的空商品成功夹具。
		fmt.Fprint(w, `{"ret":["SUCCESS::调用成功"],"data":{"pageCount":2}}`)
	}))
	defer server.Close()
	// client 仅访问测试服务，不调用真实账号。
	client := &ClientImpl{HTTPClient: &http.Client{Transport: &rewriteTransport{base: server.Client().Transport, target: server.URL}}}
	// result、err 保存全量查询结果，不能由页数制造商品或继续请求不存在的下一页。
	result, err := client.FetchAllItems(context.Background(), consignCookies, 20, 3)
	if err != nil || result == nil || result.TotalCount != 0 || result.TotalPages != 1 || len(result.Items) != 0 {
		t.Fatalf("成功省略 cardList 必须得到完整空列表: err=%v", err)
	}
}
