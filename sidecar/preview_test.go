package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testConfig(extendedBase string) config {
	return config{
		extendedBase: extendedBase,
		presetURL:    "https://example.com/preset.ini",
		probeTimeout: 2 * time.Second,
	}
}

func TestValidateURLEmpty(t *testing.T) {
	for _, raw := range []string{"", "   ", "\t\n "} {
		if got := validateURL(raw); got != "bad_url_empty" {
			t.Errorf("validateURL(%q) = %q, want bad_url_empty", raw, got)
		}
	}
}

func TestValidateURLFormat(t *testing.T) {
	for _, raw := range []string{
		"not a url",
		"ftp://example.com/sub",
		"ssh://example.com",
		"example.com/sub", // 无 scheme
		"http://",         // 无 host
	} {
		if got := validateURL(raw); got != "bad_url_format" {
			t.Errorf("validateURL(%q) = %q, want bad_url_format", raw, got)
		}
	}
}

func TestValidateURLPrivate(t *testing.T) {
	private := []string{
		"http://localhost/sub",
		"http://LocalHost:8080/sub",
		"http://localhost./sub",
		"http://127.0.0.1/sub",
		"http://127.8.8.8/sub",
		"http://10.1.2.3/sub",
		"http://172.16.0.1/sub",
		"http://172.31.255.255/sub",
		"http://192.168.1.10/sub",
		"http://169.254.169.254/sub",
		"http://0.0.0.0/sub",
		"http://[::1]/sub",
		"http://[::]/sub",
		"http://[fc00::1]/sub",
		"http://[::ffff:10.0.0.1]/sub",
	}
	for _, raw := range private {
		if got := validateURL(raw); got != "bad_url_private" {
			t.Errorf("validateURL(%q) = %q, want bad_url_private", raw, got)
		}
	}
	public := []string{
		"https://example.com/sub?x=1",
		"http://8.8.8.8/sub",
		"http://172.32.0.1/sub",  // 172.32 不在 172.16/12 内
		"http://172.15.0.1/sub",  // 边界外
		"http://192.169.1.1/sub", // 192.169 不在 192.168/16 内
		"http://[2606:4700::1]/sub",
	}
	for _, raw := range public {
		if got := validateURL(raw); got != "" {
			t.Errorf("validateURL(%q) = %q, want 通过", raw, got)
		}
	}
}

func TestBuildLink(t *testing.T) {
	got := buildLink("http://192.168.1.10:8080", "auto",
		"https://sub.example.com/list?token=a b", "https://example.com/p.ini", "")
	want := "http://192.168.1.10:8080/sub?target=auto&url=" +
		"https%3A%2F%2Fsub.example.com%2Flist%3Ftoken%3Da%20b&config=https%3A%2F%2Fexample.com%2Fp.ini"
	if got != want {
		t.Errorf("buildLink =\n%s\nwant\n%s", got, want)
	}
	if strings.Contains(got, "+") {
		t.Errorf("空格被编码成 +，应 encodeURIComponent 语义（%%20）: %s", got)
	}
}

func TestBuildLinkWithToken(t *testing.T) {
	got := buildLink("http://h", "clash", "https://s.example/a", "https://c.example/i", "tok en&1")
	if !strings.Contains(got, "&token=tok%20en%261") {
		t.Errorf("token 未按 encodeURIComponent 追加: %s", got)
	}
	noTok := buildLink("http://h", "clash", "https://s.example/a", "https://c.example/i", "")
	if strings.Contains(noTok, "token=") {
		t.Errorf("SUB_TOKEN 为空时不应有 token 参数: %s", noTok)
	}
}

func TestFormatLooksValid(t *testing.T) {
	cases := []struct {
		target string
		body   string
		want   bool
	}{
		{"clash", "proxies:\n  - name: a", true},
		{"clash", "port: 7890", false},
		{"surge", "[General]\nloglevel = notify", true},
		{"surge", "#!MANAGED-CONFIG http://x/sub interval=86400", true},
		{"surge", "[Proxy]\n DIRECT", false},
		{"singbox", `{"outbounds":[{"type":"direct"}]}`, true},
		{"singbox", `{"inbounds":[]}`, false},
		{"singbox", `not json`, false},
		{"v2rayn", "vmess://abcd", true},
		{"v2rayn", "Invalid request: x", false},
		{"v2rayn", "  ", false},
	}
	for _, c := range cases {
		if got := formatLooksValid(c.target, []byte(c.body)); got != c.want {
			t.Errorf("formatLooksValid(%s, %q) = %v, want %v", c.target, c.body, got, c.want)
		}
	}
}

func TestMapUpstreamError(t *testing.T) {
	got := mapUpstreamError(400, []byte("Invalid request: unsupported target value. 无效请求：不支持的 target 参数值。"))
	if got.errorCode != "upstream_error" || !strings.Contains(got.message, "指定客户端") {
		t.Errorf("unsupported target 映射 = %+v，应引导展开指定客户端", got)
	}
	got = mapUpstreamError(500, []byte("some other error"))
	if got.errorCode != "upstream_error" || !strings.Contains(got.message, "500") {
		t.Errorf("其他错误映射 = %+v，应带 HTTP 状态码", got)
	}
}

// stubExtended 起一个模拟 Extended 的 /sub 服务
func stubExtended(t *testing.T, status int, body string, delay time.Duration) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(delay)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

// postPreview 直接调 handler，返回解析后的响应与使用的探测请求 UA
func postPreview(t *testing.T, s *server, body string, host, xfp string) (previewResponse, *http.Request) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/preview", strings.NewReader(body))
	r.Host = host
	if xfp != "" {
		r.Header.Set("X-Forwarded-Proto", xfp)
	}
	w := httptest.NewRecorder()
	s.handlePreview(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("preview 响应码 = %d，契约要求恒 200", w.Code)
	}
	var resp previewResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("preview 响应不是合法 JSON: %v", err)
	}
	return resp, r
}

func TestPreviewOKClashAuto(t *testing.T) {
	var gotUA, gotTarget string
	ext := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		gotTarget = r.URL.Query().Get("target")
		w.WriteHeader(200)
		_, _ = w.Write([]byte("proxies:\n  - name: fixture"))
	}))
	defer ext.Close()
	s := newServer(testConfig(ext.URL))
	resp, _ := postPreview(t, s, `{"url":"https://sub.example.com/list"}`, "192.168.1.10:8080", "")
	if !resp.OK || resp.Target != "clash" || resp.ErrorCode != nil || resp.Message != "" {
		t.Fatalf("ok 响应 = %+v", resp)
	}
	if gotTarget != "clash" || gotUA != "clash-verge/v1.7.7" {
		t.Errorf("探测请求 target=%s UA=%s，auto 应以 clash + clash UA 探测", gotTarget, gotUA)
	}
	if want := "http://192.168.1.10:8080/sub?target=auto&url=https%3A%2F%2Fsub.example.com%2Flist&config=https%3A%2F%2Fexample.com%2Fpreset.ini"; resp.Link != want {
		t.Errorf("link = %s\nwant %s", resp.Link, want)
	}
}

func TestPreviewOKSurgeExplicit(t *testing.T) {
	ext := stubExtended(t, 200, "[General]\nloglevel = notify", 0)
	defer ext.Close()
	s := newServer(testConfig(ext.URL))
	resp, _ := postPreview(t, s, `{"url":"https://sub.example.com/s","target":"surge"}`, "h:1", "")
	if !resp.OK || resp.Target != "surge" {
		t.Fatalf("显式 surge 响应 = %+v", resp)
	}
}

func TestPreviewFormatFailUpstreamError(t *testing.T) {
	ext := stubExtended(t, 200, "hello not a config", 0)
	defer ext.Close()
	s := newServer(testConfig(ext.URL))
	resp, _ := postPreview(t, s, `{"url":"https://sub.example.com/s"}`, "h:1", "")
	if resp.OK || *resp.ErrorCode != "upstream_error" {
		t.Fatalf("格式粗验不过应为 upstream_error: %+v", resp)
	}
}

func TestPreviewUnsupportedTargetBody(t *testing.T) {
	ext := stubExtended(t, 400, "Invalid request: unsupported target value. 无效请求：不支持的 target 参数值。", 0)
	defer ext.Close()
	s := newServer(testConfig(ext.URL))
	resp, _ := postPreview(t, s, `{"url":"https://sub.example.com/s"}`, "h:1", "")
	if resp.OK || *resp.ErrorCode != "upstream_error" || !strings.Contains(resp.Message, "指定客户端") {
		t.Fatalf("unsupported target 响应 = %+v", resp)
	}
}

func TestPreviewOtherHTTPError(t *testing.T) {
	ext := stubExtended(t, 500, "boom", 0)
	defer ext.Close()
	s := newServer(testConfig(ext.URL))
	resp, _ := postPreview(t, s, `{"url":"https://sub.example.com/s"}`, "h:1", "")
	if resp.OK || *resp.ErrorCode != "upstream_error" || !strings.Contains(resp.Message, "500") {
		t.Fatalf("500 响应 = %+v", resp)
	}
}

func TestPreviewBackendDown(t *testing.T) {
	ext := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := ext.URL
	ext.Close() // 立即关闭 → 连接拒绝
	s := newServer(testConfig(url))
	resp, _ := postPreview(t, s, `{"url":"https://sub.example.com/s"}`, "h:1", "")
	if resp.OK || *resp.ErrorCode != "backend_down" {
		t.Fatalf("连接拒绝应 backend_down: %+v", resp)
	}
}

func TestPreviewTimeout(t *testing.T) {
	ext := stubExtended(t, 200, "proxies:", 3*time.Second)
	defer ext.Close()
	cfg := testConfig(ext.URL)
	cfg.probeTimeout = 200 * time.Millisecond
	s := newServer(cfg)
	resp, _ := postPreview(t, s, `{"url":"https://sub.example.com/s"}`, "h:1", "")
	if resp.OK || *resp.ErrorCode != "upstream_timeout" {
		t.Fatalf("超时应 upstream_timeout: %+v", resp)
	}
}

func TestPreviewBadURLStopsBeforeUpstream(t *testing.T) {
	called := false
	ext := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(200)
	}))
	defer ext.Close()
	s := newServer(testConfig(ext.URL))
	// D4 语义核心：ok:false 时 link 照常返回且可用
	cases := []struct {
		body string
		code string
	}{
		{`{"url":""}`, "bad_url_empty"},
		{`{"url":"   "}`, "bad_url_empty"},
		{`{}`, "bad_url_empty"},
		{`not json`, "bad_url_empty"},
		{`{"url":"ftp://x/y"}`, "bad_url_format"},
		{`{"url":"http://127.0.0.1:25500/sub"}`, "bad_url_private"},
		{`{"url":"http://192.168.1.5/list"}`, "bad_url_private"},
	}
	for _, c := range cases {
		resp, _ := postPreview(t, s, c.body, "192.168.1.10:8080", "")
		if resp.OK || resp.ErrorCode == nil || *resp.ErrorCode != c.code {
			t.Errorf("body %s → code %v, want %s", c.body, resp.ErrorCode, c.code)
		}
		if resp.Link == "" || !strings.Contains(resp.Link, "/sub?") {
			t.Errorf("body %s → link %q 不应为空（D4：link 永远返回）", c.body, resp.Link)
		}
	}
	if called {
		t.Error("预校验失败不应触达上游")
	}
}

func TestPreviewOriginDerivation(t *testing.T) {
	ext := stubExtended(t, 200, "proxies: x", 0)
	defer ext.Close()
	s := newServer(testConfig(ext.URL))

	// X-Forwarded-Proto + Host 推导
	resp, _ := postPreview(t, s, `{"url":"https://s.example/l"}`, "dep.lan:8080", "https")
	if !strings.HasPrefix(resp.Link, "https://dep.lan:8080/") {
		t.Errorf("XFP+Host 推导失败: %s", resp.Link)
	}

	// PUBLIC_BASE_URL 覆盖一切
	s2 := newServer(func() config { c := testConfig(ext.URL); c.publicBaseURL = "https://sub.pub.example/"; return c }())
	resp, _ = postPreview(t, s2, `{"url":"https://s.example/l"}`, "ignored:1", "")
	if !strings.HasPrefix(resp.Link, "https://sub.pub.example/") {
		t.Errorf("PUBLIC_BASE_URL 覆盖失败（含去尾斜杠）: %s", resp.Link)
	}
}

func TestPreviewUnknownTargetFallsBackToClash(t *testing.T) {
	ext := stubExtended(t, 200, "proxies: x", 0)
	defer ext.Close()
	s := newServer(testConfig(ext.URL))
	// 未注册 target：契约未定义错误码，按「探测用 clash、link 原样」处理（报告列偏差）
	resp, _ := postPreview(t, s, `{"url":"https://s.example/l","target":"garbage"}`, "h:1", "")
	if !resp.OK || resp.Target != "clash" || !strings.Contains(resp.Link, "target=garbage") {
		t.Fatalf("未知 target 响应 = %+v", resp)
	}
}
