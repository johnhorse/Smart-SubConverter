// v1.7.0 机场 DNS 透传测试：提取（clash core/meta、QX/Surge 原生、base64、无 dns）、
// 翻译口径（DoH→doh-server、plain UDP→dns-server）、派生 base 生成与自检（fail-open）、
// rule_base 行叠加、/s/ 单段与两段式链路、探测同路、env off 逐字节回退。
package main

import (
	"encoding/base64"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testEngineFixture 读 testdata 的引擎模板裁剪版（结构保真：clash dns 条件块 /
// surge / loon / quanx 段与真实 all_base.tpl 同构）
func testEngineFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("读 fixture: %v", err)
	}
	return b
}

// newDNSServer 构造带本地快照档位（default 档 rewritten ini 就绪）+ testdata 引擎
// 模板的 server——dnsPassthroughConfig 的完整前置（照 derive_test 的 newSV 模式）。
// 返回值 rwPath = default 档改写产物绝对路径（作 config= 入参用）
func newDNSServer(t *testing.T) (*server, *snapshotManager, string) {
	t.Helper()
	cfg := testConfig(t, "http://unused")
	cfg.presetURL = defaultPresetURL // default 档本地化（不豁免）
	cfg.sidecarBase = "http://sidecar.test"
	cfg.passThroughDNS = true
	cfg.engineTplPath = filepath.Join("testdata", "all_base.tpl")
	cfg.engineStashPath = filepath.Join("testdata", "stash.yaml")
	s := mustServer(t, cfg)
	m := newTestManager(t)
	m.presets = []presetEntry{{"default", "ACL4SSR 默认", defaultPresetURL}}
	s.snap = m
	// 档位原料 + 改写产物（dnsPassthroughConfig 以 rewritten ini 为 ext ini 的底）
	writeTB(t, filepath.Join(m.rulesDir, "ACL4SSR_Online.ini"),
		"[custom]\ncustom_proxy_group=`T`select`[]DIRECT`\nruleset=DIRECT,[]GEOIP,CN\nruleset=🎯 全球直连,[]FINAL\n")
	writeTB(t, filepath.Join(m.rulesDir, "a.list"), "a-content")
	m.rewriteAndSwap(m.presets[0], "", false, time.Now())
	rwPath, ok := m.rewrittenPath(m.presets[0])
	if !ok {
		t.Fatal("改写产物未就绪")
	}
	return s, m, rwPath
}

const clashCoreSub = `port: 7890
dns:
  enable: true
  enhanced-mode: fake-ip
  fake-ip-range: 198.18.0.1/16
  nameserver:
    - https://doh.airport-example.com/dns-query
    - 10.0.0.53
  default-nameserver:
    - 223.5.5.5
proxies:
  - name: "机场HK-01"
    type: ss
    server: 192.0.2.1
    port: 8388
    cipher: aes-256-gcm
    password: "p"
`

const clashMetaSub = `dns:
  enable: true
  nameserver:
  - https://doh.airport-example.com/dns-query
  default-nameserver:
  - 223.5.5.5
  proxy-server-nameserver:
  - https://doh.airport-example.com/dns-query
  nameserver-policy:
    "geosite:cn":
    - https://doh.airport-example.com/cn
proxies:
  - name: "MetaHK-01"
    type: ss
    server: 192.0.2.3
    port: 8388
    cipher: aes-256-gcm
    password: "p"
`

// 提取：clash core schema——yamlBlock 整块保真 + servers 双形态（DoH + plain）
func TestExtractClashCore(t *testing.T) {
	ad := extractAirportDNS([]byte(clashCoreSub))
	if ad.empty() {
		t.Fatal("应提取到 dns")
	}
	if !strings.Contains(string(ad.yamlBlock), "fake-ip-range: 198.18.0.1/16") {
		t.Errorf("yamlBlock 应保真含模式字段:\n%s", ad.yamlBlock)
	}
	if !strings.HasPrefix(string(ad.yamlBlock), "dns:") {
		t.Errorf("yamlBlock 应从 dns: 顶层键起:\n%s", ad.yamlBlock)
	}
	if strings.Contains(string(ad.yamlBlock), "proxies:") {
		t.Errorf("yamlBlock 不得越过 dns 块边界:\n%s", ad.yamlBlock)
	}
	if len(ad.servers) != 2 || !ad.servers[0].doh || ad.servers[1].doh {
		t.Errorf("servers = %+v，应 1 DoH + 1 plain", ad.servers)
	}
	if ad.servers[0].addr != "https://doh.airport-example.com/dns-query" || ad.servers[1].addr != "10.0.0.53" {
		t.Errorf("servers 地址错: %+v", ad.servers)
	}
}

// 提取：clash meta schema——nameserver-policy 嵌套 map 原样在块内（保真不解释）
func TestExtractClashMeta(t *testing.T) {
	ad := extractAirportDNS([]byte(clashMetaSub))
	if ad.empty() {
		t.Fatal("应提取到 dns")
	}
	for _, want := range []string{"nameserver-policy:", "geosite:cn", "proxy-server-nameserver:"} {
		if !strings.Contains(string(ad.yamlBlock), want) {
			t.Errorf("meta 块应含 %s:\n%s", want, ad.yamlBlock)
		}
	}
	if len(ad.servers) != 1 || ad.servers[0].addr != "https://doh.airport-example.com/dns-query" {
		t.Errorf("servers = %+v", ad.servers)
	}
	// policy 嵌套的服务器不属于 nameserver 列表，不重复计入
	if strings.Contains(ad.servers[0].addr, "/cn") {
		t.Errorf("policy 服务器误入 nameserver: %+v", ad.servers)
	}
}

// 提取：无 dns（明文节点列表 / base64 节点列表 / 无 dns 键的 yaml）→ 空（no-op 路径）
func TestExtractAbsent(t *testing.T) {
	plain := "ss://YWVzLTI1Ni1nY206cA==@192.0.2.10:8388#n1\nss://YWVzLTI1Ni1nY206cA==@192.0.2.11:8388#n2\n"
	if ad := extractAirportDNS([]byte(plain)); !ad.empty() {
		t.Errorf("明文节点列表应无 dns: %+v", ad)
	}
	b64 := base64.StdEncoding.EncodeToString([]byte(plain))
	if ad := extractAirportDNS([]byte(b64)); !ad.empty() {
		t.Errorf("base64 节点列表应无 dns: %+v", ad)
	}
	noDNSYAML := "port: 7890\nproxies:\n  - name: a\n    type: ss\n"
	if ad := extractAirportDNS([]byte(noDNSYAML)); !ad.empty() {
		t.Errorf("无 dns 键 yaml 应空: %+v", ad)
	}
	garbage := "\x00\x01 not a config at all {{{"
	if ad := extractAirportDNS([]byte(garbage)); !ad.empty() {
		t.Errorf("乱内容应空（fail-open）: %+v", ad)
	}
	emptyDNSKey := "dns:\nproxies:\n  - name: a\n"
	if ad := extractAirportDNS([]byte(emptyDNSKey)); !ad.empty() {
		t.Errorf("空 dns 块应空: %+v", ad)
	}
}

// 提取：QX 原生 [dns] 段（机场按 UA 给原生格式的场景）——servers 进、yamlBlock 空
func TestExtractQXNative(t *testing.T) {
	qx := "[general]\nnetwork_check_url=https://x/204\n\n[dns]\nserver=119.29.29.29\nserver=223.5.5.5\ndoh-server=https://doh.pub/dns-query, https://dns.alidns.com/dns-query\n\n[policy]\nstatic=A, direct\n"
	ad := extractAirportDNS([]byte(qx))
	if ad.empty() {
		t.Fatal("应提取到 QX dns")
	}
	if ad.yamlBlock != nil {
		t.Errorf("QX 原生无 yamlBlock: %s", ad.yamlBlock)
	}
	var plain, doh int
	for _, sv := range ad.servers {
		if sv.doh {
			doh++
		} else {
			plain++
		}
	}
	if plain != 2 || doh != 2 {
		t.Errorf("servers = %+v，应 2 plain + 2 doh", ad.servers)
	}
}

// 提取：Surge 原生 [General] dns-server / doh-server（逗号多值）
func TestExtractSurgeNative(t *testing.T) {
	surge := "#!MANAGED-CONFIG https://x interval=86400\n[General]\nloglevel = notify\ndns-server = 119.29.29.29, 223.5.5.5\ndoh-server = https://223.5.5.5/resolve\n[Rule]\nFINAL,DIRECT\n"
	ad := extractAirportDNS([]byte(surge))
	if ad.empty() {
		t.Fatal("应提取到 Surge dns")
	}
	if len(ad.servers) != 3 {
		t.Errorf("servers = %+v，应 2 plain + 1 doh", ad.servers)
	}
}

// 翻译：QX 口径（闸门 G4 定稿）——plain 每行一个 server=、DoH 单行逗号分隔 doh-server=
func TestTranslateQX(t *testing.T) {
	lines := translateQXDnsLines([]dnsServer{
		{addr: "10.0.0.53"}, {addr: "10.0.0.54"},
		{addr: "https://doh.airport-example.com/dns-query", doh: true},
		{addr: "https://doh.airport-example.com/dns-query2", doh: true},
	})
	want := []string{
		"server=10.0.0.53",
		"server=10.0.0.54",
		"doh-server=https://doh.airport-example.com/dns-query, https://doh.airport-example.com/dns-query2",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Errorf("QX 翻译 = %v\nwant %v", lines, want)
	}
	// 纯 DoH：不产 server= 行；纯 plain：不产 doh-server= 行
	if l := translateQXDnsLines([]dnsServer{{addr: "https://d/x", doh: true}}); len(l) != 1 || !strings.HasPrefix(l[0], "doh-server=") {
		t.Errorf("纯 DoH 翻译 = %v", l)
	}
	if l := translateQXDnsLines([]dnsServer{{addr: "10.0.0.53"}}); len(l) != 1 || !strings.HasPrefix(l[0], "server=") {
		t.Errorf("纯 plain 翻译 = %v", l)
	}
}

// 翻译：Surge/Loon [General] 口径——dns-server = a, b 与 doh-server = u1, u2 两行
func TestTranslateGeneral(t *testing.T) {
	lines := translateGeneralDnsLines([]dnsServer{
		{addr: "10.0.0.53"}, {addr: "10.0.0.54"},
		{addr: "https://doh.airport-example.com/dns-query", doh: true},
	})
	want := []string{
		"dns-server = 10.0.0.53, 10.0.0.54",
		"doh-server = https://doh.airport-example.com/dns-query",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Errorf("General 翻译 = %v\nwant %v", lines, want)
	}
}

// 派生 base：clash 条件块整块替换（无条件渲染）+ quanx [dns] 翻译 + surge/loon 行
// 替换 + stash 顶层块替换，一次全过
func TestDeriveDNSBases(t *testing.T) {
	ad := extractAirportDNS([]byte(clashCoreSub))
	tpl := testEngineFixture(t, "all_base.tpl")
	stash := testEngineFixture(t, "stash.yaml")
	tplOut, stashOut, ok := deriveDNSBases(tpl, stash, ad)
	if !ok {
		t.Fatal("派生应成功")
	}
	ts, ss := string(tplOut), string(stashOut)

	// clash 段：条件块消失、机场块无条件在
	if strings.Contains(ts, "request.clash.dns") {
		t.Errorf("clash dns 条件块应被移除:\n%s", ts)
	}
	if !strings.Contains(ts, "fake-ip-range: 198.18.0.1/16") || !strings.Contains(ts, "doh.airport-example.com/dns-query") {
		t.Errorf("机场 dns 块应嵌入 clash 段:\n%s", ts)
	}
	// 原引擎默认 dns（条件块内）不得残留
	if strings.Contains(ts, "dns.alidns.com") && strings.Contains(ts, "doh.pub") {
		t.Errorf("默认 dns 残留:\n%s", ts)
	}
	// quanx 段：[dns] 段体换为翻译行
	if !strings.Contains(ts, "server=10.0.0.53") || !strings.Contains(ts, "doh-server=https://doh.airport-example.com/dns-query") {
		t.Errorf("quanx [dns] 翻译行缺失:\n%s", ts)
	}
	if strings.Contains(ts, "server=8.8.8.8") || strings.Contains(ts, "server=1.0.0.1") {
		t.Errorf("quanx 旧 server 残留:\n%s", ts)
	}
	// surge 段：dns-server 行替换
	if !strings.Contains(ts, "dns-server = 10.0.0.53") {
		t.Errorf("surge dns-server 翻译行缺失:\n%s", ts)
	}
	if strings.Contains(ts, "dns-server = 119.29.29.29,223.5.5.5") {
		t.Errorf("surge 旧 dns-server 残留:\n%s", ts)
	}
	// loon 段：dns-server / doh-server 双行替换
	if !strings.Contains(ts, "doh-server = https://doh.airport-example.com/dns-query") {
		t.Errorf("loon doh-server 翻译行缺失:\n%s", ts)
	}
	if strings.Contains(ts, "sm2.doh.pub") || strings.Contains(ts, "119.29.29.29, 223.5.5.5") {
		t.Errorf("loon 旧 dns 行残留:\n%s", ts)
	}
	// 段结构完整（自检已过，再显式断言段头）
	for _, seg := range []string{`{% if request.target == "clash"`, `{% if request.target == "surge"`, `{% if request.target == "loon"`, `{% if request.target == "quanx"`} {
		if !strings.Contains(ts, seg) {
			t.Errorf("段头丢失: %s", seg)
		}
	}
	// stash：顶层 dns 块替换、其余键不动
	if !strings.Contains(ss, "doh.airport-example.com/dns-query") || !strings.Contains(ss, "fake-ip-range: 198.18.0.1/16") {
		t.Errorf("stash dns 块应替换:\n%s", ss)
	}
	if strings.Contains(ss, "1.12.12.12") {
		t.Errorf("stash 旧 dns 残留:\n%s", ss)
	}
	if !strings.Contains(ss, "mode: rule") || !strings.Contains(ss, "rules:") {
		t.Errorf("stash 其余顶层键被误动:\n%s", ss)
	}
}

// 派生 base：clash 条件块漂移（上游改版丢特征）→ 只降级 clash 透传（条件块原样 =
// clash 输出维持现状无 dns），其余家族照常、stash 照常——单家漂移不连坐全局（评审 F2）
func TestDeriveDNSBasesTemplateDrift(t *testing.T) {
	ad := extractAirportDNS([]byte(clashCoreSub))
	broken := strings.ReplaceAll(string(testEngineFixture(t, "all_base.tpl")), `{% if default(request.clash.dns, "") == "1" %}`, `{% if default(request.clash.dnsx, "") == "2" %}`)
	tplOut, stashOut, ok := deriveDNSBases([]byte(broken), testEngineFixture(t, "stash.yaml"), ad)
	if !ok {
		t.Fatal("clash 漂移不应连坐其余家族（ok 应 true）")
	}
	ts := string(tplOut)
	if !strings.Contains(ts, "clash.dnsx") {
		t.Errorf("clash 条件块应原样保留（该家降级）:\n%s", ts)
	}
	if strings.Contains(ts, "fake-ip-range: 198.18.0.1/16") {
		t.Errorf("clash 漂移时机场 yamlBlock 不应注入:\n%s", ts)
	}
	if !strings.Contains(ts, "server=10.0.0.53") || !strings.Contains(ts, "dns-server = 10.0.0.53") {
		t.Errorf("quanx/surge 翻译应照常:\n%s", ts)
	}
	if stashOut == nil || !strings.Contains(string(stashOut), "doh.airport-example.com") {
		t.Errorf("stash 应照常派生: %v", stashOut)
	}
}

// 派生 base：loon 段 doh-server 排在 dns-server 之前（逆序模板形态）——旧 dns/doh 行
// 仍应全删不残留（评审 F1：旧删除逻辑只清 anchor 之后的行，逆序时旧行泄漏进产物）
func TestDeriveDNSBasesGeneralReverseOrder(t *testing.T) {
	ad := extractAirportDNS([]byte(clashCoreSub))
	tpl := string(testEngineFixture(t, "all_base.tpl"))
	tpl = strings.Replace(tpl,
		"dns-server = 119.29.29.29, 223.5.5.5\n# DNS over HTTPS服务器，用,隔开多个服务器\ndoh-server = https://223.5.5.5/resolve, https://sm2.doh.pub/dns-query",
		"doh-server = https://old-doh.example/dns-query\n# 逆序形态\ndns-server = 119.29.29.29, 223.5.5.5", 1)
	tplOut, _, ok := deriveDNSBases([]byte(tpl), testEngineFixture(t, "stash.yaml"), ad)
	if !ok {
		t.Fatal("逆序模板派生应成功")
	}
	ts := string(tplOut)
	if strings.Contains(ts, "old-doh.example") || strings.Contains(ts, "119.29.29.29, 223.5.5.5") {
		t.Errorf("loon 旧 dns 行残留（删除应与行序无关）:\n%s", ts)
	}
	if !strings.Contains(ts, "doh-server = https://doh.airport-example.com/dns-query") {
		t.Errorf("loon 新翻译行缺失:\n%s", ts)
	}
}

// 派生 base：quanx 段 [dns] 段头漂移（上游改名）→ 只降级 quanx（旧 server 行原样 =
// QX 输出维持现状），clash/surge/loon/stash 照常（评审 F2）
func TestDeriveDNSBasesQuanxDrift(t *testing.T) {
	ad := extractAirportDNS([]byte(clashCoreSub))
	tpl := strings.ReplaceAll(string(testEngineFixture(t, "all_base.tpl")), "[dns]\n", "[dns-renamed]\n")
	tplOut, stashOut, ok := deriveDNSBases([]byte(tpl), testEngineFixture(t, "stash.yaml"), ad)
	if !ok {
		t.Fatal("quanx 漂移不应连坐其余家族（ok 应 true）")
	}
	ts := string(tplOut)
	if !strings.Contains(ts, "server=8.8.8.8") {
		t.Errorf("quanx 旧 server 行应原样保留（该家降级）:\n%s", ts)
	}
	if !strings.Contains(ts, "fake-ip-range: 198.18.0.1/16") || !strings.Contains(ts, "dns-server = 10.0.0.53") {
		t.Errorf("clash/surge 应照常透传:\n%s", ts)
	}
	if stashOut == nil {
		t.Error("stash 应照常派生")
	}
}

// 派生 base：servers-only（机场给 QX/Surge 原生、无 YAML 块）——clash 段不动
// （条件块原样保留 = clash 输出维持现状无 dns），翻译行照常注入
func TestDeriveDNSBasesServersOnly(t *testing.T) {
	ad := airportDNS{servers: []dnsServer{{addr: "10.0.0.53"}, {addr: "https://doh.airport-example.com/dns-query", doh: true}}}
	tplOut, stashOut, ok := deriveDNSBases(testEngineFixture(t, "all_base.tpl"), testEngineFixture(t, "stash.yaml"), ad)
	if !ok {
		t.Fatal("servers-only 派生应成功")
	}
	ts := string(tplOut)
	if !strings.Contains(ts, "request.clash.dns") {
		t.Errorf("clash 段应保持原条件块（无 yamlBlock 不动）:\n%s", ts)
	}
	if !strings.Contains(ts, "server=10.0.0.53") || !strings.Contains(ts, "doh-server = https://doh.airport-example.com/dns-query") {
		t.Errorf("翻译行应注入:\n%s", ts)
	}
	if stashOut != nil {
		t.Errorf("无 yamlBlock 时 stash 不产出（clash 系 no-op），got %s", stashOut)
	}
}

// rule_base 行集：tpl 四行 + stash 一行（yamlBlock 空则 stash 行省略）
func TestRuleBaseLines(t *testing.T) {
	all := ruleBaseLines("http://s/t.tpl", "http://s/t.yaml")
	want := []string{
		"clash_rule_base=http://s/t.tpl",
		"surge_rule_base=http://s/t.tpl",
		"loon_rule_base=http://s/t.tpl",
		"quanx_rule_base=http://s/t.tpl",
		"stash_rule_base=http://s/t.yaml",
	}
	if strings.Join(all, "|") != strings.Join(want, "|") {
		t.Errorf("rule_base 行 = %v\nwant %v", all, want)
	}
	tplOnly := ruleBaseLines("http://s/t.tpl", "")
	if len(tplOnly) != 4 || strings.Contains(strings.Join(tplOnly, ""), "stash") {
		t.Errorf("无 stash 时应 4 行: %v", tplOnly)
	}
}

// rule_base 行插入 [custom] 段内：段中（下一段头前）与段尾两形态；无 [custom] 报错
func TestAppendRuleBaseLines(t *testing.T) {
	mid := "[custom]\ncustom_proxy_group=`T`select`[]DIRECT`\nruleset=DIRECT,[]FINAL\n[rule]\nx=1\n"
	out, err := appendRuleBaseLines([]byte(mid), []string{"clash_rule_base=http://s/t.tpl"})
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(out), "\n")
	idxCustom, idxRB, idxRule := -1, -1, -1
	for i, l := range lines {
		switch {
		case strings.HasPrefix(l, "[custom]"):
			idxCustom = i
		case strings.HasPrefix(l, "clash_rule_base="):
			idxRB = i
		case strings.HasPrefix(l, "[rule]"):
			idxRule = i
		}
	}
	if idxCustom < 0 || idxRB < idxCustom || idxRB > idxRule {
		t.Errorf("rule_base 行应落 [custom] 段内（段头=%d 行=%d 段尾=%d）:\n%s", idxCustom, idxRB, idxRule, out)
	}
	tail := "[custom]\nruleset=DIRECT,[]FINAL\n"
	if out, err = appendRuleBaseLines([]byte(tail), []string{"clash_rule_base=http://s/t.tpl"}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(strings.TrimRight(string(out), "\n"), "clash_rule_base=http://s/t.tpl") {
		t.Errorf("[custom] 为末段时行应落段尾:\n%s", out)
	}
	if _, err = appendRuleBaseLines([]byte("[other]\nx=1\n"), []string{"k=v"}); err == nil {
		t.Error("无 [custom] 段应报错")
	}
}

// dnsPassthroughConfig 编排：成功路径——三件套落盘、共享 ini = 档位 ini + rule_base
// 行、返回 URL 与 rb 行；幂等（二次调用同 stem）
func TestDNSPassthroughConfigApplied(t *testing.T) {
	s, _, rwPath := newDNSServer(t)
	newURL, rb := s.dnsPassthroughConfig(clashCoreSub, rwPath)
	if len(rb) != 5 {
		t.Fatalf("rb 行 = %v，应 5 行", rb)
	}
	if !strings.HasPrefix(newURL, "http://sidecar.test/internal/derived/dns-default-") || !strings.HasSuffix(newURL, ".ini") {
		t.Fatalf("共享 ini URL 形态错: %s", newURL)
	}
	dir := filepath.Join(s.cfg.dataDir, "derived")
	stem := strings.TrimSuffix(filepath.Base(newURL), ".ini")
	tplData, err := os.ReadFile(filepath.Join(dir, stem+".tpl"))
	if err != nil || !strings.Contains(string(tplData), "doh.airport-example.com/dns-query") {
		t.Errorf("派生 tpl 缺失或内容错: %v", err)
	}
	yamlData, err := os.ReadFile(filepath.Join(dir, stem+".yaml"))
	if err != nil || !strings.Contains(string(yamlData), "fake-ip-range: 198.18.0.1/16") {
		t.Errorf("派生 stash yaml 缺失或内容错: %v", err)
	}
	iniData, err := os.ReadFile(filepath.Join(dir, stem+".ini"))
	if err != nil {
		t.Fatal(err)
	}
	iniStr := string(iniData)
	if !strings.Contains(iniStr, "clash_rule_base=http://sidecar.test/internal/derived/"+stem+".tpl") ||
		!strings.Contains(iniStr, "stash_rule_base=http://sidecar.test/internal/derived/"+stem+".yaml") ||
		!strings.Contains(iniStr, "quanx_rule_base=") {
		t.Errorf("共享 ini 缺 rule_base 行:\n%s", iniStr)
	}
	if !strings.Contains(iniStr, "custom_proxy_group=`T`select`[]DIRECT`") || !strings.Contains(iniStr, "ruleset=DIRECT,[]GEOIP,CN") {
		t.Errorf("共享 ini 应以档位 ini 为底（组行与规则行在）:\n%s", iniStr)
	}
	// 幂等：同内容二次调用同 URL
	again, _ := s.dnsPassthroughConfig(clashCoreSub, rwPath)
	if again != newURL {
		t.Errorf("幂等失败: %s vs %s", again, newURL)
	}
}

// dnsPassthroughConfig：无 dns → 原样返回（no-op）；豁免档 config（远端 URL）→ 原样；
// env off → 原样且不落任何派生文件
func TestDNSPassthroughConfigNoopPaths(t *testing.T) {
	s, _, rwPath := newDNSServer(t)
	if u, rb := s.dnsPassthroughConfig("ss://x@1.2.3.4:1#n\n", rwPath); u != rwPath || rb != nil {
		t.Errorf("无 dns 应原样: %s %v", u, rb)
	}
	if u, rb := s.dnsPassthroughConfig(clashCoreSub, "https://remote.example/preset.ini"); u != "https://remote.example/preset.ini" || rb != nil {
		t.Errorf("豁免档远端 config 应原样: %s %v", u, rb)
	}
	off := *s
	off.cfg.passThroughDNS = false
	if u, rb := off.dnsPassthroughConfig(clashCoreSub, rwPath); u != rwPath || rb != nil {
		t.Errorf("env off 应原样: %s %v", u, rb)
	}
	if _, err := os.Stat(filepath.Join(off.cfg.dataDir, "derived")); !os.IsNotExist(err) {
		t.Error("env off 不应产生派生目录")
	}
}

// /s/ 单段链（surge）：dns on → 引擎收到 url=内部回环（代拉）+ config=共享 dns ini；
// dns ini 可从 sidecar 回环拉回（引擎真实取用路径）
func TestShortLinkSingleStageDNS(t *testing.T) {
	airport := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(clashCoreSub))
	}))
	defer airport.Close()

	var gotURL, gotConfig string
	ext := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.Query().Get("url")
		gotConfig = r.URL.Query().Get("config")
		_, _ = w.Write([]byte("[General]\ndns-server = 1.2.3.4\n"))
	}))
	defer ext.Close()

	cfg := testConfig(t, ext.URL)
	cfg.presetURL = defaultPresetURL
	cfg.passThroughDNS = true
	cfg.engineTplPath = filepath.Join("testdata", "all_base.tpl")
	cfg.engineStashPath = filepath.Join("testdata", "stash.yaml")
	s := mustServer(t, cfg)
	m := newTestManager(t)
	m.presets = []presetEntry{{"default", "ACL4SSR 默认", defaultPresetURL}}
	s.snap = m
	writeTB(t, filepath.Join(m.rulesDir, "ACL4SSR_Online.ini"), "[custom]\nruleset=DIRECT,[]GEOIP,CN\n")
	m.rewriteAndSwap(m.presets[0], "", false, time.Now())
	rwPath, _ := m.rewrittenPath(m.presets[0])

	ts := httptest.NewServer(s.routes())
	defer ts.Close()
	s.cfg.sidecarBase = ts.URL
	code := newLink(t, s, airport.URL+"/sub", "surge", "default", rwPath)

	resp := get(t, ts.URL+"/s/"+code)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "[General]") {
		t.Fatalf("单段输出异常: %d %s", resp.StatusCode, body)
	}
	if !strings.HasPrefix(gotURL, ts.URL+"/internal/sub/") {
		t.Errorf("单段 url 应为代拉回环: %s", gotURL)
	}
	if !strings.Contains(gotConfig, "/internal/derived/dns-default-") {
		t.Errorf("config 应为共享 dns ini: %s", gotConfig)
	}
	// dns ini 可回环拉回且含 rule_base 行（引擎真实取用路径）
	stem := strings.TrimSuffix(filepath.Base(gotConfig), ".ini")
	iniResp := get(t, ts.URL+"/internal/derived/"+stem+".ini")
	iniBody, _ := io.ReadAll(iniResp.Body)
	iniResp.Body.Close()
	if !strings.Contains(string(iniBody), "surge_rule_base=") {
		t.Errorf("共享 ini 回环内容缺 rule_base:\n%s", iniBody)
	}
	tplResp := get(t, ts.URL+"/internal/derived/"+stem+".tpl")
	tplBody, _ := io.ReadAll(tplResp.Body)
	tplResp.Body.Close()
	if !strings.Contains(string(tplBody), "doh.airport-example.com") {
		t.Errorf("派生 tpl 回环内容缺机场 dns:\n%s", tplBody)
	}
}

// /s/ 单段链：PASS_THROUGH_DNS=off → url=原始订阅 URL、config=原档位路径（逐字节回退）
func TestShortLinkDNSOffFallback(t *testing.T) {
	airport := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(clashCoreSub))
	}))
	defer airport.Close()

	var gotURL, gotConfig string
	ext := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.Query().Get("url")
		gotConfig = r.URL.Query().Get("config")
		_, _ = w.Write([]byte("[General]\n"))
	}))
	defer ext.Close()

	cfg := testConfig(t, ext.URL)
	cfg.presetURL = defaultPresetURL
	cfg.passThroughDNS = false
	s := mustServer(t, cfg)
	m := newTestManager(t)
	m.presets = []presetEntry{{"default", "ACL4SSR 默认", defaultPresetURL}}
	s.snap = m
	writeTB(t, filepath.Join(m.rulesDir, "ACL4SSR_Online.ini"), "[custom]\nruleset=DIRECT,[]GEOIP,CN\n")
	m.rewriteAndSwap(m.presets[0], "", false, time.Now())
	rwPath, _ := m.rewrittenPath(m.presets[0])

	ts := httptest.NewServer(s.routes())
	defer ts.Close()
	s.cfg.sidecarBase = ts.URL
	code := newLink(t, s, airport.URL+"/sub", "surge", "default", rwPath)

	resp := get(t, ts.URL+"/s/"+code)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("off 单段状态 = %d", resp.StatusCode)
	}
	if gotURL != airport.URL+"/sub" {
		t.Errorf("off 时 url 应为原始订阅 URL: %s", gotURL)
	}
	if gotConfig != rwPath {
		t.Errorf("off 时 config 应为原档位路径: %s", gotConfig)
	}
	if _, err := os.Stat(filepath.Join(s.cfg.dataDir, "derived")); !os.IsNotExist(err) {
		t.Error("off 时不应产生派生文件")
	}
}

// /s/ 两段式（clash）+ dns：第二段 config= 指共享 dns ini；mixed 第一段 config 恒空
func TestShortLinkTwoStageDNS(t *testing.T) {
	airport := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(clashCoreSub))
	}))
	defer airport.Close()

	var gotConfigs []string
	ext := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		gotConfigs = append(gotConfigs, q.Get("config"))
		switch q.Get("target") {
		case "mixed":
			content, err := fetchBody(q.Get("url"))
			if err != nil {
				http.Error(w, err.Error(), 502)
				return
			}
			_, _ = w.Write([]byte(base64.StdEncoding.EncodeToString(content)))
		case "clash":
			_, _ = w.Write([]byte("proxies:\n  - name: n\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ext.Close()

	cfg := testConfig(t, ext.URL)
	cfg.presetURL = defaultPresetURL
	cfg.passThroughDNS = true
	cfg.engineTplPath = filepath.Join("testdata", "all_base.tpl")
	cfg.engineStashPath = filepath.Join("testdata", "stash.yaml")
	s := mustServer(t, cfg)
	m := newTestManager(t)
	m.presets = []presetEntry{{"default", "ACL4SSR 默认", defaultPresetURL}}
	s.snap = m
	writeTB(t, filepath.Join(m.rulesDir, "ACL4SSR_Online.ini"), "[custom]\nruleset=DIRECT,[]GEOIP,CN\n")
	m.rewriteAndSwap(m.presets[0], "", false, time.Now())
	rwPath, _ := m.rewrittenPath(m.presets[0])

	ts := httptest.NewServer(s.routes())
	defer ts.Close()
	s.cfg.sidecarBase = ts.URL
	code := newLink(t, s, airport.URL+"/sub", "clash", "default", rwPath)

	resp := get(t, ts.URL+"/s/"+code)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "proxies:") {
		t.Fatalf("两段式输出异常: %d %s", resp.StatusCode, body)
	}
	if len(gotConfigs) != 2 {
		t.Fatalf("引擎调用数 = %d", len(gotConfigs))
	}
	if gotConfigs[0] != "" {
		t.Errorf("mixed 第一段 config 应空: %s", gotConfigs[0])
	}
	if !strings.Contains(gotConfigs[1], "/internal/derived/dns-default-") {
		t.Errorf("第二段 config 应为共享 dns ini: %s", gotConfigs[1])
	}
}

// groups + dns 同链叠加：{code}.ini 同时含组行与 rule_base 行（一根派生 ini 承载）
func TestShortLinkGroupsDNSCombined(t *testing.T) {
	airport := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(clashCoreSub))
	}))
	defer airport.Close()

	var gotConfig string
	ext := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("target") == "mixed" {
			content, err := fetchBody(q.Get("url"))
			if err != nil {
				http.Error(w, err.Error(), 502)
				return
			}
			_, _ = w.Write([]byte(base64.StdEncoding.EncodeToString(content)))
			return
		}
		gotConfig = q.Get("config")
		_, _ = w.Write([]byte("proxies:\n  - name: n\n"))
	}))
	defer ext.Close()

	cfg := testConfig(t, ext.URL)
	cfg.presetURL = defaultPresetURL
	cfg.passThroughDNS = true
	cfg.engineTplPath = filepath.Join("testdata", "all_base.tpl")
	cfg.engineStashPath = filepath.Join("testdata", "stash.yaml")
	s := mustServer(t, cfg)
	m := newTestManager(t)
	m.presets = []presetEntry{{"default", "ACL4SSR 默认", defaultPresetURL}}
	s.snap = m
	writeTB(t, filepath.Join(m.rulesDir, "ACL4SSR_Online.ini"),
		"[custom]\ncustom_proxy_group=`T`select`[]DIRECT`\nruleset=DIRECT,[]GEOIP,CN\n")
	m.rewriteAndSwap(m.presets[0], "", false, time.Now())
	rwPath, _ := m.rewrittenPath(m.presets[0])

	ts := httptest.NewServer(s.routes())
	defer ts.Close()
	s.cfg.sidecarBase = ts.URL
	groupsJSON := `[{"name":"专线","keywords":["机场"],"urlTest":false}]`
	code, err := s.store.getOrCreateLink(airport.URL+"/sub", "clash", "default", rwPath, groupsJSON, "")
	if err != nil {
		t.Fatal(err)
	}

	resp := get(t, ts.URL+"/s/"+code)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("groups+dns 链状态 = %d %s", resp.StatusCode, body)
	}
	if !strings.HasPrefix(gotConfig, ts.URL+"/internal/derived/") || strings.HasSuffix(gotConfig, ".ini") {
		t.Fatalf("config 应为 {code} 无后缀派生 URL（存量形态）: %s", gotConfig)
	}
	// {code}.ini 内组行 + rule_base 行同在
	name := strings.TrimPrefix(gotConfig, ts.URL+"/internal/derived/")
	data, err := os.ReadFile(filepath.Join(s.cfg.dataDir, "derived", name+".ini"))
	if err != nil {
		t.Fatal(err)
	}
	str := string(data)
	if !strings.Contains(str, "custom_proxy_group=专线`select`(?=.*机场)") {
		t.Errorf("派生 ini 缺组行:\n%s", str)
	}
	for _, key := range []string{"clash_rule_base=", "quanx_rule_base=", "stash_rule_base="} {
		if !strings.Contains(str, key) {
			t.Errorf("派生 ini 缺 %s 行:\n%s", key, str)
		}
	}
}

// /s/ 单段（surge）带组链 + 机场无 dns（base64 节点列表）：dns 无产出时组行派生仍必须
// 发生（回归：曾因派生嵌在 dns 条件内，无 dns 机场的带组单段链静默丢组）
func TestShortLinkSingleStageGroupsNoDNS(t *testing.T) {
	airport := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(base64.StdEncoding.EncodeToString([]byte("ss://x@1.2.3.4:1#n\n"))))
	}))
	defer airport.Close()

	var gotURL, gotConfig string
	ext := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.Query().Get("url")
		gotConfig = r.URL.Query().Get("config")
		_, _ = w.Write([]byte("[General]\n"))
	}))
	defer ext.Close()

	cfg := testConfig(t, ext.URL)
	cfg.presetURL = defaultPresetURL
	cfg.passThroughDNS = true
	cfg.engineTplPath = filepath.Join("testdata", "all_base.tpl")
	cfg.engineStashPath = filepath.Join("testdata", "stash.yaml")
	s := mustServer(t, cfg)
	m := newTestManager(t)
	m.presets = []presetEntry{{"default", "ACL4SSR 默认", defaultPresetURL}}
	s.snap = m
	writeTB(t, filepath.Join(m.rulesDir, "ACL4SSR_Online.ini"),
		"[custom]\ncustom_proxy_group=`T`select`[]DIRECT`\nruleset=DIRECT,[]GEOIP,CN\n")
	m.rewriteAndSwap(m.presets[0], "", false, time.Now())
	rwPath, _ := m.rewrittenPath(m.presets[0])

	ts := httptest.NewServer(s.routes())
	defer ts.Close()
	s.cfg.sidecarBase = ts.URL
	groupsJSON := `[{"name":"专线","keywords":["机场"],"urlTest":false}]`
	code, err := s.store.getOrCreateLink(airport.URL+"/sub", "surge", "default", rwPath, groupsJSON, "")
	if err != nil {
		t.Fatal(err)
	}

	resp := get(t, ts.URL+"/s/"+code)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("无 dns 带组单段链状态 = %d %s", resp.StatusCode, body)
	}
	if !strings.HasPrefix(gotURL, ts.URL+"/internal/sub/") {
		t.Errorf("单段 url 应为代拉回环: %s", gotURL)
	}
	if gotConfig != ts.URL+"/internal/derived/"+code {
		t.Fatalf("config 应为 {code} 派生 URL（存量形态）: %s", gotConfig)
	}
	data, err := os.ReadFile(filepath.Join(s.cfg.dataDir, "derived", code+".ini"))
	if err != nil {
		t.Fatal(err)
	}
	str := string(data)
	if !strings.Contains(str, "custom_proxy_group=专线`select`(?=.*机场)") {
		t.Errorf("派生 ini 缺组行:\n%s", str)
	}
	if strings.Contains(str, "rule_base=") {
		t.Errorf("无 dns 时派生 ini 不应含 rule_base 行:\n%s", str)
	}
}

// /s/ 单段（surge）带组链 + 代拉失败（airport 500，fail-open 回退引擎自拉）：url 回退
// 原始订阅，组行派生仍必须发生（回归：同上，派生曾被嵌进 dns 成功分支）
func TestShortLinkSingleStageGroupsFetchFail(t *testing.T) {
	airport := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer airport.Close()

	var gotURL, gotConfig string
	ext := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.Query().Get("url")
		gotConfig = r.URL.Query().Get("config")
		_, _ = w.Write([]byte("[General]\n"))
	}))
	defer ext.Close()

	cfg := testConfig(t, ext.URL)
	cfg.presetURL = defaultPresetURL
	cfg.passThroughDNS = true
	cfg.engineTplPath = filepath.Join("testdata", "all_base.tpl")
	cfg.engineStashPath = filepath.Join("testdata", "stash.yaml")
	s := mustServer(t, cfg)
	m := newTestManager(t)
	m.presets = []presetEntry{{"default", "ACL4SSR 默认", defaultPresetURL}}
	s.snap = m
	writeTB(t, filepath.Join(m.rulesDir, "ACL4SSR_Online.ini"),
		"[custom]\ncustom_proxy_group=`T`select`[]DIRECT`\nruleset=DIRECT,[]GEOIP,CN\n")
	m.rewriteAndSwap(m.presets[0], "", false, time.Now())
	rwPath, _ := m.rewrittenPath(m.presets[0])

	ts := httptest.NewServer(s.routes())
	defer ts.Close()
	s.cfg.sidecarBase = ts.URL
	groupsJSON := `[{"name":"专线","keywords":["机场"],"urlTest":false}]`
	code, err := s.store.getOrCreateLink(airport.URL+"/sub", "surge", "default", rwPath, groupsJSON, "")
	if err != nil {
		t.Fatal(err)
	}

	resp := get(t, ts.URL+"/s/"+code)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("代拉失败带组单段链状态 = %d %s", resp.StatusCode, body)
	}
	if gotURL != airport.URL+"/sub" {
		t.Errorf("代拉失败 url 应回退原始订阅: %s", gotURL)
	}
	if gotConfig != ts.URL+"/internal/derived/"+code {
		t.Fatalf("config 应为 {code} 派生 URL（组行不能因代拉失败丢失）: %s", gotConfig)
	}
	data, err := os.ReadFile(filepath.Join(s.cfg.dataDir, "derived", code+".ini"))
	if err != nil {
		t.Fatal(err)
	}
	str := string(data)
	if !strings.Contains(str, "custom_proxy_group=专线`select`(?=.*机场)") {
		t.Errorf("派生 ini 缺组行:\n%s", str)
	}
	if strings.Contains(str, "rule_base=") {
		t.Errorf("代拉失败不应产出 rule_base 行:\n%s", str)
	}
}

// preview 探测（非两段式 target）与拉取同路：代拉回环 url + 共享 dns ini config
func TestPreviewProbeSingleStageDNS(t *testing.T) {
	airport := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(clashCoreSub))
	}))
	defer airport.Close()

	var gotURL, gotConfig string
	ext := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.Query().Get("url")
		gotConfig = r.URL.Query().Get("config")
		_, _ = w.Write([]byte("[General]\ndns-server = 1.2.3.4\n"))
	}))
	defer ext.Close()

	cfg := testConfig(t, ext.URL)
	cfg.presetURL = defaultPresetURL
	cfg.passThroughDNS = true
	cfg.engineTplPath = filepath.Join("testdata", "all_base.tpl")
	cfg.engineStashPath = filepath.Join("testdata", "stash.yaml")
	s := mustServer(t, cfg)
	m := newTestManager(t)
	m.presets = []presetEntry{{"default", "ACL4SSR 默认", defaultPresetURL}}
	s.snap = m
	writeTB(t, filepath.Join(m.rulesDir, "ACL4SSR_Online.ini"), "[custom]\nruleset=DIRECT,[]GEOIP,CN\n")
	m.rewriteAndSwap(m.presets[0], "", false, time.Now())

	ts := httptest.NewServer(s.routes())
	defer ts.Close()
	s.cfg.sidecarBase = ts.URL

	pr := s.probeConvert(airport.URL+"/sub", "surge", s.engineConfig(s.presets[0]))
	if !pr.ok {
		t.Fatalf("探测应绿: %+v", pr)
	}
	if !strings.HasPrefix(gotURL, ts.URL+"/internal/sub/") {
		t.Errorf("探测 url 应为代拉回环: %s", gotURL)
	}
	if !strings.Contains(gotConfig, "/internal/derived/dns-default-") {
		t.Errorf("探测 config 应为共享 dns ini: %s", gotConfig)
	}
}

// /internal/derived 后缀扩展：dns-*.tpl/.yaml 可拉（引擎 rule_base 回环取用路径）、
// 既有 {code} 无后缀形态兼容、非白名单后缀 404
func TestInternalDerivedSuffix(t *testing.T) {
	s, _, rwPath := newDNSServer(t)
	ts := httptest.NewServer(s.routes())
	defer ts.Close()
	s.cfg.sidecarBase = ts.URL // 先把回环基址指向真实测试 server，派生 URL 才可拉
	_, rb := s.dnsPassthroughConfig(clashCoreSub, rwPath)
	if len(rb) == 0 {
		t.Fatal("派生未生效")
	}

	for _, line := range rb {
		u := strings.TrimPrefix(line, strings.SplitN(line, "=", 2)[0]+"=")
		resp := get(t, u)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || len(body) == 0 {
			t.Errorf("派生物回环拉取失败 %s: %d", u, resp.StatusCode)
		}
	}
	// 既有 {code} 无后缀形态（存量 URL 兼容）
	writeTB(t, filepath.Join(s.cfg.dataDir, "derived", "AbCd1234.ini"), "[custom]\n")
	resp := get(t, ts.URL+"/internal/derived/AbCd1234")
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("无后缀 {code} 形态应兼容: %d", resp.StatusCode)
	}
	// 非白名单后缀 / 路径穿越拒绝
	for _, p := range []string{"/internal/derived/x.exe", "/internal/derived/dns-a-b.txt", "/internal/derived/../state.db"} {
		resp := get(t, ts.URL+p)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s 应 404: %d", p, resp.StatusCode)
		}
	}
}

// 日志卫生（D6 延伸）：dns_passthrough 日志不得含订阅 URL 或机场 dns 服务器地址
// （只记 preset/stem/数量摘要）
func TestDNSPassthroughLogSanitized(t *testing.T) {
	s, _, rwPath := newDNSServer(t)
	var buf syncBuffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	s.dnsPassthroughConfig(clashCoreSub, rwPath)
	out := buf.String()
	if !strings.Contains(out, "dns_passthrough=applied") {
		t.Fatalf("应有 applied 日志: %q", out)
	}
	if strings.Contains(out, "airport-example.com") || strings.Contains(out, "10.0.0.53") {
		t.Errorf("日志泄露 dns 服务器: %q", out)
	}
	if strings.Contains(out, "http://") && !strings.Contains(out, "sidecar.test/internal") {
		t.Errorf("日志含意外 URL: %q", out)
	}
}

// syncBuffer 串行测试用 log 缓冲（dns 日志单 goroutine 写，无需锁）
type syncBuffer struct {
	b strings.Builder
}

func (sb *syncBuffer) Write(p []byte) (int, error) { return sb.b.Write(p) }
func (sb *syncBuffer) String() string              { return sb.b.String() }
