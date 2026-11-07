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
	var airportTags []string
	if len(airportTagsArgs) > 0 {
		airportTags = airportTagsArgs[0]
	}
	if len(groups) > 0 {
		var err error
		lines, err = appendGroups(lines, groups, info, airportTags)
		if err != nil {
			return nil, err
		}
	}
	if len(airportTags) >= 2 {
		lines = appendAirportGroups(lines, airportTags, info)
	}
	out := []byte(strings.Join(lines, "\n"))
	if len(removed) > 0 {
		if err := selfCheckPruned(out, removed, requiredTargets); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// overlayTagPattern 将机场标签前瞻正则 (?i)(?=.*\[{tag}\]) 叠加到 basePattern 上
// 铁律：base 若为关键词前瞻串联（以 (?= 开头），直接拼接；
// base 若为普通正则/字面量，必须用 (?=.*(?:...)) 包裹，与前瞻串联形成「既含 [tag] 又含 base」的 AND 语义，
// 绝不能写成 (?=.*\[tag\])pattern——后者在 position 0 处要求 pattern 立即从 '[' 开始匹配，导致全部节点匹配失败（code-review finding 1 & 3）
func overlayTagPattern(tag, base string) string {
	tagPrefix := fmt.Sprintf("(?i)(?=.*\\[%s\\])", regexp.QuoteMeta(tag))
	raw := strings.TrimPrefix(base, "(?i)")
	if strings.HasPrefix(raw, "(?=") {
		return tagPrefix + raw
	}
	return fmt.Sprintf("%s(?=.*(?:%s))", tagPrefix, raw)
}

// isPrimarySelectGroup 判定是否为主代理选择组（🚀 节点选择 / PROXY / Node Select 等）
func isPrimarySelectGroup(name string) bool {
	if strings.Contains(name, "节点选择") {
		return true
	}
	norm := strings.TrimSpace(strings.ToLower(name))
	return norm == "proxy" || norm == "proxies" || norm == "node select" || norm == "node-select"
}

// isRegionUrlTestGroup 判断是否为模板自带的地区测速组
func isRegionUrlTestGroup(name, typ string, members []string) bool {
	if typ != "url-test" || len(members) == 0 {
		return false
	}
	pat := members[0]
	if pat == ".*" || pat == "" {
		return false
	}
	if strings.HasPrefix(name, "✈️ ") || strings.HasPrefix(name, "⚡ ") || strings.HasPrefix(name, "🔯 ") {
		return false
	}
	return isRegionName(name)
}

// isRegionName 判断组名是否包含地区标识（国旗 emoji、含「节点」或常见地区名）
func isRegionName(name string) bool {
	if strings.Contains(name, "节点") {
		return true
	}
	for _, r := range name {
		if r >= 0x1F1E6 && r <= 0x1F1FF {
			return true
		}
	}
	regionWords := []string{
		"香港", "台湾", "日本", "美国", "狮城", "新加坡",
		"韩国", "英国", "德国", "法国", "俄罗斯", "加拿大",
		"澳大利亚", "欧洲", "印度", "马来西亚", "泰国",
	}
	for _, w := range regionWords {
		if strings.Contains(name, w) {
			return true
		}
	}
	return false
}

// appendAirportGroups 为多机场自动生成各机场专属测速组、全局 🔯 机场容灾 策略组，
// 并将模板原生的地区测速组重写为主备测速 + fallback 容灾组；全局容灾组仅收窄注入到名称含「节点选择」的 select 组。
func appendAirportGroups(lines []string, airportTags []string, info templateInfo) []string {
	if len(airportTags) < 2 {
		return lines
	}

	primaryTag := airportTags[0]
	backupTags := airportTags[1:]

	newLines := make([]string, 0, len(lines)+16)
	var selectRows []int
	inCustom := false

	for _, line := range lines {
		t := strings.TrimSpace(line)
		if name, ok := sectionName(t); ok {
			inCustom = name == "custom"
			newLines = append(newLines, line)
			continue
		}
		if !inCustom {
			newLines = append(newLines, line)
			continue
		}

		name, typ, members, ok := parseCPGLine(t)
		if !ok {
			newLines = append(newLines, line)
			continue
		}

		// 识别模板原生的地区测速组，重写为主备测速子组 + fallback 容灾组
		if _, isTemplate := info.byName[name]; isTemplate && isRegionUrlTestGroup(name, typ, members) {
			pat := members[0]
			tail := urlTestTail
			if len(members) > 1 {
				tail = strings.Join(members[1:], "`")
			}

			primarySubName := fmt.Sprintf("⚡ [%s] %s", primaryTag, name)
			primaryPat := overlayTagPattern(primaryTag, pat)
			newLines = append(newLines, fmt.Sprintf("custom_proxy_group=%s`url-test`%s`%s", primarySubName, primaryPat, tail))

			fbMembers := "`[]" + primarySubName
			for _, bTag := range backupTags {
				backupSubName := fmt.Sprintf("⚡ [%s] %s", bTag, name)
				backupPat := overlayTagPattern(bTag, pat)
				newLines = append(newLines, fmt.Sprintf("custom_proxy_group=%s`url-test`%s`%s", backupSubName, backupPat, tail))
				fbMembers += "`[]" + backupSubName
			}

			newLines = append(newLines, fmt.Sprintf("custom_proxy_group=%s`fallback%s`%s", name, fbMembers, tail))
			continue
		}

		// 记录 select 类组（排除已生成的 ⚡ 子组）
		if isSelectCPG(t) && !strings.HasPrefix(name, "⚡ ") {
			selectRows = append(selectRows, len(newLines))
		}
		newLines = append(newLines, line)
	}

	// 找到 [custom] 段最后一个 custom_proxy_group= 行
	lastCPG := -1
	inCustom = false
	for i, line := range newLines {
		t := strings.TrimSpace(line)
		if name, ok := sectionName(t); ok {
			inCustom = name == "custom"
			continue
		}
		if inCustom && strings.HasPrefix(t, "custom_proxy_group=") {
			lastCPG = i
		}
	}
	if lastCPG < 0 {
		return newLines
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

	// 收窄注入：[]🔯 机场容灾 及 airportGroupWire 仅注入到名称包含「节点选择」或主选择组（如 🚀 节点选择、PROXY 等），
	// 绝不注入到 💬 Ai平台、🌍 国外媒体 等具体业务组，彻底杜绝跨区串流封号
	hasInjected := false
	for _, i := range selectRows {
		line := newLines[i]
		name, ok := parseProxyGroupName(line)
		if !ok || !isPrimarySelectGroup(name) {
			continue
		}
		const selMarker = "`select`"
		if idx := strings.Index(line, selMarker); idx >= 0 {
			prefix := line[:idx+len(selMarker)]
			suffix := line[idx+len(selMarker):]
			newLines[i] = prefix + "[]" + fbName + "`" + suffix + airportGroupWire
			hasInjected = true
		}
	}
	if !hasInjected && len(selectRows) > 0 {
		i := selectRows[0]
		line := newLines[i]
		const selMarker = "`select`"
		if idx := strings.Index(line, selMarker); idx >= 0 {
			prefix := line[:idx+len(selMarker)]
			suffix := line[idx+len(selMarker):]
			newLines[i] = prefix + "[]" + fbName + "`" + suffix + airportGroupWire
		}
	}

	out := make([]string, 0, len(newLines)+len(gen))
	out = append(out, newLines[:lastCPG+1]...)
	out = append(out, gen...)
	out = append(out, newLines[lastCPG+1:]...)
	return out
}

// appendGroups 组行生成 + 撞名兜底 + 插入位 + 全 select 组接线（契约第 3–6 步）。
// taken 用删减前模板原始名单（含被删名——用户组不得复用被删组名，避免与删减自检
// 冲突，契约第 4 步）；主组接线（用户裁决 B，2026-10-28）：每个 select 类模板组行
// 行尾为每组追加 []组引用段，规则流量可达自定义组。
// v1.9.0: 支持 airportTags 可变参数——多机场时每个自定义分组升级为主备测速子组 + 顶层 fallback 组
func appendGroups(lines []string, groups []nodeGroup, info templateInfo, airportTagsArgs ...[]string) ([]string, error) {
	var airportTags []string
	if len(airportTagsArgs) > 0 {
		airportTags = airportTagsArgs[0]
	}
	multiAirport := len(airportTags) >= 2

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

		if !multiAirport {
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
			if g.URLTest {
				gen = append(gen, "custom_proxy_group="+name+"`url-test`"+escaped+"`"+urlTestTail)
			} else {
				gen = append(gen, "custom_proxy_group="+name+"`select`"+escaped)
			}
		} else {
			// v1.9.0: 多机场同区容灾与替补规则
			primaryTag := airportTags[0]
			primarySubName := fmt.Sprintf("⚡ [%s] %s", primaryTag, name)
			taken[primarySubName] = true

			var primaryBase string
			if g.Pattern != "" {
				primaryBase = g.Pattern
			} else {
				var kb strings.Builder
				for _, w := range g.Keywords {
					kb.WriteString("(?=.*")
					kb.WriteString(regexp.QuoteMeta(w))
					kb.WriteString(")")
				}
				primaryBase = kb.String()
			}
			primaryPat := overlayTagPattern(primaryTag, primaryBase)
			if g.URLTest {
				gen = append(gen, fmt.Sprintf("custom_proxy_group=%s`url-test`%s`%s", primarySubName, primaryPat, urlTestTail))
			} else {
				gen = append(gen, fmt.Sprintf("custom_proxy_group=%s`select`%s", primarySubName, primaryPat))
			}

			fbMembers := "`[]" + primarySubName
			for _, bTag := range airportTags[1:] {
				backupSubName := fmt.Sprintf("⚡ [%s] %s", bTag, name)
				taken[backupSubName] = true
				var backupBase string
				if rule, ok := g.BackupRules[bTag]; ok && strings.TrimSpace(rule) != "" {
					backupBase = strings.TrimSpace(rule)
				} else {
					backupBase = primaryBase
				}
				backupPat := overlayTagPattern(bTag, backupBase)
				gen = append(gen, fmt.Sprintf("custom_proxy_group=%s`url-test`%s`%s", backupSubName, backupPat, urlTestTail))
				fbMembers += "`[]" + backupSubName
			}

			// 顶层 Fallback 容灾组：原名 {name}
			gen = append(gen, fmt.Sprintf("custom_proxy_group=%s`fallback%s`%s", name, fbMembers, urlTestTail))
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
