package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseBackendVersion(t *testing.T) {
	html := `<html><body><div class="info-label">Version</div><div class="info-value">v1.10.5</div></body></html>`
	// 直接测正则行为（与 pollBackendVersion 共用 versionRe）
	if mm := versionRe.FindSubmatch([]byte(html)); mm == nil || string(mm[1]) != "v1.10.5" {
		t.Errorf("versionRe 未提取出版本: %v", mm)
	}
	if mm := versionRe.FindSubmatch([]byte("<p>no version here</p>")); mm != nil {
		t.Errorf("无版本页面不应匹配: %v", mm)
	}
}

// 契约五步判定序（Success Criteria 定案）
func TestStatusOfFiveSteps(t *testing.T) {
	now := time.Now()
	h1 := now.Add(-1 * time.Hour)
	d2 := now.Add(-25 * time.Hour)

	tests := []struct {
		name string
		st   presetState
		want string
	}{
		{"1 从未成功 → unknown", presetState{}, "unknown"},
		{"2 最近失败（即使 lastModified 很新）→ stale", presetState{lastSuccessAt: &h1, lastModifiedAt: &h1, lastAttemptFailed: true}, "stale"},
		{"3 成功但 lastModifiedAt null → unknown", presetState{lastSuccessAt: &h1, lastModifiedAt: nil}, "unknown"},
		{"4 lastModified < 24h → fresh", presetState{lastSuccessAt: &h1, lastModifiedAt: &h1}, "fresh"},
		{"5 lastModified ≥ 24h → stale", presetState{lastSuccessAt: &h1, lastModifiedAt: &d2}, "stale"},
		{"边界 24h 整 → stale（≥24h）", presetState{lastSuccessAt: &h1, lastModifiedAt: ptrTime(now.Add(-24 * time.Hour))}, "stale"},
	}
	for _, tt := range tests {
		if got := tt.st.statusOf(now); got != tt.want {
			t.Errorf("%s: statusOf = %s, want %s", tt.name, got, tt.want)
		}
	}
}

func ptrTime(t time.Time) *time.Time { return &t }

// pointPresetsAt 把三档 url 全指向同一地址（单测离线跑：内置档位 URL 指向 GitHub raw，不打外网）
func pointPresetsAt(m *monitor, rawURL string) {
	for i := range m.defs {
		m.defs[i].URL = rawURL
	}
}

// presetState 变迁语义：失败保留旧值、成功覆盖、Last-Modified 头缺失→null
func TestPollPresetStateTransitions(t *testing.T) {
	// HTTP Last-Modified 头为秒精度，期望值同步截断
	lastMod := time.Now().Add(-2 * time.Hour).UTC().Truncate(time.Second)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead {
			t.Errorf("轮询应发 HEAD，收到 %s", r.Method)
		}
		w.Header().Set("ETag", `"abc"`)
		w.Header().Set("Last-Modified", lastMod.Format(http.TimeFormat))
		w.WriteHeader(200)
	}))
	defer srv.Close()

	m := newMonitor(config{presetURL: srv.URL})
	pointPresetsAt(m, srv.URL)
	m.pollPresets()

	m.mu.Lock()
	st := m.presets[0]
	m.mu.Unlock()
	if st.lastAttemptFailed || st.lastSuccessAt == nil || st.lastModifiedAt == nil || st.etag != `"abc"` {
		t.Fatalf("成功轮询后状态 = %+v", st)
	}
	if !st.lastModifiedAt.Equal(lastMod) {
		t.Errorf("lastModifiedAt = %v, want %v", st.lastModifiedAt, lastMod)
	}

	// 头缺失（GitHub raw 场景）：lastModifiedAt → null，etag 更新
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"def"`)
		w.WriteHeader(200)
	}))
	defer srv2.Close()
	m2 := newMonitor(config{presetURL: srv2.URL})
	pointPresetsAt(m2, srv2.URL)
	m2.pollPresets()
	m2.mu.Lock()
	st2 := m2.presets[0]
	m2.mu.Unlock()
	if st2.lastModifiedAt != nil || st2.etag != `"def"` || st2.statusOf(time.Now()) != "unknown" {
		t.Fatalf("无 Last-Modified 头状态 = %+v（lastModifiedAt 应 null → status unknown）", st2)
	}

	// 失败：保留旧值 + lastAttemptFailed
	old := time.Now().Add(-1 * time.Hour)
	m3 := newMonitor(config{presetURL: srv.URL})
	m3.presets[0] = presetState{etag: `"x"`, lastModifiedAt: &old, lastSuccessAt: &old}
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close()
	pointPresetsAt(m3, deadURL)
	m3.pollPresets()
	m3.mu.Lock()
	st3 := m3.presets[0]
	m3.mu.Unlock()
	if !st3.lastAttemptFailed || st3.lastSuccessAt == nil || !st3.lastSuccessAt.Equal(old) || st3.lastModifiedAt == nil {
		t.Fatalf("失败后状态 = %+v（应保留旧 lastModifiedAt/lastSuccessAt）", st3)
	}
	if got := st3.statusOf(time.Now()); got != "stale" {
		t.Errorf("最近失败应 stale，got %s", got)
	}
}

func TestSnapshotShape(t *testing.T) {
	// 相对时间：断言含「距今 <24h → fresh」滑动窗口，固定日期会在跨日时炸
	lm := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	ls := time.Now().UTC().Add(-1 * time.Hour).Truncate(time.Second)
	m := newMonitor(config{presetURL: "https://example.com/p.ini"})
	m.backendVersion = "v1.10.5"
	m.presets[0] = presetState{lastModifiedAt: &lm, lastSuccessAt: &ls}

	b, err := json.Marshal(m.snapshot())
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{
		`"backendVersion":"v1.10.5"`,
		`"name":"ACL4SSR 默认"`,
		`"url":"https://example.com/p.ini"`,
		`"lastModifiedAt":"` + lm.Format(time.RFC3339) + `"`,
		`"lastSuccessAt":"` + ls.Format(time.RFC3339) + `"`,
		`"status":"fresh"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("snapshot 缺 %s: %s", want, s)
		}
	}

	// null 语义：未抓取过 → lastModifiedAt/lastSuccessAt 均为 JSON null
	m2 := newMonitor(config{presetURL: "https://example.com/p.ini"})
	b2, _ := json.Marshal(m2.snapshot())
	if !strings.Contains(string(b2), `"lastModifiedAt":null`) || !strings.Contains(string(b2), `"lastSuccessAt":null`) {
		t.Errorf("未抓取时 null 序列化错误: %s", b2)
	}
}

// 三档快照：内置顺序（default 永远第一）与各档 name/url；PRESET_URL 只覆盖 default 档
func TestSnapshotThreePresetTiers(t *testing.T) {
	m := newMonitor(config{presetURL: "https://example.com/preset.ini"})
	resp := m.snapshot()
	if len(resp.Presets) != 3 {
		t.Fatalf("presets 档数 = %d，应为 3", len(resp.Presets))
	}
	want := []presetStatus{
		{Name: "ACL4SSR 默认", URL: "https://example.com/preset.ini", Status: "unknown"},
		{Name: "地区分组 + AI 分流", URL: "https://raw.githubusercontent.com/ACL4SSR/ACL4SSR/master/Clash/config/ACL4SSR_Online_Full_MultiMode.ini", Status: "unknown"},
		{Name: "全量 + 去广告", URL: "https://raw.githubusercontent.com/ACL4SSR/ACL4SSR/master/Clash/config/ACL4SSR_Online_Full_AdblockPlus.ini", Status: "unknown"},
	}
	for i, w := range want {
		got := resp.Presets[i]
		if got.Name != w.Name || got.URL != w.URL || got.Status != w.Status || got.LastModifiedAt != nil || got.LastSuccessAt != nil {
			t.Errorf("presets[%d] = %+v, want %+v", i, got, w)
		}
	}
}

// health handler 即时返回缓存（不在线抓取）：对从未抓取的 monitor 直接 200 + unknown
func TestHandleHealthImmediateCache(t *testing.T) {
	m := newMonitor(config{presetURL: "https://example.com/p.ini"})
	s := &server{cfg: config{}, mon: m}
	w := httptest.NewRecorder()
	s.handleHealth(w, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	if w.Code != 200 {
		t.Fatalf("health 响应码 = %d", w.Code)
	}
	var resp healthResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.BackendVersion != "unknown" || resp.Presets[0].Status != "unknown" {
		t.Errorf("空缓存 health = %+v", resp)
	}
}

// 端到端：version（HTML）+ preset HEAD → 缓存快照正确
func TestHealthPollEndToEnd(t *testing.T) {
	versionSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/version" {
			t.Errorf("version 轮询应打 /version，收到 %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`<div class="info-value">v9.9.9</div>`))
	}))
	defer versionSrv.Close()
	lm := time.Now().UTC().Add(-2 * time.Hour)
	presetSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Last-Modified", lm.Format(http.TimeFormat))
		w.WriteHeader(200)
	}))
	defer presetSrv.Close()

	// extendedBase 指向 versionSrv，三档 preset 全指向 presetSrv（两台桩分饰两角，离线）
	m := newMonitor(config{extendedBase: versionSrv.URL, presetURL: presetSrv.URL})
	pointPresetsAt(m, presetSrv.URL)
	m.poll()

	resp := m.snapshot()
	if resp.BackendVersion != "v9.9.9" {
		t.Errorf("backendVersion = %s, want v9.9.9", resp.BackendVersion)
	}
	if len(resp.Presets) != 3 {
		t.Fatalf("presets 档数 = %d，应为 3", len(resp.Presets))
	}
	p := resp.Presets[0]
	if p.Status != "fresh" || p.LastModifiedAt == nil || !strings.HasSuffix(*p.LastModifiedAt, "Z") {
		t.Errorf("preset 快照 = %+v（2h 前 Last-Modified 应 fresh 且 Z 后缀）", p)
	}
}

// version 挂掉 → unknown，不影响 preset 状态
func TestHealthPollVersionDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`no version div`))
	}))
	defer srv.Close()
	m := newMonitor(config{extendedBase: srv.URL, presetURL: "https://example.com/p.ini"})
	pointPresetsAt(m, "http://127.0.0.1:1/p.ini") // 端口 1 快速拒连，preset 侧不打外网
	m.poll()
	if got := m.snapshot().BackendVersion; got != "unknown" {
		t.Errorf("version 解析失败应 unknown，got %s", got)
	}
}

// 启动时序：version 首次 404（Extended 未就绪）→ pollLoop 短间隔重试直到取到版本
func TestPollLoopRetriesUntilReady(t *testing.T) {
	var calls int32
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`<div class="info-value">v9.9.9</div>`))
	}))
	defer ok.Close()
	preset := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer preset.Close()

	m := newMonitor(config{extendedBase: ok.URL, presetURL: preset.URL})
	pointPresetsAt(m, preset.URL)
	m.retryInterval = 50 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.pollLoop(ctx, time.Hour)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if m.snapshot().BackendVersion == "v9.9.9" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("首次 404 后 pollLoop 未在 2s 内重试取到版本（应为未就绪时短间隔重试）")
}
