// 拉取期派生（v1.5.0）：命中带 groups 的短链时，读该码冻结档位当前生效快照 ini
// （{DATA_DIR}/rules/ 内改写产物），追加 custom_proxy_group= 组行后写
// {DATA_DIR}/derived/{code}.ini；引擎 config= 指向容器内回环 serve URL（闸门实测：
// 引擎 config= 只认 http URL，本地路径与 file:// 均被当 opaque URL 拒 400）。
// 契约「GET /s/{code} 拉取期派生」节；fail-closed（失败 502 derive_error 不回退无组输出）。
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// url-test 行尾两段（模板原生形态，R5 定案：测速流量穿节点，外网测速 URL 勿换国内；
// 间隔 300、tolerance 50 与 ACL4SSR 模板同款）
const urlTestTail = "http://www.gstatic.com/generate_204`300,,50"

// 撞名后缀带圈数字②..⑳（U+2461..U+2473，①留作原名的天然形态）；撞穿全表报错
// （闸门实证撞名有害：同名追加行被引擎静默吞、用户组丢失，闸门例 5）
var circledSuffixes = func() []string {
	out := make([]string, 0, 19)
	for r := rune(0x2461); r <= 0x2473; r++ {
		out = append(out, string(r))
	}
	return out
}()

// parseProxyGroupName custom_proxy_group= 行取组名：模板原生形态名在首个反引号
// 之前（`custom_proxy_group=🚀 节点选择`select`...`）；兼容罕见前导反引号形态
func parseProxyGroupName(line string) (string, bool) {
	if !strings.HasPrefix(line, "custom_proxy_group=") {
		return "", false
	}
	rest := strings.TrimPrefix(line[len("custom_proxy_group="):], "`")
	j := strings.IndexByte(rest, '`')
	if j <= 0 { // 0=空名，<0=无分隔符非组行
		return "", false
	}
	return rest[:j], true
}

// isSelectCPG custom_proxy_group= 行是否 select 类（名`select`…）——select 组有手动
// 选择语义才注入自定义组；url-test/fallback 等自动组不注入（用户裁决 A）
func isSelectCPG(line string) bool {
	name, ok := parseProxyGroupName(line)
	if !ok {
		return false
	}
	rest := strings.TrimPrefix(line[len("custom_proxy_group="):], "`")
	return strings.HasPrefix(rest[len(name):], "`select`")
}

// deriveINI 派生总入口（v1.5.0 增补承载 exclude）：先删减模板组（exclude 非空时：删组行 /
// 摘成员引用 / 规则回退 / fail-closed 自检），再追加自定义组行并接线。groups 为空仅
// exclude 非空 → 跳过第 3–6 步（生成/撞名/接线），只做删减+自检（契约第 2b 步）。
// v1.7.1: 支持 airportTags 可变参数——有多机场时自动生成各机场测速组与 🔯 机场容灾 组
func deriveINI(src []byte, groups []nodeGroup, exclude []string, airportTagsArgs ...[]string) ([]byte, error) {
	lines := strings.Split(string(src), "\n")
	info := analyzeTemplate(src)
	removed := map[string]bool{}
	for _, name := range exclude {
		if _, ok := info.byName[name]; !ok || info.locked(name) {
			continue // 模板中不存在的名与 locked 组静默忽略（上游改名容错，契约第 2b 步）
		}
		removed[name] = true
	}
	requiredTargets := map[string]bool{} // 规则改写用到的回退目标组（自检要求产物中存在）
	for name := range removed {
		if info.ruleTargets[name] {
			requiredTargets[fallbackRuleTarget(info, name)] = true
		}
	}
	if len(removed) > 0 {
		lines = pruneTemplateGroups(lines, info, removed)
	}
	if len(groups) > 0 {
		var err error
		lines, err = appendGroups(lines, groups, info)
		if err != nil {
			return nil, err
		}
	}
	if len(airportTagsArgs) > 0 && len(airportTagsArgs[0]) >= 2 {
		lines = appendAirportGroups(lines, airportTagsArgs[0], info)
	}
	out := []byte(strings.Join(lines, "\n"))
	if len(removed) > 0 {
		if err := selfCheckPruned(out, removed, requiredTargets); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// appendAirportGroups 为多机场自动生成各机场专属测速组与 🔯 机场容灾 策略组，并注入各 select 组首位
func appendAirportGroups(lines []string, airportTags []string, info templateInfo) []string {
	if len(airportTags) < 2 {
		return lines
	}

	lastCPG := -1
	var selectRows []int
	inCustom := false
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if name, ok := sectionName(t); ok {
			inCustom = name == "custom"
			continue
		}
		if !inCustom {
			continue
		}
		if _, ok := parseProxyGroupName(t); ok {
			if isSelectCPG(t) {
				selectRows = append(selectRows, i)
			}
			lastCPG = i
		}
	}
	if lastCPG < 0 {
		return lines
	}

	var gen []string
	var members string
	var airportGroupWire string
	for _, tag := range airportTags {
		gName := fmt.Sprintf("✈️ [%s] 全部节点", tag)
		pat := fmt.Sprintf(`(\[%s\])`, regexp.QuoteMeta(tag))
		gen = append(gen, fmt.Sprintf("custom_proxy_group=%s`url-test`%s`%s", gName, pat, urlTestTail))
		members += "`[]" + gName
		airportGroupWire += "`[]" + gName
	}
	fbName := "🔯 机场容灾"
	gen = append(gen, fmt.Sprintf("custom_proxy_group=%s`fallback%s`%s", fbName, members, urlTestTail))

	// 将 []🔯 机场容灾 注入为所有 select 类组的首选，行尾追加各机场专属组引用
	for _, i := range selectRows {
		line := lines[i]
		const selMarker = "`select`"
		if idx := strings.Index(line, selMarker); idx >= 0 {
			prefix := line[:idx+len(selMarker)]
			suffix := line[idx+len(selMarker):]
			lines[i] = prefix + "[]" + fbName + "`" + suffix + airportGroupWire
		}
	}

	out := make([]string, 0, len(lines)+len(gen))
	out = append(out, lines[:lastCPG+1]...)
	out = append(out, gen...)
	out = append(out, lines[lastCPG+1:]...)
	return out
}

// appendGroups 组行生成 + 撞名兜底 + 插入位 + 全 select 组接线（契约第 3–6 步）。
// taken 用删减前模板原始名单（含被删名——用户组不得复用被删组名，避免与删减自检
// 冲突，契约第 4 步）；主组接线（用户裁决 B，2026-10-28）：每个 select 类模板组行
// 行尾为每组追加 []组引用段，规则流量可达自定义组。
func appendGroups(lines []string, groups []nodeGroup, info templateInfo) ([]string, error) {
	taken := make(map[string]bool, len(info.groups))
	for _, g := range info.groups {
		taken[g.Name] = true
	}
	lastCPG := -1        // 既有组行块尾（插入位锚点；删减后行集）
	var selectRows []int // select 类模板组行（全接线，用户裁决 A）
	inCustom := false
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if name, ok := sectionName(t); ok {
			inCustom = name == "custom"
			continue
		}
		if !inCustom {
			continue
		}
		if _, ok := parseProxyGroupName(t); ok {
			if isSelectCPG(t) {
				selectRows = append(selectRows, i)
			}
			lastCPG = i
		}
	}
	if lastCPG < 0 {
		return nil, fmt.Errorf("快照 ini 无 [custom] 段 custom_proxy_group= 锚点")
	}
	gen := make([]string, 0, len(groups))
	wire := "" // 主组行尾追加段（[]组引用，模板原生形态——主组成员本就含 []♻️ 自动选择 等）
	for _, g := range groups {
		name := g.Name
		if taken[name] {
			name = ""
			for _, sfx := range circledSuffixes {
				if cand := g.Name + sfx; !taken[cand] {
					name = cand
					break
				}
			}
			if name == "" {
				return nil, fmt.Errorf("组名撞穿模板组名单（后缀②..⑳全占用）")
			}
		}
		taken[name] = true
		wire += "`[]" + name
		// 匹配表达式两态（v1.6.1 pattern 优先）：pattern 非空 = 正则组，未以 (?i)
		// 开头自动拼前缀（默认忽略大小写，设计决策 4；引擎 PCRE 实测支持 inline
		// flag），pattern 原样放入不 Compile——Go regexp 是 RE2 不支持 Perl 前瞻，
		// 语法合法性由前端 JS RegExp 把关，服务端只校长度/非法字符（铁律）；
		// pattern 空 = 关键词组，前瞻串联 AND（gate2 实证写法）：词 w1..wn 各
		// QuoteMeta 转义后拼 (?=.*w1)(?=.*w2)…(?=.*wn)；单词同款写法（gate2 例 4
		// 与裸词等价，不按词数分支）
		var escaped string
		if g.Pattern != "" {
			if strings.HasPrefix(g.Pattern, "(?i)") {
				escaped = g.Pattern
			} else {
				escaped = "(?i)" + g.Pattern
			}
		} else {
			var kb strings.Builder
			for _, w := range g.Keywords {
				kb.WriteString("(?=.*")
				kb.WriteString(regexp.QuoteMeta(w))
				kb.WriteString(")")
			}
			escaped = kb.String()
		}
		// 行格式 = 闸门实证形态（与 ACL4SSR 模板原生行同构，无前导反引号）：
		// name`select`escaped / name`url-test`escaped`url`interval,,tolerance。
		// 实证（2026-10-28 本机引擎复测）：前导反引号形态（=`name`select`k`）组被
		// 引擎静默吞——正是闸门例 5 记录的有害形态；契约节内行示例已对齐本形态
		if g.URLTest {
			gen = append(gen, "custom_proxy_group="+name+"`url-test`"+escaped+"`"+urlTestTail)
		} else {
			gen = append(gen, "custom_proxy_group="+name+"`select`"+escaped)
		}
	}
	// 全 select 组接线（用户裁决 A，2026-10-28）：自定义组追加到每个 select 类模板组行尾
	// （名`select`行——手动选择语义处处可选，如 MultiMode 的 💬 Ai平台；兼容 .* 正则与
	// []DIRECT 尾段两形态）；url-test 等自动组行不注入；每次派生从原始 ini 全量重建、
	// 覆盖写，幂等天然成立。极端模板无 select 组行时不接线（组仍生成，GLOBAL 模式可用）
	for _, i := range selectRows {
		lines[i] += wire
	}
	out := make([]string, 0, len(lines)+len(gen))
	out = append(out, lines[:lastCPG+1]...)
	out = append(out, gen...)
	out = append(out, lines[lastCPG+1:]...)
	return out, nil
}

// prepareDerivedConfig /s/ 拉取期派生入口：读冻结档位当前生效本地 ini（豁免/出树态
// 建链期已被能力位挡，兜底撞上在此失败）→ 派生（groups 追加 + exclude 删减）→ 写
// {DATA_DIR}/derived/{code}.ini（拉取时覆盖写——快照刷新后下次拉取自动用新模板）→
// 返回引擎可用的回环 URL。任一步失败返回错误（上层 502 derive_error）。
// rbLines（v1.7.0 DNS 透传）：非空时同文件追加 rule_base 行（机场 dns 派生 base 的
// 回环 URL 集）——一根派生 ini 同时承载组行 + rule_base 行（设计定案的叠加共存）。
func (s *server) prepareDerivedConfig(code, frozenConfigURL, groupsJSON, excludeJSON string, rbLines []string, airportTagsArgs ...[]string) (string, error) {
	groups, err := parseGroupsJSON(groupsJSON)
	if err != nil {
		return "", err
	}
	exclude, err := parseExcludeJSON(excludeJSON)
	if err != nil {
		return "", err
	}
	var airportTags []string
	if len(airportTagsArgs) > 0 {
		airportTags = airportTagsArgs[0]
	}
	if len(groups) == 0 && len(exclude) == 0 && len(rbLines) == 0 && len(airportTags) < 2 {
		return "", fmt.Errorf("groups 与 exclude 及 airportTags 均为空，不应进入派生路径")
	}
	if !strings.HasPrefix(frozenConfigURL, "/") {
		// v1.5.0 兜底（评审 F3）：改写产物缺（bootstrap 预生成原料不齐、产物被删等残余
		// 窗口）且冻结 URL 匹配内置档原始 URL → 本地原料齐备时同步改写当场补产物再派生；
		// 非内置 URL（豁免档自定义远端）无本地原料，维持 fail-closed
		if s.snap != nil {
			if p, ok := s.snap.presetByURL(frozenConfigURL); ok && s.snap.ensureRewritten(p) {
				frozenConfigURL, _ = s.snap.rewrittenPath(p)
			}
		}
	}
	if !strings.HasPrefix(frozenConfigURL, "/") {
		return "", fmt.Errorf("冻结档位无本地快照（%s 非 /base 树内路径），无法派生", sanitizePathLog(frozenConfigURL))
	}
	src, err := os.ReadFile(frozenConfigURL)
	if err != nil {
		return "", fmt.Errorf("读快照失败: %w", err)
	}
	out, err := deriveINI(src, groups, exclude, airportTags)
	if err != nil {
		return "", err
	}
	if len(rbLines) > 0 {
		if out, err = appendRuleBaseLines(out, rbLines); err != nil {
			return "", err
		}
	}
	dir := filepath.Join(s.cfg.dataDir, "derived")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("创建 %s 失败: %w", dir, err)
	}
	if err := atomicWrite(filepath.Join(dir, code+".ini"), out); err != nil {
		return "", fmt.Errorf("写派生文件失败: %w", err)
	}
	return s.cfg.sidecarBase + "/internal/derived/" + code, nil
}

// sanitizePathLog 非本地路径形态（远端 URL，可能含部署者自定义 query）只留 host 段进日志
func sanitizePathLog(u string) string {
	if i := strings.Index(u, "://"); i > 0 {
		rest := u[i+3:]
		if j := strings.IndexAny(rest, "/?#"); j >= 0 {
			return rest[:j]
		}
		return rest
	}
	return u
}

// 502 人话页文案（derive_error 家族，内容协商同 upstream_unavailable）
const (
	deriveErrTitle  = "订阅生成遇到问题（自定义分组）"
	deriveErrDetail = "这条短链配置了自定义分组，服务器在生成配置时遇到错误。请稍后重试；如果持续出现，请联系部署者。"
)

// handleInternalDerived GET /internal/derived/{name}：引擎专用（回环 listener，nginx 不
// 路由），serve 派生 ini。照 /internal/sub/ 模式但无 TTL——短链存活期内可重复拉取；
// miss → 404（引擎侧表现为拉取失败，sidecar 日志有根因）。
// v1.7.0 DNS 透传增补：除既有 {code} 形态（读 code+".ini"——短码 base62 无点，存量
// URL 逐字节兼容）外，接受带后缀文件名（dns-{preset}-{hash}.ini/.tpl/.yaml——rule_base
// 只认 http，派生 base 也走本端点回环 serve，闸门 G1 实测）；后缀白名单外的形态一律 404。
func (s *server) handleInternalDerived(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/internal/derived/")
	if name == "" || strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		http.NotFound(w, r)
		return
	}
	file := name
	switch filepath.Ext(name) {
	case ".ini", ".tpl", ".yaml":
		// 带后缀 = v1.7.0 派生物（dns 三件套），文件名即真名
	default:
		// 无白名单后缀 = 既有 {code} 形态，落盘文件名补 .ini（存量 URL 兼容）
		if strings.Contains(name, ".") {
			http.NotFound(w, r)
			return
		}
		file = name + ".ini"
	}
	data, err := os.ReadFile(filepath.Join(s.cfg.dataDir, "derived", file))
	if err != nil {
		log.Printf("internal-derived miss name=%s***", logCode(name))
		http.NotFound(w, r)
		return
	}
	ct := "text/plain; charset=utf-8"
	w.Header().Set("Content-Type", ct)
	_, _ = w.Write(data)
}
