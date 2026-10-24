// 规则快照本地化（v1.3.0）：三档预设 .ini 与其引用的全部规则集文件快照落
// {DATA_DIR}/rules/，sidecar 把 .ini 中规则集 GitHub URL 改写为本地绝对路径，
// 引擎读本地文件、对 GitHub 零网络依赖。分层：出厂层 /base/rules-snapshot（镜像
// 内置只读兜底）+ 运行时层 {DATA_DIR}/rules（volume 持久）。契约「规则快照（v1.3.0）」节。
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// 引擎本地读取硬限的路径树（闸门 2026-10-24 实测：canonical path 校验，
	// symlink 出树被拒、范围外 ruleset 静默丢弃——证据 /tmp/gate-probe.txt）
	engineBaseDir = "/base"
	// 出厂快照层（镜像构建时 COPY 进来；开发/单测环境可不存在）
	vendorSnapshotDir = "/base/rules-snapshot"
	// vendor 层拉取时间戳文件名（内容 RFC3339 或 unix 秒）
	snapshotMetaFile = "SNAPSHOT_META"
)

type snapshotState struct {
	lastRefreshedAt *time.Time // 当前所用快照获得时间：出厂复制时刻（SNAPSHOT_META）/ 运行时刷新成功时刻
	lastModifiedAt  *time.Time // 最近一次刷新成功时上游 .ini 的 Last-Modified 头（缺头为 nil）
}

type snapshotManager struct {
	mu        sync.Mutex // 只护 states
	dataDir   string
	rulesDir  string        // {dataDir}/rules
	vendorDir string        // 出厂层（测试注入）
	baseDir   string        // 引擎可读树根，默认 /base（测试注入）
	interval  time.Duration // 刷新周期（启动先刷一轮）
	client    *http.Client
	presets   []presetEntry             // 内置三档原始 URL（不含 PRESET_URL 覆盖值）
	exempt    func(presetEntry) bool    // 豁免档（PRESET_URL 覆盖的 default）不参与刷新/本地化；nil = 全不豁免
	states    map[string]*snapshotState // key = preset id
}

func newSnapshotManager(cfg config, exempt func(presetEntry) bool) *snapshotManager {
	interval := cfg.presetRefreshInterval
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	table := make([]presetEntry, len(presetTable))
	copy(table, presetTable)
	m := &snapshotManager{
		dataDir:   cfg.dataDir,
		rulesDir:  filepath.Join(cfg.dataDir, "rules"),
		vendorDir: vendorSnapshotDir,
		baseDir:   engineBaseDir,
		interval:  interval,
		// 单文件最大实测 1.4MB（BanEasyPrivacy），慢网络整轮拉取留足余量；
		// SNAPSHOT_PROXY 非空时走代理（契约「规则快照」env 节）
		client:  upstreamClient(cfg, 2*time.Minute),
		presets: table,
		exempt:  exempt,
		states:  map[string]*snapshotState{},
	}
	m.bootstrap()
	return m
}

// bootstrap 启动引导：目录无改写产物时从出厂层复制原始文件（不覆盖已有），
// SNAPSHOT_META 时间戳作为各档初始 lastRefreshedAt；出厂层不存在（开发/单测
// 环境）则跳过复制等首轮刷新（已注入 rules/ 的原始文件首轮改写即可用）。
func (m *snapshotManager) bootstrap() {
	if err := os.MkdirAll(m.rulesDir, 0o755); err != nil {
		log.Printf("snapshot 创建 %s 失败: %v", m.rulesDir, err)
		return
	}
	entries, err := os.ReadDir(m.vendorDir)
	if err != nil {
		return // 出厂层不存在：跳过复制
	}
	for _, e := range entries {
		if e.IsDir() || e.Name() == snapshotMetaFile {
			continue
		}
		dst := filepath.Join(m.rulesDir, e.Name())
		if _, err := os.Stat(dst); err == nil {
			continue // 运行时层已有（上轮留存）不覆盖
		}
		data, err := os.ReadFile(filepath.Join(m.vendorDir, e.Name()))
		if err != nil {
			continue
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			log.Printf("snapshot 复制出厂文件 %s 失败: %v", e.Name(), err)
		}
	}
	if meta := m.readVendorMeta(); meta != nil {
		m.mu.Lock()
		for _, p := range m.presets {
			if m.exempt != nil && m.exempt(p) {
				continue
			}
			st := m.stateLocked(p.ID)
			if st.lastRefreshedAt == nil {
				t := *meta
				st.lastRefreshedAt = &t
			}
		}
		m.mu.Unlock()
	}
}

// readVendorMeta 解析出厂层 SNAPSHOT_META（RFC3339 或 unix 秒），失败返回 nil
func (m *snapshotManager) readVendorMeta() *time.Time {
	data, err := os.ReadFile(filepath.Join(m.vendorDir, snapshotMetaFile))
	if err != nil {
		return nil
	}
	raw := strings.TrimSpace(string(data))
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		utc := t.UTC()
		return &utc
	}
	var sec int64
	if _, err := fmt.Sscanf(raw, "%d", &sec); err == nil && sec > 0 {
		t := time.Unix(sec, 0).UTC()
		return &t
	}
	log.Printf("snapshot %s 内容无法解析: %q", snapshotMetaFile, raw)
	return nil
}

func (m *snapshotManager) refreshLoop(ctx context.Context) {
	m.refreshOnce() // 启动先刷一轮（含改写；上游不可达时用本地已有文件）
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.refreshOnce()
		}
	}
}

// refreshOnce 逐文件拉上游（单文件成功即覆盖、失败保留旧文件 = 逐文件
// last-known-good），随后重做 .ini 改写（校验不过不切换）。
func (m *snapshotManager) refreshOnce() {
	// 阶段一：拉三档 .ini
	iniModified := map[string]string{} // preset id → 本轮拉到的上游 Last-Modified
	fetchedNew := map[string]bool{}    // preset id → 本轮是否有新内容落地
	for _, p := range m.presets {
		if m.exempt != nil && m.exempt(p) {
			continue
		}
		body, lm, err := m.fetch(p.URL)
		if err != nil {
			log.Printf("snapshot 刷新 %s 模板失败（保留旧文件）: %v", p.ID, err)
			continue
		}
		if err := atomicWrite(filepath.Join(m.rulesDir, urlBasename(p.URL)), body); err != nil {
			log.Printf("snapshot 写 %s 模板失败: %v", p.ID, err)
			continue
		}
		iniModified[p.ID] = lm
		fetchedNew[p.ID] = true
	}

	// 阶段二：本地已存在 .ini 引用的规则集全集，逐个拉
	rulesetURLs := map[string]bool{}
	for _, p := range m.presets {
		if m.exempt != nil && m.exempt(p) {
			continue
		}
		src, err := os.ReadFile(filepath.Join(m.rulesDir, urlBasename(p.URL)))
		if err != nil {
			continue // 该档尚无模板（无出厂层且上游不可达）
		}
		for _, u := range extractRulesetURLs(src) {
			rulesetURLs[u] = true
		}
	}
	fetchedRulesets := map[string]bool{}
	for u := range rulesetURLs {
		body, _, err := m.fetch(u)
		if err != nil {
			log.Printf("snapshot 刷新规则集 %s 失败（保留旧文件）: %v", urlBasename(u), err)
			continue
		}
		if err := atomicWrite(filepath.Join(m.rulesDir, urlBasename(u)), body); err != nil {
			log.Printf("snapshot 写规则集 %s 失败: %v", urlBasename(u), err)
			continue
		}
		fetchedRulesets[u] = true
	}

	// 阶段三：重做 .ini 改写。本轮该档零新内容落地时 lastRefreshedAt 不动
	// （当前所用快照仍是出厂/上轮内容，获得时间如实保留）
	now := time.Now()
	for _, p := range m.presets {
		if m.exempt != nil && m.exempt(p) {
			continue
		}
		m.rewriteAndSwap(p, iniModified[p.ID], fetchedNew[p.ID] || dependsOnAny(extractRulesetURLsOf(m, p), fetchedRulesets), now)
	}
}

func extractRulesetURLsOf(m *snapshotManager, p presetEntry) []string {
	src, err := os.ReadFile(filepath.Join(m.rulesDir, urlBasename(p.URL)))
	if err != nil {
		return nil
	}
	return extractRulesetURLs(src)
}

func dependsOnAny(urls []string, fetched map[string]bool) bool {
	for _, u := range urls {
		if fetched[u] {
			return true
		}
	}
	return false
}

// rewriteAndSwap 改写该档 .ini 并在校验通过后原子切换。静默丢规则防护（契约
// 硬性要求）：改写产物引用的全部规则集文件必须存在，否则不切换、保留旧产物。
func (m *snapshotManager) rewriteAndSwap(p presetEntry, upstreamLM string, updated bool, now time.Time) {
	origPath := filepath.Join(m.rulesDir, urlBasename(p.URL))
	src, err := os.ReadFile(origPath)
	if err != nil {
		return // 该档尚无模板，无可改写
	}
	rewritten, refs := rewriteINI(src, m.rulesDir)
	for _, ref := range refs {
		if !pathInScope(ref, m.baseDir) {
			log.Printf("snapshot %s 改写校验不过：引用 %s 缺失或出 %s 树，保留旧产物不切换", p.ID, ref, m.baseDir)
			return
		}
	}
	outPath := rewrittenPathOf(origPath)
	if err := atomicWrite(outPath, rewritten); err != nil {
		log.Printf("snapshot 写 %s 改写产物失败: %v", p.ID, err)
		return
	}
	if !updated {
		return
	}
	m.mu.Lock()
	st := m.stateLocked(p.ID)
	t := now
	st.lastRefreshedAt = &t
	st.lastModifiedAt = nil // 缺头为 null（契约信息保真）
	if upstreamLM != "" {
		if pt, err := http.ParseTime(upstreamLM); err == nil {
			utc := pt.UTC()
			st.lastModifiedAt = &utc
		}
	}
	m.mu.Unlock()
}

// rewrittenPath 该档改写后 .ini 绝对路径（存在时）；preview 探测、建链冻结与
// /s/ 映射共用的引擎 config= 值
func (m *snapshotManager) rewrittenPath(p presetEntry) (string, bool) {
	outPath := rewrittenPathOf(filepath.Join(m.rulesDir, urlBasename(p.URL)))
	if _, err := os.Stat(outPath); err != nil {
		return "", false
	}
	return outPath, true
}

// mapFrozenConfig 存量短链兼容（契约）：冻结 config 为内置三档 GitHub URL 时
// 运行时映射为对应本地改写后 .ini 路径（库不动）；无可用产物则原样使用
func (m *snapshotManager) mapFrozenConfig(rawURL string) (string, bool) {
	for _, p := range m.presets {
		if p.URL == rawURL {
			return m.rewrittenPath(p)
		}
	}
	return "", false
}

func (m *snapshotManager) stateOf(id string) snapshotState {
	m.mu.Lock()
	defer m.mu.Unlock()
	if st, ok := m.states[id]; ok {
		return *st
	}
	return snapshotState{}
}

func (m *snapshotManager) stateLocked(id string) *snapshotState {
	st, ok := m.states[id]
	if !ok {
		st = &snapshotState{}
		m.states[id] = st
	}
	return st
}

func (m *snapshotManager) fetch(rawURL string) (body []byte, lastModified string, err error) {
	resp, err := m.client.Get(rawURL)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err = io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, "", err
	}
	return body, resp.Header.Get("Last-Modified"), nil
}

// rewrittenPathOf ACL4SSR_Online.ini → ACL4SSR_Online.rewritten.ini
func rewrittenPathOf(origPath string) string {
	return strings.TrimSuffix(origPath, ".ini") + ".rewritten.ini"
}

// urlBasename 取 URL 路径最后一段作快照文件名（规则集实测 35 URL 无 basename 冲突可平铺）
func urlBasename(rawURL string) string {
	u := rawURL
	if i := strings.IndexByte(u, '?'); i >= 0 {
		u = u[:i]
	}
	return path.Base(u)
}

// pathInScope 文件存在且 canonical 后位于引擎可读树内（闸门实测：出树/symlink
// 桥接路径引擎会静默丢规则不报错，改写校验必须挡在切换之前）
func pathInScope(file, baseDir string) bool {
	if _, err := os.Stat(file); err != nil {
		return false
	}
	real, err := filepath.EvalSymlinks(file)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(baseDir, real)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}

// atomicWrite 先写 .tmp 再 rename（读端无半成品窗口）
func atomicWrite(file string, data []byte) error {
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, file)
}

func isHTTPSeg(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

// rewriteINI 把 ruleset= 行中 URL 形态的段替换为 rulesDir 内本地绝对路径（引擎
// cwd 基准：相对路径按 cwd 解析而非 .ini 所在目录，闸门实测——产物必须绝对路径）。
// 非 URL 段（[]FINAL/[]GEOIP,CN 内置型，逗号天然分段）、其他行原样保留。
// 返回改写后内容与引用的本地路径列表（供存在性校验）。
func rewriteINI(src []byte, rulesDir string) ([]byte, []string) {
	lines := strings.Split(string(src), "\n")
	refs := make([]string, 0, 32)
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if !strings.HasPrefix(t, "ruleset=") {
			continue
		}
		segs := strings.Split(t[len("ruleset="):], ",")
		changed := false
		for j, seg := range segs {
			s := strings.TrimSpace(seg)
			if !isHTTPSeg(s) {
				continue
			}
			local := filepath.Join(rulesDir, urlBasename(s))
			segs[j] = local
			refs = append(refs, local)
			changed = true
		}
		if changed {
			lines[i] = "ruleset=" + strings.Join(segs, ",")
		}
	}
	return []byte(strings.Join(lines, "\n")), refs
}

// extractRulesetURLs 提取 .ini 中全部规则集 URL（去重保序）
func extractRulesetURLs(src []byte) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, line := range strings.Split(string(src), "\n") {
		t := strings.TrimSpace(line)
		if !strings.HasPrefix(t, "ruleset=") {
			continue
		}
		for _, seg := range strings.Split(t[len("ruleset="):], ",") {
			s := strings.TrimSpace(seg)
			if isHTTPSeg(s) && !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		}
	}
	return out
}

// snapshotEnabled DATA_DIR（canonical 后）位于引擎可读的 /base 树内才启用快照
// 本地化；出树（显式自设部署）→ 整体禁用（三档按豁免档语义、source=remote），
// 不拒启动（契约「降级行为」节）
func snapshotEnabled(dataDir string) bool {
	dir := dataDir
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	rel, err := filepath.Rel(engineBaseDir, dir)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, "../")
}
