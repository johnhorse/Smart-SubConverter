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
	m.pollPreset()

	m.mu.Lock()
	st := m.preset
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
	m2.pollPreset()
	m2.mu.Lock()
	st2 := m2.preset
	m2.mu.Unlock()
	if st2.lastModifiedAt != nil || st2.etag != `"def"` || st2.statusOf(time.Now()) != "unknown" {
		t.Fatalf("无 Last-Modified 头状态 = %+v（lastModifiedAt 应 null → status unknown）", st2)
	}

	// 失败：保留旧值 + lastAttemptFailed
	old := time.Now().Add(-1 * time.Hour)
	m3 := newMonitor(config{presetURL: srv.URL})
	m3.preset = presetState{etag: `"x"`, lastModifiedAt: &old, lastSuccessAt: &old}
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close()
	m3.presetURL = deadURL
	m3.pollPreset()
	m3.mu.Lock()
	st3 := m3.preset
	m3.mu.Unlock()
	if !st3.lastAttemptFailed || st3.lastSuccessAt == nil || !st3.lastSuccessAt.Equal(old) || st3.lastModifiedAt == nil {
		t.Fatalf("失败后状态 = %+v（应保留旧 lastModifiedAt/lastSuccessAt）", st3)
	}
	if got := st3.statusOf(time.Now()); got != "stale" {
		t.Errorf("最近失败应 stale，got %s", got)
	}
}

func TestSnapshotShape(t *testing.T) {
	lm := time.Date(2026, 10, 23, 15, 0, 0, 0, time.UTC)
	ls := time.Date(2026, 10, 23, 16, 0, 0, 0, time.UTC)
	m := newMonitor(config{presetURL: "https://example.com/p.ini"})
	m.backendVersion = "v1.10.5"
	m.preset = presetState{lastModifiedAt: &lm, lastSuccessAt: &ls}

	b, err := json.Marshal(m.snapshot())
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{
		`"backendVersion":"v1.10.5"`,
		`"name":"ACL4SSR Online"`,
		`"url":"https://example.com/p.ini"`,
		`"lastModifiedAt":"2026-10-23T15:00:00Z"`,
		`"lastSuccessAt":"2026-10-23T16:00:00Z"`,
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

	// extendedBase 指向 versionSrv，presetURL 指向 presetSrv（两台桩分饰两角）
	m := newMonitor(config{extendedBase: versionSrv.URL, presetURL: presetSrv.URL})
	m.poll()

	resp := m.snapshot()
	if resp.BackendVersion != "v9.9.9" {
		t.Errorf("backendVersion = %s, want v9.9.9", resp.BackendVersion)
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
