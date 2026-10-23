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
	mu              sync.Mutex
	backendVersion  string
	preset          presetState
	extendedBase    string
	presetURL       string
	presetName      string
	client          *http.Client
	retryInterval   time.Duration // 未就绪时的重试间隔（默认 5s；测试注入短值）
}

func newMonitor(cfg config) *monitor {
	return &monitor{
		backendVersion: "unknown",
		extendedBase:   cfg.extendedBase,
		presetURL:      cfg.presetURL,
		presetName:     defaultPresetName,
		client:         &http.Client{Timeout: 10 * time.Second},
		retryInterval:  5 * time.Second,
	}
}

func (m *monitor) pollLoop(ctx context.Context, interval time.Duration) {
	m.poll()
	for {
		// 启动时序：s6 并行起三进程，首次 poll 可能跑在 Extended bind 之前；
		// preset 从未成功或版本未取到时用短间隔重试，直到就绪再进入正常周期
		d := interval
		m.mu.Lock()
		ready := m.preset.lastSuccessAt != nil && m.backendVersion != "unknown"
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
	m.pollPreset()
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

// pollPreset 对预设 URL 发 HEAD，读 etag + last-modified。
// 抓取失败保留旧 lastModifiedAt/lastSuccessAt（上次成功的时间不因本次失败失效）。
func (m *monitor) pollPreset() {
	failed := true
	var etag, lastModified string
	req, err := http.NewRequest(http.MethodHead, m.presetURL, nil)
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
		log.Printf("health poll preset 失败: %T", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if failed {
		m.preset.lastAttemptFailed = true
		return
	}
	m.preset.lastAttemptFailed = false
	now := time.Now()
	m.preset.lastSuccessAt = &now
	m.preset.etag = etag
	m.preset.lastModifiedAt = nil
	if lastModified != "" {
		if t, err := http.ParseTime(lastModified); err == nil {
			utc := t.UTC()
			m.preset.lastModifiedAt = &utc
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

func (m *monitor) snapshot() healthResponse {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.preset
	return healthResponse{
		BackendVersion: m.backendVersion,
		Presets: []presetStatus{{
			Name:           m.presetName,
			URL:            m.presetURL,
			LastModifiedAt: rfc3339Ptr(p.lastModifiedAt),
			LastSuccessAt:  rfc3339Ptr(p.lastSuccessAt),
			Status:         p.statusOf(time.Now()),
		}},
	}
}
