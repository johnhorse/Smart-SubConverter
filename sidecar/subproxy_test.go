// 短链端点单测：GET /s/{code} 代理（miss/hit/UA 透传/流式/上游故障/超时/token 注入）、
// 管理端点（列表形状、DELETE 幂等、token 鉴权两态）。
package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// newLink 建一条短链并返回短码
func newLink(t *testing.T, s *server, subURL, target, presetID, configURL string) string {
	t.Helper()
	code, err := s.store.getOrCreateLink(subURL, target, presetID, configURL)
	if err != nil {
		t.Fatalf("getOrCreateLink: %v", err)
	}
	return code
}

func get(t *testing.T, url string) *http.Response {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func doReq(t *testing.T, method, url string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestShortLinkMiss404(t *testing.T) {
	s := mustServer(t, testConfig(t, "http://unused"))
	ts := httptest.NewServer(s.routes())
	defer ts.Close()
	resp := get(t, ts.URL+"/s/NOPEcode")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("miss 状态码 = %d，应 404", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if strings.TrimSpace(string(body)) != `{"error":"not_found"}` {
		t.Errorf("miss body = %s", body)
	}
}

// hit 透传：上游收到的 target/url/config 为冻结值；典型 UA 原样透传；body 透传
func TestShortLinkHitPassthrough(t *testing.T) {
	var gotTarget, gotURL, gotConfig, gotUA, gotToken string
	ext := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		gotTarget, gotURL, gotConfig, gotToken = q.Get("target"), q.Get("url"), q.Get("config"), q.Get("token")
		gotUA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("proxies:\n  - name: fixture"))
	}))
	defer ext.Close()
	s := mustServer(t, testConfig(t, ext.URL))
	code := newLink(t, s, "https://sub.example.com/l", "auto", "default", "https://example.com/preset.ini")
	ts := httptest.NewServer(s.routes())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/s/"+code, nil)
	req.Header.Set("User-Agent", "ClashforWindows/0.20.39")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("hit 状态码 = %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "proxies:\n  - name: fixture" {
		t.Errorf("body 透传失败: %q", body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Errorf("Content-Type 透传失败: %q", ct)
	}
	if gotTarget != "auto" || gotURL != "https://sub.example.com/l" || gotConfig != "https://example.com/preset.ini" {
		t.Errorf("上游参数 = target=%s url=%s config=%s，应为冻结值", gotTarget, gotURL, gotConfig)
	}
	if gotUA != "ClashforWindows/0.20.39" {
		t.Errorf("UA 透传失败: %q", gotUA)
	}
	if gotToken != "" {
		t.Errorf("SUB_TOKEN 为空时上游不应有 token: %q", gotToken)
	}
}

// 空 UA：上游收到空串（绝不能是 Go-http-client/1.1——OH-12 Transport 补默认 UA 陷阱）
func TestShortLinkEmptyUA(t *testing.T) {
	var gotUA string
	ext := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		_, _ = w.Write([]byte("proxies: x"))
	}))
	defer ext.Close()
	s := mustServer(t, testConfig(t, ext.URL))
	code := newLink(t, s, "https://sub.example.com/l", "auto", "default", "https://example.com/preset.ini")
	ts := httptest.NewServer(s.routes())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/s/"+code, nil)
	req.Header.Set("User-Agent", "") // 显式空串 → 客户端省略该头，服务端 Get 为 ""
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d", resp.StatusCode)
	}
	if gotUA != "" {
		t.Errorf("空 UA 应透传为空，上游收到 %q", gotUA)
	}
	if gotUA == "Go-http-client/1.1" {
		t.Error("UA 未显式 Set，被 Transport 补了默认值")
	}
}

// 上游 5xx → 502 upstream_unavailable
func TestShortLinkUpstream5xx(t *testing.T) {
	ext := stubExtended(t, 500, "boom", 0)
	defer ext.Close()
	s := mustServer(t, testConfig(t, ext.URL))
	code := newLink(t, s, "https://sub.example.com/l", "clash", "default", "https://example.com/preset.ini")
	ts := httptest.NewServer(s.routes())
	defer ts.Close()
	resp := get(t, ts.URL+"/s/"+code)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("上游 5xx → 状态码 %d，应 502", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if strings.TrimSpace(string(body)) != `{"error":"upstream_unavailable"}` {
		t.Errorf("502 body = %s", body)
	}
}

// v1.3.1 错误内容协商（契约「GET /s/{code}」第 5 条）：
// Accept: text/html（浏览器直开）→ 同状态码人话 HTML；客户端/程序化（*/json）→ JSON 不变
func TestShortLinkErrorHTMLNegotiation(t *testing.T) {
	ext := stubExtended(t, 400, "unresolved target", 0) // 模拟 UA 未识别 → 引擎 400
	defer ext.Close()
	s := mustServer(t, testConfig(t, ext.URL))
	code := newLink(t, s, "https://sub.example.com/l", "auto", "default", "https://example.com/preset.ini")
	ts := httptest.NewServer(s.routes())
	defer ts.Close()

	getWithAccept := func(path, accept string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, ts.URL+path, nil)
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	// 浏览器直开（上游 400 → 502）：人话 HTML，状态码与 JSON 形态一致
	resp := getWithAccept("/s/"+code, "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("Accept html → 状态码 %d，应 502", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("Content-Type = %q，应 text/html", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	for _, want := range []string{"订阅链接", "客户端"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("人话页应含 %q：\n%s", want, body)
		}
	}

	// 程序化消费（无 Accept，Go 默认不发）：JSON 不变
	resp2 := get(t, ts.URL+"/s/"+code)
	defer resp2.Body.Close()
	body2, _ := io.ReadAll(resp2.Body)
	if strings.TrimSpace(string(body2)) != `{"error":"upstream_unavailable"}` {
		t.Errorf("无 Accept → body = %s，应 JSON", body2)
	}

	// 明确 json 优先：JSON 不变
	resp3 := getWithAccept("/s/"+code, "application/json")
	defer resp3.Body.Close()
	body3, _ := io.ReadAll(resp3.Body)
	if strings.TrimSpace(string(body3)) != `{"error":"upstream_unavailable"}` {
		t.Errorf("Accept json → body = %s，应 JSON", body3)
	}

	// 浏览器直开不存在的短码（404）：人话 404
	resp4 := getWithAccept("/s/zzzzzzzz", "text/html")
	defer resp4.Body.Close()
	if resp4.StatusCode != http.StatusNotFound {
		t.Fatalf("miss + html → 状态码 %d，应 404", resp4.StatusCode)
	}
	if ct := resp4.Header.Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("miss + html Content-Type = %q，应 text/html", ct)
	}
	body4, _ := io.ReadAll(resp4.Body)
	if !strings.Contains(string(body4), "不存在或已被撤销") {
		t.Errorf("miss 人话页应含撤销说明：\n%s", body4)
	}
}

// 上游连不上 → 502
func TestShortLinkUpstreamDown(t *testing.T) {
	ext := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := ext.URL
	ext.Close()
	s := mustServer(t, testConfig(t, url))
	code := newLink(t, s, "https://sub.example.com/l", "clash", "default", "https://example.com/preset.ini")
	ts := httptest.NewServer(s.routes())
	defer ts.Close()
	resp := get(t, ts.URL+"/s/"+code)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("上游不可达 → 状态码 %d，应 502", resp.StatusCode)
	}
}

// 超时（SUB_PROXY_TIMEOUT）→ 502
func TestShortLinkTimeout(t *testing.T) {
	ext := stubExtended(t, 200, "proxies: x", 500*time.Millisecond)
	defer ext.Close()
	cfg := testConfig(t, ext.URL)
	cfg.subProxyTimeout = 120 * time.Millisecond
	s := mustServer(t, cfg)
	code := newLink(t, s, "https://sub.example.com/l", "clash", "default", "https://example.com/preset.ini")
	ts := httptest.NewServer(s.routes())
	defer ts.Close()
	resp := get(t, ts.URL+"/s/"+code)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("超时 → 状态码 %d，应 502", resp.StatusCode)
	}
}

// 流式透传：上游首块发出但整体未完时，客户端应先收到首块（不整包缓冲）
func TestShortLinkStreaming(t *testing.T) {
	ch := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(ch) }) }
	ext := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("part1"))
		w.(http.Flusher).Flush()
		<-ch // 模拟订阅生成慢：首块已发、整体未完
		_, _ = w.Write([]byte("part2"))
	}))
	defer ext.Close()
	defer release()
	s := mustServer(t, testConfig(t, ext.URL))
	code := newLink(t, s, "https://sub.example.com/l", "clash", "default", "https://example.com/preset.ini")
	ts := httptest.NewServer(s.routes())
	defer ts.Close()

	resp := get(t, ts.URL+"/s/"+code)
	defer resp.Body.Close()
	buf := make([]byte, 5)
	res := make(chan error, 1)
	go func() {
		_, err := io.ReadFull(resp.Body, buf)
		res <- err
	}()
	select {
	case err := <-res:
		if err != nil || string(buf) != "part1" {
			t.Fatalf("首块 = %q (err %v)", buf, err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("首块未在上游整体完成前到达——不是流式透传")
	}
	release()
	rest, err := io.ReadAll(resp.Body)
	if err != nil || string(rest) != "part2" {
		t.Errorf("收尾块 = %q (err %v)", rest, err)
	}
}

// SUB_TOKEN 非空：token 只在容器内注入上游；朋友的 /s/ 请求本身不带 token 也通（OH-1）
func TestShortLinkTokenInjected(t *testing.T) {
	var gotToken string
	ext := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.URL.Query().Get("token")
		_, _ = w.Write([]byte("proxies: x"))
	}))
	defer ext.Close()
	cfg := testConfig(t, ext.URL)
	cfg.subToken = "sec"
	s := mustServer(t, cfg)
	code := newLink(t, s, "https://sub.example.com/l", "clash", "default", "https://example.com/preset.ini")
	ts := httptest.NewServer(s.routes())
	defer ts.Close()
	resp := get(t, ts.URL+"/s/"+code) // 不带 token
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/s/ 不应要求 token，状态码 = %d", resp.StatusCode)
	}
	if gotToken != "sec" {
		t.Errorf("上游 token = %q，应容器内注入 sec", gotToken)
	}
}

// 管理端点（局域网档 SUB_TOKEN 空 → 开放）：列表形状 + DELETE 幂等 + 删除后短链立即 404
func TestAdminEndpointsOpenMode(t *testing.T) {
	ext := stubExtended(t, 200, "proxies: x", 0)
	defer ext.Close()
	s := mustServer(t, testConfig(t, ext.URL))
	code1 := newLink(t, s, "https://sub.example.com/l", "surge", "full_multimode", fullMultiModeURL)
	code2 := newLink(t, s, "https://sub.example.com/m", "clash", "default", "https://example.com/preset.ini")
	ts := httptest.NewServer(s.routes())
	defer ts.Close()

	resp := get(t, ts.URL+"/api/links") // 无 token 也通（局域网档开放）
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/links 状态码 = %d", resp.StatusCode)
	}
	var raw struct {
		Links []map[string]any `json:"links"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Fatal(err)
	}
	if len(raw.Links) != 2 {
		t.Fatalf("links 条数 = %d，应 2", len(raw.Links))
	}
	wantKeys := []string{"code", "target", "preset", "sourceUrl", "createdAt"}
	for i, l := range raw.Links {
		for _, k := range wantKeys {
			if _, ok := l[k]; !ok {
				t.Errorf("links[%d] 缺字段 %s: %v", i, k, l)
			}
		}
	}
	var surgeEntry map[string]any
	for _, l := range raw.Links {
		if l["code"] == code1 {
			surgeEntry = l
		}
	}
	if surgeEntry == nil || surgeEntry["target"] != "surge" || surgeEntry["preset"] != "full_multimode" ||
		surgeEntry["sourceUrl"] != "https://sub.example.com/l" {
		t.Errorf("code1 条目 = %v", surgeEntry)
	}

	// DELETE 幂等：两次都 204；之后 /s/{code} 立即 404
	for i := 0; i < 2; i++ {
		del := doReq(t, http.MethodDelete, ts.URL+"/api/links/"+code1)
		del.Body.Close()
		if del.StatusCode != http.StatusNoContent {
			t.Fatalf("第 %d 次 DELETE 状态码 = %d，应恒 204", i+1, del.StatusCode)
		}
	}
	miss := get(t, ts.URL+"/s/"+code1)
	miss.Body.Close()
	if miss.StatusCode != http.StatusNotFound {
		t.Errorf("删除后 /s/ 状态码 = %d，应 404", miss.StatusCode)
	}
	keep := get(t, ts.URL+"/s/"+code2)
	keep.Body.Close()
	if keep.StatusCode != http.StatusOK {
		t.Errorf("未删的 %s 状态码 = %d，不应受影响", code2, keep.StatusCode)
	}
}

// 管理端点鉴权：SUB_TOKEN 非空 → 缺失/不符 401，匹配通过
func TestAdminTokenAuth(t *testing.T) {
	cfg := testConfig(t, "http://unused")
	cfg.subToken = "sec"
	s := mustServer(t, cfg)
	ts := httptest.NewServer(s.routes())
	defer ts.Close()

	cases := []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/api/links", http.StatusUnauthorized},
		{http.MethodGet, "/api/links?token=wrong", http.StatusUnauthorized},
		{http.MethodGet, "/api/links?token=sec", http.StatusOK},
		{http.MethodDelete, "/api/links/AAAAAAAA", http.StatusUnauthorized},
		{http.MethodDelete, "/api/links/AAAAAAAA?token=sec", http.StatusNoContent},
	}
	for _, c := range cases {
		resp := doReq(t, c.method, ts.URL+c.path)
		resp.Body.Close()
		if resp.StatusCode != c.want {
			t.Errorf("%s %s → %d, want %d", c.method, c.path, resp.StatusCode, c.want)
		}
	}
}
