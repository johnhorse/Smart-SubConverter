// 模板群组删减（v1.5.0 增补）：从快照 ini 的 [custom] 段解析模板自带组与规则引用，
// 供 GET /api/clients 下发组清单（locked 标记）、probe 命中计数（groupHits）与拉取期
// 派生删减（exclude：删组行 / 摘成员引用 / 规则回退 / fail-closed 自检）使用。
// 契约「模板群组删减」与「GET /s/{code} 拉取期派生」第 2/2b 步；gate3 实证引擎对
// 悬空组引用不报错、原样输出（T2），摘引用与自检是硬性步骤。
package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// 规则回退目标组（契约第 2b 步：被删组首成员属直连类 → 全球直连，否则 → 节点选择）
const (
	fallbackDirect = "🎯 全球直连"
	fallbackProxy  = "🚀 节点选择"
)

// directClassMembers 直连类成员清单（规则回退分派；契约 2b 与设计裁决 2）
var directClassMembers = map[string]bool{
	"DIRECT":    true,
	"REJECT":    true,
	"🎯 全球直连":    true,
	"🛑 全球拦截":    true,
	"🛑 广告拦截":    true,
	"🍃 应用净化":    true,
	"🆎 AdBlock": true,
	"🛡️ 隐私防护":   true,
}

// templateGroup 模板自带组（[custom] 段 custom_proxy_group= 行解析结果）
type templateGroup struct {
	Name    string
	Type    string   // select / url-test / fallback / load-balance …
	Members []string // 类型位之后的 token（成员引用 / 正则段 / 测速 URL / 间隔）
}

// templateInfo 整份 ini 的组视图（组清单 + 规则引用关系）
type templateInfo struct {
	groups        []templateGroup
	byName        map[string]int  // 组名 → groups 下标
	ruleTargets   map[string]bool // 被 ruleset=<组名>,… 行指向的目标组
	inlineTargets map[string]bool // 其中来源为内联规则（[]FINAL / []GEOIP / FINAL）的目标组
	referenced    map[string]bool // 被其他组行以 []组名 成员引用过的组
}

// parseCPGLine custom_proxy_group= 行拆分：组名、类型、类型位之后的 token。
// 组名切分与 parseProxyGroupName 共用同一实现（含罕见前导反引号形态），防两套解析漂移
func parseCPGLine(line string) (name, typ string, members []string, ok bool) {
	name, ok = parseProxyGroupName(line)
	if !ok {
		return "", "", nil, false
	}
	rest := strings.TrimPrefix(line[len("custom_proxy_group="):], "`")
	toks := strings.Split(rest[len(name)+1:], "`")
	return name, toks[0], toks[1:], true
}

// sectionName 段头解析：行以 [ 开头且含 ] → 第一个 ] 前的段名（trim 后）。
// 带尾注释的段头（`[custom] ; comment`）与 `[custom]` 等价；整行精确匹配会把
// 带注释段头漏判成普通行，整份模板解析为空、exclude 静默失效
func sectionName(line string) (string, bool) {
	if !strings.HasPrefix(line, "[") {
		return "", false
	}
	end := strings.IndexByte(line, ']')
	if end < 0 {
		return "", false
	}
	return strings.TrimSpace(line[1:end]), true
}

// analyzeTemplate 解析 ini：组清单按模板原序；规则引用扫全文件（ruleset= 行也可能
// 出现在 [custom] 之外），组定义只认 [custom] 段（与既有派生口径一致）
func analyzeTemplate(src []byte) templateInfo {
	info := templateInfo{
		byName:        map[string]int{},
		ruleTargets:   map[string]bool{},
		inlineTargets: map[string]bool{},
		referenced:    map[string]bool{},
	}
	inCustom := false
	for _, raw := range strings.Split(string(src), "\n") {
		t := strings.TrimSpace(raw)
		if name, ok := sectionName(t); ok {
			inCustom = name == "custom"
			continue
		}
		if strings.HasPrefix(t, "ruleset=") {
			target, source, hasSource := strings.Cut(t[len("ruleset="):], ",")
			name := strings.TrimSpace(target)
			if name == "" {
				continue
			}
			info.ruleTargets[name] = true
			if hasSource {
				s := strings.TrimSpace(source)
				// 内联规则（[]FINAL / []GEOIP,CN / 裸 FINAL）非 URL、非快照本地路径
				if strings.HasPrefix(s, "[]") || strings.EqualFold(s, "FINAL") {
					info.inlineTargets[name] = true
				}
			}
			continue
		}
		if !inCustom {
			continue
		}
		name, typ, members, ok := parseCPGLine(t)
		if !ok {
			continue
		}
		info.byName[name] = len(info.groups)
		info.groups = append(info.groups, templateGroup{Name: name, Type: typ, Members: members})
		for _, m := range members {
			if strings.HasPrefix(m, "[]") && len(m) > 2 {
				info.referenced[m[2:]] = true
			}
		}
	}
	return info
}

// isIntervalToken url-test 行尾 interval,,tolerance（`300,,50` 形态）
func isIntervalToken(tok string) bool {
	if tok == "" || !strings.ContainsRune(tok, ',') {
		return false
	}
	for _, r := range tok {
		if (r < '0' || r > '9') && r != ',' {
			return false
		}
	}
	return true
}

// isRegexToken 含正则元字符的成员位 token（`.*`、`(港|HK)` 等）；`[]` 引用已在调用前排除
func isRegexToken(tok string) bool {
	return strings.ContainsAny(tok, `.*+?()[]{}|^$\`)
}

// firstMember 被删组首成员（规则回退分派）：首个 `[]X` 引用或首个非正则裸成员；
// 正则段、测速 URL、间隔段不算；无首成员返回 ""
func (g templateGroup) firstMember() string {
	for _, tok := range g.Members {
		if tok == "" {
			continue
		}
		if strings.HasPrefix(tok, "[]") {
			return tok[2:]
		}
		if strings.Contains(tok, "://") || isIntervalToken(tok) || isRegexToken(tok) {
			continue
		}
		return tok
	}
	return ""
}

// regexToken 组的节点匹配正则（groupHits 用）：成员位里首个非 `[]` 开头的 token
// （url-test/fallback/load-balance 的首段；带正则段的 select 组如 `🎥 奈飞节点`、`.*`）；
// 测速 URL 与间隔段不算。无则 ""
func (g templateGroup) regexToken() string {
	for _, tok := range g.Members {
		if tok == "" || strings.HasPrefix(tok, "[]") {
			continue
		}
		if strings.Contains(tok, "://") || isIntervalToken(tok) {
			continue
		}
		return tok
	}
	return ""
}

// locked 模板组不可删判定（设计裁决 3，2026-10-30 修正版）：路由骨架与安全组恒锁——
//   - 规则回退目标常量（🚀 节点选择 / 🎯 全球直连）恒锁：规则改写后目标必须存在
//   - 承载内联规则的组（ruleset=X,[]FINAL / []GEOIP）——兜底路由组
//   - 首成员 REJECT 的拦截/净化安全组
//   - 被其他组以 []组名 成员引用的骨架组
//
// 自动策略组、地区组、业务组（含被 ruleset 指向的 💬 Ai平台 / 🌍 国外媒体）均可删。
// 初稿「被任意 ruleset= 指向即锁定」实测会把业务组全部锁死（default 11 组锁 9 个），
// 已按上述启发式修正，三档实测结果与设计裁决名单一致。
func (t templateInfo) locked(name string) bool {
	if name == fallbackProxy || name == fallbackDirect {
		return true
	}
	if !t.ruleTargets[name] {
		return false
	}
	if t.inlineTargets[name] || t.referenced[name] {
		return true
	}
	if i, ok := t.byName[name]; ok {
		return t.groups[i].firstMember() == "REJECT"
	}
	return false
}

// memberRefBoundary 成员引用边界：后随反引号 / 行尾 / CRLF 回车。空格不算边界——
// 组名 X 是另一组名 X Y 的前缀时，`[]X Y 是完整引用，按空格截断会把引用误摘成裸成员 Y
func memberRefBoundary(line string, end int) bool {
	return end == len(line) || line[end] == '`' || line[end] == '\r'
}

// containsMemberRef line 中是否存在成员引用 `[]组名（带边界）
func containsMemberRef(line, name string) bool {
	token := "`[]" + name
	for i := 0; ; {
		idx := strings.Index(line[i:], token)
		if idx < 0 {
			return false
		}
		idx += i
		if memberRefBoundary(line, idx+len(token)) {
			return true
		}
		i = idx + 1
	}
}

// stripMemberRef 摘除成员引用 `[]组名（**连前导反引号**整体摘除，防止留空成员——
// gate3 闸门发现 3）；非边界命中（前缀同名组）跳过
func stripMemberRef(line, name string) string {
	token := "`[]" + name
	var b strings.Builder
	i := 0
	for {
		idx := strings.Index(line[i:], token)
		if idx < 0 {
			b.WriteString(line[i:])
			return b.String()
		}
		idx += i
		end := idx + len(token)
		if memberRefBoundary(line, end) {
			b.WriteString(line[i:idx])
			i = end
			continue
		}
		b.WriteString(line[i : idx+1])
		i = idx + 1
	}
}

// fallbackRuleTarget 被删组规则回退目标：首成员属直连类 → 🎯 全球直连（国内媒体类流量
// 不绕代理），否则 → 🚀 节点选择；无首成员 → 🚀 节点选择
func fallbackRuleTarget(info templateInfo, name string) string {
	if i, ok := info.byName[name]; ok {
		if m := info.groups[i].firstMember(); directClassMembers[m] {
			return fallbackDirect
		}
	}
	return fallbackProxy
}

// pruneTemplateGroups 行级删减（契约第 2b 步）：删被删组组行 → 其余行摘成员引用 →
// ruleset 目标回退。excluded 为有效删减集合（exclude ∩ 模板组名单 − locked）
func pruneTemplateGroups(lines []string, info templateInfo, excluded map[string]bool) []string {
	out := make([]string, 0, len(lines))
	for _, raw := range lines {
		if name, ok := parseProxyGroupName(strings.TrimSpace(raw)); ok && excluded[name] {
			continue // 删组行
		}
		line := raw
		for name := range excluded {
			line = stripMemberRef(line, name)
		}
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "ruleset=") {
			target, tail, hasTail := strings.Cut(t[len("ruleset="):], ",")
			if excluded[strings.TrimSpace(target)] {
				line = "ruleset=" + fallbackRuleTarget(info, strings.TrimSpace(target))
				if hasTail {
					line += "," + tail
				}
			}
		}
		out = append(out, line)
	}
	return out
}

// selfCheckPruned 派生自检（fail-closed，契约第 2b 步末 + 设计裁决 17）：被删组名不得
// 再以组定义、ruleset 目标或成员引用形式残留；另发生规则改写时，回退目标组
// （🚀 节点选择 / 🎯 全球直连）必须存在于产物（上游模板改名防护）。命中 → 上层
// 502 derive_error（gate3 T2：引擎不做兜底）
func selfCheckPruned(out []byte, removed, requiredTargets map[string]bool) error {
	defined := map[string]bool{}
	for _, raw := range strings.Split(string(out), "\n") {
		t := strings.TrimSpace(raw)
		if name, ok := parseProxyGroupName(t); ok {
			defined[name] = true
			if removed[name] {
				return fmt.Errorf("自检失败：被删组 %q 仍作为 custom_proxy_group= 定义残留", name)
			}
		}
		if strings.HasPrefix(t, "ruleset=") {
			target, _, _ := strings.Cut(t[len("ruleset="):], ",")
			if name := strings.TrimSpace(target); removed[name] {
				return fmt.Errorf("自检失败：被删组 %q 仍作为 ruleset 目标残留", name)
			}
		}
		for name := range removed {
			if containsMemberRef(raw, name) {
				return fmt.Errorf("自检失败：被删组 %q 仍作为成员引用残留", name)
			}
		}
	}
	for name := range requiredTargets {
		if !defined[name] {
			return fmt.Errorf("自检失败：规则回退目标组 %q 在产物中不存在（上游模板改名防护）", name)
		}
	}
	return nil
}

// groupHitsFromInfo 模板组节点命中数（probe 响应 groupHits）：只对带正则 token 的组
// 计数，正则编译失败或无正则的组不出现（契约 probe 模式节）
func groupHitsFromInfo(info templateInfo, nodes []string) map[string]int {
	hits := map[string]int{}
	for _, g := range info.groups {
		pat := g.regexToken()
		if pat == "" {
			continue
		}
		re, err := regexp.Compile(pat)
		if err != nil {
			continue
		}
		n := 0
		for _, node := range nodes {
			if re.MatchString(node) {
				n++
			}
		}
		hits[g.Name] = n
	}
	return hits
}

// templateInfoFor 读该档当前生效快照 ini 并解析；能力位 false / 无改写产物 / 读失败 → false
func (s *server) templateInfoFor(p presetEntry) (templateInfo, bool) {
	if !s.presetCustomGroups(p) {
		return templateInfo{}, false
	}
	path, ok := s.snap.rewrittenPath(p)
	if !ok {
		return templateInfo{}, false
	}
	src, err := os.ReadFile(path)
	if err != nil {
		return templateInfo{}, false
	}
	return analyzeTemplate(src), true
}

// presetGroups GET /api/clients presets[].groups：模板原序组清单 + locked 标记；
// 豁免档 / 快照不可用 → []（契约 GET /api/clients 末条 bullet）
func (s *server) presetGroups(p presetEntry) []presetGroupInfo {
	out := []presetGroupInfo{}
	info, ok := s.templateInfoFor(p)
	if !ok {
		return out
	}
	for _, g := range info.groups {
		out = append(out, presetGroupInfo{Name: g.Name, Locked: info.locked(g.Name)})
	}
	return out
}

// probeGroupHits probe 响应 groupHits：能力位 false / 无本地 ini → {}（契约 probe 模式节）
func (s *server) probeGroupHits(p presetEntry, nodes []string) map[string]int {
	info, ok := s.templateInfoFor(p)
	if !ok {
		return map[string]int{}
	}
	return groupHitsFromInfo(info, nodes)
}
