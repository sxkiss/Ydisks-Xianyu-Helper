package server

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// TestDecodeCardDraftTracksMetadataPresence 验证卡券更新能区分省略规格字段和显式清空规格字段。
func TestDecodeCardDraftTracksMetadataPresence(t *testing.T) {
	// omittedRequest 是只提交名称和类型的兼容更新请求。
	omittedRequest := httptest.NewRequest("PUT", "/api/v1/cards/1", strings.NewReader(`{"name":"改名","type":"data"}`))
	// omittedDraft 保存省略规格字段后的应用输入。
	omittedDraft, omittedErr := decodeCardDraft(omittedRequest)
	if omittedErr != nil || omittedDraft.DataContentSet || omittedDraft.IsMultiSpecSet || omittedDraft.SpecNameSet || omittedDraft.SpecValueSet {
		t.Fatalf("省略规格字段被误判为显式提交 draft=%+v err=%v", omittedDraft, omittedErr)
	}
	// explicitRequest 是显式关闭多规格并清空名称和值的更新请求。
	explicitRequest := httptest.NewRequest("PUT", "/api/v1/cards/1", strings.NewReader(`{"name":"改名","type":"data","data_content":"","is_multi_spec":false,"spec_name":"","spec_value":""}`))
	// explicitDraft 保存显式规格字段后的应用输入。
	explicitDraft, explicitErr := decodeCardDraft(explicitRequest)
	if explicitErr != nil || !explicitDraft.DataContentSet || !explicitDraft.IsMultiSpecSet || !explicitDraft.SpecNameSet || !explicitDraft.SpecValueSet || explicitDraft.IsMultiSpec || explicitDraft.SpecName != "" || explicitDraft.SpecValue != "" {
		t.Fatalf("显式规格清空未保留请求意图 draft=%+v err=%v", explicitDraft, explicitErr)
	}
}
