package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
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

// 五家深链拼装精确断言（契约「导入深链」节拼装表；期望串为 urlencode/base64 后的手写完整值）
func TestBuildDeepLinkFiveSchemes(t *testing.T) {
	link := "http://192.168.1.10:8080/sub?target=surge&url=https%3A%2F%2Fsub.example.com%2Fl&config=https%3A%2F%2Fexample.com%2Fpreset.ini"
	qe := "http%3A%2F%2F192.168.1.10%3A8080%2Fsub%3Ftarget%3Dsurge%26url%3Dhttps%253A%252F%252Fsub.example.com%252Fl%26config%3Dhttps%253A%252F%252Fexample.com%252Fpreset.ini"
	cases := []struct{ target, want string }{
		{"surge", "surge://install-config?url=" + qe},
		{"loon", "loon://import?sub=" + qe},
		{"stash", "stash://install-profile?url=" + qe},
		{"quanx", "quantumult-x:///add-resource?remote-resource=" +
			"%7B%22server_remote%22%3A%5B%22" + qe + "%2C+tag%3DSmart+SubConverter%22%5D%7D"},
		{"shadowrocket", "shadowrocket://add/sub://" +
			"aHR0cDovLzE5Mi4xNjguMS4xMDo4MDgwL3N1Yj90YXJnZXQ9c3VyZ2UmdXJsPWh0dHBzJTNBJTJGJTJGc3ViLmV4YW1wbGUuY29tJTJGbCZjb25maWc9aHR0cHMlM0ElMkYlMkZleGFtcGxlLmNvbSUyRnByZXNldC5pbmk="},
	}
	for _, c := range cases {
		got := buildDeepLink(c.target, link)
		if got == nil || *got != c.want {
			t.Errorf("buildDeepLink(%s) =\n%v\nwant\n%s", c.target, got, c.want)
		}
	}
}

// 无深链模板的 target：auto/clash/singbox/v2rayn（及未注册值，同「link 原样透传由上游拒绝」口径）→ nil
func TestBuildDeepLinkNoTemplate(t *testing.T) {
	for _, target := range []string{"auto", "clash", "singbox", "v2rayn", "garbage"} {
		if got := buildDeepLink(target, "http://h/sub?x=1"); got != nil {
			t.Errorf("buildDeepLink(%s) = %q, want nil", target, *got)
		}
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

// 深链：target=surge 的 ok 响应带精确深链（与 link 同源派生，期望串为手写完整值）
func TestPreviewDeepLinkSurgeOK(t *testing.T) {
	ext := stubExtended(t, 200, "[General]\nloglevel = notify", 0)
	defer ext.Close()
	s := newServer(testConfig(ext.URL))
	resp, _ := postPreview(t, s, `{"url":"https://sub.example.com/s","target":"surge"}`, "192.168.1.10:8080", "")
	if !resp.OK {
		t.Fatalf("surge ok 响应 = %+v", resp)
	}
	want := "surge://install-config?url=http%3A%2F%2F192.168.1.10%3A8080%2Fsub%3Ftarget%3Dsurge%26url%3Dhttps%253A%252F%252Fsub.example.com%252Fs%26config%3Dhttps%253A%252F%252Fexample.com%252Fpreset.ini"
	if resp.DeepLink == nil || *resp.DeepLink != want {
		t.Errorf("deepLink =\n%v\nwant\n%s", resp.DeepLink, want)
	}
}

// 深链与 link 同源：ok:false（上游失败）时 deepLink 照给
func TestPreviewDeepLinkGivenOnFailure(t *testing.T) {
	ext := stubExtended(t, 500, "boom", 0)
	defer ext.Close()
	s := newServer(testConfig(ext.URL))
	resp, _ := postPreview(t, s, `{"url":"https://sub.example.com/s","target":"surge"}`, "h:1", "")
	if resp.OK || resp.ErrorCode == nil || *resp.ErrorCode != "upstream_error" {
		t.Fatalf("500 响应 = %+v", resp)
	}
	if resp.DeepLink == nil || *resp.DeepLink != "surge://install-config?url="+url.QueryEscape(resp.Link) {
		t.Errorf("ok:false 时 deepLink 应照给: %v", resp.DeepLink)
	}
}

// bad_preset 时 deepLink 同样照给（loon 前缀，link 以 default 档生成）
func TestPreviewDeepLinkBadPresetStillGiven(t *testing.T) {
	ext := stubExtended(t, 200, "proxies: x", 0)
	defer ext.Close()
	s := newServer(testConfig(ext.URL))
	resp, _ := postPreview(t, s, `{"url":"https://sub.example.com/l","preset":"nope","target":"loon"}`, "h:1", "")
	if resp.ErrorCode == nil || *resp.ErrorCode != "bad_preset" {
		t.Fatalf("bad_preset 响应 = %+v", resp)
	}
	if resp.DeepLink == nil || *resp.DeepLink != "loon://import?sub="+url.QueryEscape(resp.Link) {
		t.Errorf("bad_preset 时 deepLink 应照给: %v", resp.DeepLink)
	}
}

// 无深链 target 的 handler 级断言：缺省/auto/clash/singbox/v2rayn → null
func TestPreviewDeepLinkNullTargets(t *testing.T) {
	ext := stubExtended(t, 200, "proxies: x", 0)
	defer ext.Close()
	s := newServer(testConfig(ext.URL))
	for _, body := range []string{
		`{"url":"https://sub.example.com/l"}`,
		`{"url":"https://sub.example.com/l","target":"auto"}`,
		`{"url":"https://sub.example.com/l","target":"clash"}`,
		`{"url":"https://sub.example.com/l","target":"singbox"}`,
		`{"url":"https://sub.example.com/l","target":"v2rayn"}`,
	} {
		resp, _ := postPreview(t, s, body, "h:1", "")
		if resp.DeepLink != nil {
			t.Errorf("body %s → deepLink %q, want null", body, *resp.DeepLink)
		}
	}
}

const (
	fullMultiModeURL   = "https://raw.githubusercontent.com/ACL4SSR/ACL4SSR/master/Clash/config/ACL4SSR_Online_Full_MultiMode.ini"
	fullAdblockPlusURL = "https://raw.githubusercontent.com/ACL4SSR/ACL4SSR/master/Clash/config/ACL4SSR_Online_Full_AdblockPlus.ini"
)

// preset 缺省/空串/"default" 均落 default 档；testConfig 的 presetURL 即覆盖后的 default url
func TestPreviewPresetDefaultVariants(t *testing.T) {
	var gotConfig string
	ext := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotConfig = r.URL.Query().Get("config")
		w.WriteHeader(200)
		_, _ = w.Write([]byte("proxies: x"))
	}))
	defer ext.Close()
	s := newServer(testConfig(ext.URL))
	for _, body := range []string{
		`{"url":"https://sub.example.com/l"}`,
		`{"url":"https://sub.example.com/l","preset":""}`,
		`{"url":"https://sub.example.com/l","preset":"default"}`,
	} {
		resp, _ := postPreview(t, s, body, "h:1", "")
		if !resp.OK || resp.Preset == nil || resp.Preset.ID != "default" || resp.Preset.Name != "ACL4SSR 默认" {
			t.Fatalf("body %s → preset 回显 %+v，应 default 档", body, resp.Preset)
		}
		if gotConfig != "https://example.com/preset.ini" {
			t.Errorf("body %s → 探测 config=%s，default 档应用 PRESET_URL 覆盖值", body, gotConfig)
		}
		if !strings.Contains(resp.Link, "config="+jsQueryEscape("https://example.com/preset.ini")) {
			t.Errorf("body %s → link 未烙 default 档 config: %s", body, resp.Link)
		}
	}
}

// 合法档位：探测与 link 的 config= 均为该档内置 URL（PRESET_URL 只覆盖 default 档，不影响其余两档）
func TestPreviewPresetFullMultiMode(t *testing.T) {
	var gotConfig string
	ext := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotConfig = r.URL.Query().Get("config")
		w.WriteHeader(200)
		_, _ = w.Write([]byte("proxies: x"))
	}))
	defer ext.Close()
	s := newServer(testConfig(ext.URL))
	resp, _ := postPreview(t, s, `{"url":"https://sub.example.com/l","preset":"full_multimode"}`, "h:1", "")
	if !resp.OK || resp.Preset == nil || resp.Preset.ID != "full_multimode" || resp.Preset.Name != "地区分组 + AI 分流" {
		t.Fatalf("preset 回显 = %+v", resp.Preset)
	}
	if gotConfig != fullMultiModeURL {
		t.Errorf("探测 config=%s，应用 Full_MultiMode URL（PRESET_URL 不得覆盖该档）", gotConfig)
	}
	if !strings.Contains(resp.Link, "config="+jsQueryEscape(fullMultiModeURL)) {
		t.Errorf("link 未烙 full_multimode 档 config: %s", resp.Link)
	}
}

func TestPreviewBadPreset(t *testing.T) {
	called := false
	ext := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(200)
	}))
	defer ext.Close()
	s := newServer(testConfig(ext.URL))

	resp, _ := postPreview(t, s, `{"url":"https://sub.example.com/l","preset":"nope"}`, "h:1", "")
	if resp.OK || resp.ErrorCode == nil || *resp.ErrorCode != "bad_preset" {
		t.Fatalf("非法 preset 响应 = %+v", resp)
	}
	if resp.Preset != nil {
		t.Errorf("bad_preset 时 preset 应回显 null: %+v", resp.Preset)
	}
	if resp.Message == "" {
		t.Error("bad_preset 时 message 必填")
	}
	if resp.Target != "clash" {
		t.Errorf("target = %s，auto 探测值应为 clash", resp.Target)
	}
	if called {
		t.Error("bad_preset 不应触达上游")
	}
	// link 仍以 default 档生成（此处为 testConfig 覆盖后的 default url），维持「link 永远非空」
	if !strings.Contains(resp.Link, "config="+jsQueryEscape("https://example.com/preset.ini")) {
		t.Errorf("bad_preset 的 link 应以 default 档生成: %s", resp.Link)
	}

	// 契约预校验判定序：bad_url_* 先于 bad_preset
	resp2, _ := postPreview(t, s, `{"url":"","preset":"nope"}`, "h:1", "")
	if resp2.ErrorCode == nil || *resp2.ErrorCode != "bad_url_empty" {
		t.Errorf("url 与 preset 双非法 → code %v，应 bad_url_empty 优先", resp2.ErrorCode)
	}
	// bad_url_* 时 preset 合法 → 恒回显该档
	resp3, _ := postPreview(t, s, `{"url":"not a url","preset":"full_multimode"}`, "h:1", "")
	if resp3.ErrorCode == nil || *resp3.ErrorCode != "bad_url_format" {
		t.Fatalf("bad_url_format 响应 = %+v", resp3)
	}
	if resp3.Preset == nil || resp3.Preset.ID != "full_multimode" {
		t.Errorf("bad_url_format + 合法 preset → 回显 %+v，应恒回显生效档", resp3.Preset)
	}
}

// ok:false（上游失败）时 preset 仍回显生效档位，link 仍烙该档 config=
func TestPreviewPresetEchoOnFailure(t *testing.T) {
	ext := stubExtended(t, 500, "boom", 0)
	defer ext.Close()
	s := newServer(testConfig(ext.URL))
	resp, _ := postPreview(t, s, `{"url":"https://sub.example.com/l","preset":"full_adblockplus"}`, "h:1", "")
	if resp.OK || resp.ErrorCode == nil || *resp.ErrorCode != "upstream_error" {
		t.Fatalf("响应 = %+v", resp)
	}
	if resp.Preset == nil || resp.Preset.ID != "full_adblockplus" || resp.Preset.Name != "全量 + 去广告" {
		t.Errorf("ok:false 时 preset 应回显生效档位: %+v", resp.Preset)
	}
	if !strings.Contains(resp.Link, "config="+jsQueryEscape(fullAdblockPlusURL)) {
		t.Errorf("link 应烙 full_adblockplus 档 config: %s", resp.Link)
	}
}

// /api/clients：三档顺序与精确值；PRESET_URL 只覆盖 default 档 url，id/name 不变
func TestHandleClientsPresets(t *testing.T) {
	w := httptest.NewRecorder()
	newServer(testConfig("http://unused")).handleClients(w, httptest.NewRequest(http.MethodGet, "/api/clients", nil))
	var resp clientsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Presets) != 3 {
		t.Fatalf("presets 档数 = %d，应为 3", len(resp.Presets))
	}
	want := []presetEntry{
		{"default", "ACL4SSR 默认", "https://example.com/preset.ini"},
		{"full_multimode", "地区分组 + AI 分流", fullMultiModeURL},
		{"full_adblockplus", "全量 + 去广告", fullAdblockPlusURL},
	}
	for i, w := range want {
		if resp.Presets[i] != w {
			t.Errorf("presets[%d] = %+v, want %+v", i, resp.Presets[i], w)
		}
	}

	// 无 PRESET_URL 覆盖时 default 档用内置 url
	cfg := testConfig("http://unused")
	cfg.presetURL = ""
	w2 := httptest.NewRecorder()
	newServer(cfg).handleClients(w2, httptest.NewRequest(http.MethodGet, "/api/clients", nil))
	var resp2 clientsResponse
	if err := json.Unmarshal(w2.Body.Bytes(), &resp2); err != nil {
		t.Fatal(err)
	}
	if resp2.Presets[0].URL != defaultPresetURL {
		t.Errorf("无覆盖时 default url = %s, want %s", resp2.Presets[0].URL, defaultPresetURL)
	}
}
