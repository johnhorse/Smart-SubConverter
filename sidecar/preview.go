// POST /api/preview：URL 预校验（D7）→ 探测转换（10s 超时）→ link 服务端拼接（D4：永远返回）。
// 日志卫生（D6）：任何日志不得含订阅 URL、query string、token——失败只记 target 与 errorCode。
package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// D7 预校验：返回空串表示通过，否则为 bad_url_* 错误码
func validateURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "bad_url_empty"
	}
	u, err := url.Parse(trimmed)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "bad_url_format"
	}
	if isPrivateHost(u.Hostname()) {
		return "bad_url_private"
	}
	return ""
}

// isPrivateHost 判定主机名是否私网/回环字面量（契约清单：localhost、127/8、10/8、
// 172.16/12、192.168/16、169.254/16、0.0.0.0、[::1]；[::] 与 fc00::/7 一并覆盖）。
// 只查字面量，不查域名（公网域名可能落在 CDN 私网出口，查 DNS 会误伤）。
func isPrivateHost(hostname string) bool {
	h := strings.ToLower(strings.TrimSuffix(hostname, "."))
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	if ip == nil {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified()
}

// 探测 UA 按 target 家族给代表性值（契约：「大致像就行」；显式 target 时 UA 影响很小）
var probeUserAgents = map[string]string{
	"clash":        "clash-verge/v1.7.7",
	"surge":        "Surge Mac/2398",
	"quanx":        "Quantumult X/1.0.30",
	"loon":         "Loon/3.2.4",
	"stash":        "Stash/2.8.0",
	"shadowrocket": "Shadowrocket/2.2.51",
	"singbox":      "SFA/1.3.5",
	"v2rayn":       "v2rayN/6.0",
	"auto":         "clash-verge/v1.7.7",
}

func uaFor(target string) string {
	if ua, ok := probeUserAgents[target]; ok {
		return ua
	}
	return "clash-verge/v1.7.7"
}

type probeResult struct {
	ok        bool
	errorCode string
	message   string
}

// mapUpstreamError 把 Extended 非 200 响应映射为 upstream_error 细节文案。
// 双语错误体识别（Assignment ⑤）：「unsupported target value」= auto 识别失败，
// 前端会引导展开「指定客户端」。
func mapUpstreamError(status int, body []byte) probeResult {
	if bytes.Contains(body, []byte("unsupported target value")) {
		return probeResult{false, "upstream_error", "上游无法自动识别目标客户端，请在页面展开「指定客户端」显式选择后重试"}
	}
	return probeResult{false, "upstream_error", fmt.Sprintf("上游转换引擎返回错误（HTTP %d）", status)}
}

// formatLooksValid 按探测 target 家族粗验输出格式（契约「探测转换」节）
func formatLooksValid(target string, body []byte) bool {
	s := string(body)
	switch target {
	case "clash":
		return strings.Contains(s, "proxies:")
	case "surge":
		return strings.Contains(s, "[General]") || strings.HasPrefix(strings.TrimSpace(s), "#!MANAGED-CONFIG")
	case "singbox":
		var m map[string]any
		return json.Unmarshal(body, &m) == nil && m["outbounds"] != nil
	default:
		return strings.TrimSpace(s) != "" && !strings.Contains(s, "Invalid request")
	}
}

func isTimeoutErr(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// probeConvert 向 Extended 发探测 GET /sub 并按契约归类结果（config 用所选档位 url）。
// 日志只记 target 与 errorCode（D6：探测 URL 含订阅链接，绝不入日志）。
func (s *server) probeConvert(subURL, target, presetURL string) probeResult {
	q := url.Values{}
	q.Set("target", target)
	q.Set("url", subURL)
	q.Set("config", presetURL)
	req, err := http.NewRequest(http.MethodGet, s.cfg.extendedBase+"/sub?"+q.Encode(), nil)
	if err != nil {
		return probeResult{false, "backend_down", "构造上游请求失败"}
	}
	req.Header.Set("User-Agent", uaFor(target))
	resp, err := s.probeClient.Do(req)
	if err != nil {
		if isTimeoutErr(err) {
			log.Printf("preview probe: target=%s code=upstream_timeout", target)
			return probeResult{false, "upstream_timeout", "上游转换超时（10s）"}
		}
		log.Printf("preview probe: target=%s code=backend_down (%T)", target, err)
		return probeResult{false, "backend_down", "上游转换引擎连接失败"}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode != http.StatusOK {
		pr := mapUpstreamError(resp.StatusCode, body)
		log.Printf("preview probe: target=%s code=%s status=%d", target, pr.errorCode, resp.StatusCode)
		return pr
	}
	if !formatLooksValid(target, body) {
		log.Printf("preview probe: target=%s code=upstream_error (format check)", target)
		return probeResult{false, "upstream_error", "上游返回的内容不像有效配置"}
	}
	return probeResult{ok: true}
}

// jsQueryEscape 等价 JS encodeURIComponent（url.QueryEscape 会把空格编成 +，链接里要 %20）
func jsQueryEscape(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}

// buildLink 服务端拼接最终 /sub 链接（前端一律直接使用，不自行拼接）。
// target 用请求原样（auto 或指定值）；SUB_TOKEN 非空时追加。
func buildLink(origin, target, subURL, presetURL, token string) string {
	link := origin + "/sub?target=" + jsQueryEscape(target) +
		"&url=" + jsQueryEscape(subURL) +
		"&config=" + jsQueryEscape(presetURL)
	if token != "" {
		link += "&token=" + jsQueryEscape(token)
	}
	return link
}

// buildDeepLink 拼装选中客户端的一键导入深链（契约「导入深链」节拼装表，仅五家有模板）。
// 与 link 同源派生：ok:false、bad_preset 时同样照给；urlencode 按契约用 url.QueryEscape。
// target 为 auto/clash/singbox/v2rayn（及未注册值）时无深链，返回 nil。
func buildDeepLink(target, link string) *string {
	var dl string
	switch target {
	case "surge":
		dl = "surge://install-config?url=" + url.QueryEscape(link)
	case "loon":
		dl = "loon://import?sub=" + url.QueryEscape(link)
	case "stash":
		dl = "stash://install-profile?url=" + url.QueryEscape(link)
	case "quanx":
		payload := `{"server_remote":["` + link + `, tag=Smart SubConverter"]}`
		dl = "quantumult-x:///add-resource?remote-resource=" + url.QueryEscape(payload)
	case "shadowrocket":
		dl = "shadowrocket://add/sub://" + base64.StdEncoding.EncodeToString([]byte(link))
	default:
		return nil
	}
	return &dl
}

// 预校验失败的服务端 message（最终文案前端按 errorCode 硬编码，此处为兜底）
var validateMessages = map[string]string{
	"bad_url_empty":   "未提供订阅链接",
	"bad_url_format":  "不是有效的 http(s) 链接",
	"bad_url_private": "链接指向私网或回环地址，转换服务无法访问",
	"bad_preset":      "指定的规则模板不可用",
}

// resolvePreset：preset 缺省/空串/"default" → default 档；注册表 id → 该档；否则非法
func resolvePreset(presets []presetEntry, id string) (presetEntry, bool) {
	if id == "" {
		return presets[0], true
	}
	for _, p := range presets {
		if p.ID == id {
			return p, true
		}
	}
	return presetEntry{}, false
}

func (s *server) handlePreview(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SubURL string `json:"url"`
		Target string `json:"target"`
		Preset string `json:"preset"`
	}
	// 契约要求响应恒 200：请求体解析失败按「未提供链接」处理，不发明新错误码
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req)
	if req.Target == "" {
		req.Target = "auto"
	}
	linkTarget := req.Target // link 里 target 用请求原样（契约「link 拼接」）
	// 探测 target：显式注册值用该值；auto 及未知值按定案启发式用 clash
	probeTarget := req.Target
	if !isRegisteredTarget(req.Target) || req.Target == "auto" {
		probeTarget = "clash"
	}

	// 生效档位烙进探测请求与 link 的 config=（契约「选中的档位烙进」）；
	// bad_preset 时 link 仍以 default 档生成，维持「link 永远非空」
	sel, presetOK := resolvePreset(s.presets, req.Preset)
	linkPresetURL := sel.URL
	if !presetOK {
		linkPresetURL = s.presets[0].URL
	}
	// preset 回显：bad_preset 时 null，其余情况（含 ok:false）恒回显解析后的合法档
	var echo *presetRef
	if presetOK {
		echo = &presetRef{ID: sel.ID, Name: sel.Name}
	}

	origin := deriveOrigin(s.cfg.publicBaseURL, r.Host, r.Header.Get("X-Forwarded-Proto"))
	link := buildLink(origin, linkTarget, req.SubURL, linkPresetURL, s.cfg.subToken)
	deepLink := buildDeepLink(linkTarget, link)

	// 契约预校验判定序：bad_url_* 先于 bad_preset
	if code := validateURL(req.SubURL); code != "" {
		writeJSON(w, http.StatusOK, previewResponse{
			OK: false, Target: probeTarget, Preset: echo, ErrorCode: &code, Message: validateMessages[code], Link: link, DeepLink: deepLink,
		})
		return
	}
	if !presetOK {
		// 不静默回落（防「以为选了 A、实际拿到默认模板」的链接流出）；preset 输入值不入日志
		code := "bad_preset"
		log.Printf("preview 预校验: code=bad_preset target=%s", probeTarget)
		writeJSON(w, http.StatusOK, previewResponse{
			OK: false, Target: probeTarget, Preset: nil, ErrorCode: &code, Message: validateMessages[code], Link: link, DeepLink: deepLink,
		})
		return
	}

	pr := s.probeConvert(req.SubURL, probeTarget, sel.URL)
	resp := previewResponse{OK: pr.ok, Target: probeTarget, Preset: echo, Message: pr.message, Link: link, DeepLink: deepLink}
	if !pr.ok {
		resp.ErrorCode = &pr.errorCode
	}
	writeJSON(w, http.StatusOK, resp)
}
