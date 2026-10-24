// v1.2.0 短链 HTTP 端点：GET /s/{code} 订阅代理 + GET /api/links、DELETE /api/links/{code} 管理。
// 日志卫生（D6 + OH-2：短码即 bearer 凭据）：只记脱敏摘要——code 前 3 位 + 状态 + 耗时，
// 不记完整码、不记订阅 URL、不记 token。
package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type errBody struct {
	Error string `json:"error"`
}

// logCode 短码脱敏：只保留前 3 位（防极短输入越界）
func logCode(code string) string {
	if len(code) > 3 {
		return code[:3]
	}
	return code
}

// handleShortLink GET /s/{code}：查表 miss → 404；hit → 容器内代理 Extended /sub，
// 参数用建链时刻冻结的值（防 env 改 PRESET_URL 后同码内容漂移），SUB_TOKEN 非空时
// 容器内注入（朋友的短链不带 token）。响应流式透传，不整包缓冲。
func (s *server) handleShortLink(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")
	start := time.Now()
	lnk, found, err := s.store.linkByCode(code)
	if err != nil {
		log.Printf("s/ 查库失败: %T", err)
		writeJSON(w, http.StatusInternalServerError, errBody{"internal"})
		return
	}
	if !found {
		log.Printf("s/ miss code=%s***", logCode(code))
		writeSubError(w, r, http.StatusNotFound, "not_found",
			"这根链接不存在或已被撤销",
			"短码不存在，或已被部署者撤销。如果是刚复制的地址，请核对完整后重试。")
		return
	}

	// v1.3.0 存量短链兼容（契约「存量短链兼容」）：冻结 config 为内置三档 GitHub
	// URL → 运行时映射为本地改写后 .ini 路径（库不动，旧短码原地升级为快照直读）
	configURL := lnk.ConfigURL
	if s.snap != nil {
		if mapped, ok := s.snap.mapFrozenConfig(configURL); ok {
			configURL = mapped
		}
	}

	q := url.Values{}
	q.Set("target", lnk.Target)
	q.Set("url", lnk.SourceURL)
	q.Set("config", configURL)
	if s.cfg.subToken != "" {
		q.Set("token", s.cfg.subToken)
	}
	req, err := http.NewRequest(http.MethodGet, s.cfg.extendedBase+"/sub?"+q.Encode(), nil)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errBody{"internal"})
		return
	}
	// UA 显式 Set（含空串）：不 Set 时 Go Transport 会补 Go-http-client/1.1，
	// 破坏 Extended 的 target=auto 识别（OH-12；Set 空串时 Transport 省略该头，等价直连无 UA）
	req.Header.Set("User-Agent", r.Header.Get("User-Agent"))

	resp, err := s.proxyClient.Do(req)
	if err != nil {
		log.Printf("s/ hit code=%s*** status=%d latency=%s (upstream %T)", logCode(code), http.StatusBadGateway, time.Since(start).Round(time.Millisecond), err)
		writeSubError(w, r, http.StatusBadGateway, "upstream_unavailable", subErrTitle, subErrDetail)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Printf("s/ hit code=%s*** status=%d latency=%s (upstream %d)", logCode(code), http.StatusBadGateway, time.Since(start).Round(time.Millisecond), resp.StatusCode)
		writeSubError(w, r, http.StatusBadGateway, "upstream_unavailable", subErrTitle, subErrDetail)
		return
	}

	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(http.StatusOK)
	copyErr := streamBody(w, resp.Body)
	log.Printf("s/ hit code=%s*** status=%d latency=%s", logCode(code), http.StatusOK, time.Since(start).Round(time.Millisecond))
	if copyErr != nil {
		// 响应头已发，无法改状态码；客户端拉完即断是常态，仅记脱敏摘要
		log.Printf("s/ 透传中断 code=%s*** copyErr=%T", logCode(code), copyErr)
	}
}

// streamBody 逐块转发并即时 flush：真正的流式透传（订阅生成慢时客户端边生成边收）
func streamBody(w http.ResponseWriter, src io.Reader) error {
	flusher, canFlush := w.(http.Flusher)
	buf := make([]byte, 32<<10)
	for {
		n, rerr := src.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return werr
			}
			if canFlush {
				flusher.Flush()
			}
		}
		if rerr != nil {
			if rerr == io.EOF {
				return nil
			}
			return rerr
		}
	}
}

type linksResponse struct {
	Links []linkEntry `json:"links"`
}

// 502 人话页文案（浏览器直开短链最常见的原因：UA 不在 Extended 识别表 → 引擎 400）
const (
	subErrTitle  = "这是一根订阅链接，浏览器打不开它"
	subErrDetail = "请把它添加到 Clash / Surge / Loon / Stash / Shadowrocket 等客户端的订阅里使用。如果你是在客户端里更新订阅时看到此页，说明上游转换服务暂时不可用，稍后再试。"
)

// writeSubError /s/ 错误响应内容协商（契约「GET /s/{code}」第 5 条，v1.3.1）：
// Accept 含 text/html（浏览器直开）→ 同状态码人话 HTML 页，消除冷 JSON 被误读为「服务故障」；
// 其余（客户端 / 程序化消费）→ JSON error code 不变。title/detail 为常量，无注入面。
func writeSubError(w http.ResponseWriter, r *http.Request, status int, code, title, detail string) {
	if strings.Contains(r.Header.Get("Accept"), "text/html") {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		fmt.Fprintf(w, `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>%s</title>
<style>body{font:16px/1.7 -apple-system,"PingFang SC","Microsoft YaHei",sans-serif;max-width:36em;margin:14vh auto;padding:0 1.2em;color:#333;background:#fafafa}h1{font-size:1.35em;margin-bottom:.4em}a{color:#06c}</style>
</head><body>
<h1>%s</h1>
<p>%s</p>
<p><a href="/">← 返回首页</a></p>
</body></html>`, title, title, detail)
		return
	}
	writeJSON(w, status, errBody{code})
}

// requireSubToken：SUB_TOKEN 非空时管理端点必须带 ?token= 且匹配（契约「管理端点」）；
// 为空（局域网档）开放，信任边界 = 局域网（码即授权）。
func (s *server) requireSubToken(w http.ResponseWriter, r *http.Request) bool {
	if s.cfg.subToken == "" {
		return true
	}
	if r.URL.Query().Get("token") != s.cfg.subToken {
		writeJSON(w, http.StatusUnauthorized, errBody{"unauthorized"})
		return false
	}
	return true
}

// handleLinks GET /api/links：响应即凭据库，仅部署者自用
func (s *server) handleLinks(w http.ResponseWriter, r *http.Request) {
	if !s.requireSubToken(w, r) {
		return
	}
	links, err := s.store.listLinks()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errBody{"internal"})
		return
	}
	writeJSON(w, http.StatusOK, linksResponse{Links: links})
}

// handleDeleteLink DELETE /api/links/{code}：恒 204（幂等：码不存在也 204）
func (s *server) handleDeleteLink(w http.ResponseWriter, r *http.Request) {
	if !s.requireSubToken(w, r) {
		return
	}
	if err := s.store.deleteLink(r.PathValue("code")); err != nil {
		writeJSON(w, http.StatusInternalServerError, errBody{"internal"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
