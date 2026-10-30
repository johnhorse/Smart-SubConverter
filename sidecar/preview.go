// POST /api/preview：URL 预校验（D7）→ 探测转换（PROBE_TIMEOUT 超时）→ 短链查/建
// （store_error 降级 /sub 长链；D4：link 永远返回）。
// 日志卫生（D6）：任何日志不得含订阅 URL、query string、token——失败只记 target 与 errorCode。
package main

import (
	"bytes"
	"context"
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
	"time"
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
	// v1.4.0 两段式探测（契约 preview「sidecar 行为」第 2 条）：quanx / clash（含 auto
	// 默认的 clash——handlePreview 已把 auto 归一化为 clash 探测值）与 /s/ 拉取同一两段式
	// 路径，所见即所得：preview 绿 = 受限网络消费端拉得到内联节点；其余 target 直通不变
	if target == "quanx" || target == "clash" {
		return s.probeTwoStage(subURL, target, presetURL)
	}
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
			return probeResult{false, "upstream_timeout", fmt.Sprintf("上游转换超时（%s）", s.cfg.probeTimeout)}
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

// probeTwoStage preview 探测的两段式路径（与 /s/ 拉取共用 twoStageConvert）：整体预算
// min(PROBE_TIMEOUT, SUB_PROXY_TIMEOUT)（v1.4.0 M2：代拉也纳入——否则挂死源最坏
// internalSubStore 120s + curl 45s 重试远超 PROBE_TIMEOUT），代拉后每段再分预算一半
// （默认 30s → 每段 15s，整体贴近既有 PROBE_TIMEOUT 交互预期；/s/ 维持 SUB_PROXY_TIMEOUT/2
// 不变——同路指转换与代拉逻辑同构，超时按各自交互场景）。失败映射既有错误码（M3：按段
// 区分——stage 0 代拉失败是订阅源侧问题归 upstream_error；stage 1/2 才是引擎侧：超时
// upstream_timeout、不可达 backend_down）。
func (s *server) probeTwoStage(subURL, target, presetURL string) probeResult {
	ua := uaFor(target)
	budget := s.cfg.probeTimeout
	if s.cfg.subProxyTimeout > 0 && s.cfg.subProxyTimeout < budget {
		budget = s.cfg.subProxyTimeout
	}
	stageTimeout := budget / 2
	if stageTimeout <= 0 { // 防零值（生产 env 解析保证 >0；直接构造 config 的路径兜底）
		stageTimeout = 15 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()

	resp, _, cancelStage2, fail := s.twoStageConvert(ctx, s.probeClient, ua, subURL, presetURL, target, stageTimeout)
	if fail != nil {
		switch {
		case fail.kind == "internal":
			log.Printf("preview probe: target=%s code=backend_down twostage=%d/2", target, fail.stage)
			return probeResult{false, "backend_down", "上游请求构造失败"}
		case fail.stage == 0: // 代拉失败（err / airport）：订阅源侧不可用或被拦，不是引擎问题
			log.Printf("preview probe: target=%s code=upstream_error twostage=0/2", target)
			return probeResult{false, "upstream_error", "订阅源拉取失败（不可用或被拦截）"}
		case fail.kind == "err" && isTimeoutErr(fail.err):
			log.Printf("preview probe: target=%s code=upstream_timeout twostage=%d/2", target, fail.stage)
			return probeResult{false, "upstream_timeout", fmt.Sprintf("上游转换超时（%s）", budget)}
		case fail.kind == "err":
			log.Printf("preview probe: target=%s code=backend_down twostage=%d/2", target, fail.stage)
			return probeResult{false, "backend_down", "上游转换引擎连接失败"}
		default: // stage 1/2 的 upstream / nodes
			log.Printf("preview probe: target=%s code=upstream_error twostage=%d/2", target, fail.stage)
			return probeResult{false, "upstream_error", "上游转换失败（订阅源无有效节点）"}
		}
	}
	defer cancelStage2() // B1：body 读完后才取消第二段 ctx（失败路径核心内已自行 cancel）
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	// 内联节点粗验（契约「内联节点数 > 0」，M1：取代早前「含 proxies: 即可」的等价论证——
	// 该论证有漏洞，URI 全是引擎不识别协议时引擎 200 返回空 proxies: 也会被误判绿）：
	// 第一段 nodes==0 已把关拦掉「解析不出 URI」，这里把关第二段「引擎识别不了 URI」的残余
	if !twoStageInlineNodes(target, body) {
		log.Printf("preview probe: target=%s code=upstream_error (inline nodes check)", target)
		return probeResult{false, "upstream_error", "上游转换未产出任何内联节点"}
	}
	return probeResult{ok: true}
}

// twoStageInlineNodes 两段式输出的内联节点粗验（M1）：只服务两段式路径的 quanx/clash，
// 不引 YAML 库——clash 数顶级 proxies: 键之下、缩进 >0 的 "- " 列表条目（缩进归 0 即下一个
// 顶级键，停止）；proxies: [] 或空列表自然计 0。quanx 数 [server_local] 段内的节点行
// （该段是节点专用段，段内非空非注释且含裸 token 赋值的行即节点，不依赖等号空格风格）。
func twoStageInlineNodes(target string, body []byte) bool {
	switch target {
	case "clash":
		return countClashInlineProxies(string(body)) > 0
	case "quanx":
		return countQuanxServerLocalNodes(string(body)) > 0
	}
	return false
}

func countClashInlineProxies(s string) int {
	in := false
	n := 0
	for _, ln := range strings.Split(s, "\n") {
		trimmed := strings.TrimRight(ln, " \t\r")
		if strings.TrimSpace(trimmed) == "" {
			continue
		}
		indent := len(trimmed) - len(strings.TrimLeft(trimmed, " \t"))
		if !in {
			if indent == 0 && strings.HasPrefix(trimmed, "proxies:") {
				in = true
			}
			continue
		}
		if indent == 0 {
			break // 下一个顶级键，proxies 列表结束
		}
		if t := strings.TrimSpace(trimmed); strings.HasPrefix(t, "- ") || t == "-" {
			n++
		}
	}
	return n
}

func countQuanxServerLocalNodes(s string) int {
	in := false
	n := 0
	for _, ln := range strings.Split(s, "\n") {
		t := strings.TrimSpace(ln)
		if !in {
			if t == "[server_local]" {
				in = true
			}
			continue
		}
		if strings.HasPrefix(t, "[") { // 下一段
			break
		}
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if i := strings.Index(t, "="); i > 0 && !strings.ContainsAny(strings.TrimSpace(t[:i]), " \t") {
			n++
		}
	}
	return n
}

// jsQueryEscape 等价 JS encodeURIComponent（url.QueryEscape 会把空格编成 +，链接里要 %20）
func jsQueryEscape(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}

// buildLink 拼 /sub 旧长链（v1.2.0 起仅作降级输出：store_error，以及 bad_url_* 无效 URL
// 不落库场景；/sub 已是 deprecated 遗留兼容路由，存量链接不断）。
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
	"groups_disabled": "当前部署或所选模板不支持自定义分组",
	"bad_groups":      "分组信息有误——组名和关键词不超过 32 字，别用特殊符号，同名分组只留一个",
}

// probeNodeList probe 模式核心（v1.5.0）：复用两段式第一段（stage1Mixed，代拉订阅 →
// 引擎 target=mixed 展开为 URI 列表），逐条解析节点名返回；不执行第二段 target 转换。
// 零副作用：不算指纹、不写库。预算与起算沿用 v1.4.0 两段式探测口径（整体
// min(PROBE_TIMEOUT, SUB_PROXY_TIMEOUT) 从代拉起算；probe 不分段——第一段吃全额预算）。
func (s *server) probeNodeList(subURL, target string) ([]string, probeResult) {
	ua := uaFor(target)
	budget := s.cfg.probeTimeout
	if s.cfg.subProxyTimeout > 0 && s.cfg.subProxyTimeout < budget {
		budget = s.cfg.subProxyTimeout
	}
	if budget <= 0 { // 防零值（生产 env 解析保证 >0；直接构造 config 的路径兜底）
		budget = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()

	uris, fail := s.stage1Mixed(ctx, s.probeClient, ua, subURL, 0)
	if fail != nil {
		switch {
		case fail.stage == 0: // 代拉失败（err / airport）：订阅源侧不可用或被拦，归 upstream_error
			log.Printf("preview probe: target=%s mode=probe twostage=0/2", target)
			return nil, probeResult{false, "upstream_error", "订阅源拉取失败（不可用或被拦截）"}
		case fail.kind == "internal":
			return nil, probeResult{false, "backend_down", "上游请求构造失败"}
		case fail.kind == "err" && isTimeoutErr(fail.err):
			log.Printf("preview probe: target=%s mode=probe code=upstream_timeout", target)
			return nil, probeResult{false, "upstream_timeout", fmt.Sprintf("上游转换超时（%s）", budget)}
		case fail.kind == "err":
			log.Printf("preview probe: target=%s mode=probe code=backend_down (%T)", target, fail.err)
			return nil, probeResult{false, "backend_down", "上游转换引擎连接失败"}
		case fail.kind == "upstream":
			log.Printf("preview probe: target=%s mode=probe code=upstream_error status=%d", target, fail.status)
			return nil, probeResult{false, "upstream_error", fmt.Sprintf("上游转换引擎返回错误（HTTP %d）", fail.status)}
		default: // nodes：代拉成功但第一段解析不出有效节点
			log.Printf("preview probe: target=%s mode=probe code=upstream_error (nodes=0)", target)
			return nil, probeResult{false, "upstream_error", "上游转换失败（订阅源无有效节点）"}
		}
	}
	names := []string{}
	for _, u := range uris {
		if n, ok := nodeNameFromURI(u); ok {
			names = append(names, n)
		}
	}
	return names, probeResult{ok: true}
}

// nodeNameFromURI 按解析节点名（契约 SC-2）：vmess:// base64 JSON 的 ps；ss:// /
// trojan:// 及其余 scheme 用 URL fragment（Go url.Parse 已做 % 解码），无 fragment 或
// 解析失败则该节点缺席列表
func nodeNameFromURI(uri string) (string, bool) {
	i := strings.Index(uri, "://")
	if i <= 0 {
		return "", false
	}
	if strings.EqualFold(uri[:i], "vmess") {
		payload := uri[i+3:]
		if j := strings.IndexByte(payload, '#'); j >= 0 {
			payload = payload[:j]
		}
		b, err := decodeVmessB64(payload)
		if err != nil {
			return "", false
		}
		var v struct {
			PS string `json:"ps"`
		}
		if json.Unmarshal(b, &v) != nil || v.PS == "" {
			return "", false
		}
		return v.PS, true
	}
	u, err := url.Parse(uri)
	if err != nil || u.Fragment == "" {
		return "", false
	}
	return u.Fragment, true
}

// decodeVmessB64 vmess 载荷 base64 解码：std / 无填充 / url-safe / url-safe 无填充
// 四形态都试（各订阅产出不齐）
func decodeVmessB64(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	var firstErr error
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding,
		base64.URLEncoding, base64.RawURLEncoding,
	} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, nil
		} else if firstErr == nil {
			firstErr = err
		}
	}
	return nil, firstErr
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
		SubURL  string      `json:"url"`
		Target  string      `json:"target"`
		Preset  string      `json:"preset"`
		Groups  []nodeGroup `json:"groups"`  // v1.5.0：组意图（转换提交时携带；probe 模式忽略）
		Exclude []string    `json:"exclude"` // v1.5.0 增补：模板组删减名单（probe 模式忽略、不校验）
		Probe   bool        `json:"probe"`   // v1.5.0：true = 探测模式（取名两段式，零副作用）
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

	// 生效档位烙进探测请求与 link 的 config=（契约「选中的档位烙进」）；config 值走
	// engineConfig：本地化档 = 快照改写后 .ini 本地路径（v1.3.0），豁免档透传原 URL；
	// bad_preset 时 link 仍以 default 档生成，维持「link 永远非空」
	sel, presetOK := resolvePreset(s.presets, req.Preset)
	linkPresetURL := s.engineConfig(s.presets[0])
	if presetOK {
		linkPresetURL = s.engineConfig(sel)
	}
	// preset 回显：bad_preset 时 null，其余情况（含 ok:false）恒回显解析后的合法档
	var echo *presetRef
	if presetOK {
		echo = &presetRef{ID: sel.ID, Name: sel.Name}
	}

	origin := deriveOrigin(s.cfg.publicBaseURL, r.Host, r.Header.Get("X-Forwarded-Proto"))

	// 短链烙创建时刻的档位（bad_preset 时 link 仍以 default 档生成，契约预校验节）
	linkPresetID := "default"
	if presetOK {
		linkPresetID = sel.ID
	}
	// makeLink 查/建短链（契约 sidecar 行为 step 3：与探测结果无关，永远执行）。
	// 存储失败 → link 降级 /sub 旧长链并置 store_error（唯一保留长链返回的正常场景）。
	// v1.5.0 groups/exclude 校验/能力位失败时二者传 ""（丢弃后照常建短链，对齐 bad_preset）
	makeLink := func(gj, ej string) (link string, storeCode string) {
		code, err := s.store.getOrCreateLink(req.SubURL, linkTarget, linkPresetID, linkPresetURL, gj, ej)
		if err != nil {
			log.Printf("preview 短链生成失败: %T (store_error)", err)
			return buildLink(origin, linkTarget, req.SubURL, linkPresetURL, s.cfg.subToken), "store_error"
		}
		return origin + "/s/" + code, ""
	}

	// probe 模式响应（v1.5.0）：link/deepLink 恒 null（零副作用即目的），ok:false 时
	// nodes 恒 []、groupHits 恒 {}；预校验失败复用既有错误码族
	writeProbeResult := func(pr probeResult, nodes []string, hits map[string]int) {
		resp := probePreviewResponse{OK: pr.ok, Target: probeTarget, Preset: echo, Message: pr.message, Nodes: nodes, GroupHits: hits}
		if !pr.ok {
			resp.ErrorCode = &pr.errorCode
			resp.Nodes = []string{}
			resp.GroupHits = map[string]int{}
		}
		if resp.Nodes == nil {
			resp.Nodes = []string{}
		}
		if resp.GroupHits == nil {
			resp.GroupHits = map[string]int{}
		}
		writeJSON(w, http.StatusOK, resp)
	}

	// 契约预校验判定序：bad_url_* 先于 bad_preset。
	// 无效 URL 不落库（D7：私网/垃圾 URL 不持久化供 /s/ 重放），link 用 /sub 旧长链维持 D4 非空
	if code := validateURL(req.SubURL); code != "" {
		if req.Probe {
			writeProbeResult(probeResult{false, code, validateMessages[code]}, nil, nil)
			return
		}
		link := buildLink(origin, linkTarget, req.SubURL, linkPresetURL, s.cfg.subToken)
		writeJSON(w, http.StatusOK, previewResponse{
			OK: false, Target: probeTarget, Preset: echo, ErrorCode: &code, Message: validateMessages[code],
			Link: link, DeepLink: buildDeepLink(linkTarget, link), Nodes: []string{},
		})
		return
	}
	if !presetOK {
		// 不静默回落（防「以为选了 A、实际拿到默认模板」的链接流出）；preset 输入值不入日志
		code := "bad_preset"
		log.Printf("preview 预校验: code=bad_preset target=%s", probeTarget)
		if req.Probe {
			writeProbeResult(probeResult{false, code, validateMessages[code]}, nil, nil)
			return
		}
		link, _ := makeLink("", "") // bad_preset 优先于 store_error；link 仍以 default 档短链生成
		writeJSON(w, http.StatusOK, previewResponse{
			OK: false, Target: probeTarget, Preset: nil, ErrorCode: &code, Message: validateMessages[code],
			Link: link, DeepLink: buildDeepLink(linkTarget, link), Nodes: []string{},
		})
		return
	}

	// v1.5.0 groups/exclude 服务端兜底（probe 模式两字段均忽略、不校验）：groups 或
	// exclude 任一非空且能力位任一 false → groups_disabled（预校验错不调上游，UI 正常
	// 流程点不出，防手改请求）；随后 bad_groups（groups 与 exclude 共用错误码）。
	// 判定序：bad_url_* > bad_preset > groups_disabled > bad_groups（preset 无效时档位
	// 未知，groups_disabled 判不了——上面已提前返回）。两错均丢 groups/exclude 照常
	// 建短链（对齐 bad_preset 的 link 语义）
	groups := normalizeGroups(req.Groups)
	groupsJSON := "" // 空 = 无组（存量行为；列存 NULL、指纹用 "[]"）
	if len(groups) > 0 {
		groupsJSON = canonicalGroupsJSON(groups)
	}
	exclude, excludeErr := normalizeExclude(req.Exclude)
	excludeJSON := "" // 空 = 不删减（列存 NULL、指纹用 "[]"）
	if excludeErr == nil && len(exclude) > 0 {
		excludeJSON = canonicalExcludeJSON(exclude)
	}
	if !req.Probe && (len(req.Groups) > 0 || len(req.Exclude) > 0) {
		if !s.customGroupsEnabled() || !s.presetCustomGroups(sel) {
			code := "groups_disabled"
			log.Printf("preview 预校验: code=groups_disabled target=%s preset=%s", probeTarget, sel.ID)
			link, _ := makeLink("", "")
			writeJSON(w, http.StatusOK, previewResponse{
				OK: false, Target: probeTarget, Preset: echo, ErrorCode: &code, Message: validateMessages[code],
				Link: link, DeepLink: buildDeepLink(linkTarget, link), Nodes: []string{},
			})
			return
		}
		if len(groups) > 0 {
			if err := validateGroups(groups); err != nil {
				code := "bad_groups"
				log.Printf("preview 预校验: code=bad_groups target=%s", probeTarget)
				link, _ := makeLink("", "")
				writeJSON(w, http.StatusOK, previewResponse{
					OK: false, Target: probeTarget, Preset: echo, ErrorCode: &code, Message: validateMessages[code],
					Link: link, DeepLink: buildDeepLink(linkTarget, link), Nodes: []string{},
				})
				return
			}
		}
		if excludeErr != nil {
			code := "bad_groups"
			log.Printf("preview 预校验: code=bad_groups target=%s", probeTarget)
			link, _ := makeLink("", "")
			writeJSON(w, http.StatusOK, previewResponse{
				OK: false, Target: probeTarget, Preset: echo, ErrorCode: &code, Message: validateMessages[code],
				Link: link, DeepLink: buildDeepLink(linkTarget, link), Nodes: []string{},
			})
			return
		}
	}

	if req.Probe {
		nodes, pr := s.probeNodeList(req.SubURL, probeTarget)
		hits := map[string]int{}
		if pr.ok {
			hits = s.probeGroupHits(sel, nodes)
		}
		writeProbeResult(pr, nodes, hits)
		return
	}

	pr := s.probeConvert(req.SubURL, probeTarget, s.engineConfig(sel))
	link, storeCode := makeLink(groupsJSON, excludeJSON)
	resp := previewResponse{OK: pr.ok, Target: probeTarget, Preset: echo, Message: pr.message, Link: link, DeepLink: buildDeepLink(linkTarget, link), Nodes: []string{}}
	if !pr.ok {
		resp.ErrorCode = &pr.errorCode // 探测错误优先；store_error 仅在探测成功时体现
	} else if storeCode != "" {
		resp.ErrorCode = &storeCode
		resp.Message = "短链生成失败，已降级为普通链接（请部署者检查 /data 卷）"
	}
	writeJSON(w, http.StatusOK, resp)
}
