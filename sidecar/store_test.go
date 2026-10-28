// 短链存储层单测：指纹规范化（契约级钉死）、幂等复用、两类唯一冲突分类处理（OH-9）、
// user_version 幂等迁移、库损坏拒启动、文件权限、删除轮换语义。
package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func openTestStore(t *testing.T) *store {
	t.Helper()
	st, err := openStore(t.TempDir())
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { st.close() })
	return st
}

// stubCodeGen 注入固定短码序列（码碰撞/轮换测试用）
func stubCodeGen(t *testing.T, st *store, codes ...string) {
	t.Helper()
	i := 0
	st.genCode = func() (string, error) {
		if i >= len(codes) {
			t.Fatalf("genCode 调用超出注入序列（第 %d 次）", i+1)
		}
		c := codes[i]
		i++
		return c, nil
	}
}

// 指纹规范化：等价组内指纹相同，不等价组指纹不同（契约「幂等与指纹」规则逐条对应）
func TestShortFingerprintNormalization(t *testing.T) {
	equalPairs := [][2]string{
		{" https://x.example.com/a ", "https://x.example.com/a"},           // 两端 trim
		{"HTTPS://X.Example.COM:443/a?b=1", "https://x.example.com/a?b=1"}, // scheme/host 小写 + 去默认端口
		{"http://x.example.com:80/a", "http://x.example.com/a"},            // http 默认端口
		{"https://x.example.com", "https://x.example.com/"},                // path 空补 /
		{"https://x.example.com/a#fragment", "https://x.example.com/a"},    // fragment 丢弃
		{"https://x.example.com:8443/a", "https://X.Example.com:8443/a"},   // 非默认端口保留（host 大小写不影响）
		{"https://[2001:db8::1]:8443/a", "https://[2001:DB8::1]:8443/a"},   // IPv6 字面量小写化
	}
	for _, p := range equalPairs {
		if shortFingerprint(p[0], "clash", "https://c.example/i", "") != shortFingerprint(p[1], "clash", "https://c.example/i", "") {
			t.Errorf("应同指纹：%q ≡ %q", p[0], p[1])
		}
	}
	base := "https://x.example.com/a"
	diffCases := []struct{ name, raw, target, configURL string }{
		{"query 原样（顺序不同即不同）", "https://x.example.com/a?b=2&a=1", "clash", "https://c.example/i"},
		{"尾斜杠不同", "https://x.example.com/a/", "clash", "https://c.example/i"},
		{"非默认端口 vs 无端口", "https://x.example.com:8443/a", "clash", "https://c.example/i"},
		{"target 不同", base, "surge", "https://c.example/i"},
		{"config_url 不同", base, "clash", "https://c.example/other.ini"},
	}
	baseFp := shortFingerprint(base, "clash", "https://c.example/i", "")
	for _, c := range diffCases {
		if shortFingerprint(c.raw, c.target, c.configURL, "") == baseFp {
			t.Errorf("应不同指纹（%s）：%q vs %q", c.name, c.raw, base)
		}
	}
}

// 同参数幂等：永远复用既有短码，库里只有一行
func TestGetOrCreateIdempotent(t *testing.T) {
	st := openTestStore(t)
	c1, err := st.getOrCreateLink("https://sub.example.com/l", "auto", "default", "https://c.example/i", "")
	if err != nil {
		t.Fatal(err)
	}
	c2, err := st.getOrCreateLink("https://sub.example.com/l", "auto", "default", "https://c.example/i", "")
	if err != nil {
		t.Fatal(err)
	}
	if c1 != c2 {
		t.Errorf("同参数应复用同码：%s vs %s", c1, c2)
	}
	var n int
	if err := st.db.QueryRow("SELECT COUNT(*) FROM links").Scan(&n); err != nil || n != 1 {
		t.Errorf("links 行数 = %d (err %v)，应为 1", n, err)
	}
	if len(c1) != 8 {
		t.Errorf("短码长度 = %d，应为 8", len(c1))
	}
}

// 指纹冲突：库里已有同指纹行 → 重查返回既有码（不新插、不报错）
func TestGetOrCreateFingerprintConflict(t *testing.T) {
	st := openTestStore(t)
	sub, cfgURL := "https://sub.example.com/l", "https://c.example/i"
	fp := shortFingerprint(sub, "auto", cfgURL, "")
	if _, err := st.db.Exec(
		"INSERT INTO links (code, fingerprint, source_url, target, preset_id, config_url, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
		"EXISTING", fp, sub, "auto", "default", cfgURL, 100); err != nil {
		t.Fatal(err)
	}
	c, err := st.getOrCreateLink(sub, "auto", "default", cfgURL, "")
	if err != nil {
		t.Fatalf("指纹冲突应复用既有码而非报错: %v", err)
	}
	if c != "EXISTING" {
		t.Errorf("指纹冲突应返回既有码 %s，得到 %s", "EXISTING", c)
	}
}

// 码碰撞：首码与既有行撞 PK → 换码重插成功
func TestGetOrCreateCodeCollision(t *testing.T) {
	st := openTestStore(t)
	stubCodeGen(t, st, "FIRSTCOD", "SECONDCO")
	// 用不同 URL 建首行占住 FIRSTCOD
	if _, err := st.getOrCreateLink("https://other.example/a", "clash", "default", "https://c.example/i", ""); err != nil {
		t.Fatal(err)
	}
	stubCodeGen(t, st, "FIRSTCOD", "SECONDCO") // 重置序列
	c, err := st.getOrCreateLink("https://sub.example.com/l", "auto", "default", "https://c.example/i", "")
	if err != nil {
		t.Fatal(err)
	}
	if c != "SECONDCO" {
		t.Errorf("码碰撞应换码重插，得到 %s，want SECONDCO", c)
	}
}

// 码碰撞耗尽：5 次仍撞 → 报错
func TestGetOrCreateCodeCollisionExhausted(t *testing.T) {
	st := openTestStore(t)
	if _, err := st.getOrCreateLink("https://other.example/a", "clash", "default", "https://c.example/i", ""); err != nil {
		t.Fatal(err)
	}
	codes := []string{"AAAAAAAA", "BBBBBBBB", "CCCCCCCC", "DDDDDDDD", "EEEEEEEE", "FFFFFFFF"}
	for _, c := range codes {
		if _, err := st.db.Exec(
			"INSERT INTO links (code, fingerprint, source_url, target, preset_id, config_url, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
			c, "fp-"+c, "https://x.example/"+c, "clash", "default", "https://c.example/i", 1); err != nil {
			t.Fatal(err)
		}
	}
	stubCodeGen(t, st, codes...)
	if _, err := st.getOrCreateLink("https://sub.example.com/l", "auto", "default", "https://c.example/i", ""); err == nil {
		t.Error("5 次码碰撞后应报错")
	}
}

// 删除后同参数重建：指纹相同但插新随机码（轮换语义）；linkByCode 立即 miss
func TestDeleteAndRotate(t *testing.T) {
	st := openTestStore(t)
	stubCodeGen(t, st, "FIRSTCOD")
	c1, err := st.getOrCreateLink("https://sub.example.com/l", "auto", "default", "https://c.example/i", "")
	if err != nil || c1 != "FIRSTCOD" {
		t.Fatalf("首次建链 = %s (err %v)", c1, err)
	}
	if err := st.deleteLink(c1); err != nil {
		t.Fatal(err)
	}
	if err := st.deleteLink(c1); err != nil {
		t.Errorf("deleteLink 应幂等: %v", err)
	}
	if _, found, err := st.linkByCode(c1); err != nil || found {
		t.Errorf("删除后 linkByCode 应 miss: found=%v err=%v", found, err)
	}
	stubCodeGen(t, st, "SECONDCO")
	c2, err := st.getOrCreateLink("https://sub.example.com/l", "auto", "default", "https://c.example/i", "")
	if err != nil {
		t.Fatal(err)
	}
	if c2 != "SECONDCO" || c2 == c1 {
		t.Errorf("删除后重建应得新码：%s vs %s", c1, c2)
	}
}

// user_version 迁移幂等：首次建库 =2（v1 建表 + v2 groups 列）；重开不重复迁移且数据仍在
func TestMigrateIdempotent(t *testing.T) {
	dir := t.TempDir()
	st, err := openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	var v int
	if err := st.db.QueryRow("PRAGMA user_version").Scan(&v); err != nil || v != 2 {
		t.Fatalf("user_version = %d (err %v)，应为 2", v, err)
	}
	if _, err := st.getOrCreateLink("https://sub.example.com/l", "auto", "default", "https://c.example/i", ""); err != nil {
		t.Fatal(err)
	}
	if err := st.close(); err != nil {
		t.Fatal(err)
	}

	st2, err := openStore(dir) // 重开：迁移应跳过（v 已 2）
	if err != nil {
		t.Fatalf("重开库失败: %v", err)
	}
	defer st2.close()
	if err := st2.db.QueryRow("PRAGMA user_version").Scan(&v); err != nil || v != 2 {
		t.Fatalf("重开后 user_version = %d (err %v)，应仍为 2", v, err)
	}
	var n int
	if err := st2.db.QueryRow("SELECT COUNT(*) FROM links").Scan(&n); err != nil || n != 1 {
		t.Errorf("重开后数据应仍在：行数 = %d (err %v)", n, err)
	}
}

// 存量 v1 库升级：v1 schema（无 groups 列）+ user_version=1 → openStore 迁移到 v2，
// 存量记录不动、照常 hit（契约：v1 存量指纹记录不迁移、存量码照常 hit）
func TestMigrateV1ExistingDB(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE links (
		code        TEXT PRIMARY KEY,
		fingerprint TEXT NOT NULL UNIQUE,
		source_url  TEXT NOT NULL,
		target      TEXT NOT NULL,
		preset_id   TEXT NOT NULL,
		config_url  TEXT NOT NULL,
		created_at  INTEGER NOT NULL
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO links (code, fingerprint, source_url, target, preset_id, config_url, created_at)
		VALUES ('V1LEGACY', 'v1fp-legacy', 'https://sub.example.com/l', 'auto', 'default', 'https://c.example/i', 100)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA user_version = 1"); err != nil {
		t.Fatal(err)
	}
	db.Close()

	st, err := openStore(dir)
	if err != nil {
		t.Fatalf("v1 库升级失败: %v", err)
	}
	defer st.close()
	var v int
	if err := st.db.QueryRow("PRAGMA user_version").Scan(&v); err != nil || v != 2 {
		t.Fatalf("迁移后 user_version = %d (err %v)，应为 2", v, err)
	}
	lnk, found, err := st.linkByCode("V1LEGACY")
	if err != nil || !found {
		t.Fatalf("存量 v1 记录应照常 hit: found=%v err=%v", found, err)
	}
	if lnk.SourceURL != "https://sub.example.com/l" || lnk.Target != "auto" || lnk.Groups != "" {
		t.Errorf("存量记录字段 = %+v", lnk)
	}
	// 重开一次：迁移不重放（ALTER 已生效，若非事务化幂等会报 duplicate column 拒启动）
	st2, err := openStore(dir)
	if err != nil {
		t.Fatalf("重开应跳过已生效迁移: %v", err)
	}
	st2.close()
}

// 库损坏：非「文件不存在」的损坏文件 → openStore 报错，信息含备份指引
func TestOpenStoreCorrupt(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "state.db"), []byte("this is definitely not a sqlite database at all, nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := openStore(dir)
	if err == nil {
		t.Fatal("损坏库应报错，不能静默清空重建")
	}
	msg := err.Error()
	if !strings.Contains(msg, "备份") || !strings.Contains(msg, "勿直接删除") {
		t.Errorf("错误信息应含备份指引：\n%s", msg)
	}
}

// 凭据库文件权限 0600
func TestStoreFilePerms(t *testing.T) {
	dir := t.TempDir()
	st, err := openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.close()
	fi, err := os.Stat(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("state.db 权限 = %o，应为 0600", fi.Mode().Perm())
	}
}

// 管理列表：createdAt 降序（同秒并列按 code 倒序保持稳定）
func TestListLinksOrder(t *testing.T) {
	st := openTestStore(t)
	for i, code := range []string{"AAA", "BBB", "CCC"} {
		if _, err := st.db.Exec(
			"INSERT INTO links (code, fingerprint, source_url, target, preset_id, config_url, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
			code, "fp"+code, "https://x.example/"+code, "clash", "default", "https://c.example/i", 1000-i); err != nil {
			t.Fatal(err)
		}
	}
	links, err := st.listLinks()
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 3 || links[0].Code != "AAA" || links[1].Code != "BBB" || links[2].Code != "CCC" {
		t.Errorf("listLinks 顺序 = %v，应按 createdAt 降序", links)
	}
	if links[0].Preset != "default" || links[0].SourceURL != "https://x.example/AAA" || links[0].CreatedAt != 1000 {
		t.Errorf("listLinks 字段 = %+v", links[0])
	}
}

// 指纹 v2：groupsJSON 入指纹——同参数同 groups 同码、改组新码；空串与 "[]" 等价（空组
// 规范化）；groups 影响指纹但不影响 URL 各字段规范化规则（沿用 v1）
func TestShortFingerprintV2Groups(t *testing.T) {
	base := "https://sub.example.com/l"
	if shortFingerprint(base, "clash", "https://c.example/i", "") != shortFingerprint(base, "clash", "https://c.example/i", "[]") {
		t.Error("空串与 [] 应等价（空组规范化）")
	}
	g1 := `[{"name":"专线","keywords":["专线"],"urlTest":false}]`
	g2 := `[{"name":"专线","keywords":["专线"],"urlTest":true}]`
	g3 := `[{"name":"家宽","keywords":["家宽"],"urlTest":false}]`
	fp0 := shortFingerprint(base, "clash", "https://c.example/i", "")
	fp1 := shortFingerprint(base, "clash", "https://c.example/i", g1)
	if fp1 == fp0 {
		t.Error("有组与无组应不同指纹（改组=新短链）")
	}
	if shortFingerprint(base, "clash", "https://c.example/i", g1) != fp1 {
		t.Error("同参数同 groups 应同指纹")
	}
	for _, g := range []string{g2, g3} {
		if shortFingerprint(base, "clash", "https://c.example/i", g) == fp1 {
			t.Errorf("groups %s 与 %s 不应同指纹", g2, g)
		}
	}
}

// groups 列读写往返：无组 NULL、有组存规范化 JSON；同参数幂等复用、改组新码；
// linkByCode 读回冻结 groups（同码冻结语义）
func TestGetOrCreateGroupsRoundtrip(t *testing.T) {
	st := openTestStore(t)
	sub, cfgURL := "https://sub.example.com/l", "https://c.example/i"
	g1 := `[{"name":"专线","keywords":["专线"],"urlTest":false}]`
	g2 := `[{"name":"家宽","keywords":["家宽"],"urlTest":true}]`

	c0, err := st.getOrCreateLink(sub, "clash", "default", cfgURL, "")
	if err != nil {
		t.Fatal(err)
	}
	var groups0 sql.NullString
	if err := st.db.QueryRow("SELECT groups FROM links WHERE code = ?", c0).Scan(&groups0); err != nil || groups0.Valid {
		t.Errorf("无组应列存 NULL：valid=%v err=%v", groups0.Valid, err)
	}
	c1, err := st.getOrCreateLink(sub, "clash", "default", cfgURL, g1)
	if err != nil {
		t.Fatal(err)
	}
	if c1 == c0 {
		t.Errorf("改组应新码：%s vs %s", c1, c0)
	}
	var stored string
	if err := st.db.QueryRow("SELECT groups FROM links WHERE code = ?", c1).Scan(&stored); err != nil || stored != g1 {
		t.Errorf("groups 列 = %q (err %v)，应存规范化 JSON", stored, err)
	}
	// 同参数同 groups：幂等复用
	c1b, err := st.getOrCreateLink(sub, "clash", "default", cfgURL, g1)
	if err != nil || c1b != c1 {
		t.Errorf("同参数同 groups 应复用：%s vs %s (err %v)", c1b, c1, err)
	}
	// 再改组：又新码
	c2, err := st.getOrCreateLink(sub, "clash", "default", cfgURL, g2)
	if err != nil {
		t.Fatal(err)
	}
	if c2 == c1 || c2 == c0 {
		t.Errorf("再改组应新码：%s", c2)
	}
	// 读回冻结值
	lnk, found, err := st.linkByCode(c1)
	if err != nil || !found || lnk.Groups != g1 {
		t.Errorf("linkByCode groups 读回 = %q found=%v err=%v", lnk.Groups, found, err)
	}
	lnk0, _, _ := st.linkByCode(c0)
	if lnk0.Groups != "" {
		t.Errorf("无组读回应为空串，得 %q", lnk0.Groups)
	}
}
