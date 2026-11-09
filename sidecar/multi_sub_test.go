package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParseSubscriptions(t *testing.T) {
	// 单 URL
	single := "https://example.com/sub"
	res := parseSubscriptions(single)
	if len(res) != 1 || res[0].URL != single || res[0].Tag != "" {
		t.Fatalf("unexpected single parse result: %+v", res)
	}

	// 多订阅 JSON
	jsonStr := `[{"url":"https://a.com/sub","tag":"主力"},{"url":"https://b.com/sub","tag":"备用"}]`
	res2 := parseSubscriptions(jsonStr)
	if len(res2) != 2 {
		t.Fatalf("expected 2 items, got: %d", len(res2))
	}
	if res2[0].Tag != "主力" || res2[1].Tag != "备用" {
		t.Fatalf("unexpected tags: %+v", res2)
	}

	// 空串
	if parseSubscriptions("") != nil {
		t.Fatalf("expected nil for empty input")
	}
}

func TestResolveSubscriptions(t *testing.T) {
	// 自动从 host 提取 tag + 去重
	items := []subscriptionItem{
		{URL: "https://sub.airport-one.com/link", Tag: ""},
		{URL: "https://sub.airport-one.com/link2", Tag: ""},
	}
	normalized, errCode := resolveSubscriptions("", &items)
	if errCode != "" {
		t.Fatalf("expected no error, got: %s", errCode)
	}
	if len(normalized) != 2 {
		t.Fatalf("expected 2 items")
	}
	if normalized[0].Tag != "airport-one" {
		t.Errorf("expected tag airport-one, got: %s", normalized[0].Tag)
	}
	if normalized[1].Tag != "airport-one-2" {
		t.Errorf("expected tag airport-one-2, got: %s", normalized[1].Tag)
	}

	// 包含非法私网地址
	badItems := []subscriptionItem{
		{URL: "https://example.com/sub", Tag: "主力"},
		{URL: "http://192.168.1.1/sub", Tag: "备用"},
	}
	_, badCode := resolveSubscriptions("", &badItems)
	if badCode != "bad_url_private" {
		t.Errorf("expected bad_url_private, got: %s", badCode)
	}

	// 经由 rawURL 传入带竖线分隔的订阅（非 JSON 单串输入）
	pipeSubs, pipeCode := resolveSubscriptions("https://sub.airport-one.com/link|https://sub.airport-two.com/link", nil)
	if pipeCode != "" {
		t.Fatalf("expected no error for pipe separated subs, got: %s", pipeCode)
	}
	if len(pipeSubs) != 2 {
		t.Fatalf("expected 2 items for pipe separated subs, got: %d", len(pipeSubs))
	}
	if pipeSubs[0].Tag != "airport-one" || pipeSubs[1].Tag != "airport-two" {
		t.Errorf("unexpected tags for pipe subs: %+v", pipeSubs)
	}
}

func TestStage1MixedMulti_Tolerance(t *testing.T) {
	// 启动 mock upstream 引擎
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "fail") || strings.Contains(r.URL.RawQuery, "fail") {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("airport down"))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ss://YWVzLTEyOC1nY206cGFzc3dvcmQ=@1.1.1.1:8388#NodeA\n"))
	}))
	defer mockServer.Close()

	srv := &server{
		cfg: config{
			extendedBase:    mockServer.URL,
			subProxyTimeout: 5 * time.Second,
		},
		isub: &internalSubStore{
			entries:  map[string]internalSubEntry{},
			bySource: map[string]internalSubEntry{},
			client:   mockServer.Client(),
		},
	}

	subs := []subscriptionItem{
		{URL: mockServer.URL + "/healthy-airport", Tag: "主力"},
		{URL: mockServer.URL + "/fail-airport", Tag: "备用"},
	}

	ctx := context.Background()
	uris, _, activeTags, fail := srv.stage1MixedMulti(ctx, mockServer.Client(), "clash", subs, 2*time.Second)
	if fail != nil {
		t.Fatalf("expected tolerance when one airport fails, but got fail: %+v", fail)
	}
	if len(uris) != 1 {
		t.Fatalf("expected 1 uri from healthy airport, got: %d", len(uris))
	}
	if !strings.Contains(uris[0], "%5B%E4%B8%BB%E5%8A%9B%5D") { // "[主力]" URL-encoded
		t.Errorf("expected [主力] tag in node URI: %s", uris[0])
	}
	if len(activeTags) != 1 || activeTags[0] != "主力" {
		t.Errorf("expected activeTags to be ['主力'], got: %v", activeTags)
	}
}
