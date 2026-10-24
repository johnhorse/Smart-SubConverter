// /api/health 数据层：sidecar 周期自采（启动立即一次，之后每 POLL_INTERVAL），
// 接口即时返回缓存快照，不做在线抓取。ETag 仅内部变更检测，不进契约。
package main

import (
	"context"
	"io"
	"log"
	"net/http"
	"regexp"
	"sync"
	"time"
)

// /version 返回 HTML，版本号嵌在 <div class="info-value">v…</div>（Assignment ②）
var versionRe = regexp.MustCompile(`info-value[^>]*>\s*(v[\d.]+)`)

type presetState struct {
	etag              string // 内部变更检测用，不进契约
	lastModifiedAt    *time.Time
	lastSuccessAt     *time.Time
	lastAttemptFailed bool
}

type monitor struct {
	mu             sync.Mutex
	backendVersion string
	presets        []presetState // 与 defs 下标对齐（仅豁免/remote 档使用）
	defs           []presetEntry // 解析后的三档（default 档 url 已按 PRESET_URL 覆盖）
	extendedBase   string
	client         *http.Client
	retryInterval  time.Duration          // 未就绪时的重试间隔（默认 5s；测试注入短值）
	snap           *snapshotManager       // v1.3.0：nil = 快照本地化禁用，全档 remote 语义
	exempt         func(presetEntry) bool // 豁免档判定（与 server.presetExempt 同源）
	freshWindow    time.Duration          // 本地化档 fresh 阈值 = 2× PRESET_REFRESH_INTERVAL
}

func newMonitor(cfg config) *monitor {
	interval := cfg.presetRefreshInterval
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	return &monitor{
		backendVersion: "unknown",
		presets:        make([]presetState, len(presetTable)),
		defs:           resolvePresets(cfg.presetURL),
		extendedBase:   cfg.extendedBase,
		client:         upstreamClient(cfg, 10*time.Second), // 豁免档远端 HEAD：与快照刷新同一代理口径
		retryInterval:  5 * time.Second,
		freshWindow:    2 * interval,
	}
}

// isLocalPreset 该档是否快照本地化档（本地化档不发远端 HEAD，status 由快照刷新驱动）
func (m *monitor) isLocalPreset(def presetEntry) bool {
	return m.snap != nil && (m.exempt == nil || !m.exempt(def))
}

func (m *monitor) pollLoop(ctx context.Context, interval time.Duration) {
	m.poll()
	for {
		// 启动时序：s6 并行起三进程，首次 poll 可能跑在 Extended bind 之前；
		// preset 从未成功或版本未取到时用短间隔重试，直到就绪再进入正常周期
		d := interval
		m.mu.Lock()
		presetReady := false
		for i := range m.presets {
			if m.presets[i].lastSuccessAt != nil {
				presetReady = true
				break
			}
		}
		if !presetReady {
			for _, def := range m.defs {
				if m.isLocalPreset(def) && m.snap.stateOf(def.ID).lastRefreshedAt != nil {
					presetReady = true
					break
				}
			}
		}
		ready := presetReady && m.backendVersion != "unknown"
		m.mu.Unlock()
		if !ready && d > m.retryInterval {
			d = m.retryInterval
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(d):
			m.poll()
		}
	}
}

func (m *monitor) poll() {
	m.pollBackendVersion()
	m.pollPresets()
}

func (m *monitor) pollBackendVersion() {
	v := "unknown"
	resp, err := m.client.Get(m.extendedBase + "/version")
	if err == nil {
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if mm := versionRe.FindSubmatch(body); mm != nil {
			v = string(mm[1])
		}
	}
	m.mu.Lock()
	m.backendVersion = v
	m.mu.Unlock()
}

// pollPresets 仅对豁免/remote 档发远端 HEAD（v1.3.0 契约：本地化档由快照刷新循环驱动）。
// 抓取失败保留旧 lastModifiedAt/lastSuccessAt（上次成功的时间不因本次失败失效）。
func (m *monitor) pollPresets() {
	for i, def := range m.defs {
		if m.isLocalPreset(def) {
			continue
		}
		m.pollPresetAt(i, def.URL)
	}
}

func (m *monitor) pollPresetAt(i int, rawURL string) {
	failed := true
	var etag, lastModified string
	req, err := http.NewRequest(http.MethodHead, rawURL, nil)
	if err == nil {
		var resp *http.Response
		resp, err = m.client.Do(req)
		if err == nil {
			defer resp.Body.Close()
			if resp.StatusCode/100 == 2 {
				failed = false
				etag = resp.Header.Get("ETag")
				lastModified = resp.Header.Get("Last-Modified")
			}
		}
	}
	if err != nil {
		// preset URL 是部署者配置而非用户订阅链接，错误类型入日志可定位网络问题且不违 D6
		log.Printf("health poll preset[%d] 失败: %T", i, err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	st := &m.presets[i]
	if failed {
		st.lastAttemptFailed = true
		return
	}
	st.lastAttemptFailed = false
	now := time.Now()
	st.lastSuccessAt = &now
	st.etag = etag
	st.lastModifiedAt = nil
	if lastModified != "" {
		if t, err := http.ParseTime(lastModified); err == nil {
			utc := t.UTC()
			st.lastModifiedAt = &utc
		}
	}
}

// statusOf 按契约五步判定序（缺步即未知是语义：头缺失不冒充 fresh）
func (p presetState) statusOf(now time.Time) string {
	switch {
	case p.lastSuccessAt == nil:
		return "unknown" // 1. 从未成功抓取
	case p.lastAttemptFailed:
		return "stale" // 2. 最近一次抓取失败
	case p.lastModifiedAt == nil:
		return "unknown" // 3. 头缺失
	case now.Sub(*p.lastModifiedAt) < 24*time.Hour:
		return "fresh" // 4.
	default:
		return "stale" // 5.
	}
}

func rfc3339Ptr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}

// snapshotStatusOf 本地化档 status（契约「GET /api/health」）：lastRefreshedAt 距今
// < 2×PRESET_REFRESH_INTERVAL → fresh；≥ → stale；无时间戳 → unknown（不冒充 fresh）
func snapshotStatusOf(at *time.Time, freshWindow time.Duration, now time.Time) string {
	if at == nil {
		return "unknown"
	}
	if now.Sub(*at) < freshWindow {
		return "fresh"
	}
	return "stale"
}

func (m *monitor) snapshot() healthResponse {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	out := make([]presetStatus, len(m.defs))
	for i, def := range m.defs {
		if m.isLocalPreset(def) {
			// 本地化档：数据源 = 快照刷新循环；lastSuccessAt 同 lastRefreshedAt（兼容前端字段名）
			st := m.snap.stateOf(def.ID)
			refreshed := rfc3339Ptr(st.lastRefreshedAt)
			out[i] = presetStatus{
				Name:            def.Name,
				URL:             def.URL,
				Source:          "snapshot",
				LastModifiedAt:  rfc3339Ptr(st.lastModifiedAt),
				LastRefreshedAt: refreshed,
				LastSuccessAt:   refreshed,
				Status:          snapshotStatusOf(st.lastRefreshedAt, m.freshWindow, now),
			}
			continue
		}
		st := m.presets[i]
		out[i] = presetStatus{
			Name:            def.Name,
			URL:             def.URL,
			Source:          "remote",
			LastModifiedAt:  rfc3339Ptr(st.lastModifiedAt),
			LastRefreshedAt: nil,
			LastSuccessAt:   rfc3339Ptr(st.lastSuccessAt),
			Status:          st.statusOf(now),
		}
	}
	return healthResponse{BackendVersion: m.backendVersion, Presets: out}
}
