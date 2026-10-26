package main

import (
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func testConfig(t *testing.T, extendedBase string) config {
	t.Helper()
	return config{
		extendedBase: extendedBase,
		presetURL:    "https://example.com/preset.ini",
		probeTimeout: 2 * time.Second,
		dataDir:      t.TempDir(),
	}
}

// mustServer 打开带独立 tmp store 的 server（newServer 可能因库损坏失败，测试直接 Fatal）
func mustServer(t *testing.T, cfg config) *server {
	t.Helper()
	s, err := newServer(cfg)
	if err != nil {
		t.Fatalf("newServer: %v", err)
	}
	return s
}

// storedFor 从短链 link 提取短码并查库（断言「冻结创建时参数」用）
func storedFor(t *testing.T, s *server, link string) storedLink {
	t.Helper()
	i := strings.LastIndex(link, "/s/")
	if i < 0 {
		t.Fatalf("link 不是短链形态: %s", link)
	}
	lnk, found, err := s.store.linkByCode(link[i+3:])
	if err != nil || !found {
		t.Fatalf("短码查库失败 found=%v err=%v", found, err)
	}
	return lnk
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

// publicAirportURL 起监听 0.0.0.0 的机场 fixture，URL 用容器 hostname 表达：hostname 字面量
// 非 localhost / 非 IP（过 D7 预校验 bad_url_private），而 Docker 把容器 hostname 写进
// /etc/hosts（纯 Go 解析器读之，代拉解析回本机可达，无外网 DNS 依赖）——preview 两段式
// 测试的代拉源底座（v1.4.0；直通用不到，/s/ 侧无预校验仍用 127.0.0.1 httptest）。
// 起服后自拨一次 hostname:port（m2 守卫）：不解析/不可达（本机 mac hostname 场景）则
// Skip 而不是误报用例失败——项目约定测试走 docker，本机直跑是已知限制。
func publicAirportURL(t *testing.T, body string) string {
	t.Helper()
	l, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Skipf("无法监听 0.0.0.0（项目约定测试走 docker，本机直跑的已知限制）: %v", err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	})}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(l.Addr().String())
	addr := net.JoinHostPort(host, port)
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Skipf("hostname %s 不可解析/不可达（本机直跑场景；项目约定测试走 docker）: %v", host, err)
	}
	conn.Close()
	return "http://" + addr
}

// twoStageHit 引擎 stub 收到的一次 /sub 请求（断言两段式序列用）
type twoStageHit struct {
	target string
	url    string
	config string
	ua     string
}

// twoStageEnv v1.4.0 两段式 preview 测试底座：机场 fixture（两节点明文列表，publicAirportURL
// 形态以过 D7）+ 引擎 stub（mixed → 代拉内容 base64 回显，即模拟「引擎从内部回环拉内容转
// 通用 URI 列表」；finalTarget → finalBody；其余 target 404）。返回机场 URL（作 preview 的
// 订阅 url）与引擎收到的请求序列。调用方须把 s.cfg.sidecarBase 指向挂 /internal/sub/ 的
// 测试 server。
func twoStageEnv(t *testing.T, finalTarget, finalBody string) (subURL, extURL string, hits *[]twoStageHit) {
	t.Helper()
	subURL = publicAirportURL(t, "ss://YWJjLTEyOjEuMi4zLjQ6NDQz#node-a\nvmess://eyJ2IjoiMiJ9#node-b\n")
	h := []twoStageHit{}
	ext := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		h = append(h, twoStageHit{q.Get("target"), q.Get("url"), q.Get("config"), r.Header.Get("User-Agent")})
		switch q.Get("target") {
		case "mixed":
			content, err := fetchBody(q.Get("url"))
			if err != nil {
				http.Error(w, err.Error(), 502)
				return
			}
			_, _ = w.Write([]byte(base64.StdEncoding.EncodeToString(content)))
		case finalTarget:
			_, _ = w.Write([]byte(finalBody))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ext.Close)
	return subURL, ext.URL, &h
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
	subURL, extURL, hits := twoStageEnv(t, "clash", "proxies:\n  - name: fixture")
	s := mustServer(t, testConfig(t, extURL))
	ts := httptest.NewServer(s.routes())
	defer ts.Close()
	s.cfg.sidecarBase = ts.URL
	resp, _ := postPreview(t, s, `{"url":"`+subURL+`"}`, "192.168.1.10:8080", "")
	if !resp.OK || resp.Target != "clash" || resp.ErrorCode != nil || resp.Message != "" {
		t.Fatalf("ok 响应 = %+v", resp)
	}
	// v1.4.0：auto 默认的 clash 探测走两段式（mixed→clash，与 /s/ 拉取同路）
	if len(*hits) != 2 || (*hits)[0].target != "mixed" || (*hits)[1].target != "clash" {
		t.Fatalf("auto 探测应两段式 mixed→clash，实际 %+v", *hits)
	}
	if !strings.Contains((*hits)[0].url, "/internal/sub/") {
		t.Errorf("第一段 url 应为内部回环 ref: %q", (*hits)[0].url)
	}
	if !strings.Contains((*hits)[1].url, "ss://") || !strings.Contains((*hits)[1].url, "|") {
		t.Errorf("第二段 url 应为 | 连接的节点列表: %q", (*hits)[1].url)
	}
	if (*hits)[1].ua != "clash-verge/v1.7.7" {
		t.Errorf("探测 UA = %q，应用 clash 代表 UA", (*hits)[1].ua)
	}
	// v1.2.0：link 为短链 ${origin}/s/{8 字符码}，冻结创建时参数
	if !strings.HasPrefix(resp.Link, "http://192.168.1.10:8080/s/") {
		t.Fatalf("link = %s，应为短链", resp.Link)
	}
	if code := strings.TrimPrefix(resp.Link, "http://192.168.1.10:8080/s/"); len(code) != 8 {
		t.Errorf("短码 %q 应为 8 字符", code)
	}
	if lnk := storedFor(t, s, resp.Link); lnk.Target != "auto" ||
		lnk.SourceURL != subURL ||
		lnk.ConfigURL != "https://example.com/preset.ini" {
		t.Errorf("冻结参数 = %+v", lnk)
	}
	// 幂等：同参数复用既有短码
	resp2, _ := postPreview(t, s, `{"url":"`+subURL+`"}`, "192.168.1.10:8080", "")
	if resp2.Link != resp.Link {
		t.Errorf("同参数应复用同码: %s vs %s", resp.Link, resp2.Link)
	}
}

func TestPreviewOKSurgeExplicit(t *testing.T) {
	var targets []string
	ext := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targets = append(targets, r.URL.Query().Get("target"))
		w.WriteHeader(200)
		_, _ = w.Write([]byte("[General]\nloglevel = notify"))
	}))
	defer ext.Close()
	s := mustServer(t, testConfig(t, ext.URL))
	resp, _ := postPreview(t, s, `{"url":"https://sub.example.com/s","target":"surge"}`, "h:1", "")
	if !resp.OK || resp.Target != "surge" {
		t.Fatalf("显式 surge 响应 = %+v", resp)
	}
	// v1.4.0：surge 不在两段式集合，直通不变（引擎只收到一次原样 target，无 mixed 先行）
	if len(targets) != 1 || targets[0] != "surge" {
		t.Errorf("surge 探测应直通单段，实际 %v", targets)
	}
}

// 格式粗验失败：singbox 直通靶（v1.4.0 起 auto/clash 探测走两段式，default 粗验分支的
// 直通敏感 target 换 singbox 保语义——body 非 JSON → upstream_error）
func TestPreviewFormatFailUpstreamError(t *testing.T) {
	ext := stubExtended(t, 200, "hello not a config", 0)
	defer ext.Close()
	s := mustServer(t, testConfig(t, ext.URL))
	resp, _ := postPreview(t, s, `{"url":"https://sub.example.com/s","target":"singbox"}`, "h:1", "")
	if resp.OK || *resp.ErrorCode != "upstream_error" {
		t.Fatalf("格式粗验不过应为 upstream_error: %+v", resp)
	}
}

func TestPreviewUnsupportedTargetBody(t *testing.T) {
	ext := stubExtended(t, 400, "Invalid request: unsupported target value. 无效请求：不支持的 target 参数值。", 0)
	defer ext.Close()
	s := mustServer(t, testConfig(t, ext.URL))
	resp, _ := postPreview(t, s, `{"url":"https://sub.example.com/s","target":"loon"}`, "h:1", "")
	if resp.OK || *resp.ErrorCode != "upstream_error" || !strings.Contains(resp.Message, "指定客户端") {
		t.Fatalf("unsupported target 响应 = %+v", resp)
	}
}

func TestPreviewOtherHTTPError(t *testing.T) {
	ext := stubExtended(t, 500, "boom", 0)
	defer ext.Close()
	s := mustServer(t, testConfig(t, ext.URL))
	resp, _ := postPreview(t, s, `{"url":"https://sub.example.com/s","target":"loon"}`, "h:1", "")
	if resp.OK || *resp.ErrorCode != "upstream_error" || !strings.Contains(resp.Message, "500") {
		t.Fatalf("500 响应 = %+v", resp)
	}
}

func TestPreviewBackendDown(t *testing.T) {
	ext := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := ext.URL
	ext.Close() // 立即关闭 → 连接拒绝
	s := mustServer(t, testConfig(t, url))
	resp, _ := postPreview(t, s, `{"url":"https://sub.example.com/s","target":"loon"}`, "h:1", "")
	if resp.OK || *resp.ErrorCode != "backend_down" {
		t.Fatalf("连接拒绝应 backend_down: %+v", resp)
	}
}

func TestPreviewTimeout(t *testing.T) {
	ext := stubExtended(t, 200, "proxies:", 3*time.Second)
	defer ext.Close()
	cfg := testConfig(t, ext.URL)
	cfg.probeTimeout = 200 * time.Millisecond
	s := mustServer(t, cfg)
	resp, _ := postPreview(t, s, `{"url":"https://sub.example.com/s","target":"loon"}`, "h:1", "")
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
	s := mustServer(t, testConfig(t, ext.URL))
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
	s := mustServer(t, testConfig(t, ext.URL))

	// X-Forwarded-Proto + Host 推导（loon 直通靶：本测试只断言 link origin）
	resp, _ := postPreview(t, s, `{"url":"https://s.example/l","target":"loon"}`, "dep.lan:8080", "https")
	if !strings.HasPrefix(resp.Link, "https://dep.lan:8080/") {
		t.Errorf("XFP+Host 推导失败: %s", resp.Link)
	}

	// PUBLIC_BASE_URL 覆盖一切
	s2 := mustServer(t, func() config { c := testConfig(t, ext.URL); c.publicBaseURL = "https://sub.pub.example/"; return c }())
	resp, _ = postPreview(t, s2, `{"url":"https://s.example/l","target":"loon"}`, "ignored:1", "")
	if !strings.HasPrefix(resp.Link, "https://sub.pub.example/") {
		t.Errorf("PUBLIC_BASE_URL 覆盖失败（含去尾斜杠）: %s", resp.Link)
	}
}

func TestPreviewUnknownTargetFallsBackToClash(t *testing.T) {
	subURL, extURL, hits := twoStageEnv(t, "clash", "proxies:\n  - {name: node-a}\n")
	s := mustServer(t, testConfig(t, extURL))
	ts := httptest.NewServer(s.routes())
	defer ts.Close()
	s.cfg.sidecarBase = ts.URL
	// 未注册 target：契约未定义错误码，按「探测用 clash、target 原样冻结进短链」处理（报告列偏差）
	// v1.4.0：归一化后的 clash 探测走两段式
	resp, _ := postPreview(t, s, `{"url":"`+subURL+`","target":"garbage"}`, "h:1", "")
	if !resp.OK || resp.Target != "clash" {
		t.Fatalf("未知 target 响应 = %+v", resp)
	}
	if lnk := storedFor(t, s, resp.Link); lnk.Target != "garbage" {
		t.Errorf("冻结的 target = %s，应为请求原样 garbage", lnk.Target)
	}
	if len(*hits) != 2 || (*hits)[0].target != "mixed" || (*hits)[1].target != "clash" {
		t.Errorf("未知 target 的 clash 探测应两段式，实际 %+v", *hits)
	}
}

// 深链：target=surge 的 ok 响应带精确深链（与 link 同源派生，嵌 /s/ 短链）
func TestPreviewDeepLinkSurgeOK(t *testing.T) {
	ext := stubExtended(t, 200, "[General]\nloglevel = notify", 0)
	defer ext.Close()
	s := mustServer(t, testConfig(t, ext.URL))
	resp, _ := postPreview(t, s, `{"url":"https://sub.example.com/s","target":"surge"}`, "192.168.1.10:8080", "")
	if !resp.OK {
		t.Fatalf("surge ok 响应 = %+v", resp)
	}
	if !strings.Contains(resp.Link, "/s/") {
		t.Fatalf("link = %s，应为短链", resp.Link)
	}
	want := "surge://install-config?url=" + url.QueryEscape(resp.Link)
	if resp.DeepLink == nil || *resp.DeepLink != want {
		t.Errorf("deepLink =\n%v\nwant\n%s", resp.DeepLink, want)
	}
}

// 深链与 link 同源：ok:false（上游失败）时 deepLink 照给
func TestPreviewDeepLinkGivenOnFailure(t *testing.T) {
	ext := stubExtended(t, 500, "boom", 0)
	defer ext.Close()
	s := mustServer(t, testConfig(t, ext.URL))
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
	s := mustServer(t, testConfig(t, ext.URL))
	resp, _ := postPreview(t, s, `{"url":"https://sub.example.com/l","preset":"nope","target":"loon"}`, "h:1", "")
	if resp.ErrorCode == nil || *resp.ErrorCode != "bad_preset" {
		t.Fatalf("bad_preset 响应 = %+v", resp)
	}
	if resp.DeepLink == nil || *resp.DeepLink != "loon://import?sub="+url.QueryEscape(resp.Link) {
		t.Errorf("bad_preset 时 deepLink 应照给: %v", resp.DeepLink)
	}
}

// 无深链 target 的 handler 级断言：缺省/auto/clash/singbox/v2rayn → null
// （缺省/auto/clash 的探测走两段式，用 twoStageEnv 底座；singbox/v2rayn 直通对 stub
// 落 404，探测失败但 deepLink 断言与探测结果无关）
func TestPreviewDeepLinkNullTargets(t *testing.T) {
	subURL, extURL, _ := twoStageEnv(t, "clash", "proxies:\n  - {name: node-a}\n")
	s := mustServer(t, testConfig(t, extURL))
	ts := httptest.NewServer(s.routes())
	defer ts.Close()
	s.cfg.sidecarBase = ts.URL
	for _, body := range []string{
		`{"url":"` + subURL + `"}`,
		`{"url":"` + subURL + `","target":"auto"}`,
		`{"url":"` + subURL + `","target":"clash"}`,
		`{"url":"` + subURL + `","target":"singbox"}`,
		`{"url":"` + subURL + `","target":"v2rayn"}`,
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
// （探测两段式：第二段 target=clash 的 config= 即探测档位 url）
func TestPreviewPresetDefaultVariants(t *testing.T) {
	subURL, extURL, hits := twoStageEnv(t, "clash", "proxies:\n  - {name: node-a}\n")
	s := mustServer(t, testConfig(t, extURL))
	ts := httptest.NewServer(s.routes())
	defer ts.Close()
	s.cfg.sidecarBase = ts.URL
	var firstLink string
	for _, body := range []string{
		`{"url":"` + subURL + `"}`,
		`{"url":"` + subURL + `","preset":""}`,
		`{"url":"` + subURL + `","preset":"default"}`,
	} {
		resp, _ := postPreview(t, s, body, "h:1", "")
		if !resp.OK || resp.Preset == nil || resp.Preset.ID != "default" || resp.Preset.Name != "ACL4SSR 默认" {
			t.Fatalf("body %s → preset 回显 %+v，应 default 档", body, resp.Preset)
		}
		if last := (*hits)[len(*hits)-1]; last.target != "clash" || last.config != "https://example.com/preset.ini" {
			t.Errorf("body %s → 探测 %s config=%s，default 档应用 PRESET_URL 覆盖值", body, last.target, last.config)
		}
		if !strings.Contains(resp.Link, "/s/") {
			t.Errorf("body %s → link 应为短链: %s", body, resp.Link)
		}
		if lnk := storedFor(t, s, resp.Link); lnk.ConfigURL != "https://example.com/preset.ini" {
			t.Errorf("body %s → 冻结的 config_url = %s，应 default 档", body, lnk.ConfigURL)
		}
		if firstLink == "" {
			firstLink = resp.Link
		} else if resp.Link != firstLink {
			t.Errorf("body %s → 同指纹（default 档）应复用同码: %s vs %s", body, resp.Link, firstLink)
		}
	}
}

// 合法档位：探测与 link 的 config= 均为该档内置 URL（PRESET_URL 只覆盖 default 档，不影响其余两档）
func TestPreviewPresetFullMultiMode(t *testing.T) {
	subURL, extURL, hits := twoStageEnv(t, "clash", "proxies:\n  - {name: node-a}\n")
	s := mustServer(t, testConfig(t, extURL))
	ts := httptest.NewServer(s.routes())
	defer ts.Close()
	s.cfg.sidecarBase = ts.URL
	resp, _ := postPreview(t, s, `{"url":"`+subURL+`","preset":"full_multimode"}`, "h:1", "")
	if !resp.OK || resp.Preset == nil || resp.Preset.ID != "full_multimode" || resp.Preset.Name != "地区分组 + AI 分流" {
		t.Fatalf("preset 回显 = %+v", resp.Preset)
	}
	if last := (*hits)[len(*hits)-1]; last.config != fullMultiModeURL {
		t.Errorf("探测 config=%s，应用 Full_MultiMode URL（PRESET_URL 不得覆盖该档）", last.config)
	}
	if lnk := storedFor(t, s, resp.Link); lnk.ConfigURL != fullMultiModeURL {
		t.Errorf("冻结的 config_url = %s，应 full_multimode 档 URL", lnk.ConfigURL)
	}
	// 不同档位（不同 config_url）→ 不同指纹 → 不同短码
	respDef, _ := postPreview(t, s, `{"url":"`+subURL+`"}`, "h:1", "")
	if respDef.Link == resp.Link {
		t.Errorf("不同档位应得不同短码: %s", resp.Link)
	}
}

func TestPreviewBadPreset(t *testing.T) {
	called := false
	ext := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(200)
	}))
	defer ext.Close()
	s := mustServer(t, testConfig(t, ext.URL))

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
	// link 仍以 default 档短链生成（此处为 testConfig 覆盖后的 default url），维持「link 永远非空」
	if lnk := storedFor(t, s, resp.Link); lnk.ConfigURL != "https://example.com/preset.ini" {
		t.Errorf("bad_preset 的 link 应以 default 档生成: %+v", lnk)
	}
	var storedPresetID string
	if err := s.store.db.QueryRow("SELECT preset_id FROM links WHERE code = ?",
		resp.Link[strings.LastIndex(resp.Link, "/s/")+3:]).Scan(&storedPresetID); err != nil || storedPresetID != "default" {
		t.Errorf("冻结的 preset_id = %q (err %v)，应 default", storedPresetID, err)
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
// （loon 直通靶：断言重点是 preset 回显与冻结，与探测 target 无关）
func TestPreviewPresetEchoOnFailure(t *testing.T) {
	ext := stubExtended(t, 500, "boom", 0)
	defer ext.Close()
	s := mustServer(t, testConfig(t, ext.URL))
	resp, _ := postPreview(t, s, `{"url":"https://sub.example.com/l","preset":"full_adblockplus","target":"loon"}`, "h:1", "")
	if resp.OK || resp.ErrorCode == nil || *resp.ErrorCode != "upstream_error" {
		t.Fatalf("响应 = %+v", resp)
	}
	if resp.Preset == nil || resp.Preset.ID != "full_adblockplus" || resp.Preset.Name != "全量 + 去广告" {
		t.Errorf("ok:false 时 preset 应回显生效档位: %+v", resp.Preset)
	}
	if !strings.Contains(resp.Link, "/s/") {
		t.Errorf("ok:false 时 link 仍应为短链: %s", resp.Link)
	}
	if lnk := storedFor(t, s, resp.Link); lnk.ConfigURL != fullAdblockPlusURL {
		t.Errorf("冻结的 config_url = %s，应 full_adblockplus 档 URL", lnk.ConfigURL)
	}
}

// /api/clients：三档顺序与精确值；PRESET_URL 只覆盖 default 档 url，id/name 不变
func TestHandleClientsPresets(t *testing.T) {
	w := httptest.NewRecorder()
	mustServer(t, testConfig(t, "http://unused")).handleClients(w, httptest.NewRequest(http.MethodGet, "/api/clients", nil))
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
	cfg := testConfig(t, "http://unused")
	cfg.presetURL = ""
	w2 := httptest.NewRecorder()
	mustServer(t, cfg).handleClients(w2, httptest.NewRequest(http.MethodGet, "/api/clients", nil))
	var resp2 clientsResponse
	if err := json.Unmarshal(w2.Body.Bytes(), &resp2); err != nil {
		t.Fatal(err)
	}
	if resp2.Presets[0].URL != defaultPresetURL {
		t.Errorf("无覆盖时 default url = %s, want %s", resp2.Presets[0].URL, defaultPresetURL)
	}
}

// store_error：存储失败 → link 降级 /sub 旧长链（唯一保留长链返回的正常场景）、
// errorCode=store_error；探测成功时 ok 仍为 true（探测结果不被存储故障覆盖）
// （target=clash 走两段式探测：探测照常成功，store_error 才能体现「ok 仍 true」语义）
func TestPreviewStoreError(t *testing.T) {
	subURL, extURL, _ := twoStageEnv(t, "clash", "proxies:\n  - {name: node-a}\n")
	s := mustServer(t, testConfig(t, extURL))
	ts := httptest.NewServer(s.routes())
	defer ts.Close()
	s.cfg.sidecarBase = ts.URL
	if err := s.store.close(); err != nil {
		t.Fatal(err)
	}
	resp, _ := postPreview(t, s, `{"url":"`+subURL+`","target":"clash"}`, "h:1", "")
	if resp.ErrorCode == nil || *resp.ErrorCode != "store_error" {
		t.Fatalf("errorCode = %v，应 store_error", resp.ErrorCode)
	}
	if !resp.OK {
		t.Errorf("探测成功的 ok 不应被 store_error 覆盖: %+v", resp)
	}
	if !strings.HasPrefix(resp.Link, "http://h:1/sub?target=clash&url=") {
		t.Errorf("store_error 的 link 应降级 /sub 长链: %s", resp.Link)
	}
	if resp.Message == "" {
		t.Error("store_error 时 message 必填")
	}
}

// v1.4.0：target=clash 显式探测走两段式（mixed → clash），与 /s/ 拉取同路
func TestPreviewTwoStageClashExplicit(t *testing.T) {
	subURL, extURL, hits := twoStageEnv(t, "clash", "proxies:\n  - name: fixture")
	s := mustServer(t, testConfig(t, extURL))
	ts := httptest.NewServer(s.routes())
	defer ts.Close()
	s.cfg.sidecarBase = ts.URL
	resp, _ := postPreview(t, s, `{"url":"`+subURL+`","target":"clash"}`, "h:1", "")
	if !resp.OK || resp.Target != "clash" || resp.ErrorCode != nil {
		t.Fatalf("clash 探测响应 = %+v", resp)
	}
	if len(*hits) != 2 || (*hits)[0].target != "mixed" || (*hits)[1].target != "clash" {
		t.Fatalf("应两段式 mixed→clash，实际 %+v", *hits)
	}
	if got := (*hits)[1].url; !strings.Contains(got, "ss://") || !strings.Contains(got, "vmess://") || !strings.Contains(got, "|") {
		t.Errorf("第二段 url 应为 | 连接的节点列表: %q", got)
	}
	if (*hits)[1].config != "https://example.com/preset.ini" {
		t.Errorf("第二段 config = %q，应所选档位 url", (*hits)[1].config)
	}
}

// v1.4.0：两段式第一段空节点 → 探测失败（upstream_error 既有等价类——订阅源无有效节点）
func TestPreviewTwoStageEmptyNodes(t *testing.T) {
	airportURL := publicAirportURL(t, "garbage-no-uri")
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
	t.Cleanup(ext.Close)
	s := mustServer(t, testConfig(t, ext.URL))
	ts := httptest.NewServer(s.routes())
	defer ts.Close()
	s.cfg.sidecarBase = ts.URL
	resp, _ := postPreview(t, s, `{"url":"`+airportURL+`","target":"clash"}`, "h:1", "")
	if resp.OK || resp.ErrorCode == nil || *resp.ErrorCode != "upstream_error" {
		t.Fatalf("空节点列表探测应 upstream_error: %+v", resp)
	}
}

// M1：机场 URI 合法、引擎 200 但 proxies 列表为空（URI 全是引擎不识别协议的模拟）→
// 内联节点计数粗验必须判红（早前「含 proxies: 即可」的等价论证漏洞场景）
func TestPreviewTwoStageEngineDropsAll(t *testing.T) {
	subURL, extURL, _ := twoStageEnv(t, "clash", "proxies:\nrules:\n  - MATCH,DIRECT\n")
	s := mustServer(t, testConfig(t, extURL))
	ts := httptest.NewServer(s.routes())
	defer ts.Close()
	s.cfg.sidecarBase = ts.URL
	resp, _ := postPreview(t, s, `{"url":"`+subURL+`","target":"clash"}`, "h:1", "")
	if resp.OK || resp.ErrorCode == nil || *resp.ErrorCode != "upstream_error" {
		t.Fatalf("引擎丢弃全部节点（空 proxies 列表）应 upstream_error: %+v", resp)
	}
}

// m1：preview 两段式超时分类——引擎第一段响应慢于段超时 → upstream_timeout
func TestPreviewTwoStageTimeout(t *testing.T) {
	airportURL := publicAirportURL(t, "vmess://x\n")
	ext := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(3 * time.Second) // 远超段超时（probeTimeout 200ms → 段 100ms）
		_, _ = w.Write([]byte("proxies:\n  - {name: node-a}\n"))
	}))
	t.Cleanup(ext.Close)
	cfg := testConfig(t, ext.URL)
	cfg.probeTimeout = 200 * time.Millisecond
	s := mustServer(t, cfg)
	ts := httptest.NewServer(s.routes())
	defer ts.Close()
	s.cfg.sidecarBase = ts.URL
	resp, _ := postPreview(t, s, `{"url":"`+airportURL+`","target":"clash"}`, "h:1", "")
	if resp.OK || resp.ErrorCode == nil || *resp.ErrorCode != "upstream_timeout" {
		t.Fatalf("段超时应 upstream_timeout: %+v", resp)
	}
}

// m1：preview 两段式引擎不可达分类——代拉正常、引擎拒连 → backend_down（stage 1）
func TestPreviewTwoStageBackendDown(t *testing.T) {
	airportURL := publicAirportURL(t, "vmess://x\n")
	ext := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	extURL := ext.URL
	ext.Close() // 立即关闭 → 连接拒绝
	s := mustServer(t, testConfig(t, extURL))
	ts := httptest.NewServer(s.routes())
	defer ts.Close()
	s.cfg.sidecarBase = ts.URL
	resp, _ := postPreview(t, s, `{"url":"`+airportURL+`","target":"clash"}`, "h:1", "")
	if resp.OK || resp.ErrorCode == nil || *resp.ErrorCode != "backend_down" {
		t.Fatalf("引擎拒连应 backend_down: %+v", resp)
	}
}

// M1 计数器直测：clash 空/非空列表与 flow 空数组、quanx server_local 空段/有节点/等号空格风格
func TestTwoStageInlineNodes(t *testing.T) {
	cases := []struct {
		target string
		body   string
		want   bool
	}{
		{"clash", "proxies:\n  - {name: a}\n  - {name: b}\nrules:\n  - MATCH,DIRECT", true},
		{"clash", "proxies:\n  - name: a\n    type: ss\nrules:\n  - MATCH,DIRECT", true},
		{"clash", "proxies:\nrules:\n  - MATCH,DIRECT", false},
		{"clash", "proxies: []\nrules:\n  - MATCH,DIRECT", false},
		{"clash", "port: 7890\nrules:\n  - MATCH,DIRECT", false},
		{"quanx", "[server_local]\nvmess = 5.6.7.8:443, tag=b\n", true},
		{"quanx", "[server_local]\nshadowsocks=1.2.3.4:8388, method=aes-128-gcm, password=x, tag=a\n", true},
		{"quanx", "[server_local]\n[filter_remote]\n", false},
		{"quanx", "#!MANAGED-CONFIG\n[filter_remote]\n", false},
		{"surge", "[General]\n", false}, // 非两段式 target 恒 false（不该被调到，防御）
	}
	for _, c := range cases {
		if got := twoStageInlineNodes(c.target, []byte(c.body)); got != c.want {
			t.Errorf("twoStageInlineNodes(%s) = %v, want %v\nbody:\n%s", c.target, got, c.want, c.body)
		}
	}
}
