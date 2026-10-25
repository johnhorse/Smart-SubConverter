// 内部订阅代拉的代理接线单测：订阅走 SUBSCRIPTION_PROXY，与 SNAPSHOT_PROXY 解耦（v1.3.8）。
package main

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestInternalSubStoreProxyWiring(t *testing.T) {
	subProxy, _ := url.Parse("http://127.0.0.1:7890")
	snapProxy, _ := url.Parse("http://127.0.0.1:9999")
	req, _ := http.NewRequest(http.MethodGet, "https://airport.example/sub", nil)

	// 配了 subscriptionProxy：Go client 的 Transport 走该代理，curl -x 同值
	st := newInternalSubStore(config{subscriptionProxy: subProxy, snapshotProxy: snapProxy})
	tr, ok := st.client.Transport.(*http.Transport)
	if !ok || tr.Proxy == nil {
		t.Fatalf("subscriptionProxy 配置后应有带 Proxy 的 Transport，得到 %T", st.client.Transport)
	}
	if got, err := tr.Proxy(req); err != nil || got == nil || got.String() != subProxy.String() {
		t.Errorf("Transport 应走 subscriptionProxy %s，得到 %v err=%v", subProxy, got, err)
	}
	if st.proxyURL != subProxy.String() {
		t.Errorf("proxyURL 应为 %s，得到 %q", subProxy, st.proxyURL)
	}

	// 只配 snapshotProxy：订阅代拉必须直连（两个代理解耦）
	st = newInternalSubStore(config{snapshotProxy: snapProxy})
	if st.client.Transport != nil {
		t.Errorf("仅配 snapshotProxy 时订阅 client 不应带代理 Transport，得到 %T", st.client.Transport)
	}
	if st.proxyURL != "" {
		t.Errorf("仅配 snapshotProxy 时 proxyURL 应为空，得到 %q", st.proxyURL)
	}
}

// sanitizeURLErr：*url.Error 的 URL（含 token/节点凭据）不得进日志，Op 与内层错误保留；非 url.Error 原样
func TestSanitizeURLErr(t *testing.T) {
	inner := errors.New("connection refused")
	ue := &url.Error{Op: "Get", URL: "https://airport.example/sub?token=SECRET", Err: inner}
	got := sanitizeURLErr(ue).Error()
	if strings.Contains(got, "airport.example") || strings.Contains(got, "SECRET") {
		t.Errorf("脱敏失败，URL 泄漏: %q", got)
	}
	if !strings.Contains(got, "Get") || !strings.Contains(got, "connection refused") {
		t.Errorf("应保留 Op 与内层错误: %q", got)
	}
	if got := sanitizeURLErr(inner).Error(); got != "connection refused" {
		t.Errorf("非 url.Error 应原样: %q", got)
	}
}
