// 派生函数表驱动单测（契约「GET /s/{code} 拉取期派生」；单测口径 R3-6：组行无条件
// 写入派生 ini——零命中不特判，引擎原生退化仅 DIRECT 不在单测断言）。
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixtureACL4SSR 读仓库内置真实模板（image/rules-snapshot 同源拷贝；build-sidecar
// 只送 sidecar/ 进容器，fixture 必须在树内）
func fixtureACL4SSR(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "ACL4SSR_Online.ini"))
	if err != nil {
		t.Fatalf("读 fixture: %v", err)
	}
	return b
}

// 真实模板：行格式两态 + 插入位（既有 custom_proxy_group= 行块之后、enable_rule_generator
// 之前）+ 模板组行逐行保留
func TestDeriveGroupsINIRealFixture(t *testing.T) {
	src := fixtureACL4SSR(t)
	groups := []nodeGroup{
		{Name: "专线", Keywords: []string{"专线"}, URLTest: false},
		{Name: "家宽", Keywords: []string{"家宽"}, URLTest: true},
	}
	out, err := deriveINI(src, groups, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	wantSelect := "custom_proxy_group=专线`select`(?=.*专线)"
	wantURLTest := "custom_proxy_group=家宽`url-test`(?=.*家宽)`http://www.gstatic.com/generate_204`300,,50"
	if !strings.Contains(s, wantSelect) {
		t.Errorf("select 行缺失或格式错:\n%s", wantSelect)
	}
	if !strings.Contains(s, wantURLTest) {
		t.Errorf("url-test 行缺失或格式错:\n%s", wantURLTest)
	}
	// 模板原有 11 行组逐行原样保留
	for _, line := range strings.Split(string(src), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "custom_proxy_group=") && !strings.Contains(s, line) {
			t.Errorf("模板组行被改动: %s", line)
		}
	}
	lines := strings.Split(s, "\n")
	lastCPG, firstGen, erg := -1, -1, -1
	for i, ln := range lines {
		tl := strings.TrimSpace(ln)
		if strings.HasPrefix(tl, "custom_proxy_group=") {
			lastCPG = i
		}
		if tl == wantSelect && firstGen < 0 {
			firstGen = i
		}
		if strings.TrimSpace(tl) == "enable_rule_generator=true" {
			erg = i
		}
	}
	if lastCPG < firstGen || firstGen < 0 || erg < lastCPG {
		t.Errorf("插入位错：lastCPG=%d firstGen=%d ERG=%d（应 CPG 块后、ERG 前）", lastCPG, firstGen, erg)
	}
	// 除插入行外逐行等价（其余内容零改动）
	if len(lines) != len(strings.Split(string(src), "\n"))+2 {
		t.Errorf("行数 = %d，应恰多 2 行", len(lines))
	}
}

// QuoteMeta：元字符关键词转义（闸门 4b 实证不转义会误命中 IEPL 1x0）
func TestDeriveGroupsINIQuoteMeta(t *testing.T) {
	out, err := deriveINI(fixtureACL4SSR(t), []nodeGroup{{Name: "IEPL", Keywords: []string{"IEPL 1.0"}, URLTest: false}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "custom_proxy_group=IEPL`select`(?=.*IEPL 1\\.0)") {
		t.Errorf("QuoteMeta 转义缺失:\n%s", string(out))
	}
}

// 撞名：模板组名单 → 后缀带圈数字单级；模板已占 ② → 多级递增；同批已生成名同样回避
func TestDeriveGroupsINICollision(t *testing.T) {
	out, err := deriveINI(fixtureACL4SSR(t), []nodeGroup{{Name: "🚀 节点选择", Keywords: []string{"专线"}, URLTest: false}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "custom_proxy_group=🚀 节点选择②`select`(?=.*专线)") {
		t.Errorf("撞名应后缀②:\n%s", string(out))
	}

	// 模板占 ②：同名第二撞 → ③
	tpl := "[custom]\n" +
		"custom_proxy_group=`节点选择`select`[]DIRECT`\n" +
		"custom_proxy_group=`节点选择②`select`[]DIRECT`\n" +
		"custom_proxy_group=`节点选择③`select`[]DIRECT`\n" +
		"enable_rule_generator=true\n"
	out, err = deriveINI([]byte(tpl), []nodeGroup{{Name: "节点选择", Keywords: []string{"k"}, URLTest: false}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "custom_proxy_group=节点选择④`select`(?=.*k)") {
		t.Errorf("②③已占应取④:\n%s", string(out))
	}

	// 同批已生成名回避：组 [A, A]（校验层禁重名，但派生层自守）→ 第二个取 A②
	tpl2 := "[custom]\ncustom_proxy_group=`T`select`[]DIRECT`\nenable_rule_generator=true\n"
	out, err = deriveINI([]byte(tpl2), []nodeGroup{
		{Name: "A", Keywords: []string{"k1"}, URLTest: false},
		{Name: "A", Keywords: []string{"k2"}, URLTest: false},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "custom_proxy_group=A`select`(?=.*k1)") || !strings.Contains(string(out), "custom_proxy_group=A②`select`(?=.*k2)") {
		t.Errorf("同批撞名应后缀②:\n%s", string(out))
	}
}

// 撞穿全表：模板把 基名+②..⑳ 全占 → error（上层 502 derive_error）
func TestDeriveGroupsINICollisionExhausted(t *testing.T) {
	var b strings.Builder
	b.WriteString("[custom]\n")
	b.WriteString("custom_proxy_group=`X`select`[]DIRECT`\n")
	for r := rune(0x2461); r <= 0x2473; r++ {
		b.WriteString("custom_proxy_group=`X" + string(r) + "`select`[]DIRECT`\n")
	}
	b.WriteString("enable_rule_generator=true\n")
	if _, err := deriveINI([]byte(b.String()), []nodeGroup{{Name: "X", Keywords: []string{"k"}, URLTest: false}}, nil); err == nil {
		t.Error("撞穿全表应报错")
	}
}

// 无锚点 fail-closed：无 [custom] 段 / 有段无既有组行 → error（不发明插入位）
func TestDeriveGroupsININoAnchor(t *testing.T) {
	cases := []struct {
		name   string
		src    string
		groups []nodeGroup
	}{
		{"无 [custom] 段", "; no sections here\nruleset=whatever\n", []nodeGroup{{Name: "A", Keywords: []string{"k"}}}},
		{"有段无组行", "[custom]\nenable_rule_generator=true\n", []nodeGroup{{Name: "A", Keywords: []string{"k"}}}},
	}
	for _, c := range cases {
		if _, err := deriveINI([]byte(c.src), c.groups, nil); err == nil {
			t.Errorf("%s：应 fail-closed 报错", c.name)
		}
	}
}

// 多组顺序写入；零命中关键词组行照写（无条件写入，R3-6）
func TestDeriveGroupsINIMultiAndZeroHit(t *testing.T) {
	tpl := "[custom]\ncustom_proxy_group=`T`select`[]DIRECT`\nenable_rule_generator=true\n"
	groups := []nodeGroup{
		{Name: "一", Keywords: []string{"不存在的词xyz"}, URLTest: false},
		{Name: "二", Keywords: []string{"二"}, URLTest: true},
		{Name: "三", Keywords: []string{"三"}, URLTest: false},
	}
	out, err := deriveINI([]byte(tpl), groups, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{
		"custom_proxy_group=一`select`(?=.*不存在的词xyz)",
		"custom_proxy_group=二`url-test`(?=.*二)`http://www.gstatic.com/generate_204`300,,50",
		"custom_proxy_group=三`select`(?=.*三)",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("缺行:\n%s", want)
		}
	}
	i1, i2, i3 := strings.Index(s, "custom_proxy_group=一`"), strings.Index(s, "custom_proxy_group=二`"), strings.Index(s, "custom_proxy_group=三`")
	if !(i1 < i2 && i2 < i3) {
		t.Errorf("组行应按请求顺序：i1=%d i2=%d i3=%d", i1, i2, i3)
	}
}

// hasLine 整行精确匹配（行级断言，防子串误命中）
func hasLine(s, want string) bool {
	for _, ln := range strings.Split(s, "\n") {
		if ln == want {
			return true
		}
	}
	return false
}

// 主组接线（用户裁决 B，2026-10-28）：三档真实模板首个 custom_proxy_group= 行（主组
// 「🚀 节点选择」）行尾追加 []组引用——尾段 .*（ACL4SSR_Online）与 []DIRECT
// （MultiMode/AdblockPlus）两形态均行尾追加；整行 = 原主组行 + 追加段（原成员无损）
// 全 select 组接线（用户裁决 A）：每个 select 类模板组行尾按序追加组引用、原成员
// 无损；url-test 组行不注入（保持原样）
func TestDeriveGroupsINISelectWiring(t *testing.T) {
	groups := []nodeGroup{
		{Name: "专线组", Keywords: []string{"专线"}, URLTest: false},
		{Name: "家宽组", Keywords: []string{"家宽"}, URLTest: true},
	}
	for _, fx := range []string{
		"ACL4SSR_Online.ini",
		"ACL4SSR_Online_Full_MultiMode.ini",
		"ACL4SSR_Online_Full_AdblockPlus.ini",
	} {
		b, err := os.ReadFile(filepath.Join("testdata", fx))
		if err != nil {
			t.Fatalf("读 fixture %s: %v", fx, err)
		}
		out, err := deriveINI(b, groups, nil)
		if err != nil {
			t.Fatalf("%s: %v", fx, err)
		}
		s := string(out)
		var selLines, autoLines int
		for _, ln := range strings.Split(string(b), "\n") {
			if !strings.HasPrefix(ln, "custom_proxy_group=") {
				continue
			}
			if isSelectCPG(ln) {
				selLines++
				if !hasLine(s, ln+"`[]专线组`[]家宽组") {
					t.Errorf("%s：select 组行应原成员无损、行尾按序追加组引用:\n%s", fx, ln)
				}
			} else {
				autoLines++
				if !hasLine(s, ln) {
					t.Errorf("%s：非 select 组行应保持原样（不注入）:\n%s", fx, ln)
				}
			}
		}
		if selLines == 0 || autoLines == 0 {
			t.Fatalf("%s：fixture 应同时含 select 与非 select 组行（sel=%d auto=%d）", fx, selLines, autoLines)
		}
		t.Logf("%s：select=%d 接线、auto=%d 不注入", fx, selLines, autoLines)
	}
}

// 全 select 组接线撞名联动：撞名组（🚀 节点选择 → ②）追加的是后缀名；多组按请求顺序追加
func TestDeriveGroupsINISelectWiringCollision(t *testing.T) {
	out, err := deriveINI(fixtureACL4SSR(t), []nodeGroup{
		{Name: "🚀 节点选择", Keywords: []string{"专线"}, URLTest: false},
		{Name: "家宽", Keywords: []string{"家宽"}, URLTest: false},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := "custom_proxy_group=🚀 节点选择`select`[]♻️ 自动选择`[]DIRECT`.*`[]🚀 节点选择②`[]家宽"
	if !hasLine(string(out), want) {
		t.Errorf("撞名组应以后缀名追加进主组:\nwant %s", want)
	}
}

// bootstrap 预生成（评审 F3 主路径）：新卷出厂层原料齐备 → bootstrap 即产出改写
// 产物（不等 refreshLoop 首轮拉取）；原料不齐 → 不拒启动、无产物（维持 fail-closed）
func TestBootstrapPreRewrites(t *testing.T) {
	vendor := t.TempDir()
	writeTB(t, filepath.Join(vendor, "ACL4SSR_Online.ini"),
		"[custom]\ncustom_proxy_group=`T`select`[]DIRECT`\nruleset=DIRECT,http://up/a.list\n")
	writeTB(t, filepath.Join(vendor, "a.list"), "a-content")

	m := newTestManager(t)
	m.vendorDir = vendor
	m.presets = []presetEntry{{"default", "ACL4SSR 默认", "http://127.0.0.1:1/ACL4SSR_Online.ini"}}
	m.bootstrap()
	if _, ok := m.rewrittenPath(m.presets[0]); !ok {
		t.Fatal("bootstrap 后应有改写产物（新卷首启即就绪，不等首轮刷新）")
	}

	m2 := newTestManager(t)
	m2.vendorDir = t.TempDir()
	writeTB(t, filepath.Join(m2.vendorDir, "ACL4SSR_Online.ini"), "ruleset=DIRECT,http://up/x.list\n") // 引用规则集不在
	m2.presets = []presetEntry{{"default", "ACL4SSR 默认", "http://127.0.0.1:1/ACL4SSR_Online.ini"}}
	m2.bootstrap()
	if _, ok := m2.rewrittenPath(m2.presets[0]); ok {
		t.Error("原料不齐不应有产物（不阻塞启动，等首轮刷新补）")
	}
}

// prepareDerivedConfig 兜底（评审 F3）：无改写产物 + 本地原料齐备（运行时层有
// 原始 ini + 规则集）→ 当场补产物派生成功；原料不齐 → 仍 derive_error；
// 非内置档 URL（豁免档自定义远端）→ 维持 derive_error
func TestPrepareDerivedConfigBootstrapsRewritten(t *testing.T) {
	groupsJSON := `[{"name":"专线","keywords":["专线"],"urlTest":false}]`
	newSV := func() (*server, *snapshotManager) {
		cfg := testConfig(t, "http://unused")
		cfg.presetURL = defaultPresetURL // default 档本地化（不豁免）
		cfg.sidecarBase = "http://sidecar.test"
		s := mustServer(t, cfg)
		m := newTestManager(t)
		m.presets = []presetEntry{{"default", "ACL4SSR 默认", defaultPresetURL}}
		s.snap = m
		return s, m
	}

	// 原料齐备、无改写产物（模拟 mapFrozenConfig miss 传入原始 URL）→ 当场补产物
	s, m := newSV()
	writeTB(t, filepath.Join(m.rulesDir, "ACL4SSR_Online.ini"),
		"[custom]\ncustom_proxy_group=`T`select`[]DIRECT`\nruleset=DIRECT,http://up/a.list\n")
	writeTB(t, filepath.Join(m.rulesDir, "a.list"), "a-content")
	u, err := s.prepareDerivedConfig("code1", defaultPresetURL, groupsJSON, "")
	if err != nil {
		t.Fatalf("原料齐备应当场补产物派生成功: %v", err)
	}
	if want := "http://sidecar.test/internal/derived/code1"; u != want {
		t.Errorf("派生 URL = %s, want %s", u, want)
	}
	if _, ok := m.rewrittenPath(m.presets[0]); !ok {
		t.Error("兜底补生成后改写产物应存在")
	}
	derived := readTB(t, filepath.Join(s.cfg.dataDir, "derived", "code1.ini"))
	if !strings.Contains(derived, "custom_proxy_group=专线`select`(?=.*专线)") {
		t.Errorf("派生文件缺组行:\n%s", derived)
	}

	// 原料不齐（引用规则集缺失 → 校验不过）→ 仍 derive_error
	s2, m2 := newSV()
	writeTB(t, filepath.Join(m2.rulesDir, "ACL4SSR_Online.ini"),
		"[custom]\ncustom_proxy_group=`T`select`[]DIRECT`\nruleset=DIRECT,http://up/b.list\n")
	if _, err := s2.prepareDerivedConfig("code2", defaultPresetURL, groupsJSON, ""); err == nil {
		t.Error("原料不齐应维持 derive_error")
	}

	// 豁免档自定义远端 URL：无本地原料可补 → derive_error
	s3, _ := newSV()
	if _, err := s3.prepareDerivedConfig("code3", "https://deploy.example.com/custom.ini", groupsJSON, ""); err == nil {
		t.Error("非内置档 URL 应维持 derive_error")
	}
}

// 多词 AND 前瞻写法（gate2）：词各 QuoteMeta 后拼 (?=.*w1)(?=.*w2)…；单词同款不分支
// （与既有断言形态一致）；5 词上限组合；元字符词转义点不丢
func TestDeriveGroupsINIMultiKeywords(t *testing.T) {
	tpl := "[custom]\ncustom_proxy_group=`T`select`[]DIRECT`\nenable_rule_generator=true\n"
	groups := []nodeGroup{
		{Name: "美专线", Keywords: []string{"美国", "专线"}, URLTest: false},
		{Name: "五词上限", Keywords: []string{"a", "b", "c", "d", "e"}, URLTest: false},
		{Name: "IEPL", Keywords: []string{"专线", "IEPL 1.0"}, URLTest: false},
	}
	out, err := deriveINI([]byte(tpl), groups, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{
		"custom_proxy_group=美专线`select`(?=.*美国)(?=.*专线)",
		"custom_proxy_group=五词上限`select`(?=.*a)(?=.*b)(?=.*c)(?=.*d)(?=.*e)",
		"custom_proxy_group=IEPL`select`(?=.*专线)(?=.*IEPL 1\\.0)",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("缺行:\n%s", want)
		}
	}
}

// 词数校验边界：0 词拒、6 词拒、恰 5 词过
func TestValidateGroupsKeywordCount(t *testing.T) {
	five := []string{"a", "b", "c", "d", "e"}
	if err := validateGroups([]nodeGroup{{Name: "n", Keywords: five}}); err != nil {
		t.Errorf("恰 5 词应过: %v", err)
	}
	if err := validateGroups([]nodeGroup{{Name: "n", Keywords: nil}}); err == nil {
		t.Error("0 词应拒")
	}
	if err := validateGroups([]nodeGroup{{Name: "n", Keywords: append([]string{"f"}, five...)}}); err == nil {
		t.Error("6 词应拒")
	}
}

// 旧格式容错（试运行期存量）：keyword 单字段且无 keywords → 当单词组；库存旧 JSON
// 解析与请求解码共用 nodeGroup.UnmarshalJSON
func TestParseGroupsJSONLegacyKeyword(t *testing.T) {
	gs, err := parseGroupsJSON(`[{"name":"专线","keyword":"专线","urlTest":false}]`)
	if err != nil {
		t.Fatal(err)
	}
	if len(gs) != 1 || gs[0].Name != "专线" || len(gs[0].Keywords) != 1 || gs[0].Keywords[0] != "专线" || gs[0].URLTest {
		t.Errorf("旧格式解析 = %+v，应当单词组", gs)
	}
	// keywords 与 keyword 并存：keywords 优先，keyword 忽略
	gs2, err := parseGroupsJSON(`[{"name":"n","keywords":["a","b"],"keyword":"c","urlTest":true}]`)
	if err != nil {
		t.Fatal(err)
	}
	if len(gs2[0].Keywords) != 2 || gs2[0].Keywords[0] != "a" || gs2[0].Keywords[1] != "b" {
		t.Errorf("并存时应取 keywords = %v", gs2[0].Keywords)
	}
}

// groupsJSON 键序 name→keywords→urlTest、无空白、空组 "[]"（契约钉死件）
func TestCanonicalGroupsJSONKeyOrder(t *testing.T) {
	got := canonicalGroupsJSON([]nodeGroup{{Name: "美专线", Keywords: []string{"美国", "专线"}, URLTest: false}})
	if want := `[{"name":"美专线","keywords":["美国","专线"],"urlTest":false}]`; got != want {
		t.Errorf("groupsJSON = %s，want %s", got, want)
	}
	if got := canonicalGroupsJSON(nil); got != "[]" {
		t.Errorf("空组 = %s，want []", got)
	}
}

// ---- v1.5.0 增补：模板群组删减（exclude）----

// 删业务组（有 ruleset，非锁定）：组行移除、规则回退到 🚀 节点选择；其余行零改动
func TestDeriveExcludeBusinessGroup(t *testing.T) {
	src := readFixture(t, "ACL4SSR_Online.ini")
	out, err := deriveINI(src, nil, []string{"🌍 国外媒体"})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if hasLine(s, "custom_proxy_group=🌍 国外媒体`select`[]🚀 节点选择`[]♻️ 自动选择`[]🎯 全球直连`.*") {
		t.Error("被删组定义行应移除")
	}
	if !hasLine(s, "ruleset=🚀 节点选择,https://raw.githubusercontent.com/ACL4SSR/ACL4SSR/master/Clash/ProxyMedia.list") {
		t.Errorf("ProxyMedia ruleset 目标应回退 🚀 节点选择（首成员非直连类）:\n%s", s)
	}
	if n, want := len(strings.Split(s, "\n")), len(strings.Split(string(src), "\n"))-1; n != want {
		t.Errorf("行数 = %d，应恰少 1 行（只删组行）", n)
	}
	// 其余组行逐行原样（未被删组未被引用）
	for _, ln := range strings.Split(string(src), "\n") {
		if strings.HasPrefix(ln, "custom_proxy_group=") && ln != "custom_proxy_group=🌍 国外媒体`select`[]🚀 节点选择`[]♻️ 自动选择`[]🎯 全球直连`.*" && !hasLine(s, ln) {
			t.Errorf("无关组行被改动: %s", ln)
		}
	}
}

// 删直连类业务组（首成员 DIRECT）：规则回退 🎯 全球直连（国内媒体类流量不绕代理）
func TestDeriveExcludeDirectClassGroup(t *testing.T) {
	src := readFixture(t, "ACL4SSR_Online_Full_MultiMode.ini")
	out, err := deriveINI(src, nil, []string{"🌏 国内媒体"})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if strings.Contains(s, "custom_proxy_group=🌏 国内媒体") {
		t.Error("被删组定义行应移除")
	}
	if !hasLine(s, "ruleset=🎯 全球直连,https://raw.githubusercontent.com/ACL4SSR/ACL4SSR/master/Clash/ChinaMedia.list") {
		t.Errorf("ChinaMedia ruleset 目标应回退 🎯 全球直连:\n%s", s)
	}
}

// 删地区组（url-test，无 ruleset）：组行移除 + 其余组行成员引用连前导反引号摘净
func TestDeriveExcludeRegionGroupStripsRefs(t *testing.T) {
	src := readFixture(t, "ACL4SSR_Online_Full_MultiMode.ini")
	out, err := deriveINI(src, nil, []string{"🇰🇷 韩国节点"})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if strings.Contains(s, "🇰🇷 韩国节点") {
		t.Errorf("被删组名不应有任何残留（定义/引用）:\n%s", s)
	}
	wantMain := "custom_proxy_group=🚀 节点选择`select`[]♻️ 自动选择`[]🔯 故障转移`[]🔮 负载均衡`[]🇭🇰 香港节点`[]🇨🇳 台湾节点`[]🇸🇬 狮城节点`[]🇯🇵 日本节点`[]🇺🇲 美国节点`[]🚀 手动切换`[]DIRECT"
	if !hasLine(s, wantMain) {
		t.Errorf("主组应只摘除韩国成员、其余成员与顺序不变:\n%s", wantMain)
	}
	if strings.Contains(s, "``") {
		t.Error("摘引用不应留空成员（``）")
	}
}

// 锁定组（规则骨架/内联目标）与模板中不存在的名：静默忽略，产物逐字节不变
func TestDeriveExcludeLockedAndMissingIgnored(t *testing.T) {
	src := readFixture(t, "ACL4SSR_Online.ini")
	out, err := deriveINI(src, nil, []string{"🚀 节点选择", "🎯 全球直连", "🐟 漏网之鱼", "不存在的组"})
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != string(src) {
		t.Error("锁定组/不存在名应静默忽略，产物应逐字节不变")
	}
}

// 仅 exclude（groups 空）：跳过生成/撞名/接线，只删减 + 自检；groups 与 exclude 均空时
// deriveINI 原样返回（该组合由调用方前置校验旁路，见 prepareDerivedConfig）
func TestDeriveExcludeOnly(t *testing.T) {
	src := readFixture(t, "ACL4SSR_Online.ini")
	out, err := deriveINI(src, nil, []string{"🌍 国外媒体"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "custom_proxy_group=🌍 国外媒体`") {
		t.Error("仅 exclude 应删组行")
	}
	if strings.Contains(string(out), "select`(?=.*") {
		t.Error("groups 为空不应生成组行")
	}
	same, err := deriveINI(src, nil, nil)
	if err != nil {
		t.Fatalf("空 groups+空 exclude 不应报错: %v", err)
	}
	if string(same) != string(src) {
		t.Error("空 groups+空 exclude 应原样返回")
	}
}

// 撞名取删减前原始名单：用户组复用被删组名 → 后缀②（避免与删减自检冲突）
func TestDeriveExcludeCollisionUsesOriginalNames(t *testing.T) {
	src := readFixture(t, "ACL4SSR_Online.ini")
	out, err := deriveINI(src, []nodeGroup{{Name: "🌍 国外媒体", Keywords: []string{"专线"}, URLTest: false}}, []string{"🌍 国外媒体"})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !hasLine(s, "custom_proxy_group=🌍 国外媒体②`select`(?=.*专线)") {
		t.Errorf("用户组复用被删组名应取后缀②:\n%s", s)
	}
	if hasLine(s, "custom_proxy_group=🌍 国外媒体`select`(?=.*专线)") {
		t.Error("不得复用被删组原名的定义行")
	}
	// 接线只追加最终组名（②），不出现裸名引用
	if !strings.Contains(s, "`[]🌍 国外媒体②") {
		t.Error("select 组接线应用最终组名")
	}
}

// groups + exclude 同提交：两组动作都生效（删组/摘引用/回退 + 生成/接线）
func TestDeriveExcludeAndGroupsTogether(t *testing.T) {
	src := readFixture(t, "ACL4SSR_Online_Full_MultiMode.ini")
	out, err := deriveINI(src, []nodeGroup{{Name: "专线", Keywords: []string{"专线"}, URLTest: false}}, []string{"🇰🇷 韩国节点"})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if strings.Contains(s, "🇰🇷 韩国节点") {
		t.Error("exclude 未生效")
	}
	if !hasLine(s, "custom_proxy_group=专线`select`(?=.*专线)") {
		t.Error("groups 未生成")
	}
	// 接线：每个 select 模板组行尾追加（含主组）
	if !strings.Contains(s, "custom_proxy_group=🚀 节点选择`select`") || !strings.Contains(s, "`[]专线") {
		t.Error("select 组接线缺失")
	}
}

// 无 exclude（nil 与空切片等价）：deriveINI 输出逐字节一致（回归）
func TestDeriveININoExcludeByteIdentical(t *testing.T) {
	src := readFixture(t, "ACL4SSR_Online.ini")
	groups := []nodeGroup{{Name: "专线", Keywords: []string{"专线"}, URLTest: false}}
	a, err := deriveINI(src, groups, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := deriveINI(src, groups, []string{})
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Error("nil 与空切片 exclude 应等价（逐字节一致）")
	}
}
