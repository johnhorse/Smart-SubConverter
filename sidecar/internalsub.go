// v1.3.5 内部订阅端点：机场 WAF 按 TLS 指纹拦引擎直拉（实测 busybox/libcurl 指纹 403、
// curl 指纹 200，UA 无关），改为 sidecar 代拉（Go 抓取，可选 SUBSCRIPTION_PROXY）后经
// 容器内回环端点喂给引擎——引擎对 127.0.0.1 的 http 抓取无 WAF。
// 端点仅挂在 sidecar 回环 listener（nginx 不路由 /internal/）；ref 随机不可枚举，
// 内容缓存 60s（对齐引擎订阅缓存语义），过期即失效。
package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"os/exec"
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
	client   *http.Client // Go 代拉（SUBSCRIPTION_PROXY 生效时走代理）
	proxyURL string       // 同上代理地址（curl -x 用；空 = 直连）
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
		proxyURL: proxyURLString(cfg),
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

	body, err := s.fetch(ua, sourceURL)
	if err != nil {
		return "", err
	}

	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	ref := hex.EncodeToString(buf)
	s.mu.Lock()
	s.gcLocked()
	s.entries[ref] = internalSubEntry{ref: ref, content: body, expires: time.Now().Add(internalSubTTL)}
	s.bySource[sourceURL] = s.entries[ref]
	s.mu.Unlock()
	return sidecarBase + "/internal/sub/" + ref, nil
}

// fetch 代拉：Go 抓取优先，失败时 exec curl 重试（v1.3.6）——实测部分机场 WAF 按 TLS 指纹
// 放行（任意 IP + curl 指纹全通，Go/busybox/libcurl 指纹被拦；HTTP 代理 CONNECT 隧道不换
// 指纹），curl 是已验证可行的形态；两路均带 SUBSCRIPTION_PROXY 代理。
func (s *internalSubStore) fetch(ua, sourceURL string) (string, error) {
	goBody, goErr := s.fetchGo(ua, sourceURL)
	if goErr == nil {
		return goBody, nil
	}
	if body, err := s.fetchCurl(ua, sourceURL); err == nil {
		log.Printf("internal-sub Go 拉取失败（%v），curl 重试成功", goErr)
		return body, nil
	} else {
		return "", fmt.Errorf("go: %v; curl: %w", goErr, err)
	}
}

func (s *internalSubStore) fetchGo(ua, sourceURL string) (string, error) {
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
		log.Printf("internal-sub Go 代拉 status=%d bytes=%d", resp.StatusCode, len(body))
		return "", errUpstreamStatus{resp.StatusCode}
	}
	return string(body), nil
}

func (s *internalSubStore) fetchCurl(ua, sourceURL string) (string, error) {
	curl, err := exec.LookPath("curl")
	if err != nil {
		return "", err
	}
	// -f 必需：HTTP ≥400 时 curl 失败退出（exit 22）——否则 WAF 的 403 拦截页会被当
	// 作订阅内容传给引擎，表现为「代拉成功但 mixed 无有效节点」的 400（2026-10-25 实抓）
	args := []string{"-fsS", "--max-time", "45", "--max-filesize", "8388608"}
	if ua != "" {
		args = append(args, "-A", ua)
	}
	if s.proxyURL != "" {
		args = append(args, "-x", s.proxyURL)
	}
	args = append(args, sourceURL)
	var out, errBuf bytes.Buffer
	cmd := exec.Command(curl, args...)
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%v stderr=%s", err, strings.TrimSpace(errBuf.String()))
	}
	return out.String(), nil
}

type errUpstreamStatus struct{ code int }

func (e errUpstreamStatus) Error() string { return "upstream status" }

// proxyURLString cfg 里代理的字符串形态（curl -x 用；nil = 空直连）
func proxyURLString(cfg config) string {
	if cfg.snapshotProxy == nil {
		return ""
	}
	return cfg.snapshotProxy.String()
}

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
