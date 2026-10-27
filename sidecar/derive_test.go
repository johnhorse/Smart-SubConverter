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
		{Name: "专线", Keyword: "专线", URLTest: false},
		{Name: "家宽", Keyword: "家宽", URLTest: true},
	}
	out, err := deriveGroupsINI(src, groups)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	wantSelect := "custom_proxy_group=专线`select`专线"
	wantURLTest := "custom_proxy_group=家宽`url-test`家宽`http://www.gstatic.com/generate_204`300,,50"
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
	out, err := deriveGroupsINI(fixtureACL4SSR(t), []nodeGroup{{Name: "IEPL", Keyword: "IEPL 1.0", URLTest: false}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "custom_proxy_group=IEPL`select`IEPL 1\\.0") {
		t.Errorf("QuoteMeta 转义缺失:\n%s", string(out))
	}
}

// 撞名：模板组名单 → 后缀带圈数字单级；模板已占 ② → 多级递增；同批已生成名同样回避
func TestDeriveGroupsINICollision(t *testing.T) {
	out, err := deriveGroupsINI(fixtureACL4SSR(t), []nodeGroup{{Name: "🚀 节点选择", Keyword: "专线", URLTest: false}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "custom_proxy_group=🚀 节点选择②`select`专线") {
		t.Errorf("撞名应后缀②:\n%s", string(out))
	}

	// 模板占 ②：同名第二撞 → ③
	tpl := "[custom]\n" +
		"custom_proxy_group=`节点选择`select`[]DIRECT`\n" +
		"custom_proxy_group=`节点选择②`select`[]DIRECT`\n" +
		"custom_proxy_group=`节点选择③`select`[]DIRECT`\n" +
		"enable_rule_generator=true\n"
	out, err = deriveGroupsINI([]byte(tpl), []nodeGroup{{Name: "节点选择", Keyword: "k", URLTest: false}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "custom_proxy_group=节点选择④`select`k") {
		t.Errorf("②③已占应取④:\n%s", string(out))
	}

	// 同批已生成名回避：组 [A, A]（校验层禁重名，但派生层自守）→ 第二个取 A②
	tpl2 := "[custom]\ncustom_proxy_group=`T`select`[]DIRECT`\nenable_rule_generator=true\n"
	out, err = deriveGroupsINI([]byte(tpl2), []nodeGroup{
		{Name: "A", Keyword: "k1", URLTest: false},
		{Name: "A", Keyword: "k2", URLTest: false},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "custom_proxy_group=A`select`k1") || !strings.Contains(string(out), "custom_proxy_group=A②`select`k2") {
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
	if _, err := deriveGroupsINI([]byte(b.String()), []nodeGroup{{Name: "X", Keyword: "k", URLTest: false}}); err == nil {
		t.Error("撞穿全表应报错")
	}
}

// 无锚点 fail-closed：无 [custom] 段 / 有段无既有组行 → error（不发明插入位）；
// 空 groups → error（调用方应旁路派生）
func TestDeriveGroupsININoAnchor(t *testing.T) {
	cases := []struct {
		name   string
		src    string
		groups []nodeGroup
	}{
		{"无 [custom] 段", "; no sections here\nruleset=whatever\n", []nodeGroup{{Name: "A", Keyword: "k"}}},
		{"有段无组行", "[custom]\nenable_rule_generator=true\n", []nodeGroup{{Name: "A", Keyword: "k"}}},
		{"空 groups", "[custom]\ncustom_proxy_group=`T`select`[]DIRECT`\n", nil},
	}
	for _, c := range cases {
		if _, err := deriveGroupsINI([]byte(c.src), c.groups); err == nil {
			t.Errorf("%s：应 fail-closed 报错", c.name)
		}
	}
}

// 多组顺序写入；零命中关键词组行照写（无条件写入，R3-6）
func TestDeriveGroupsINIMultiAndZeroHit(t *testing.T) {
	tpl := "[custom]\ncustom_proxy_group=`T`select`[]DIRECT`\nenable_rule_generator=true\n"
	groups := []nodeGroup{
		{Name: "一", Keyword: "不存在的词xyz", URLTest: false},
		{Name: "二", Keyword: "二", URLTest: true},
		{Name: "三", Keyword: "三", URLTest: false},
	}
	out, err := deriveGroupsINI([]byte(tpl), groups)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{
		"custom_proxy_group=一`select`不存在的词xyz",
		"custom_proxy_group=二`url-test`二`http://www.gstatic.com/generate_204`300,,50",
		"custom_proxy_group=三`select`三",
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
func TestDeriveGroupsINIMainGroupWiring(t *testing.T) {
	groups := []nodeGroup{
		{Name: "专线组", Keyword: "专线", URLTest: false},
		{Name: "家宽组", Keyword: "家宽", URLTest: true},
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
		main := ""
		for _, ln := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(ln, "custom_proxy_group=") {
				main = ln
				break
			}
		}
		if !strings.HasPrefix(main, "custom_proxy_group=🚀 节点选择`") {
			t.Fatalf("%s：首个组行非主组「🚀 节点选择」:\n%s", fx, main)
		}
		out, err := deriveGroupsINI(b, groups)
		if err != nil {
			t.Fatalf("%s: %v", fx, err)
		}
		want := main + "`[]专线组`[]家宽组"
		if !hasLine(string(out), want) {
			t.Errorf("%s：主组行应原成员无损、行尾按序追加组引用:\nwant %s", fx, want)
		}
	}
}

// 主组接线撞名联动：撞名组（🚀 节点选择 → ②）追加的是后缀名；多组按请求顺序追加
func TestDeriveGroupsINIMainGroupWiringCollision(t *testing.T) {
	out, err := deriveGroupsINI(fixtureACL4SSR(t), []nodeGroup{
		{Name: "🚀 节点选择", Keyword: "专线", URLTest: false},
		{Name: "家宽", Keyword: "家宽", URLTest: false},
	})
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
	groupsJSON := `[{"name":"专线","keyword":"专线","urlTest":false}]`
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
	u, err := s.prepareDerivedConfig("code1", defaultPresetURL, groupsJSON)
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
	if !strings.Contains(derived, "custom_proxy_group=专线`select`专线") {
		t.Errorf("派生文件缺组行:\n%s", derived)
	}

	// 原料不齐（引用规则集缺失 → 校验不过）→ 仍 derive_error
	s2, m2 := newSV()
	writeTB(t, filepath.Join(m2.rulesDir, "ACL4SSR_Online.ini"),
		"[custom]\ncustom_proxy_group=`T`select`[]DIRECT`\nruleset=DIRECT,http://up/b.list\n")
	if _, err := s2.prepareDerivedConfig("code2", defaultPresetURL, groupsJSON); err == nil {
		t.Error("原料不齐应维持 derive_error")
	}

	// 豁免档自定义远端 URL：无本地原料可补 → derive_error
	s3, _ := newSV()
	if _, err := s3.prepareDerivedConfig("code3", "https://deploy.example.com/custom.ini", groupsJSON); err == nil {
		t.Error("非内置档 URL 应维持 derive_error")
	}
}
