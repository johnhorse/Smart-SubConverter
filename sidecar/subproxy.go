// v1.2.0 短链 HTTP 端点：GET /s/{code} 订阅代理 + GET /api/links、DELETE /api/links/{code} 管理。
// 日志卫生（D6 + OH-2：短码即 bearer 凭据）：只记脱敏摘要——code 前 3 位 + 状态 + 耗时，
// 不记完整码、不记订阅 URL、不记 token。
package main

import (
	"context"
	"errors"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
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

	// v1.5.0 拉取期派生：该码带 groups 或 exclude → 读冻结档位当前生效快照 ini 派生，
	// 引擎 config= 改指派生文件的容器内回环 URL（两段式链第二段与单段链同样生效——第一段
	// mixed 无 config 参数）。失败 fail-closed 502 derive_error（不回退无组输出，R2）
	// v1.7.0 时机分叉：dns 透传开启时派生延后到代拉之后（rule_base 行与组行同落
	// {code}.ini，见 proxyTwoStage 钩子与下方单段分支）；关闭时维持原时机（逐字节回退现状）
	// v1.7.1：多订阅（>=2）同样需要派生机场容灾组
	subs := parseSubscriptions(lnk.SourceURL)
	groupsPending := false
	if lnk.Groups != "" || lnk.Exclude != "" || len(subs) >= 2 {
		if s.cfg.passThroughDNS {
			groupsPending = true
		} else {
			var tags []string
			if len(subs) >= 2 {
				for _, it := range subs {
					tags = append(tags, it.Tag)
				}
			}
			derived, err := s.prepareDerivedConfig(code, configURL, lnk.Groups, lnk.Exclude, nil, tags)
			if err != nil {
				log.Printf("s/ hit code=%s*** status=%d latency=%s derive_error: %v", logCode(code), http.StatusBadGateway, time.Since(start).Round(time.Millisecond), err)
				writeSubError(w, r, http.StatusBadGateway, "derive_error", deriveErrTitle, deriveErrDetail)
				return
			}
			configURL = derived
		}
	}

	// v1.3.3 QX 两段式服务端代取；v1.4.0 扩展到 clash；多订阅（>=2）统一走两段式输出合并内联节点
	if stageTarget, ok := twoStageRouteTarget(lnk.Target, r.Header.Get("User-Agent")); ok || len(subs) >= 2 {
		if !ok {
			stageTarget = lnk.Target
			if stageTarget == "auto" {
				stageTarget = "clash"
			}
		}
		s.proxyTwoStage(w, r, lnk, configURL, code, stageTarget, start, groupsPending)
		return
	}

	// v1.7.0 单段链统一改 sidecar 代拉（设计定案：preview 探测与拉取同路的「加固」——
	// 绕机场 WAF 的 TLS 指纹拦截，且 UA 原样透传保留机场按 UA 的内容协商）+ DNS 透传
	// （代拉原文提取 dns → 派生 config）。代拉失败 fail-open 回退原始 URL 让引擎自拉
	// （现状行为），绝不因加固路径挂掉拉取；PASS_THROUGH_DNS=off 时整段跳过（逐字节回退）
	subURL := lnk.SourceURL
	// dns 派生产出的 rule_base 行（机场无 dns / 代拉失败时为空）。注意 groupsPending 的
	// 组行派生不依赖 dns 结果，必须独立发生——机场无 dns 或代拉失败的带组单段链若把
	// 派生嵌进 dns 条件里会静默丢组（验收回归修复）
	var dnsRuleBase []string
	if s.cfg.passThroughDNS {
		if internalURL, content, err := s.isub.fetchAndRegisterContent(r.Context(), r.Header.Get("User-Agent"), lnk.SourceURL, s.cfg.sidecarBase); err != nil {
			log.Printf("s/ hit code=%s*** 单段代拉失败（fail-open 回退引擎自拉）: %v", logCode(code), err)
		} else {
			subURL = internalURL
			newCfg, rb := s.dnsPassthroughConfig(content, configURL)
			dnsRuleBase = rb
			if len(rb) > 0 && !groupsPending {
				configURL = newCfg // 无组链直用共享 dns ini；有组链只取 rb 行，下方与组行同落 {code}.ini
			}
		}
	}
	if groupsPending {
		derived, err := s.prepareDerivedConfig(code, configURL, lnk.Groups, lnk.Exclude, dnsRuleBase)
		if err != nil {
			log.Printf("s/ hit code=%s*** status=%d latency=%s derive_error: %v", logCode(code), http.StatusBadGateway, time.Since(start).Round(time.Millisecond), err)
			writeSubError(w, r, http.StatusBadGateway, "derive_error", deriveErrTitle, deriveErrDetail)
			return
		}
		configURL = derived
	}

	q := url.Values{}
	q.Set("target", lnk.Target)
	q.Set("url", subURL)
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
		log.Printf("s/ hit code=%s*** status=%d latency=%s (upstream %v)", logCode(code), http.StatusBadGateway, time.Since(start).Round(time.Millisecond), sanitizeURLErr(err))
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

// twoStageRouteTarget 两段式路由判定 + 第二段归一 target：显式 quanx/clash 恒走（target
// 原样）；auto 链按 UA 宽匹配家族归一（quantumult → quanx、clash 系词表 {clash, mihomo,
// shellcrash} 小写包含 → clash），与 v1.3.3 QX 硬编码 quanx 的既有行为同构；其余（Surge /
// Loon / Stash 等）直通，返回 false。宽匹配仅为路由决策，权威识别仍在引擎。
// 词表三词各自独立必需：shellcrash 不含 clash 子串（crash ≠ clash，contains 实测 false）。
func twoStageRouteTarget(target, ua string) (string, bool) {
	switch target {
	case "quanx", "clash":
		return target, true
	case "auto":
		l := strings.ToLower(ua)
		if strings.Contains(l, "quantumult") {
			return "quanx", true
		}
		for _, w := range []string{"clash", "mihomo", "shellcrash"} {
			if strings.Contains(l, w) {
				return "clash", true
			}
		}
	}
	return "", false
}

// twoStageFailure 两段式失败的结构化原因：核心只产出结构，/s/ 与 preview 各自映射为
// 日志行与错误码（不碰响应）
type twoStageFailure struct {
	stage  int    // 0=代拉 1=第一段(mixed) 2=第二段(链 target 转换)
	kind   string // err=请求失败 upstream=引擎非200 nodes=第一段空节点 airport=代拉非200 internal=构造请求失败
	err    error  // kind=err / internal
	status int    // kind=upstream / airport
}

// stage1Mixed 两段式第一段（v1.5.0 评审 F7 提取，/s/ 拉取与 preview probe 节点
// 探测共用）：sidecar 代拉订阅源注册内部回环（v1.3.5：绕机场 WAF 的 TLS 指纹拦截，
// SUBSCRIPTION_PROXY 生效时走代理）→ 引擎 target=mixed 展开为通用 URI 列表。
// v1.7.0 增返代拉原文：mixed 展开会丢机场 dns 块（闸门 G4 实证），DNS 透传必须在
// 展开前从原始字节捞住——调用方（twoStageConvert 钩子）据此派生。
// stageTimeout>0 时第一段单独分得预算（/s/ SUB_PROXY_TIMEOUT/2、preview 两段式
// 探测整体预算/2）；<=0 时直接受 ctx 约束（probe 节点名单口径：全额预算不分段）。
// 失败返回 twoStageFailure（stage 0=代拉 1=第一段），成功返回 URI 列表与代拉原文；
// 调用方各自映射错误码与日志。
func (s *server) stage1Mixed(ctx context.Context, client *http.Client, ua, sourceURL string, stageTimeout time.Duration) ([]string, string, *twoStageFailure) {
	internalURL, content, err := s.isub.fetchAndRegisterContent(ctx, ua, sourceURL, s.cfg.sidecarBase)
	if err != nil {
		var st errUpstreamStatus
		if errors.As(err, &st) {
			return nil, "", &twoStageFailure{stage: 0, kind: "airport", status: st.code}
		}
		return nil, "", &twoStageFailure{stage: 0, kind: "err", err: err}
	}
	q := url.Values{}
	q.Set("target", "mixed")
	q.Set("url", internalURL)
	if s.cfg.subToken != "" {
		q.Set("token", s.cfg.subToken)
	}
	req, err := http.NewRequest(http.MethodGet, s.cfg.extendedBase+"/sub?"+q.Encode(), nil)
	if err != nil {
		return nil, "", &twoStageFailure{stage: 1, kind: "internal", err: err}
	}
	req.Header.Set("User-Agent", ua)
	if stageTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, stageTimeout)
		defer cancel()
	}
	resp, err := client.Do(req.WithContext(ctx))
	if err != nil {
		return nil, "", &twoStageFailure{stage: 1, kind: "err", err: err}
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", &twoStageFailure{stage: 1, kind: "upstream", status: resp.StatusCode}
	}
	uris := parseNodeURIList(body)
	if len(uris) == 0 {
		return nil, "", &twoStageFailure{stage: 1, kind: "nodes"}
	}
	return uris, content, nil
}

// stage1MixedMulti 多订阅并发代拉（单订阅退化为 stage1Mixed 原路）。
// 并发抓取各机场，单机场失败跳过；只要有 >=1 个机场成功就合并节点并返回主力机场 content；
// 全部失败才返回错误。
func (s *server) stage1MixedMulti(ctx context.Context, client *http.Client, ua string, subs []subscriptionItem, stageTimeout time.Duration) ([]string, string, []string, *twoStageFailure) {
	if len(subs) <= 1 {
		url := ""
		tag := ""
		if len(subs) == 1 {
			url = subs[0].URL
			tag = subs[0].Tag
		}
		uris, content, fail := s.stage1Mixed(ctx, client, ua, url, stageTimeout)
		if fail != nil {
			return nil, "", nil, fail
		}
		if tag != "" {
			uris = tagNodeURIs(uris, tag)
		}
		var activeTags []string
		if tag != "" {
			activeTags = []string{tag}
		}
		return uris, content, activeTags, nil
	}

	type subResult struct {
		index   int
		tag     string
		uris    []string
		content string
		fail    *twoStageFailure
	}

	resCh := make(chan subResult, len(subs))
	for i, sub := range subs {
		go func(idx int, item subscriptionItem) {
			uris, content, fail := s.stage1Mixed(ctx, client, ua, item.URL, stageTimeout)
			if fail == nil && item.Tag != "" {
				uris = tagNodeURIs(uris, item.Tag)
			}
			resCh <- subResult{
				index:   idx,
				tag:     item.Tag,
				uris:    uris,
				content: content,
				fail:    fail,
			}
		}(i, sub)
	}

	results := make([]subResult, len(subs))
	for i := 0; i < len(subs); i++ {
		r := <-resCh
		results[r.index] = r
	}

	var allURIs []string
	var primaryContent string
	var activeTags []string
	var lastFail *twoStageFailure

	for _, r := range results {
		if r.fail != nil {
			log.Printf("s/ 多订阅代拉跳过 [%s]: stage=%d kind=%s err=%v", r.tag, r.fail.stage, r.fail.kind, r.fail.err)
			lastFail = r.fail
			continue
		}
		if len(r.uris) > 0 {
			allURIs = append(allURIs, r.uris...)
			activeTags = append(activeTags, r.tag)
			if primaryContent == "" {
				primaryContent = r.content
			}
		}
	}

	if len(allURIs) == 0 {
		if lastFail != nil {
			return nil, "", nil, lastFail
		}
		return nil, "", nil, &twoStageFailure{stage: 1, kind: "nodes"}
	}

	return allURIs, primaryContent, activeTags, nil
}

// twoStageConvert 两段式共享核心（v1.3.3 QX；v1.4.0 参数化 target 并供 preview 同路）：
// 第一段（stage1MixedMulti）代拉订阅转通用 URI 列表，第二段以「URI|URI」明文节点列表转
// 链 target，输出内联明文节点（quanx [server_local] / clash proxies 数组），客户端
// 零二次拉取。两段各自分得 stageTimeout。
// v1.7.0 增 configHook：第一段拿到代拉原文后、第二段开跑前调用——DNS 透传（fail-open）
// 与 groups/exclude 派生（fail-closed）都在此刻完成 config= 改写；nil = 直通（旧行为）。
// 成功返回第二段响应与第二段的 context cancel（B1：resp body 由调用方流式消费，cancel
// 责任随之移交——调用方须在 body 消费完后触发；核心内 defer cancel 会在 return 瞬间取消
// ctx，大响应 body 读到一半即截断）。所有失败路径在返回前自行 cancel。
func (s *server) twoStageConvert(ctx context.Context, client *http.Client, ua, sourceURL, configURL, target string, stageTimeout time.Duration, configHook func(stage1Content string, activeTags []string) (string, *twoStageFailure)) (*http.Response, []string, context.CancelFunc, *twoStageFailure) {
	subs := parseSubscriptions(sourceURL)
	uris, content, activeTags, fail := s.stage1MixedMulti(ctx, client, ua, subs, stageTimeout)
	if fail != nil {
		return nil, nil, nil, fail
	}
	// v1.7.0 钩子：dns 透传 + groups 派生（拉取期）。钩子失败（kind=derive）直达调用方
	if configHook != nil {
		hooked, hfail := configHook(content, activeTags)
		if hfail != nil {
			return nil, nil, nil, hfail
		}
		configURL = hooked
	}

	// 第二段：节点列表 → 链 target 完整配置（内联明文节点）
	q2 := url.Values{}
	q2.Set("target", target)
	q2.Set("url", strings.Join(uris, "|"))
	q2.Set("config", configURL)
	if s.cfg.subToken != "" {
		q2.Set("token", s.cfg.subToken)
	}
	req2, err := http.NewRequest(http.MethodGet, s.cfg.extendedBase+"/sub?"+q2.Encode(), nil)
	if err != nil {
		return nil, nil, nil, &twoStageFailure{stage: 2, kind: "internal", err: err}
	}
	req2.Header.Set("User-Agent", ua)
	ctx2, cancel2 := context.WithTimeout(ctx, stageTimeout)
	resp2, err := client.Do(req2.WithContext(ctx2))
	if err != nil {
		cancel2()
		return nil, nil, nil, &twoStageFailure{stage: 2, kind: "err", err: err}
	}
	if resp2.StatusCode != http.StatusOK {
		resp2.Body.Close()
		cancel2()
		return nil, nil, nil, &twoStageFailure{stage: 2, kind: "upstream", status: resp2.StatusCode}
	}
	return resp2, uris, cancel2, nil
}

// proxyTwoStage /s/ 两段式包装（v1.3.3 QX 起家，v1.4.0 参数化到 clash）：失败恒 502
// upstream_unavailable（契约 /s/ 第 4/5 条；日志逐段脱敏摘要，补 target= 便于排障），
// 成功流式透传第二段输出。两段各自分得 SUB_PROXY_TIMEOUT 的一半。
// stageTarget = 归一后的第二段转换 target（显式链原样、auto 链按 UA 家族，见 twoStageRouteTarget）。
// groupsPending（v1.7.0）：带 groups/exclude 且 dns 透传开启时派生延到第一段后（rule_base
// 行与组行同落 {code}.ini）；dns 关闭时 handleShortLink 已提前派生，此处直通。
func (s *server) proxyTwoStage(w http.ResponseWriter, r *http.Request, lnk storedLink, configURL, code, stageTarget string, start time.Time, groupsPending bool) {
	ua := r.Header.Get("User-Agent")
	stageTimeout := s.cfg.subProxyTimeout / 2
	if stageTimeout <= 0 { // 防零值（生产 env 解析保证 >0；直接构造 config 的路径兜底）
		stageTimeout = 15 * time.Second
	}

	// v1.7.0 拉取期 config 钩子：dns 透传（fail-open，无 dns / 豁免档 / 自检失败一律
	// 原样 config）→ groups/exclude 派生（fail-closed，kind=derive → 502 derive_error
	// 与既有单段语义一致）
	baseConfig := configURL
	hook := func(stage1Content string, activeTags []string) (string, *twoStageFailure) {
		cfg := baseConfig
		var rb []string
		if s.cfg.passThroughDNS {
			var dnsCfg string
			dnsCfg, rb = s.dnsPassthroughConfig(stage1Content, cfg)
			if dnsCfg != cfg {
				cfg = dnsCfg // 共享 dns ini（无组链直用；有组链仅取 rb 行叠加）
			}
		}
		if groupsPending || len(activeTags) >= 2 {
			derived, err := s.prepareDerivedConfig(code, baseConfig, lnk.Groups, lnk.Exclude, rb, activeTags)
			if err != nil {
				return "", &twoStageFailure{stage: 1, kind: "derive", err: err}
			}
			cfg = derived
		}
		return cfg, nil
	}

	resp2, uris, cancelStage2, fail := s.twoStageConvert(r.Context(), s.proxyClient, ua, lnk.SourceURL, configURL, stageTarget, stageTimeout, hook)
	if fail != nil {
		if fail.kind == "internal" {
			writeJSON(w, http.StatusInternalServerError, errBody{"internal"})
			return
		}
		if fail.kind == "derive" { // v1.7.0：派生失败语义与既有 /s/ 拉取期派生一致（fail-closed）
			log.Printf("s/ hit code=%s*** status=%d latency=%s twostage=derive target=%s derive_error: %v", logCode(code), http.StatusBadGateway, time.Since(start).Round(time.Millisecond), stageTarget, fail.err)
			writeSubError(w, r, http.StatusBadGateway, "derive_error", deriveErrTitle, deriveErrDetail)
			return
		}
		switch {
		case fail.kind == "airport":
			log.Printf("s/ hit code=%s*** status=%d latency=%s twostage=0/2 target=%s 代拉 airport=%d", logCode(code), http.StatusBadGateway, time.Since(start).Round(time.Millisecond), stageTarget, fail.status)
		case fail.stage == 0:
			log.Printf("s/ hit code=%s*** status=%d latency=%s twostage=0/2 target=%s 代拉 err=%v", logCode(code), http.StatusBadGateway, time.Since(start).Round(time.Millisecond), stageTarget, fail.err)
		case fail.kind == "err":
			log.Printf("s/ hit code=%s*** status=%d latency=%s twostage=%d/2 target=%s err=%v", logCode(code), http.StatusBadGateway, time.Since(start).Round(time.Millisecond), fail.stage, stageTarget, sanitizeURLErr(fail.err))
		case fail.kind == "upstream":
			log.Printf("s/ hit code=%s*** status=%d latency=%s twostage=%d/2 target=%s upstream=%d", logCode(code), http.StatusBadGateway, time.Since(start).Round(time.Millisecond), fail.stage, stageTarget, fail.status)
		default: // nodes：代拉成功但第一段解析不出有效节点
			log.Printf("s/ hit code=%s*** status=%d latency=%s twostage=1/2 target=%s nodes=0", logCode(code), http.StatusBadGateway, time.Since(start).Round(time.Millisecond), stageTarget)
		}
		writeSubError(w, r, http.StatusBadGateway, "upstream_unavailable", subErrTitle, subErrDetail)
		return
	}
	// B1：第二段 resp body 在此流式消费，cancel 随之交接——defer 保证消费完成后才取消
	// 第二段 ctx（失败路径核心内已自行 cancel）
	defer cancelStage2()
	defer resp2.Body.Close()

	if ct := resp2.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(http.StatusOK)
	copyErr := streamBody(w, resp2.Body)
	log.Printf("s/ hit code=%s*** status=%d latency=%s twostage target=%s nodes=%d", logCode(code), http.StatusOK, time.Since(start).Round(time.Millisecond), stageTarget, len(uris))
	if copyErr != nil {
		// 响应头已发，无法改状态码；客户端拉完即断是常态，仅记脱敏摘要
		log.Printf("s/ 透传中断 code=%s*** copyErr=%T", logCode(code), copyErr)
	}
}

// parseNodeURIList 解 mixed 输出为节点 URI 列表：整体 base64（解码后含 :// 用解码值），
// 按空白分割；非 base64 形态（明文列表）直接分割。过滤非 URI 行。
func parseNodeURIList(body []byte) []string {
	raw := strings.TrimSpace(string(body))
	if decoded, err := base64.StdEncoding.DecodeString(raw); err == nil && strings.Contains(string(decoded), "://") {
		raw = string(decoded)
	}
	var uris []string
	for _, f := range strings.Fields(raw) {
		if i := strings.Index(f, "://"); i > 0 && i < 16 {
			uris = append(uris, f)
		}
	}
	return uris
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

// handleDeleteLink DELETE /api/links/{code}：恒 204（幂等：码不存在也 204）。
// v1.5.0 X-4：同步删 {DATA_DIR}/derived/{code}.ini（存在才删；删失败仅记日志，204 不变）
func (s *server) handleDeleteLink(w http.ResponseWriter, r *http.Request) {
	if !s.requireSubToken(w, r) {
		return
	}
	code := r.PathValue("code")
	if err := s.store.deleteLink(code); err != nil {
		writeJSON(w, http.StatusInternalServerError, errBody{"internal"})
		return
	}
	if err := os.Remove(filepath.Join(s.cfg.dataDir, "derived", code+".ini")); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("删除派生文件失败 code=%s***: %v", logCode(code), err)
	}
	w.WriteHeader(http.StatusNoContent)
}
