// 机场 DNS 透传（v1.7.0）：机场订阅自带 dns 信息时，转换产物带上机场指定的 DNS，
// 解决「拉到节点但节点域名解析不对、连不通」。设计定案见 docs/designs/dns-passthrough.md
// （含闸门实验记录：rule_base 只认 http、QX 键名是 quanx_rule_base、stash base 是纯 YAML 等）。
//
// 机制（闸门 G1–G3 实证）：external config ini [custom] 段挂 rule_base 行 → 引擎以派生
// base 渲染输出，dns 部分整块来自派生 base。派生物三件套落 {DATA_DIR}/derived/：
//   dns-{preset}-{hash}.tpl  —— 引擎 all_base.tpl 改写（clash 段嵌机场 dns YAML 块、
//                                quanx/surge/loon 段嵌翻译行，四家共存互不干扰）
//   dns-{preset}-{hash}.yaml —— 引擎 stash.yaml 改写（顶层 dns: 块替换；stash base
//                                必须是纯 YAML，喂 tpl 引擎 400）
//   dns-{preset}-{hash}.ini  —— 档位快照 ini + rule_base 行（无组链的 config= 直用；
//                                带 groups/exclude 的链由 {code}.ini 叠加同一组行）
// 与 code 无关（内容只依赖 机场dns×档位），多链共享幂等覆盖写。
//
// 失败语义（与 groups 派生的 fail-closed 相反）：dns 透传是尽力增强——提取失败 /
// 模板定位失败 / 写入自检失败一律 no-op 回退原 config（fail-open + 脱敏日志一行），
// 绝不因透传挂掉拉取。
package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// 引擎默认 base 模板落位（闸门 G2 实测：引擎 cwd=/base、pref base_path="base"、
// clash_rule_base="base/all_base.tpl" → 绝对路径；测试经 config 注入 testdata）
const (
	defaultEngineTplPath   = "/base/base/all_base.tpl"
	defaultEngineStashPath = "/base/base/stash.yaml"
)

// dnsServer 解析出的单个解析服务器（翻译 QX/Surge/Loon 用）。只认两类形态：
// https://…（DoH，跨家可翻）与纯 IP[:port]（plain UDP）；tls:// / quic:// / system
// 等其余形态 QX/Surge 无对应语法，跳过不翻（宁漏勿坏——塞进客户端不认的语法会整份拒载）
type dnsServer struct {
	addr string
	doh  bool
}

// airportDNS 提取产物：yamlBlock = 机场 dns YAML 块原文（顶层 dns: 起整块保真持有，
// clash 系透传时不解释语义原样回写）；servers = 供翻译的解析服务器列表（QX/Surge/
// Loon 格式所限只能翻服务器，模式类字段不跨家）。两者可独立为空：机场只给原生
// 格式时 yamlBlock 空（clash 系 no-op）；机场 dns 块无 nameserver 时 servers 空
type airportDNS struct {
	yamlBlock []byte
	servers   []dnsServer
}

func (a airportDNS) empty() bool { return len(a.yamlBlock) == 0 && len(a.servers) == 0 }

// extractAirportDNS 内容嗅探提取（G4 定案：不按 UA 硬走——机场按 UA 变格式的行为
// 无法穷举，内容本身才是权威）。顺序：整体 base64 节点列表（无 dns，no-op）→
// clash YAML dns: 块（最常见，且信息最全）→ QX 原生 [dns] 段 → Surge 原生 [General]
// dns 行。互相排斥（格式互异），先中先得
func extractAirportDNS(content []byte) airportDNS {
	raw := strings.TrimSpace(string(content))
	// 对齐 parseNodeURIList 的 base64 嗅探口径：base64 节点列表订阅解码后用解码值
	if decoded, err := base64StrictDecode(raw); err == nil && strings.Contains(decoded, "://") {
		raw = decoded
	}
	if ad, ok := extractClashDNSBlock(raw); ok {
		return ad
	}
	if servers := extractQXDnsSection(raw); len(servers) > 0 {
		return airportDNS{servers: servers}
	}
	if servers := extractSurgeDnsLines(raw); len(servers) > 0 {
		return airportDNS{servers: servers}
	}
	return airportDNS{}
}

func base64StrictDecode(s string) (string, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// extractClashDNSBlock clash YAML 顶层 dns: 块切出（行级缩进解析，不引 YAML 库——
// 项目零第三方依赖纪律）：块 = dns: 行 + 后续所有缩进行（到下一个非空非缩进行止）。
// 顺带解析 nameserver 列表作翻译输入（default-nameserver 是 bootstrap、
// proxy-server-nameserver 是节点侧，QX/Surge/Loon 无对应概念均不翻）
func extractClashDNSBlock(raw string) (airportDNS, bool) {
	lines := strings.Split(raw, "\n")
	idx := -1
	for i, ln := range lines {
		if strings.TrimRight(ln, " \t\r") == "dns:" {
			idx = i
			break
		}
	}
	if idx < 0 {
		return airportDNS{}, false
	}
	end := len(lines)
	for i := idx + 1; i < len(lines); i++ {
		t := lines[i]
		if strings.TrimSpace(t) == "" || strings.HasPrefix(strings.TrimLeft(t, " \t"), "#") {
			continue // 块内空行/注释不终止
		}
		if t[0] == ' ' || t[0] == '\t' {
			continue
		}
		end = i
		break
	}
	if end == idx+1 {
		return airportDNS{}, false // dns: 无子行（空块），透传无意义
	}
	block := strings.Join(lines[idx:end], "\n")
	return airportDNS{
		yamlBlock: []byte(block),
		servers:   parseNameservers(lines[idx+1 : end]),
	}, true
}

// parseNameservers 从 dns 块行集解析 nameserver 列表：定位 nameserver: 键后取其
// 序列项，直到键回浅或同级下一个键。YAML 序列两种合法缩进都认（闸门 G4 校准：
// 「- item 与键同级」与「- item 缩进一级」两种机场产出形态都常见）——
//   nameserver:        nameserver:
//   - a                - b（缩进风格）
// items 原样保留在 yamlBlock（透传侧不解释），这里只为翻译分类
func parseNameservers(blockLines []string) []dnsServer {
	// 找 nameserver: 键的缩进（精确键名——排除 nameserver-policy / default-nameserver /
	// proxy-server-nameserver 等带前后缀键）
	nsIndent, nsIdx := -1, -1
	for i, ln := range blockLines {
		t := strings.TrimRight(ln, " \t\r")
		trimmed := strings.TrimLeft(t, " \t")
		if trimmed != "nameserver:" {
			continue
		}
		nsIndent = len(t) - len(trimmed)
		nsIdx = i
		break
	}
	if nsIdx < 0 {
		return nil
	}
	var servers []dnsServer
	for _, ln := range blockLines[nsIdx+1:] {
		t := strings.TrimRight(ln, " \t\r")
		trimmed := strings.TrimLeft(t, " \t")
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(t) - len(trimmed)
		if indent < nsIndent {
			break // 键回浅：nameserver 区结束
		}
		isItem := trimmed == "-" || strings.HasPrefix(trimmed, "- ")
		if indent == nsIndent && !isItem {
			break // 同级下一个键（proxy-server-nameserver 等）：序列结束
		}
		if !isItem {
			continue // 更深缩进的嵌套 map 键行（nameserver-policy 子键等）：不收不终止
		}
		item := strings.Trim(strings.TrimSpace(strings.TrimPrefix(trimmed, "-")), `"'`)
		if strings.HasPrefix(item, "https://") {
			servers = append(servers, dnsServer{addr: item, doh: true})
		} else if isPlainIPOrHostPort(item) {
			servers = append(servers, dnsServer{addr: item})
		}
	}
	return servers
}

// isPlainIPOrHostPort plain DNS 形态（IP / IP:port / 域名[:port]）；tls:// 等带 scheme
// 前缀的不算（QX/Surge 无对应语法，跳过）
var hostPortRe = regexp.MustCompile(`^[0-9A-Za-z.\[\]-]+(:[0-9]+)?$`)

func isPlainIPOrHostPort(s string) bool {
	return hostPortRe.MatchString(s)
}

// extractQXDnsSection QX 原生 [dns] 段：server= 行（plain，每行一个）与
// doh-server= 行（DoH，单行逗号分隔多个）——机场按 UA 给 QX 原生格式的场景
func extractQXDnsSection(raw string) []dnsServer {
	return scanSectionServers(raw, "[dns]", true)
}

// extractSurgeDnsLines Surge 原生 [General] 段的 dns-server=/doh-server= 行
// （逗号分隔多值）——机场按 UA 给 Surge 原生格式的场景
func extractSurgeDnsLines(raw string) []dnsServer {
	return scanSectionServers(raw, "[general]", false)
}

// scanSectionServers 通用段扫描：段内 server=/dns-server= 行 → plain、
// doh-server= 行 → doh（逗号拆分）；qxStyle=true 时 server= 单值行也是 plain
func scanSectionServers(raw, section string, qxStyle bool) []dnsServer {
	lines := strings.Split(raw, "\n")
	inSection := false
	var servers []dnsServer
	for _, ln := range lines {
		t := strings.TrimSpace(strings.TrimRight(ln, "\r"))
		if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
			inSection = strings.EqualFold(t, section)
			continue
		}
		if !inSection {
			continue
		}
		key, val, ok := strings.Cut(t, "=")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		val = strings.TrimSpace(val)
		switch key {
		case "server", "dns-server":
			if val == "" {
				continue
			}
			// QX 的 server= 单值；Surge 的 dns-server= 逗号多值——逗号拆分两者通吃
			for _, item := range splitDnsList(val) {
				if isPlainIPOrHostPort(item) {
					servers = append(servers, dnsServer{addr: item})
				}
			}
		case "doh-server":
			for _, item := range splitDnsList(val) {
				if strings.HasPrefix(item, "https://") {
					servers = append(servers, dnsServer{addr: item, doh: true})
				}
			}
		}
	}
	return servers
}

// splitDnsList 逗号分隔的服务器列表拆分（trim 空白；空项丢弃）
func splitDnsList(val string) []string {
	var out []string
	for _, item := range strings.Split(val, ",") {
		item = strings.TrimSpace(item)
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}

// translateQXDnsLines 翻译为 QX [dns] 段行集（闸门 G4 定稿口径）：plain →
// server=<addr>（每行一个）；DoH → doh-server=<u1>, <u2>（单行逗号分隔，官方 wiki
// 口径）。两类各有才出行（全 DoH 机场不产 server= 行、反之亦然）
func translateQXDnsLines(servers []dnsServer) []string {
	return translateDnsLines(servers, "server=", "doh-server=", false)
}

// translateGeneralDnsLines 翻译为 Surge/Loon [General] 段行集：dns-server = a, b
// （plain）+ doh-server = u1, u2（DoH）。doh-suffix 机场语义未知不造。Surge/Loon
// 键名相同形态通吃（闸门 G3 实证两家模板行格式一致）
func translateGeneralDnsLines(servers []dnsServer) []string {
	return translateDnsLines(servers, "dns-server = ", "doh-server = ", true)
}

func translateDnsLines(servers []dnsServer, plainKey, dohKey string, spaceAfterKey bool) []string {
	var plain, doh []string
	for _, s := range servers {
		if s.doh {
			doh = append(doh, s.addr)
		} else {
			plain = append(plain, s.addr)
		}
	}
	var out []string
	if len(plain) > 0 {
		if spaceAfterKey {
			out = append(out, plainKey+strings.Join(plain, ", "))
		} else {
			for _, p := range plain {
				out = append(out, plainKey+p)
			}
		}
	}
	if len(doh) > 0 {
		out = append(out, dohKey+strings.Join(doh, ", "))
	}
	return out
}

// tplSegment 引擎混合模板（all_base.tpl）里的一个 target 段区间（行下标 [start,end]，
// 闭区间；含 {% if request.target == "x" %} 段头行与配对 {% endif %} 行）
type tplSegment struct {
	target string
	start  int
	end    int
}

// splitTplSegments 按深度剖析混合模板的 target 段（all_base.tpl 是 ini/YAML/JSON
// 多格式混合体，各家输出由 {% if request.target == "xxx" %} 分段；段内还有嵌套
// {% if %}（如 clash dns 条件块），深度计数穿透）
func splitTplSegments(tpl string) map[string]tplSegment {
	segments := map[string]tplSegment{}
	lines := strings.Split(tpl, "\n")
	depth := 0
	segTarget := ""
	segStart := -1
	for i, ln := range lines {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "{% if") {
			depth++
			if depth == 1 {
				segTarget = parseTargetIf(t)
				segStart = i
			}
			continue
		}
		if strings.HasPrefix(t, "{% endif %}") {
			if depth == 1 && segTarget != "" {
				segments[segTarget] = tplSegment{target: segTarget, start: segStart, end: i}
			}
			depth--
			continue
		}
	}
	return segments
}

// parseTargetIf 段头行取 target 名：{% if request.target == "clash" or request.target
// == "clashr" %} → "clash"（取首个；clashr 段与 clash 同段共用，闸门实测两者同模板段）
func parseTargetIf(t string) string {
	m := targetIfRe.FindStringSubmatch(t)
	if m == nil {
		return ""
	}
	return m[1]
}

var targetIfRe = regexp.MustCompile(`request\.target\s*==\s*"([a-z]+)"`)

// clashDNSIfPrefix 引擎模板 clash 段 dns 条件块头（闸门 G1：默认模板的 dns 块包在
// 条件里且默认关——透传必须去条件整块替换；该行形态是上游模板稳定特征）
const clashDNSIfPrefix = `{% if default(request.clash.dns, "") == "1" %}`

// deriveDNSBases 生成两份派生 base。返回 ok=false 表示无可派生或自检失败（上游大改版）
// ——调用方 no-op（fail-open）。各家独立降级（评审 F2）：clash 段/条件块定位失败只跳过
// clash 替换，quanx/surge/loon/stash 单家结构漂移只回退该家（该家输出维持模板默认 =
// 现状行为），不连坐其他家族；yamlBlock 空则 clash 段与 stash 不动，servers 空则
// quanx/surge/loon 段不动
func deriveDNSBases(tpl, stashYaml []byte, ad airportDNS) (tplOut, stashOut []byte, ok bool) {
	if ad.empty() {
		return nil, nil, false
	}
	lines := strings.Split(string(tpl), "\n")

	// clash 段：dns 条件块整块替换为机场 yamlBlock（无条件渲染）
	clashDone := false
	if len(ad.yamlBlock) > 0 {
		if seg, found := splitTplSegments(string(tpl))["clash"]; found {
			ifStart, ifEnd := -1, -1
			for i := seg.start; i <= seg.end; i++ {
				t := strings.TrimSpace(lines[i])
				if strings.HasPrefix(t, clashDNSIfPrefix) && ifStart < 0 {
					ifStart = i
					continue
				}
				if ifStart >= 0 && strings.HasPrefix(t, "{% endif %}") {
					ifEnd = i
					break
				}
			}
			if ifStart >= 0 && ifEnd >= 0 {
				block := strings.Split(string(ad.yamlBlock), "\n")
				if len(block) > 0 && block[len(block)-1] == "" {
					block = block[:len(block)-1]
				}
				lines = append(lines[:ifStart], append(block, lines[ifEnd+1:]...)...)
				clashDone = true
			}
		}
	}

	// quanx / surge / loon 段：翻译行替换（servers 空则跳过）。单家漂移（无段/无段头）
	// 只降级该家，其余家照常透传
	qxApplied, genApplied := false, false
	if len(ad.servers) > 0 {
		qxLines := translateQXDnsLines(ad.servers)
		genLines := translateGeneralDnsLines(ad.servers)
		if len(qxLines) > 0 {
			if out, err := replaceSectionBody(lines, "quanx", "[dns]", qxLines); err == nil {
				lines, qxApplied = out, true
			}
		}
		for _, target := range []string{"surge", "loon"} {
			if out, applied, err := replaceGeneralDnsLines(lines, target, genLines); err == nil && applied {
				lines, genApplied = out, true
			}
		}
	}

	// stash：独立纯 YAML（闸门 G3：stash base 必须是 YAML mapping，tpl 会 400），
	// 顶层 dns: 块整体替换；定位失败只回退 stash（nil → 无 stash_rule_base 行）
	var stashResult []byte
	if len(stashYaml) > 0 && len(ad.yamlBlock) > 0 {
		if replaced, okStash := replaceTopLevelDNSBlock(stashYaml, ad.yamlBlock); okStash {
			stashResult = replaced
		}
	}

	if !clashDone && !qxApplied && !genApplied && stashResult == nil {
		return nil, nil, false // 全家族漂移：派生等价现状，无需产出
	}
	tplResult := []byte(strings.Join(lines, "\n"))
	if !selfCheckTpl(tpl, tplResult, ad, clashDone, qxApplied, genApplied) {
		return nil, nil, false
	}
	return tplResult, stashResult, true
}

// replaceSectionBody 替换 target 段内某 ini 段（如 quanx 的 [dns]）的段体：
// 段头行保留、段内行到下一个 [ 段头前全部换为 newLines
func replaceSectionBody(lines []string, target, section string, newLines []string) ([]string, error) {
	segments := splitTplSegments(strings.Join(lines, "\n"))
	seg, found := segments[target]
	if !found {
		return nil, fmt.Errorf("模板无 %s 段", target)
	}
	secStart := -1
	for i := seg.start; i <= seg.end; i++ {
		if strings.TrimSpace(lines[i]) == section {
			secStart = i
			break
		}
	}
	if secStart < 0 {
		return nil, fmt.Errorf("%s 段内无 %s 段头", target, section)
	}
	end := seg.end + 1
	for i := secStart + 1; i <= seg.end; i++ {
		t := strings.TrimSpace(lines[i])
		if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
			end = i
			break
		}
	}
	out := make([]string, 0, len(lines)+len(newLines))
	out = append(out, lines[:secStart+1]...)
	out = append(out, newLines...)
	out = append(out, lines[end:]...)
	return out, nil
}

// replaceGeneralDnsLines 替换 target 段（surge/loon）[General] 区内的 dns-server /
// doh-server 行。段内所有旧 dns/doh 行全删（多条、任意行序——doh-server 排在
// dns-server 之前的模板形态同样清干净，评审 F1：旧删除逻辑依赖正序会残留旧行），
// newLines 落在首个旧行位置；两键都定位不到则该家不动（applied=false，不视为失败，
// 其余家照常透传）
func replaceGeneralDnsLines(lines []string, target string, newLines []string) (out []string, applied bool, err error) {
	segments := splitTplSegments(strings.Join(lines, "\n"))
	seg, found := segments[target]
	if !found {
		return nil, false, fmt.Errorf("模板无 %s 段", target)
	}
	anchor := -1
	dead := map[int]bool{}
	for i := seg.start; i <= seg.end; i++ {
		lower := strings.ToLower(strings.TrimSpace(lines[i]))
		if strings.HasPrefix(lower, "dns-server") || strings.HasPrefix(lower, "doh-server") {
			dead[i] = true
			if anchor < 0 {
				anchor = i // 首个旧行位 = newLines 插入位
			}
		}
	}
	if anchor < 0 {
		return lines, false, nil // 模板无 dns 行（改版）：该家不动，不失败
	}
	out = make([]string, 0, len(lines)+len(newLines))
	out = append(out, lines[:anchor]...)
	out = append(out, newLines...)
	for i := anchor + 1; i < len(lines); i++ {
		if !dead[i] {
			out = append(out, lines[i])
		}
	}
	return out, true, nil
}

// replaceTopLevelDNSBlock 纯 YAML（stash base）顶层 dns: 块整体替换（缩进解析同
// extractClashDNSBlock 的块边界判定）
func replaceTopLevelDNSBlock(yaml, block []byte) ([]byte, bool) {
	lines := strings.Split(string(yaml), "\n")
	idx := -1
	for i, ln := range lines {
		if strings.TrimRight(ln, " \t\r") == "dns:" {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil, false
	}
	end := len(lines)
	for i := idx + 1; i < len(lines); i++ {
		t := lines[i]
		if strings.TrimSpace(t) == "" || strings.HasPrefix(strings.TrimLeft(t, " \t"), "#") {
			continue
		}
		if t[0] == ' ' || t[0] == '\t' {
			continue
		}
		end = i
		break
	}
	blockLines := strings.Split(string(block), "\n")
	if len(blockLines) > 0 && blockLines[len(blockLines)-1] == "" {
		blockLines = blockLines[:len(blockLines)-1]
	}
	out := make([]string, 0, len(lines)+len(blockLines))
	out = append(out, lines[:idx]...)
	out = append(out, blockLines...)
	out = append(out, lines[end:]...)
	return []byte(strings.Join(out, "\n")), true
}

// selfCheckTpl 派生 tpl 写盘前的结构自检（宁可漏透传，不可让引擎拉到坏 base）：
// 段数守恒（四家不丢段）+ 实际发生的替换内容必须在产物里（clash 块首行 / QX 与
// General 翻译行首行）。未发生的替换不检查——独立降级不连坐自检（评审 F1：旧行序
// 残留正是自检盲区，General 首行检查补上）
func selfCheckTpl(orig, out []byte, ad airportDNS, clashDone, qxApplied, genApplied bool) bool {
	for _, target := range []string{"clash", "surge", "loon", "quanx"} {
		if countTargetSegments(string(orig), target) != countTargetSegments(string(out), target) {
			return false
		}
	}
	if clashDone {
		first := strings.TrimSpace(strings.Split(string(ad.yamlBlock), "\n")[0])
		if first != "" && !strings.Contains(string(out), first) {
			return false
		}
	}
	if qxApplied {
		if lines := translateQXDnsLines(ad.servers); len(lines) > 0 && !strings.Contains(string(out), lines[0]) {
			return false
		}
	}
	if genApplied {
		if lines := translateGeneralDnsLines(ad.servers); len(lines) > 0 && !strings.Contains(string(out), lines[0]) {
			return false
		}
	}
	return true
}

func countTargetSegments(tpl string, target string) int {
	n := 0
	for _, seg := range splitTplSegments(tpl) {
		if seg.target == target {
			n++
		}
	}
	return n
}

// ruleBaseLines 派生 ext ini 的 rule_base 行集（闸门 G3 键名实测：QX 是
// quanx_rule_base 不是 quan_rule_base；stash 指向 .yaml 其余指向 .tpl；rule_base
// 只认 http URL，故值为回环 URL）。tplURL/yamlURL 为空则对应行省略（yamlBlock 空
// 时 stash/clash 无从透传）
func ruleBaseLines(tplURL, stashURL string) []string {
	var out []string
	if tplURL != "" {
		out = append(out,
			"clash_rule_base="+tplURL,
			"surge_rule_base="+tplURL,
			"loon_rule_base="+tplURL,
			"quanx_rule_base="+tplURL,
		)
	}
	if stashURL != "" {
		out = append(out, "stash_rule_base="+stashURL)
	}
	return out
}

// appendRuleBaseLines 把 rule_base 行插入 ext ini 的 [custom] 段内（引擎只认段内键；
// 插在段尾——下一段头之前）。无 [custom] 段（异常模板）返回错误（调用方 no-op）
func appendRuleBaseLines(ini []byte, lines []string) ([]byte, error) {
	if len(lines) == 0 {
		return ini, nil
	}
	src := strings.Split(string(ini), "\n")
	insertAt := -1
	inCustom := false
	for i, ln := range src {
		if name, ok := sectionName(strings.TrimSpace(ln)); ok {
			if inCustom && insertAt < 0 {
				insertAt = i // [custom] 段结束（下一段头前）
			}
			inCustom = name == "custom"
			continue
		}
	}
	if inCustom && insertAt < 0 {
		insertAt = len(src) // [custom] 是最后一段
	}
	if insertAt < 0 {
		return nil, fmt.Errorf("ini 无 [custom] 段")
	}
	out := make([]string, 0, len(src)+len(lines))
	out = append(out, src[:insertAt]...)
	out = append(out, lines...)
	out = append(out, src[insertAt:]...)
	return []byte(strings.Join(out, "\n")), nil
}

// dnsPassthroughConfig /s/ 拉取与 preview 探测共用的派生编排（两段式在第一段
// mixed 拿到代拉内容后调用；单段链在代拉后调用）：
//   - env off → 原样返回（逐字节回退现状）
//   - 提取无 dns / 档位无本地快照（豁免档）/ 派生自检失败 → 原样返回（fail-open + 日志）
//   - 成功 → 写/复用三件套，返回共享 ext ini 回环 URL 与 rule_base 行（供 {code}.ini 叠加）
//
// 返回的 rbLines 供带 groups/exclude 的链把同一组行追加进 {code}.ini（一根派生 ini
// 同时承载组行 + rule_base 行，设计定案的叠加共存）。
func (s *server) dnsPassthroughConfig(stage1Content, configURL string) (newConfigURL string, rbLines []string) {
	if !s.cfg.passThroughDNS {
		return configURL, nil
	}
	ad := extractAirportDNS([]byte(stage1Content))
	if ad.empty() {
		log.Printf("s/ dns_passthrough=absent（订阅无 dns 信息，no-op）")
		return configURL, nil
	}
	preset, ok := s.presetByConfigPath(configURL)
	if !ok {
		// 豁免档 / 远端 config（无本地快照 ini 作底）——rule_base 行没法叠加，跳过
		// （与自定义分组能力位同款边界）
		log.Printf("s/ dns_passthrough=skip（档位无本地快照，无法叠加 rule_base）")
		return configURL, nil
	}
	tpl, err := os.ReadFile(s.cfg.engineTplPath)
	if err != nil {
		log.Printf("s/ dns_passthrough=error（读引擎模板失败，no-op）: %v", err)
		return configURL, nil
	}
	var stashYaml []byte
	if _, err := os.Stat(s.cfg.engineStashPath); err == nil {
		stashYaml, _ = os.ReadFile(s.cfg.engineStashPath)
	}
	tplOut, stashOut, ok := deriveDNSBases(tpl, stashYaml, ad)
	if !ok {
		log.Printf("s/ dns_passthrough=error（派生自检失败或全家族模板漂移，no-op）")
		return configURL, nil
	}
	sum := sha256.Sum256(append(append([]byte{}, ad.yamlBlock...), []byte(fmt.Sprintf("%v", ad.servers))...))
	stem := "dns-" + preset.ID + "-" + hex.EncodeToString(sum[:8]) // 64 位：多机场内容碰撞不互相覆盖（评审 F3）
	dir := filepath.Join(s.cfg.dataDir, "derived")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("s/ dns_passthrough=error（创建 %s 失败，no-op）: %v", dir, err)
		return configURL, nil
	}
	base := s.cfg.sidecarBase + "/internal/derived/"
	tplURL := base + stem + ".tpl"
	if err := atomicWrite(filepath.Join(dir, stem+".tpl"), tplOut); err != nil {
		log.Printf("s/ dns_passthrough=error（写派生 tpl 失败，no-op）: %v", err)
		return configURL, nil
	}
	stashURL := ""
	if stashOut != nil {
		stashURL = base + stem + ".yaml"
		if err := atomicWrite(filepath.Join(dir, stem+".yaml"), stashOut); err != nil {
			log.Printf("s/ dns_passthrough=error（写派生 stash 失败，no-op）: %v", err)
			return configURL, nil
		}
	}
	rb := ruleBaseLines(tplURL, stashURL)
	// 共享 ext ini：档位快照 ini 全文 + rule_base 行（无组链直用；文件名同 stem 幂等覆盖，
	// 机场 dns 变 → hash 变 → 新文件，旧文件自然留档不清理，量级 = 3 档 × dns 版本数）
	iniSrc, err := os.ReadFile(configURL)
	if err != nil {
		log.Printf("s/ dns_passthrough=error（读档位快照失败，no-op）: %v", err)
		return configURL, nil
	}
	iniOut, err := appendRuleBaseLines(iniSrc, rb)
	if err != nil {
		log.Printf("s/ dns_passthrough=error（快照 ini 缺 [custom] 段，no-op）: %v", err)
		return configURL, nil
	}
	if err := atomicWrite(filepath.Join(dir, stem+".ini"), iniOut); err != nil {
		log.Printf("s/ dns_passthrough=error（写派生 ini 失败，no-op）: %v", err)
		return configURL, nil
	}
	log.Printf("s/ dns_passthrough=applied preset=%s dns=%s servers=%d", preset.ID, stem, len(ad.servers))
	return base + stem + ".ini", rb
}

// presetByConfigPath 按 config= 本地路径反查档位（dns 派生需要该档 rewritten ini
// 作 ext ini 的底；豁免档/远端 URL 不匹配）
func (s *server) presetByConfigPath(configURL string) (presetEntry, bool) {
	if !strings.HasPrefix(configURL, "/") {
		return presetEntry{}, false
	}
	if s.snap == nil {
		return presetEntry{}, false
	}
	for _, p := range s.presets {
		if s.presetExempt(p) {
			continue
		}
		if path, ok := s.snap.rewrittenPath(p); ok && path == configURL {
			return p, true
		}
	}
	return presetEntry{}, false
}
