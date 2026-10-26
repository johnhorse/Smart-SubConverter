// 快照本地化单测：改写器、静默丢规则校验（不过不切换）、逐文件 last-known-good、
// 出厂层引导、/s/ 存量短链映射、engineConfig 回退、health 快照字段与三态、
// env 解析、DATA_DIR 出树降级。
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testINI = `[custom]
; 引用本地规则集与内置型混合（闸门实测非 URL 值仅 []FINAL/[]GEOIP,CN）
ruleset=🎯 全球直连,https://raw.githubusercontent.com/ACL4SSR/ACL4SSR/master/Clash/rules/LocalAreaNetwork.list
ruleset=🛑 全球拦截,https://raw.githubusercontent.com/ACL4SSR/ACL4SSR/master/Clash/rules/BanAD.list
ruleset=🐟 漏网之鱼,[]FINAL
ruleset=🎯 全球直连,[]GEOIP,CN
custom_proxy_group=🚀 节点选择` + "`.*`" + `
ruleset=DIRECT,https://example.com/r/other.list?ver=1
`

// newTestManager 独立 tmp 数据目录的快照管理器（baseDir 随 dataDir 注入，
// pathInScope 校验在测试目录内闭合）
func newTestManager(t *testing.T) *snapshotManager {
	t.Helper()
	cfg := config{dataDir: t.TempDir(), presetRefreshInterval: time.Hour}
	m := newSnapshotManager(cfg, nil)
	m.baseDir = cfg.dataDir
	return m
}

// SNAPSHOT_PROXY（v1.3.1）：快照刷新的上游抓取走代理；订阅链路不经此 client（代拉走 SUBSCRIPTION_PROXY，v1.3.8）
func TestSnapshotProxyApplied(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/default.ini":
			_, _ = w.Write([]byte("ruleset=DIRECT,http://" + r.Host + "/a.list\n"))
		case "/a.list":
			_, _ = w.Write([]byte("a-content"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer up.Close()

	var proxied int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&proxied, 1)
		out, err := http.Get(r.URL.String()) // 正向代理收到绝对 URI 形态请求
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer out.Body.Close()
		w.WriteHeader(out.StatusCode)
		_, _ = io.Copy(w, out.Body)
	}))
	defer proxy.Close()

	pu, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config{dataDir: t.TempDir(), presetRefreshInterval: time.Hour, snapshotProxy: pu}
	m := newSnapshotManager(cfg, nil)
	m.baseDir = cfg.dataDir
	m.presets = []presetEntry{{"default", "t", up.URL + "/default.ini"}}
	m.refreshOnce()

	if atomic.LoadInt32(&proxied) == 0 {
		t.Error("SNAPSHOT_PROXY 配置后刷新流量应经代理")
	}
	if _, err := os.Stat(filepath.Join(m.rulesDir, "default.rewritten.ini")); err != nil {
		t.Errorf("经代理刷新应产出改写产物: %v", err)
	}
}

func TestRewriteINI(t *testing.T) {
	rulesDir := "/base/data/rules"
	out, refs := rewriteINI([]byte(testINI), rulesDir)
	s := string(out)

	lan := filepath.Join(rulesDir, "LocalAreaNetwork.list")
	ban := filepath.Join(rulesDir, "BanAD.list")
	other := filepath.Join(rulesDir, "other.list")
	for _, want := range []string{
		"ruleset=🎯 全球直连," + lan,
		"ruleset=🛑 全球拦截," + ban,
		"ruleset=DIRECT," + other, // query 串剥掉，basename 落平铺
	} {
		if !strings.Contains(s, want) {
			t.Errorf("改写产物缺 %q:\n%s", want, s)
		}
	}
	// 非 URL 行与其他行原样不动
	for _, want := range []string{
		"ruleset=🐟 漏网之鱼,[]FINAL",
		"ruleset=🎯 全球直连,[]GEOIP,CN",
		"custom_proxy_group=🚀 节点选择",
		"[custom]",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("非 ruleset URL 行被误改，缺 %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "raw.githubusercontent.com") || strings.Contains(s, "example.com/r/") {
		t.Errorf("改写后仍残留上游 URL:\n%s", s)
	}
	wantRefs := []string{lan, ban, other}
	if len(refs) != len(wantRefs) {
		t.Fatalf("refs = %v, want %v", refs, wantRefs)
	}
	for i, r := range refs {
		if r != wantRefs[i] {
			t.Errorf("refs[%d] = %s, want %s", i, r, wantRefs[i])
		}
	}
	// 引用路径必须是绝对路径（引擎相对路径按 cwd 解析，闸门实测会静默丢规则）
	for _, r := range refs {
		if !filepath.IsAbs(r) {
			t.Errorf("ref %s 非绝对路径", r)
		}
	}
}

func TestExtractRulesetURLs(t *testing.T) {
	urls := extractRulesetURLs([]byte(testINI))
	want := []string{
		"https://raw.githubusercontent.com/ACL4SSR/ACL4SSR/master/Clash/rules/LocalAreaNetwork.list",
		"https://raw.githubusercontent.com/ACL4SSR/ACL4SSR/master/Clash/rules/BanAD.list",
		"https://example.com/r/other.list?ver=1",
	}
	if len(urls) != len(want) {
		t.Fatalf("urls = %v, want %v", urls, want)
	}
	for i, u := range urls {
		if u != want[i] {
			t.Errorf("urls[%d] = %s, want %s", i, u, want[i])
		}
	}
}

// upStream 桩上游：按 path 分发内容与状态（ruleset 文件名 → body/status）
type upStream struct {
	srv *httptest.Server
	mu  chan struct{}
}

// 静默丢规则防护：改写后引用的规则集文件缺失 → 不切换（保留旧产物）
func TestRewriteSwapValidationNoSwap(t *testing.T) {
	m := newTestManager(t)
	iniBody := "placeholder"
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/default.ini":
			_, _ = w.Write([]byte(iniBody))
		case "/a.list":
			_, _ = w.Write([]byte("a-content"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer up.Close()
	m.client = up.Client()
	m.presets = []presetEntry{{"default", "t", up.URL + "/default.ini"}}

	iniBody = "ruleset=DIRECT," + up.URL + "/a.list\n"
	m.refreshOnce()
	got1 := readTB(t, filepath.Join(m.rulesDir, "default.rewritten.ini"))
	if !strings.Contains(got1, "a.list") {
		t.Fatalf("第一轮改写产物异常: %s", got1)
	}

	// 第二轮模板改引 b.list（上游 404 且本地不存在）→ 校验不过，保留旧产物
	iniBody = "ruleset=DIRECT," + up.URL + "/b.list\n"
	m.refreshOnce()
	got2 := readTB(t, filepath.Join(m.rulesDir, "default.rewritten.ini"))
	if got2 != got1 {
		t.Fatalf("校验不过时应保留旧产物不切换:\n旧=%s\n新=%s", got1, got2)
	}
	if _, err := os.Stat(filepath.Join(m.rulesDir, "default.rewritten.ini.tmp")); err == nil {
		t.Error("临时文件应被 rename 掉，不应残留")
	}
}

// 逐文件 last-known-good：单文件成功即覆盖、失败保留旧文件；该档零新内容时
// lastRefreshedAt 不动
func TestRefreshLastKnownGood(t *testing.T) {
	m := newTestManager(t)

	// 预置旧文件（上一轮 last-known-good）
	writeTB(t, filepath.Join(m.rulesDir, "a.list"), "old-a")
	writeTB(t, filepath.Join(m.rulesDir, "b.list"), "old-b")

	// mode：partial = .ini/a.list 成功 b.list 500；all-fail = 全 500。
	// 两轮顺序执行、每轮 fetch 全部收尾后才切换 mode，无并发写竞争
	mode := "partial"
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if mode == "all-fail" {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		switch r.URL.Path {
		case "/default.ini":
			w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
			_, _ = w.Write([]byte("ruleset=DIRECT," + up0(r) + "/a.list\nruleset=DIRECT," + up0(r) + "/b.list\n"))
		case "/a.list":
			_, _ = w.Write([]byte("new-a"))
		default:
			http.Error(w, "boom", http.StatusInternalServerError)
		}
	}))
	defer up.Close()
	m.client = up.Client()
	m.presets = []presetEntry{{"default", "t", up.URL + "/default.ini"}}

	m.refreshOnce()
	if got := readTB(t, filepath.Join(m.rulesDir, "a.list")); got != "new-a" {
		t.Errorf("a.list 成功应覆盖: %q", got)
	}
	if got := readTB(t, filepath.Join(m.rulesDir, "b.list")); got != "old-b" {
		t.Errorf("b.list 失败应保留旧文件: %q", got)
	}
	st1 := m.stateOf("default")
	if st1.lastRefreshedAt == nil {
		t.Fatal("a.list 有新内容落地，lastRefreshedAt 应更新")
	}
	if st1.lastModifiedAt == nil {
		t.Error(".ini 上游带 Last-Modified 头，lastModifiedAt 应非空")
	}

	// 第二轮全失败：本地补 .ini 后再刷，文件全保留、lastRefreshedAt 不动
	mode = "all-fail"
	writeTB(t, filepath.Join(m.rulesDir, "default.ini"), "ruleset=DIRECT,"+up.URL+"/a.list\nruleset=DIRECT,"+up.URL+"/b.list\n")
	rewrittenBefore := readTB(t, filepath.Join(m.rulesDir, "default.rewritten.ini"))
	m.refreshOnce()
	if got := readTB(t, filepath.Join(m.rulesDir, "b.list")); got != "old-b" {
		t.Errorf("全失败轮 b.list 应保留: %q", got)
	}
	if got := readTB(t, filepath.Join(m.rulesDir, "default.rewritten.ini")); got != rewrittenBefore {
		t.Errorf("全失败轮改写产物不应漂移")
	}
	st2 := m.stateOf("default")
	if !st2.lastRefreshedAt.Equal(*st1.lastRefreshedAt) {
		t.Errorf("零新内容落地时 lastRefreshedAt 不应更新: %v → %v", st1.lastRefreshedAt, st2.lastRefreshedAt)
	}
}

// up0 从请求自身还原上游 origin（构造引用本上游的 .ini 内容用）
func up0(r *http.Request) string {
	return "http://" + r.Host
}

// 出厂层引导：vendor 文件复制（不覆盖已有）+ SNAPSHOT_META 时间戳 + 上游不可达
// （离线）仍能改写出产物
func TestBootstrapVendorOffline(t *testing.T) {
	vendor := t.TempDir()
	metaAt := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	writeTB(t, filepath.Join(vendor, "ACL4SSR_Online.ini"), "ruleset=DIRECT,http://up/a.list\n")
	writeTB(t, filepath.Join(vendor, "a.list"), "a-content")
	writeTB(t, filepath.Join(vendor, snapshotMetaFile), metaAt.Format(time.RFC3339))

	m := newTestManager(t)
	m.vendorDir = vendor
	// 上游不可达（端口 1 快速拒连，不打外网；basename 与 vendor 文件名一致）
	m.presets = []presetEntry{{"default", "ACL4SSR 默认", "http://127.0.0.1:1/ACL4SSR_Online.ini"}}

	m.bootstrap()
	for _, f := range []string{"ACL4SSR_Online.ini", "a.list"} {
		if _, err := os.Stat(filepath.Join(m.rulesDir, f)); err != nil {
			t.Fatalf("出厂文件 %s 未复制: %v", f, err)
		}
	}
	st := m.stateOf("default")
	if st.lastRefreshedAt == nil || !st.lastRefreshedAt.Equal(metaAt) {
		t.Fatalf("lastRefreshedAt = %v, want SNAPSHOT_META %v", st.lastRefreshedAt, metaAt)
	}

	m.refreshOnce()
	if _, ok := m.rewrittenPath(m.presets[0]); !ok {
		t.Fatal("离线 + 出厂层：首轮改写后应有可用产物")
	}
	st = m.stateOf("default")
	if !st.lastRefreshedAt.Equal(metaAt) {
		t.Errorf("零新内容落地时 lastRefreshedAt 应保持 vendor 时刻: %v, want %v", st.lastRefreshedAt, metaAt)
	}

	// 已有文件不被出厂层覆盖
	writeTB(t, filepath.Join(m.rulesDir, "a.list"), "newer-than-vendor")
	m.bootstrap()
	if got := readTB(t, filepath.Join(m.rulesDir, "a.list")); got != "newer-than-vendor" {
		t.Errorf("运行时层已有文件不应被出厂层覆盖: %q", got)
	}
}

// SNAPSHOT_META 为 fetch.sh 产出的 JSON 形态（v1.3.0 早期实现与 readVendorMeta 的
// 纯时间戳约定不一致导致解析失败）：readVendorMeta 兼容取 pulledAt
func TestBootstrapVendorMetaJSON(t *testing.T) {
	vendor := t.TempDir()
	metaAt := time.Now().UTC().Add(-1 * time.Hour).Truncate(time.Second)
	writeTB(t, filepath.Join(vendor, "ACL4SSR_Online.ini"), "ruleset=DIRECT,http://up/a.list\n")
	writeTB(t, filepath.Join(vendor, "a.list"), "a-content")
	writeTB(t, filepath.Join(vendor, snapshotMetaFile),
		fmt.Sprintf(`{"pulledAt":%q,"upstream":{"commit":"abc"},"files":{"a.list":"xx"}}`, metaAt.Format(time.RFC3339)))

	m := newTestManager(t)
	m.vendorDir = vendor
	m.presets = []presetEntry{{"default", "ACL4SSR 默认", "http://127.0.0.1:1/ACL4SSR_Online.ini"}}
	m.bootstrap()
	st := m.stateOf("default")
	if st.lastRefreshedAt == nil || !st.lastRefreshedAt.Equal(metaAt) {
		t.Fatalf("JSON META 的 pulledAt 未生效: %v, want %v", st.lastRefreshedAt, metaAt)
	}
}

// snapshotServerWithSnapshot 构造 snap 注入的 server（rulesDir 内预置 default 档
// 改写产物），供 /s/ 映射与 preview engineConfig 端到端用
func serverWithSnapshot(t *testing.T, cfg config) (*server, *snapshotManager) {
	t.Helper()
	s := mustServer(t, cfg)
	m := newTestManager(t)
	writeTB(t, filepath.Join(m.rulesDir, "ACL4SSR_Online.rewritten.ini"), "rewritten-content")
	s.snap = m
	s.mon.snap = m
	s.mon.exempt = s.presetExempt
	return s, m
}

// /s/ 存量短链映射：冻结 config 为内置三档 GitHub URL → 运行时映射本地产物；
// 无关 URL 原样透传
func TestShortLinkFrozenConfigMapping(t *testing.T) {
	var gotConfigs []string
	ext := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotConfigs = append(gotConfigs, r.URL.Query().Get("config"))
		_, _ = w.Write([]byte("proxies: x"))
	}))
	defer ext.Close()

	cfg := testConfig(t, ext.URL)
	cfg.presetURL = defaultPresetURL // 未覆盖 → default 本地化（不豁免）
	s, m := serverWithSnapshot(t, cfg)
	wantPath, ok := m.rewrittenPath(presetTable[0])
	if !ok {
		t.Fatal("测试预置产物缺失")
	}
	ts := httptest.NewServer(s.routes())
	defer ts.Close()

	// 直通靶 target（v1.4.0 起 clash 显式走两段式；config 映射语义与链 target 无关，直通保语义）
	old := newLink(t, s, "https://sub.example.com/old", "loon", "default", defaultPresetURL) // v1.2.0 存量链
	get(t, ts.URL+"/s/"+old).Body.Close()
	other := newLink(t, s, "https://sub.example.com/x", "loon", "default", "https://deploy.example.com/custom.ini")
	get(t, ts.URL+"/s/"+other).Body.Close()

	if len(gotConfigs) != 2 {
		t.Fatalf("上游请求数 = %d", len(gotConfigs))
	}
	if gotConfigs[0] != wantPath {
		t.Errorf("存量链 config = %s, want 映射到 %s", gotConfigs[0], wantPath)
	}
	if gotConfigs[1] != "https://deploy.example.com/custom.ini" {
		t.Errorf("非内置 URL 不应映射: %s", gotConfigs[1])
	}
}

// engineConfig：本地化档探测与建链冻结的 config = 改写后 .ini 路径；
// 产物缺失（快照未就绪窗口）→ 透传原始 URL
func TestPreviewEngineConfigSnapshot(t *testing.T) {
	var gotConfig string
	ext := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotConfig = r.URL.Query().Get("config")
		_, _ = w.Write([]byte("proxies: x"))
	}))
	defer ext.Close()

	cfg := testConfig(t, ext.URL)
	cfg.presetURL = defaultPresetURL
	s, m := serverWithSnapshot(t, cfg)
	wantPath, _ := m.rewrittenPath(presetTable[0])
	ts := httptest.NewServer(s.routes())
	defer ts.Close()

	body := `{"url":"https://sub.example.com/l","target":"loon"}` // 直通靶（engineConfig 快照路径与探测路径无关）
	resp, err := http.Post(ts.URL+"/api/preview", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var pr previewResponse
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		t.Fatal(err)
	}
	if !pr.OK {
		t.Fatalf("preview ok=false code=%s msg=%s", deref(pr.ErrorCode), pr.Message)
	}
	if gotConfig != wantPath {
		t.Errorf("探测 config = %s, want %s", gotConfig, wantPath)
	}
	if lnk := storedFor(t, s, pr.Link); lnk.ConfigURL != wantPath {
		t.Errorf("建链冻结 config = %s, want %s（本地化档应冻结改写后路径）", lnk.ConfigURL, wantPath)
	}

	// 快照未就绪（产物缺失）→ 回退透传原始 URL
	os.Remove(wantPath)
	if got := s.engineConfig(presetTable[0]); got != defaultPresetURL {
		t.Errorf("产物缺失应透传原始 URL: %s", got)
	}
}

func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

// health：本地化档 source=snapshot + lastRefreshedAt + 快照驱动 status；
// 豁免档 source=remote 维持远端 HEAD；pollPresets 不打本地化档
func TestHealthSnapshotFields(t *testing.T) {
	cfg := config{presetURL: "https://example.com/override.ini", presetRefreshInterval: time.Hour} // default 豁免
	m := newMonitor(cfg)
	s := &server{cfg: cfg}
	mgr := newSnapshotManager(config{dataDir: t.TempDir()}, s.presetExempt)
	m.snap = mgr
	m.exempt = s.presetExempt

	fresh := time.Now().Add(-30 * time.Minute)
	old := time.Now().Add(-3 * time.Hour) // freshWindow = 2h
	mgr.states["full_multimode"] = &snapshotState{lastRefreshedAt: &fresh}
	mgr.states["full_adblockplus"] = &snapshotState{lastRefreshedAt: &old}

	resp := m.snapshot()
	p0, p1, p2 := resp.Presets[0], resp.Presets[1], resp.Presets[2]
	if p0.Source != "remote" || p0.LastRefreshedAt != nil {
		t.Errorf("豁免档 = source:%s lastRefreshedAt:%v, want remote/null", p0.Source, p0.LastRefreshedAt)
	}
	if p1.Source != "snapshot" || p1.Status != "fresh" {
		t.Errorf("multimode = %+v, want snapshot/fresh", p1)
	}
	if p1.LastRefreshedAt == nil || p1.LastSuccessAt == nil || *p1.LastRefreshedAt != *p1.LastSuccessAt {
		t.Errorf("本地化档 lastRefreshedAt 应与 lastSuccessAt 同值: %v / %v", p1.LastRefreshedAt, p1.LastSuccessAt)
	}
	if p2.Source != "snapshot" || p2.Status != "stale" {
		t.Errorf("adblockplus = %+v, want snapshot/stale（3h > 2×1h）", p2)
	}

	// 从未刷新成功 → unknown（不冒充 fresh）
	mgr2 := newSnapshotManager(config{dataDir: t.TempDir()}, s.presetExempt)
	m.snap = mgr2
	p := m.snapshot().Presets[1]
	if p.Source != "snapshot" || p.Status != "unknown" || p.LastRefreshedAt != nil {
		t.Errorf("从未刷新的本地化档 = %+v, want snapshot/unknown/null", p)
	}

	// 本地化档不发远端 HEAD：桩上游只应收到豁免档（default）的请求
	var hits int32
	head := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead {
			t.Errorf("轮询应发 HEAD，收到 %s", r.Method)
		}
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(200)
	}))
	defer head.Close()
	pointPresetsAt(m, head.URL)
	m.pollPresets()
	if hits != 1 {
		t.Errorf("HEAD 请求数 = %d, want 1（仅豁免档；本地化档快照驱动不发 HEAD）", hits)
	}
}

func TestSnapshotStatusOf(t *testing.T) {
	now := time.Now()
	fresh := now.Add(-47 * time.Hour)
	stale := now.Add(-48 * time.Hour)
	nilAt := (*time.Time)(nil)
	cases := []struct {
		name       string
		at         *time.Time
		wantStatus string
	}{
		{"无时间戳 → unknown", nilAt, "unknown"},
		{"< 2×interval → fresh", &fresh, "fresh"},
		{"≥ 2×interval → stale", &stale, "stale"},
	}
	for _, c := range cases {
		if got := snapshotStatusOf(c.at, 48*time.Hour, now); got != c.wantStatus {
			t.Errorf("%s: status = %s, want %s", c.name, got, c.wantStatus)
		}
	}
}

// env 解析：PRESET_REFRESH_INTERVAL / PROBE_TIMEOUT / SUB_PROXY_TIMEOUT 默认值与覆盖
func TestEnvDurationParsing(t *testing.T) {
	t.Setenv("POLL_INTERVAL", "")
	t.Setenv("SUB_PROXY_TIMEOUT", "")
	t.Setenv("PROBE_TIMEOUT", "")
	t.Setenv("PRESET_REFRESH_INTERVAL", "")
	c := loadConfigFromEnv()
	if c.subProxyTimeout != 120*time.Second {
		t.Errorf("SUB_PROXY_TIMEOUT 默认 = %s, want 120s", c.subProxyTimeout)
	}
	if c.probeTimeout != 30*time.Second {
		t.Errorf("PROBE_TIMEOUT 默认 = %s, want 30s", c.probeTimeout)
	}
	if c.presetRefreshInterval != 24*time.Hour {
		t.Errorf("PRESET_REFRESH_INTERVAL 默认 = %s, want 24h", c.presetRefreshInterval)
	}

	t.Setenv("PROBE_TIMEOUT", "500ms")
	t.Setenv("PRESET_REFRESH_INTERVAL", "1h")
	t.Setenv("SUB_PROXY_TIMEOUT", "90s")
	c = loadConfigFromEnv()
	if c.probeTimeout != 500*time.Millisecond || c.presetRefreshInterval != time.Hour || c.subProxyTimeout != 90*time.Second {
		t.Errorf("env 覆盖解析错误: probe=%s refresh=%s subProxy=%s", c.probeTimeout, c.presetRefreshInterval, c.subProxyTimeout)
	}
}

// SUBSCRIPTION_PROXY env：合法值注入 subscriptionProxy、非法值回退 nil，且不影响 snapshotProxy
func TestEnvSubscriptionProxyParsing(t *testing.T) {
	t.Setenv("SUBSCRIPTION_PROXY", "http://127.0.0.1:7890")
	c := loadConfigFromEnv()
	if c.subscriptionProxy == nil || c.subscriptionProxy.Scheme != "http" || c.subscriptionProxy.Host != "127.0.0.1:7890" {
		t.Errorf("SUBSCRIPTION_PROXY 合法值解析错误: %+v", c.subscriptionProxy)
	}
	if c.snapshotProxy != nil {
		t.Errorf("SUBSCRIPTION_PROXY 不应影响 snapshotProxy，得到 %+v", c.snapshotProxy)
	}
	for _, bad := range []string{"127.0.0.1:7890", "ftp://x", "not a url"} {
		t.Setenv("SUBSCRIPTION_PROXY", bad)
		if c = loadConfigFromEnv(); c.subscriptionProxy != nil {
			t.Errorf("SUBSCRIPTION_PROXY %q 应回退直连，得到 %+v", bad, c.subscriptionProxy)
		}
	}
	t.Setenv("SUBSCRIPTION_PROXY", "")
	if c = loadConfigFromEnv(); c.subscriptionProxy != nil {
		t.Errorf("缺省应直连，得到 %+v", c.subscriptionProxy)
	}
}

// SNAPSHOT_PROXY env：合法 http/socks5 注入、非法值回退直连（nil）、缺省 nil
func TestEnvSnapshotProxyParsing(t *testing.T) {
	t.Setenv("SNAPSHOT_PROXY", "http://127.0.0.1:7890")
	c := loadConfigFromEnv()
	if c.snapshotProxy == nil || c.snapshotProxy.Scheme != "http" || c.snapshotProxy.Host != "127.0.0.1:7890" {
		t.Errorf("SNAPSHOT_PROXY 合法值解析错误: %+v", c.snapshotProxy)
	}
	t.Setenv("SNAPSHOT_PROXY", "socks5://host:1080")
	if c = loadConfigFromEnv(); c.snapshotProxy == nil || c.snapshotProxy.Scheme != "socks5" {
		t.Errorf("socks5 值解析错误: %+v", c.snapshotProxy)
	}
	for _, bad := range []string{"127.0.0.1:7890", "ftp://x", "not a url"} {
		t.Setenv("SNAPSHOT_PROXY", bad)
		if c = loadConfigFromEnv(); c.snapshotProxy != nil {
			t.Errorf("SNAPSHOT_PROXY %q 应回退直连，得到 %+v", bad, c.snapshotProxy)
		}
	}
	t.Setenv("SNAPSHOT_PROXY", "")
	if c = loadConfigFromEnv(); c.snapshotProxy != nil {
		t.Errorf("缺省应直连，得到 %+v", c.snapshotProxy)
	}

	t.Setenv("PROBE_TIMEOUT", "garbage")
	t.Setenv("PRESET_REFRESH_INTERVAL", "-5h") // 非正值同样回退
	c = loadConfigFromEnv()
	if c.probeTimeout != 30*time.Second || c.presetRefreshInterval != 24*time.Hour {
		t.Errorf("非法值应回退默认: probe=%s refresh=%s", c.probeTimeout, c.presetRefreshInterval)
	}
}

// DATA_DIR 出树判定（引擎硬限 /base 树）
func TestSnapshotEnabled(t *testing.T) {
	cases := []struct {
		dir  string
		want bool
	}{
		{"/base/data", true},
		{"/base", true},
		{"/base/rules/nested", true},
		{"/data", false},
		{"/basedata", false}, // 兄弟目录不算树内
		{"/var/folders/t/tmp123", false},
	}
	for _, c := range cases {
		if got := snapshotEnabled(c.dir); got != c.want {
			t.Errorf("snapshotEnabled(%s) = %v, want %v", c.dir, got, c.want)
		}
	}
}

// 降级：DATA_DIR 出树（单测 tmp 天然出树）→ snap 为 nil、全档透传原 URL、
// health 全 remote——不拒启动
func TestDegradedOutOfTree(t *testing.T) {
	s := mustServer(t, testConfig(t, "http://unused"))
	if s.snap != nil {
		t.Fatal("出树 DATA_DIR 不应构造快照管理器")
	}
	for _, p := range s.presets {
		if got := s.engineConfig(p); got != p.URL {
			t.Errorf("降级下 engineConfig(%s) = %s, want 原样透传", p.ID, got)
		}
	}
	for _, p := range s.mon.snapshot().Presets {
		if p.Source != "remote" {
			t.Errorf("降级下 health %s source = %s, want remote", p.Name, p.Source)
		}
	}
}

func writeTB(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readTB(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
