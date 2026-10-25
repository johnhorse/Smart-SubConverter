// v1.3.5 内部订阅端点：机场 WAF 按 TLS 指纹拦引擎直拉（实测 busybox/libcurl 指纹 403、
// curl 指纹 200，UA 无关），改为 sidecar 代拉（Go 抓取，可选 SUBSCRIPTION_PROXY）后经
// 容器内回环端点喂给引擎——引擎对 127.0.0.1 的 http 抓取无 WAF。
// 端点仅挂在 sidecar 回环 listener（nginx 不路由 /internal/）；ref 随机不可枚举，
// 内容缓存 60s（对齐引擎订阅缓存语义），过期即失效。
package main

import (
	"crypto/rand"
	"encoding/hex"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

const internalSubTTL = 60 * time.Second

type internalSubStore struct {
	mu sync.Mutex
	// ref -> 订阅内容；sidecar 代拉成功后注册，引擎随后来取
	entries map[string]internalSubEntry
	// sourceURL -> ref：同源 60s 内复用（对齐引擎 cache_subscription 语义）
	bySource map[string]internalSubEntry
	client   *http.Client // 代拉用（SUBSCRIPTION_PROXY 生效时走代理）
}

type internalSubEntry struct {
	ref     string
	content string
	expires time.Time
}

func newInternalSubStore(cfg config) *internalSubStore {
	// 代拉 client：与快照刷新同口径——SUBSCRIPTION_PROXY 非空时走代理（v1.3.5 起该 env
	// 语义为「sidecar 代拉订阅走代理」；引擎侧 proxy_subscription 的 pref 改写保留兜底）
	timeout := 2 * time.Minute
	if cfg.subProxyTimeout > 0 && cfg.subProxyTimeout < timeout {
		timeout = cfg.subProxyTimeout
	}
	return &internalSubStore{
		entries:  map[string]internalSubEntry{},
		bySource: map[string]internalSubEntry{},
		client:   upstreamClient(cfg, timeout),
	}
}

// fetchAndRegister 代拉订阅并注册 ref。命中未过期缓存直接复用。返回引擎可拉的回环 URL。
func (s *internalSubStore) fetchAndRegister(ua, sourceURL, sidecarBase string) (string, error) {
	s.mu.Lock()
	if e, ok := s.bySource[sourceURL]; ok && time.Now().Before(e.expires) {
		s.mu.Unlock()
		return sidecarBase + "/internal/sub/" + e.ref, nil
	}
	s.mu.Unlock()

	req, err := http.NewRequest(http.MethodGet, sourceURL, nil)
	if err != nil {
		return "", err
	}
	if ua != "" {
		req.Header.Set("User-Agent", ua)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		log.Printf("internal-sub 代拉失败 status=%d bytes=%d", resp.StatusCode, len(body))
		return "", errUpstreamStatus{resp.StatusCode}
	}

	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	ref := hex.EncodeToString(buf)
	s.mu.Lock()
	s.gcLocked()
	s.entries[ref] = internalSubEntry{ref: ref, content: string(body), expires: time.Now().Add(internalSubTTL)}
	s.bySource[sourceURL] = s.entries[ref]
	s.mu.Unlock()
	return sidecarBase + "/internal/sub/" + ref, nil
}

type errUpstreamStatus struct{ code int }

func (e errUpstreamStatus) Error() string { return "upstream status" }

// gcLocked 清理过期条目（调用方持锁）
func (s *internalSubStore) gcLocked() {
	now := time.Now()
	for ref, e := range s.entries {
		if now.After(e.expires) {
			delete(s.entries, ref)
		}
	}
	for src, e := range s.bySource {
		if now.After(e.expires) {
			delete(s.bySource, src)
		}
	}
}

// handleInternalSub GET /internal/sub/{ref}：引擎专用（回环 listener；nginx 不路由）。
// miss/过期 → 404（引擎侧表现为拉取失败，sidecar 日志有根因）。
func (s *internalSubStore) handleInternalSub(w http.ResponseWriter, r *http.Request) {
	ref := strings.TrimPrefix(r.URL.Path, "/internal/sub/")
	s.mu.Lock()
	e, ok := s.entries[ref]
	valid := ok && time.Now().Before(e.expires)
	if !valid {
		delete(s.entries, ref)
	}
	s.mu.Unlock()
	if !valid {
		log.Printf("internal-sub miss/expired ref=%s***", ref[:min(4, len(ref))])
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, e.content)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
