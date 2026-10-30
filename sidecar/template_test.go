// 模板群组删减单测：模板组解析、locked 判定、成员引用摘除边界、规则回退、派生自检
// （fail-closed 负面对照）与 probe 命中计数。契约「模板群组删减」/「拉取期派生」第 2b 步。
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("读 fixture %s: %v", name, err)
	}
	return b
}

// 三档真实模板：组清单按模板原序解析（组数 / 类型 / 成员），ruleset 引用关系齐备
func TestAnalyzeTemplateRealFixtures(t *testing.T) {
	cases := []struct {
		file   string
		groups int
	}{
		{"ACL4SSR_Online.ini", 11},
		{"ACL4SSR_Online_Full_MultiMode.ini", 31},
		{"ACL4SSR_Online_Full_AdblockPlus.ini", 33},
	}
	for _, c := range cases {
		info := analyzeTemplate(readFixture(t, c.file))
		if len(info.groups) != c.groups {
			t.Errorf("%s：组数 = %d，want %d", c.file, len(info.groups), c.groups)
		}
		if info.groups[0].Name != "🚀 节点选择" || info.groups[0].Type != "select" {
			t.Errorf("%s：首组 = %+v（模板原序首组应 🚀 节点选择 select）", c.file, info.groups[0])
		}
		// 类型解析：url-test / fallback / load-balance 至少各一（multimode/adblock）
		types := map[string]bool{}
		for _, g := range info.groups {
			types[g.Type] = true
		}
		if c.file != "ACL4SSR_Online.ini" {
			for _, want := range []string{"url-test", "fallback", "load-balance"} {
				if !types[want] {
					t.Errorf("%s：缺类型 %s（types=%v）", c.file, want, types)
				}
			}
		}
		if len(info.ruleTargets) == 0 || len(info.referenced) == 0 {
			t.Errorf("%s：规则引用关系未解析（ruleTargets=%d referenced=%d）", c.file, len(info.ruleTargets), len(info.referenced))
		}
	}
}

// 段头带尾注释（`[custom] ; comment`）：组解析、exclude、派生均正常——整行精确匹配
// 会把带注释段头漏判成普通行，整份模板解析为空、exclude 静默失效
func TestAnalyzeTemplateSectionHeaderWithComment(t *testing.T) {
	src := []byte("[custom] ; 尾注释\n" +
		"custom_proxy_group=🚀 节点选择`select`[]♻️ 自动选择\n" +
		"custom_proxy_group=♻️ 自动选择`url-test`.*`http://x`300,,50\n" +
		"custom_proxy_group=🌍 国外媒体`select`[]🚀 节点选择`.*\n" +
		"ruleset=🚀 节点选择,[]FINAL\n")
	info := analyzeTemplate(src)
	if len(info.groups) != 3 {
		t.Fatalf("带注释段头应解析出 3 组，得 %d（%+v）", len(info.groups), info.groups)
	}
	out, err := deriveINI(src, []nodeGroup{{Name: "专线", Keywords: []string{"专线"}, URLTest: false}}, []string{"🌍 国外媒体"})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if strings.Contains(s, "custom_proxy_group=🌍 国外媒体`") {
		t.Error("exclude 应生效（组行移除）")
	}
	if !strings.Contains(s, "custom_proxy_group=专线`select`(?=.*专线)") {
		t.Error("派生组行应插入（appendGroups 段头扫描须识别带注释 [custom]）")
	}
}

// 成员 token 解析：[]组引用与正则 token 区分（url-test 首段是正则、select 带正则段）
func TestTemplateGroupMemberTokens(t *testing.T) {
	info := analyzeTemplate(readFixture(t, "ACL4SSR_Online_Full_MultiMode.ini"))
	byName := map[string]templateGroup{}
	for _, g := range info.groups {
		byName[g.Name] = g
	}
	cases := []struct {
		name        string
		firstMember string
		regexToken  string
	}{
		{"🚀 节点选择", "♻️ 自动选择", ""}, // multimode 主组纯 [] 成员（无 .* 正则段）
		{"🎯 全球直连", "DIRECT", ""},  // 纯 [] 成员，无正则段 → 不进 groupHits
		{"♻️ 自动选择", "", ".*"},
		{"🇰🇷 韩国节点", "", "(KR|Korea|KOR|首尔|韩|韓)"},
		{"🎥 奈飞节点", "", "(NF|奈飞|解锁|Netflix|NETFLIX|Media)"},
		{"🎶 网易音乐", "DIRECT", "(网易|音乐|解锁|Music|NetEase)"},
		{"🌏 国内媒体", "DIRECT", ""},
	}
	for _, c := range cases {
		g, ok := byName[c.name]
		if !ok {
			t.Errorf("缺组 %s", c.name)
			continue
		}
		if got := g.firstMember(); got != c.firstMember {
			t.Errorf("%s firstMember = %q，want %q", c.name, got, c.firstMember)
		}
		if got := g.regexToken(); got != c.regexToken {
			t.Errorf("%s regexToken = %q，want %q", c.name, got, c.regexToken)
		}
	}
}

// legacy 前导反引号形态（`custom_proxy_group=`T`select`[]DIRECT`）解析
func TestParseCPGLineLegacyLeadingBacktick(t *testing.T) {
	name, typ, members, ok := parseCPGLine("custom_proxy_group=`T`select`[]DIRECT`[]X")
	if !ok || name != "T" || typ != "select" || len(members) != 2 || members[0] != "[]DIRECT" || members[1] != "[]X" {
		t.Errorf("解析 = %q %q %v %v", name, typ, members, ok)
	}
	if _, _, _, ok := parseCPGLine("ruleset=foo"); ok {
		t.Error("非组行应不解析")
	}
}

// 两套组行解析不得漂移：parseCPGLine 的组名与 parseProxyGroupName 一致（含前导反引号形态）
func TestParseCPGLineAgreesWithProxyGroupName(t *testing.T) {
	for _, line := range []string{
		"custom_proxy_group=🚀 节点选择`select`[]DIRECT`.*",
		"custom_proxy_group=`T`select`[]DIRECT`[]X",
		"custom_proxy_group=A`url-test`(KR)`http://x`300,,50",
	} {
		name, _, _, ok := parseCPGLine(line)
		name2, ok2 := parseProxyGroupName(line)
		if !ok || !ok2 {
			t.Errorf("%q：解析应成功（parseCPGLine=%v parseProxyGroupName=%v）", line, ok, ok2)
			continue
		}
		if name != name2 {
			t.Errorf("%q：组名不一致 parseCPGLine=%q parseProxyGroupName=%q", line, name, name2)
		}
	}
}

// locked 判定（设计裁决锁定名单）：三档实测 = 节点选择/全球直连/拦截净化类/漏网之鱼锁定；
// 自动组、地区组、业务组（国外媒体/国内媒体/Ai平台/…）可删
func TestTemplateLockedDesignList(t *testing.T) {
	lockedOf := func(file string) map[string]bool {
		info := analyzeTemplate(readFixture(t, file))
		out := map[string]bool{}
		for _, g := range info.groups {
			if info.locked(g.Name) {
				out[g.Name] = true
			}
		}
		return out
	}

	def := lockedOf("ACL4SSR_Online.ini")
	for _, name := range []string{"🚀 节点选择", "🎯 全球直连", "🛑 全球拦截", "🍃 应用净化", "🐟 漏网之鱼"} {
		if !def[name] {
			t.Errorf("default：%s 应 locked（%v）", name, def)
		}
	}
	for _, name := range []string{"♻️ 自动选择", "🌍 国外媒体", "📲 电报信息", "Ⓜ️ 微软服务", "🍎 苹果服务", "📢 谷歌FCM"} {
		if def[name] {
			t.Errorf("default：%s 不应 locked", name)
		}
	}

	multi := lockedOf("ACL4SSR_Online_Full_MultiMode.ini")
	for _, name := range []string{"🚀 节点选择", "🎯 全球直连", "🛑 广告拦截", "🍃 应用净化", "🐟 漏网之鱼"} {
		if !multi[name] {
			t.Errorf("multimode：%s 应 locked（%v）", name, multi)
		}
	}
	for _, name := range []string{"💬 Ai平台", "🌍 国外媒体", "🌏 国内媒体", "🇰🇷 韩国节点", "🚀 手动切换", "🎮 游戏平台", "📺 哔哩哔哩"} {
		if multi[name] {
			t.Errorf("multimode：%s 不应 locked（业务/地区/自动组均可删）", name)
		}
	}

	ad := lockedOf("ACL4SSR_Online_Full_AdblockPlus.ini")
	for _, name := range []string{"🆎 AdBlock", "🛡️ 隐私防护", "🛑 广告拦截"} {
		if !ad[name] {
			t.Errorf("adblockplus：%s 应 locked（%v）", name, ad)
		}
	}
}

// groupHits：带正则 token 的组计数；无正则组（全球直连）与编译失败组不出现
func TestGroupHitsFromInfo(t *testing.T) {
	info := analyzeTemplate(readFixture(t, "ACL4SSR_Online.ini"))
	nodes := []string{"香港专线", "日本家宽", "港人家宽"}
	hits := groupHitsFromInfo(info, nodes)
	if hits["♻️ 自动选择"] != 3 {
		t.Errorf("自动选择（.*）命中 = %d，want 3", hits["♻️ 自动选择"])
	}
	if hits["🚀 节点选择"] != 3 {
		t.Errorf("节点选择（.*）命中 = %d，want 3", hits["🚀 节点选择"])
	}
	if _, ok := hits["🎯 全球直连"]; ok {
		t.Errorf("全球直连无正则段不应出现在 groupHits：%v", hits)
	}

	// 编译失败的正则不出现；零命中也出现（计数 0）
	src := "[custom]\n" +
		"custom_proxy_group=坏正则`select`((\n" +
		"custom_proxy_group=零命中`url-test`(KR|韩国)`http://x`300,,50\n"
	hits2 := groupHitsFromInfo(analyzeTemplate([]byte(src)), nodes)
	if _, ok := hits2["坏正则"]; ok {
		t.Errorf("编译失败的正则应被跳过：%v", hits2)
	}
	if n, ok := hits2["零命中"]; !ok || n != 0 {
		t.Errorf("零命中组应出现且为 0：%v", hits2)
	}
}

// 成员引用摘除边界：只摘完整引用（连前导反引号）；前缀同名组（名②）与「X 是 X Y 前缀」
// 两形态均不误伤；CRLF 行尾算边界
func TestStripMemberRef(t *testing.T) {
	cases := []struct {
		line, name, want string
	}{
		{"a`[]X`[]Y`.*", "X", "a`[]Y`.*"},
		{"a`[]X`[]Y", "Y", "a`[]X"},
		{"a`[]X②`[]X`.*", "X", "a`[]X②`.*"}, // 前缀同名不误伤
		{"a`[]X Y`[]Z", "X", "a`[]X Y`[]Z"}, // 「X Y」是完整引用，空格不算边界
		{"a`[]XY`.*", "X", "a`[]XY`.*"},     // 非边界不误伤
		{"a`[]B\r", "B", "a\r"},             // CRLF 行尾算边界
	}
	for _, c := range cases {
		if got := stripMemberRef(c.line, c.name); got != c.want {
			t.Errorf("stripMemberRef(%q, %q) = %q，want %q", c.line, c.name, got, c.want)
		}
	}
	if !containsMemberRef("a`[]X`b", "X") || containsMemberRef("a`[]X②`b", "X") {
		t.Error("containsMemberRef 边界判定错")
	}
	if containsMemberRef("a`[]X Y`b", "X") {
		t.Error("`[]X Y 不应判为对 X 的引用")
	}
	if !containsMemberRef("a`[]B\r", "B") {
		t.Error("CRLF 行尾应识别为引用边界")
	}
}

// 前缀同名组并存（X 与 X Y）：exclude X 只摘 `[]X，完整引用 `[]X Y 保留、自检通过；
// 与模板组 X Y 定义行互不影响
func TestDeriveExcludePrefixGroupName(t *testing.T) {
	src := []byte("[custom]\n" +
		"custom_proxy_group=X`select`[]DIRECT\n" +
		"custom_proxy_group=X Y`select`[]DIRECT\n" +
		"custom_proxy_group=Main`select`[]X Y`[]X`.*\n")
	out, err := deriveINI(src, nil, []string{"X"})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if hasLine(s, "custom_proxy_group=X`select`[]DIRECT") {
		t.Error("被删组 X 定义行应移除")
	}
	if !hasLine(s, "custom_proxy_group=X Y`select`[]DIRECT") {
		t.Error("组 X Y 定义行应保留")
	}
	if !hasLine(s, "custom_proxy_group=Main`select`[]X Y`.*") {
		t.Errorf("`[]X Y 是完整引用，不应被摘成裸成员 Y:\n%s", s)
	}
}

// CRLF 行：`[]B\r 的行尾回车算引用边界（可摘除、自检可检出残留）
func TestDeriveExcludeCRLF(t *testing.T) {
	src := []byte("[custom]\r\n" +
		"custom_proxy_group=B`url-test`(KR)`http://x`300,,50\r\n" +
		"custom_proxy_group=A`select`[]B`[]DIRECT\r\n")
	out, err := deriveINI(src, nil, []string{"B"})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if strings.Contains(s, "custom_proxy_group=B`") {
		t.Error("被删组定义行应移除")
	}
	if !strings.Contains(s, "custom_proxy_group=A`select`[]DIRECT\r") {
		t.Errorf("CRLF 行尾 `[]B\\r 应被摘除:\n%q", s)
	}
	// 漏摘场景（未经 prune 的残留引用，行尾只有 \r）自检必须报错
	if err := selfCheckPruned([]byte("[custom]\r\ncustom_proxy_group=A`select`[]B\r\n"), map[string]bool{"B": true}, nil); err == nil {
		t.Error("CRLF 行尾残留引用未被自检检出")
	}
}

// 派生自检负面对照（gate3 T2：引擎不兜底）：定义 / ruleset 目标 / 成员引用三种残留
// 任一命中即报错；规则改写后回退目标缺失也报错（裁决 17）；干净产物通过
func TestSelfCheckPrunedNegative(t *testing.T) {
	removed := map[string]bool{"🇰🇷 韩国节点": true}
	bad := [][]string{
		{"[custom]", "custom_proxy_group=🇰🇷 韩国节点`url-test`(KR)`http://x`300,,50"},
		{"[custom]", "ruleset=🇰🇷 韩国节点,https://x/a.list"},
		{"[custom]", "custom_proxy_group=X`select`[]🇰🇷 韩国节点`.*"},
		{"[custom]", "custom_proxy_group=X`select`[]🇰🇷 韩国节点"},
	}
	for _, lines := range bad {
		if err := selfCheckPruned([]byte(strings.Join(lines, "\n")), removed, nil); err == nil {
			t.Errorf("残留未检出：%v", lines)
		}
	}
	if err := selfCheckPruned([]byte("[custom]\ncustom_proxy_group=X`select`[]🇰🇷 韩国节点②`.*"), removed, nil); err != nil {
		t.Errorf("前缀同名组（②）不应误报：%v", err)
	}
	if err := selfCheckPruned([]byte("[custom]\ncustom_proxy_group=X`select`[]DIRECT`.*"), removed, nil); err != nil {
		t.Errorf("干净产物不应报错：%v", err)
	}
	// 裁决 17：发生规则改写时回退目标组必须存在于产物（上游模板改名防护）
	if err := selfCheckPruned([]byte("[custom]\ncustom_proxy_group=X`select`[]DIRECT"), nil, map[string]bool{fallbackProxy: true}); err == nil {
		t.Error("回退目标缺失应报错")
	}
	if err := selfCheckPruned([]byte("[custom]\ncustom_proxy_group=🚀 节点选择`select`[]DIRECT"), nil, map[string]bool{fallbackProxy: true}); err != nil {
		t.Errorf("回退目标存在不应报错: %v", err)
	}
}

// 回退目标常量恒锁：即使该组没有任何 ruleset 行也锁定（规则改写后目标必须存在）
func TestTemplateLockedFallbackConstant(t *testing.T) {
	info := analyzeTemplate([]byte("[custom]\n" +
		"custom_proxy_group=🚀 节点选择`select`[]DIRECT\n" +
		"custom_proxy_group=🎯 全球直连`select`[]DIRECT\n" +
		"custom_proxy_group=T`select`.*\n"))
	if !info.locked("🚀 节点选择") || !info.locked("🎯 全球直连") {
		t.Error("回退目标常量应恒锁（无 ruleset 行也不例外）")
	}
	if info.locked("T") {
		t.Error("无规则引用的普通组不应锁定")
	}
}

// exclude 归一化：trim / 去重 / 升序；项数、长度、反引号、控制字符、空名均拒
func TestNormalizeExclude(t *testing.T) {
	got, err := normalizeExclude([]string{" 乙 ", "甲", "乙", "甲 "})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, "|") != "乙|甲" {
		t.Errorf("归一化 = %v，应去重且按字节升序（乙 < 甲）", got)
	}

	rune65 := strings.Repeat("组", 65)
	var many []string
	for i := 0; i < 65; i++ {
		many = append(many, "组")
	}
	bad := [][]string{
		{"  "},
		{""},
		{"a`b"},
		{"a\x00b"},
		{"a\x7fb"},
		{rune65},
		many,
	}
	for _, b := range bad {
		if _, err := normalizeExclude(b); err == nil {
			t.Errorf("应拒绝：%q", b)
		}
	}
	// 恰 64 项 / 恰 64 rune 通过
	ok64 := make([]string, 64)
	for i := range ok64 {
		ok64[i] = string(rune('a' + i%26))
	}
	if _, err := normalizeExclude(ok64); err != nil {
		t.Errorf("恰 64 项应通过: %v", err)
	}
	if _, err := normalizeExclude([]string{strings.Repeat("组", 64)}); err != nil {
		t.Errorf("恰 64 rune 应通过: %v", err)
	}
}

// canonicalExcludeJSON：空 "[]"、无空白、升序去重后序列化（指纹 v3 规范化件）
func TestCanonicalExcludeJSON(t *testing.T) {
	if got := canonicalExcludeJSON(nil); got != "[]" {
		t.Errorf("空 = %q，want []", got)
	}
	if got := canonicalExcludeJSON([]string{"a", "b"}); got != `["a","b"]` {
		t.Errorf("序列化 = %q", got)
	}
	ex, err := parseExcludeJSON(`["b","a"]`)
	if err != nil || len(ex) != 2 {
		t.Fatalf("parseExcludeJSON = %v, %v", ex, err)
	}
	if ex, err := parseExcludeJSON(""); err != nil || ex != nil {
		t.Errorf("空串应还原为 nil: %v %v", ex, err)
	}
}
