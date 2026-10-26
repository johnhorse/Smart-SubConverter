// 短链端点单测：GET /s/{code} 代理（miss/hit/UA 透传/流式/上游故障/超时/token 注入）、
// 管理端点（列表形状、DELETE 幂等、token 鉴权两态）。
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	req.Header.Set("User-Agent", "Stash/2.8.0") // v1.4.0 起 auto+clash 系 UA 走两段式，直通靶换 stash UA
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
	if gotUA != "Stash/2.8.0" {
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
	code := newLink(t, s, "https://sub.example.com/l", "loon", "default", "https://example.com/preset.ini") // 直通靶 target（clash 显式 v1.4.0 起两段式，另有专测）
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
	code := newLink(t, s, "https://sub.example.com/l", "loon", "default", "https://example.com/preset.ini")
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
	code := newLink(t, s, "https://sub.example.com/l", "loon", "default", "https://example.com/preset.ini")
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
	code := newLink(t, s, "https://sub.example.com/l", "loon", "default", "https://example.com/preset.ini")
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
	code := newLink(t, s, "https://sub.example.com/l", "loon", "default", "https://example.com/preset.ini")
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
	code2 := newLink(t, s, "https://sub.example.com/m", "loon", "default", "https://example.com/preset.ini")
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

// v1.3.3 QX 两段式：mixed 代拉机场 → 明文节点列表 → quanx（server_local）；
// 判定 = 显式 quanx 或 auto+UA 宽匹配 quantumult
func TestShortLinkQuanxTwoStage(t *testing.T) {
	uriA := "ss://YWJjLTEyOjEubTIuMy40OjQ0Mw==#a"
	uriB := "vmess://eyJ2IjoiMiJ9"
	// 机场 fixture（v1.3.5 代拉目标）
	airport := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(uriA + "\n" + uriB + "\n"))
	}))
	defer airport.Close()

	var gotTargets, gotURLs, gotConfigs []string
	ext := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		gotTargets = append(gotTargets, q.Get("target"))
		gotURLs = append(gotURLs, q.Get("url"))
		gotConfigs = append(gotConfigs, q.Get("config"))
		switch q.Get("target") {
		case "mixed":
			// 引擎 stub：真实 fetch 第一段 url（sidecar 内部回环 ref），把内容转成 mixed（base64 原样）
			content, err := fetchBody(q.Get("url"))
			if err != nil {
				http.Error(w, err.Error(), 502)
				return
			}
			_, _ = w.Write([]byte(base64.StdEncoding.EncodeToString(content)))
		case "quanx":
			_, _ = w.Write([]byte("[server_local]\nvmess = 5.6.7.8:443, tag=b\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ext.Close()
	s := mustServer(t, testConfig(t, ext.URL))
	code := newLink(t, s, airport.URL+"/sub", "quanx", "default", "https://example.com/preset.ini")
	ts := httptest.NewServer(s.routes())
	defer ts.Close()
	s.cfg.sidecarBase = ts.URL // 代拉 ref 注册到本测试 sidecar（引擎 stub 回拉可达）

	resp := get(t, ts.URL+"/s/"+code)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "vmess = 5.6.7.8") {
		t.Fatalf("两段式输出异常: %d %s", resp.StatusCode, body)
	}
	if len(gotTargets) != 2 || gotTargets[0] != "mixed" || gotTargets[1] != "quanx" {
		t.Errorf("应两段调用 mixed→quanx，实际 %v", gotTargets)
	}
	if !strings.Contains(gotURLs[0], "/internal/sub/") {
		t.Errorf("第一段 url 应为内部回环 ref（v1.3.5 代拉）: %q", gotURLs[0])
	}
	if gotURLs[1] != uriA+"|"+uriB {
		t.Errorf("第二段 url 应为 | 连接的节点列表: %q", gotURLs[1])
	}
	if gotConfigs[0] != "" || gotConfigs[1] == "" {
		t.Errorf("config 只应出现在第二段: %v", gotConfigs)
	}
}

// fetchBody 测试辅助：真实 GET（引擎 stub 拉内部回环 ref 用）
func fetchBody(u string) ([]byte, error) {
	resp, err := http.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}

// auto 链路由（契约 /s/ 第 4 条）：UA 宽匹配 quantumult / clash 系词表 → 两段式；
// 其余 UA（Surge 等）→ 单段原路径
func TestShortLinkAutoUA(t *testing.T) {
	airport := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("vmess://x\n"))
	}))
	defer airport.Close()
	var targets []string
	ext := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targets = append(targets, r.URL.Query().Get("target"))
		if r.URL.Query().Get("target") == "mixed" {
			content, err := fetchBody(r.URL.Query().Get("url"))
			if err != nil {
				http.Error(w, err.Error(), 502)
				return
			}
			_, _ = w.Write([]byte(base64.StdEncoding.EncodeToString(content)))
			return
		}
		_, _ = w.Write([]byte("proxies: ok"))
	}))
	defer ext.Close()
	s := mustServer(t, testConfig(t, ext.URL))
	code := newLink(t, s, airport.URL+"/sub", "auto", "default", "https://example.com/preset.ini")
	ts := httptest.NewServer(s.routes())
	defer ts.Close()
	s.cfg.sidecarBase = ts.URL

	targets = nil
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/s/"+code, nil)
	req.Header.Set("User-Agent", "Quantumult%20X%20v1.4.1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(targets) != 2 || targets[0] != "mixed" || targets[1] != "quanx" {
		t.Errorf("QX UA + auto 应走两段式，实际 %v", targets)
	}

	targets = nil
	req2, _ := http.NewRequest(http.MethodGet, ts.URL+"/s/"+code, nil)
	req2.Header.Set("User-Agent", "clash-verge/1.0")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if len(targets) != 2 || targets[0] != "mixed" || targets[1] != "clash" {
		t.Errorf("clash UA + auto 应走两段式（v1.4.0），实际 %v", targets)
	}

	targets = nil
	req3, _ := http.NewRequest(http.MethodGet, ts.URL+"/s/"+code, nil)
	req3.Header.Set("User-Agent", "Surge Mac/2398")
	resp3, err := http.DefaultClient.Do(req3)
	if err != nil {
		t.Fatal(err)
	}
	resp3.Body.Close()
	if len(targets) != 1 || targets[0] != "auto" {
		t.Errorf("Surge UA + auto 应走单段原路径（无 mixed 先行），实际 %v", targets)
	}
}
func TestShortLinkQuanxTwoStageEmptyNodes(t *testing.T) {
	// 机场返回无 URI 内容（代拉成功但无节点）→ 第一段 mixed 无有效节点 → 502
	airport := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("garbage-no-uri"))
	}))
	defer airport.Close()
	ext := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("target") == "mixed" {
			content, err := fetchBody(r.URL.Query().Get("url"))
			if err != nil {
				http.Error(w, err.Error(), 502)
				return
			}
			_, _ = w.Write([]byte(base64.StdEncoding.EncodeToString(content)))
			return
		}
		http.NotFound(w, r)
	}))
	defer ext.Close()
	s := mustServer(t, testConfig(t, ext.URL))
	code := newLink(t, s, airport.URL+"/sub", "quanx", "default", "https://example.com/preset.ini")
	ts := httptest.NewServer(s.routes())
	defer ts.Close()
	s.cfg.sidecarBase = ts.URL
	resp := get(t, ts.URL+"/s/"+code)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("空节点列表应 502，实际 %d", resp.StatusCode)
	}
}

// v1.4.0 clash 两段式（与 QX 同骨架）：显式 clash 链 mixed 代拉机场 → 明文节点列表 →
// clash（proxies 内联数组，客户端零二次拉取）
func TestShortLinkClashTwoStage(t *testing.T) {
	uriA := "ss://YWJjLTEyOjEubTIuMy40OjQ0Mw==#a"
	uriB := "vmess://eyJ2IjoiMiJ9"
	airport := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(uriA + "\n" + uriB + "\n"))
	}))
	defer airport.Close()

	var gotTargets, gotURLs, gotConfigs []string
	ext := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		gotTargets = append(gotTargets, q.Get("target"))
		gotURLs = append(gotURLs, q.Get("url"))
		gotConfigs = append(gotConfigs, q.Get("config"))
		switch q.Get("target") {
		case "mixed":
			content, err := fetchBody(q.Get("url"))
			if err != nil {
				http.Error(w, err.Error(), 502)
				return
			}
			_, _ = w.Write([]byte(base64.StdEncoding.EncodeToString(content)))
		case "clash":
			_, _ = w.Write([]byte("proxies:\n  - {name: node-a, type: ss, server: 5.6.7.8}"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ext.Close()
	s := mustServer(t, testConfig(t, ext.URL))
	code := newLink(t, s, airport.URL+"/sub", "clash", "default", "https://example.com/preset.ini")
	ts := httptest.NewServer(s.routes())
	defer ts.Close()
	s.cfg.sidecarBase = ts.URL

	resp := get(t, ts.URL+"/s/"+code)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "node-a") {
		t.Fatalf("clash 两段式输出异常: %d %s", resp.StatusCode, body)
	}
	if len(gotTargets) != 2 || gotTargets[0] != "mixed" || gotTargets[1] != "clash" {
		t.Errorf("应两段调用 mixed→clash，实际 %v", gotTargets)
	}
	if !strings.Contains(gotURLs[0], "/internal/sub/") {
		t.Errorf("第一段 url 应为内部回环 ref（v1.3.5 代拉）: %q", gotURLs[0])
	}
	if gotURLs[1] != uriA+"|"+uriB {
		t.Errorf("第二段 url 应为 | 连接的节点列表: %q", gotURLs[1])
	}
	if gotConfigs[0] != "" || gotConfigs[1] == "" {
		t.Errorf("config 只应出现在第二段: %v", gotConfigs)
	}
}

// auto 链 + clash 系 UA 词表（契约 {clash, mihomo, shellcrash}，小写包含）→ 两段式
func TestShortLinkAutoClashFamilyUAs(t *testing.T) {
	airport := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("vmess://x\n"))
	}))
	defer airport.Close()
	var targets []string
	ext := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targets = append(targets, r.URL.Query().Get("target"))
		if r.URL.Query().Get("target") == "mixed" {
			content, err := fetchBody(r.URL.Query().Get("url"))
			if err != nil {
				http.Error(w, err.Error(), 502)
				return
			}
			_, _ = w.Write([]byte(base64.StdEncoding.EncodeToString(content)))
			return
		}
		_, _ = w.Write([]byte("proxies: ok"))
	}))
	defer ext.Close()
	s := mustServer(t, testConfig(t, ext.URL))
	code := newLink(t, s, airport.URL+"/sub", "auto", "default", "https://example.com/preset.ini")
	ts := httptest.NewServer(s.routes())
	defer ts.Close()
	s.cfg.sidecarBase = ts.URL

	for _, ua := range []string{"clash-verge/v2.4.1", "mihomo/1.19.17", "ShellCrash/3.2.7"} {
		targets = nil
		req, _ := http.NewRequest(http.MethodGet, ts.URL+"/s/"+code, nil)
		req.Header.Set("User-Agent", ua)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if len(targets) != 2 || targets[0] != "mixed" || targets[1] != "clash" {
			t.Errorf("UA %q + auto 应走两段式 mixed→clash，实际 %v", ua, targets)
		}
	}
}

// clash 显式链空节点：代拉成功但第一段 mixed 无有效节点 → 502（与 quanx 对称）
func TestShortLinkClashTwoStageEmptyNodes(t *testing.T) {
	airport := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("garbage-no-uri"))
	}))
	defer airport.Close()
	ext := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("target") == "mixed" {
			content, err := fetchBody(r.URL.Query().Get("url"))
			if err != nil {
				http.Error(w, err.Error(), 502)
				return
			}
			_, _ = w.Write([]byte(base64.StdEncoding.EncodeToString(content)))
			return
		}
		http.NotFound(w, r)
	}))
	defer ext.Close()
	s := mustServer(t, testConfig(t, ext.URL))
	code := newLink(t, s, airport.URL+"/sub", "clash", "default", "https://example.com/preset.ini")
	ts := httptest.NewServer(s.routes())
	defer ts.Close()
	s.cfg.sidecarBase = ts.URL
	resp := get(t, ts.URL+"/s/"+code)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("clash 空节点列表应 502，实际 %d", resp.StatusCode)
	}
}

// B1 回归：第二段大响应（512KB 分块流式，远超 bufio 缓冲）必须完整透传不截断——
// cancel 责任移交前，核心内 defer cancel2 在 return 瞬间取消 ctx，大 body 读一半即断
func TestShortLinkClashTwoStageLargeBody(t *testing.T) {
	airport := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("vmess://x\n"))
	}))
	defer airport.Close()
	const total = 512 << 10
	ext := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch q.Get("target") {
		case "mixed":
			content, err := fetchBody(q.Get("url"))
			if err != nil {
				http.Error(w, err.Error(), 502)
				return
			}
			_, _ = w.Write([]byte(base64.StdEncoding.EncodeToString(content)))
		case "clash":
			chunk := make([]byte, 4096)
			for i := 0; i < total/len(chunk); i++ {
				if _, err := w.Write(chunk); err != nil {
					return
				}
				w.(http.Flusher).Flush() // 逐块 flush：确保 body 远早于响应完成就在网络上传
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer ext.Close()
	s := mustServer(t, testConfig(t, ext.URL))
	code := newLink(t, s, airport.URL+"/sub", "clash", "default", "https://example.com/preset.ini")
	ts := httptest.NewServer(s.routes())
	defer ts.Close()
	s.cfg.sidecarBase = ts.URL

	resp := get(t, ts.URL+"/s/"+code)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d，应 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("body 读取中断（ctx 取消泄漏到消费阶段）: %v", err)
	}
	if len(body) != total {
		t.Fatalf("body = %d 字节，应 %d（大响应被截断）", len(body), total)
	}
}

// m1：SUB_TOKEN 非空时两段（mixed 与 clash 转换）都容器内注入 token
func TestShortLinkClashTwoStageToken(t *testing.T) {
	airport := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("vmess://x\n"))
	}))
	defer airport.Close()
	var gotTokens []string
	ext := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		gotTokens = append(gotTokens, q.Get("token"))
		switch q.Get("target") {
		case "mixed":
			content, err := fetchBody(q.Get("url"))
			if err != nil {
				http.Error(w, err.Error(), 502)
				return
			}
			_, _ = w.Write([]byte(base64.StdEncoding.EncodeToString(content)))
		case "clash":
			_, _ = w.Write([]byte("proxies:\n  - {name: node-a}"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ext.Close()
	cfg := testConfig(t, ext.URL)
	cfg.subToken = "sec"
	s := mustServer(t, cfg)
	code := newLink(t, s, airport.URL+"/sub", "clash", "default", "https://example.com/preset.ini")
	ts := httptest.NewServer(s.routes())
	defer ts.Close()
	s.cfg.sidecarBase = ts.URL

	resp := get(t, ts.URL+"/s/"+code)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d", resp.StatusCode)
	}
	if len(gotTokens) != 2 || gotTokens[0] != "sec" || gotTokens[1] != "sec" {
		t.Errorf("两段 token = %v，应均注入 sec", gotTokens)
	}
}

// v1.3.5 内部订阅端点：注册/ref 取内容/未知 ref 404/同源 60s 复用
func TestInternalSubStore(t *testing.T) {
	airport := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("vmess://node\n"))
	}))
	defer airport.Close()
	cfg := testConfig(t, "")
	st := newInternalSubStore(cfg)
	base := "http://sidecar.test"

	u1, err := st.fetchAndRegister(context.Background(), "Quantumult%20X", airport.URL, base)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(u1, "/internal/sub/") {
		t.Fatalf("ref URL 形态异常: %q", u1)
	}
	// 同源 60s 内复用（同 ref）
	u2, _ := st.fetchAndRegister(context.Background(), "Quantumult%20X", airport.URL, base)
	if u1 != u2 {
		t.Errorf("同源 60s 内应复用 ref: %q vs %q", u1, u2)
	}
	// 引擎视角：GET ref 拿到内容
	ts := httptest.NewServer(http.HandlerFunc(st.handleInternalSub))
	defer ts.Close()
	content, err := fetchBody(ts.URL + strings.TrimPrefix(u1, base))
	if err != nil || string(content) != "vmess://node\n" {
		t.Fatalf("ref 取内容异常: %v %q", err, content)
	}
	// 未知 ref → 404
	resp, _ := http.Get(ts.URL + "/internal/sub/deadbeef")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("未知 ref 应 404，实际 %d", resp.StatusCode)
	}
}

// v1.3.6 代拉降级链：Go 拉取被 403 → exec curl 重试成功（机场 WAF 按 TLS 指纹拦 Go 的场景）
func TestInternalSubCurlFallback(t *testing.T) {
	// 机场 fixture：对 Go 抓取恒 403（模拟 WAF 指纹拦截）
	airport := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "blocked", http.StatusForbidden)
	}))
	defer airport.Close()

	// 假 curl：不真拉，直接吐节点内容（断言重试走到了 curl）
	dir := t.TempDir()
	curlPath := filepath.Join(dir, "curl")
	writeTB(t, curlPath, "#!/bin/sh\necho 'vmess://via-curl'\n")
	if err := os.Chmod(curlPath, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	st := newInternalSubStore(testConfig(t, ""))
	u, err := st.fetchAndRegister(context.Background(), "Quantumult%20X", airport.URL, "http://sidecar.test")
	if err != nil {
		t.Fatalf("Go 403 后 curl 重试应成功: %v", err)
	}
	// 直接取注册内容验证
	ts := httptest.NewServer(http.HandlerFunc(st.handleInternalSub))
	defer ts.Close()
	content, _ := fetchBody(ts.URL + strings.TrimPrefix(u, "http://sidecar.test"))
	if strings.TrimSpace(string(content)) != "vmess://via-curl" {
		t.Fatalf("内容应来自 curl 重试: %q", content)
	}
}

// v1.3.7 -f 语义：假 curl 收到 -f 且模拟 4xx（exit 22）→ fetchCurl 判失败（WAF 拦截页
// 不再被当作订阅内容）；Go 路径同样失败 → 整体报错（502，如实暴露）
func TestInternalSubCurlFailFast(t *testing.T) {
	airport := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "blocked", http.StatusForbidden)
	}))
	defer airport.Close()

	dir := t.TempDir()
	curlPath := filepath.Join(dir, "curl")
	writeTB(t, curlPath, "#!/bin/sh\n[ \"$1\" = \"-fsS\" ] && exit 22\necho should-not-reach\n")
	if err := os.Chmod(curlPath, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	st := newInternalSubStore(testConfig(t, ""))
	if _, err := st.fetchAndRegister(context.Background(), "Quantumult%20X", airport.URL, "http://sidecar.test"); err == nil {
		t.Fatal("Go 403 + curl(-f) 失败时 fetchAndRegister 应报错，而不是把拦截页当内容")
	}
}
