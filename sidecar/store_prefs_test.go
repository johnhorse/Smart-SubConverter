// 分组偏好档案存储单测（v1.6.0）：normalizeSubURL 与指纹规范化同源、迁移 v4 幂等重放、
// 两把钥匙语义（custom 按 sub_key 跨档共享 / exclude 按 sub_key+preset 档位独立）、
// 同钥匙单份覆盖（最后提交赢）、deletePrefs 清该订阅全部档案。
package main

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// normalizeSubURL 与指纹的 URL 规范化同源（v1.6.0 sub_key）：等价组（trim / 大小写 /
// 默认端口 / path 空补 / / fragment 丢弃）→ 同 sub_key；非等价（query / 路径 / 端口不同）
// → 不同 sub_key
func TestNormalizeSubURL(t *testing.T) {
	const canonical = "https://x.example.com/a?b=1"
	equal := []string{
		"  https://x.example.com/a?b=1  ",
		"HTTPS://X.Example.COM/a?b=1",
		"https://x.example.com:443/a?b=1",
		"https://x.example.com/a?b=1#frag",
	}
	for _, raw := range equal {
		if got := normalizeSubURL(raw); got != canonical {
			t.Errorf("normalizeSubURL(%q) = %q，want %q", raw, got, canonical)
		}
	}
	diff := map[string]string{
		"https://x.example.com/a?b=2":      "query 不同",
		"https://x.example.com/a?b=1&a=2":  "query 多参数（原样保留，不排序）",
		"https://x.example.com/a/?b=1":     "尾斜杠不同",
		"https://x.example.com:8443/a?b=1": "非默认端口保留",
		"https://other.example.com/a?b=1":  "host 不同",
		"http://x.example.com/a?b=1":       "scheme 不同",
	}
	for raw, name := range diff {
		if got := normalizeSubURL(raw); got == canonical {
			t.Errorf("normalizeSubURL(%q) 不应等于 %q（%s）", raw, canonical, name)
		}
	}
	// path 空补 /：整串形态
	if got := normalizeSubURL("https://x.example.com"); got != "https://x.example.com/" {
		t.Errorf("path 空应补 /：%q", got)
	}
	// 解析失败兜底空串
	for _, raw := range []string{"", "not a url", "http://"} {
		if got := normalizeSubURL(raw); got != "" {
			t.Errorf("normalizeSubURL(%q) 应兜底空串，得 %q", raw, got)
		}
	}
}

// 存量 v3 库升级：v3 schema（links 全列）+ user_version=3 → openStore 迁移到 4（prefs
// 两表建好可用）；重开幂等不重放（CREATE IF NOT EXISTS 双保险）、links 存量照常 hit
func TestMigrateV4ExistingDB(t *testing.T) {
	dir := t.TempDir()
	st, err := openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	// 先落一条数据再降版本模拟存量 v3 库（表结构一致，仅 user_version 回拨）
	if _, err := st.getOrCreateLink("https://sub.example.com/l", "auto", "default", "https://c.example/i", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := st.close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("DROP TABLE prefs_custom"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("DROP TABLE prefs_exclude"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA user_version = 3"); err != nil {
		t.Fatal(err)
	}
	db.Close()

	st2, err := openStore(dir)
	if err != nil {
		t.Fatalf("v3 库升级失败: %v", err)
	}
	defer st2.close()
	var v int
	if err := st2.db.QueryRow("PRAGMA user_version").Scan(&v); err != nil || v != 4 {
		t.Fatalf("迁移后 user_version = %d (err %v)，应为 4", v, err)
	}
	fp := shortFingerprint("https://sub.example.com/l", "auto", "https://c.example/i", "", "")
	code, err := st2.codeByFingerprint(fp)
	if err != nil {
		t.Fatalf("存量 links 记录应照常 hit: %v", err)
	}
	if _, found, err := st2.linkByCode(code); err != nil || !found {
		t.Errorf("存量 links 记录应照常 hit: found=%v err=%v", found, err)
	}
	// prefs 两表可用
	if err := st2.upsertPrefsCustom("https://sub.example.com/l", `[]`); err != nil {
		t.Fatalf("迁移后 prefs_custom 应可写: %v", err)
	}
	// 重开：迁移不重放
	st3, err := openStore(dir)
	if err != nil {
		t.Fatalf("重开应跳过已生效迁移: %v", err)
	}
	defer st3.close()
	if got, found, err := st3.getPrefsCustom("https://sub.example.com/l"); err != nil || !found || got != "[]" {
		t.Errorf("重开后 prefs 数据应仍在：got=%q found=%v err=%v", got, found, err)
	}
}

// 两把钥匙语义（v1.6.0）：custom 按 sub_key（跨档共享——表无 preset 维度）；等价 URL
// （trim/大小写/默认端口/fragment）归并同钥匙；upsert 同钥匙单份覆盖（最后提交赢）
func TestPrefsCustomKeysAndOverwrite(t *testing.T) {
	st := openTestStore(t)
	g1 := `[{"name":"专线","keywords":["专线"],"urlTest":false}]`
	g2 := `[{"name":"家宽","keywords":["家宽"],"urlTest":true}]`

	if err := st.upsertPrefsCustom(normalizeSubURL("https://sub.example.com/l"), g1); err != nil {
		t.Fatal(err)
	}
	// 等价 URL（尾空白 + 大写 host + 默认端口 + fragment）→ 同钥匙命中同一份档案
	got, found, err := st.getPrefsCustom(normalizeSubURL(" HTTPS://SUB.EXAMPLE.COM:443/l#frag "))
	if err != nil || !found || got != g1 {
		t.Fatalf("等价 URL 应命中同钥匙：got=%q found=%v err=%v", got, found, err)
	}
	// 跨档共享：custom 不带 preset 维度（单表单行即证）
	var n int
	if err := st.db.QueryRow("SELECT COUNT(*) FROM prefs_custom").Scan(&n); err != nil || n != 1 {
		t.Errorf("prefs_custom 行数 = %d (err %v)，应为 1", n, err)
	}
	// 覆盖：同钥匙再写 → 行数仍 1、内容换新（最后提交赢）
	if err := st.upsertPrefsCustom(normalizeSubURL("https://sub.example.com/l"), g2); err != nil {
		t.Fatal(err)
	}
	got, found, err = st.getPrefsCustom(normalizeSubURL("https://sub.example.com/l"))
	if err != nil || !found || got != g2 {
		t.Fatalf("覆盖后应读到新值（最后提交赢）：got=%q found=%v err=%v", got, found, err)
	}
	if err := st.db.QueryRow("SELECT COUNT(*) FROM prefs_custom").Scan(&n); err != nil || n != 1 {
		t.Errorf("覆盖后行数 = %d (err %v)，应仍 1", n, err)
	}
	// 空串入参规范化为 "[]"（列 NOT NULL，「字段出现=空档案」语义）
	if err := st.upsertPrefsCustom("https://other.example/x", ""); err != nil {
		t.Fatal(err)
	}
	if got, found, _ := st.getPrefsCustom("https://other.example/x"); !found || got != "[]" {
		t.Errorf("空串应存 []：got=%q found=%v", got, found)
	}
	// 无档案 miss
	if _, found, err := st.getPrefsCustom("https://none.example/z"); err != nil || found {
		t.Errorf("无档案应 miss：found=%v err=%v", found, err)
	}
}

// exclude 钥匙 = sub_key + preset_id：同订阅不同档各一份互不串；同钥匙覆盖；跨订阅隔离
func TestPrefsExcludeKeysAndOverwrite(t *testing.T) {
	st := openTestStore(t)
	kA := normalizeSubURL("https://sub.example.com/l")
	kB := normalizeSubURL("https://other.example/s")
	e1 := `["🌍 国外媒体"]`
	e2 := `["🌍 国外媒体","🇰🇷 韩国节点"]`
	e3 := `["💬 Ai平台"]`

	if err := st.upsertPrefsExclude(kA, "default", e1); err != nil {
		t.Fatal(err)
	}
	if err := st.upsertPrefsExclude(kA, "full_multimode", e3); err != nil {
		t.Fatal(err)
	}
	if err := st.upsertPrefsExclude(kB, "default", e2); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := st.db.QueryRow("SELECT COUNT(*) FROM prefs_exclude").Scan(&n); err != nil || n != 3 {
		t.Fatalf("prefs_exclude 行数 = %d (err %v)，应为 3", n, err)
	}
	// 档位独立：default 档与 full_multimode 档各自命中各自的
	if got, found, _ := st.getPrefsExclude(kA, "default"); !found || got != e1 {
		t.Errorf("A+default 应命中 e1：got=%q found=%v", got, found)
	}
	if got, found, _ := st.getPrefsExclude(kA, "full_multimode"); !found || got != e3 {
		t.Errorf("A+full_multimode 应命中 e3：got=%q found=%v", got, found)
	}
	// 跨订阅隔离
	if _, found, _ := st.getPrefsExclude(kB, "full_multimode"); found {
		t.Error("B+full_multimode 应 miss（跨订阅隔离）")
	}
	// 同钥匙覆盖（最后提交赢）
	if err := st.upsertPrefsExclude(kA, "default", e2); err != nil {
		t.Fatal(err)
	}
	if got, found, _ := st.getPrefsExclude(kA, "default"); !found || got != e2 {
		t.Errorf("A+default 覆盖后应读 e2：got=%q found=%v", got, found)
	}
	if err := st.db.QueryRow("SELECT COUNT(*) FROM prefs_exclude").Scan(&n); err != nil || n != 3 {
		t.Errorf("覆盖后行数 = %d (err %v)，应仍 3", n, err)
	}
	// 无档案 miss
	if _, found, err := st.getPrefsExclude(kA, "full_adblockplus"); err != nil || found {
		t.Errorf("无档位档案应 miss：found=%v err=%v", found, err)
	}
}

// deletePrefs 清该订阅全部档案（custom + exclude 全档），他订阅不动；幂等（再删 false/0）
func TestDeletePrefs(t *testing.T) {
	st := openTestStore(t)
	kA := normalizeSubURL("https://sub.example.com/l")
	kB := normalizeSubURL("https://other.example/s")
	if err := st.upsertPrefsCustom(kA, `[]`); err != nil {
		t.Fatal(err)
	}
	if err := st.upsertPrefsCustom(kB, `[]`); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"default", "full_multimode", "full_adblockplus"} {
		if err := st.upsertPrefsExclude(kA, p, `["g"]`); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.upsertPrefsExclude(kB, "default", `["g"]`); err != nil {
		t.Fatal(err)
	}

	dc, de, err := st.deletePrefs(kA)
	if err != nil || !dc || de != 3 {
		t.Fatalf("deletePrefs(A) = (%v, %d, %v)，want (true, 3, nil)", dc, de, err)
	}
	if _, found, _ := st.getPrefsCustom(kA); found {
		t.Error("删除后 A custom 应 miss")
	}
	if _, found, _ := st.getPrefsExclude(kA, "default"); found {
		t.Error("删除后 A exclude(default) 应 miss")
	}
	// B 完好
	if _, found, _ := st.getPrefsCustom(kB); !found {
		t.Error("B custom 应不受影响")
	}
	if _, found, _ := st.getPrefsExclude(kB, "default"); !found {
		t.Error("B exclude(default) 应不受影响")
	}
	// 幂等：再删空手而归
	dc, de, err = st.deletePrefs(kA)
	if err != nil || dc || de != 0 {
		t.Errorf("deletePrefs 幂等 = (%v, %d, %v)，want (false, 0, nil)", dc, de, err)
	}
}

// upsertPrefs 单事务复合写（v1.6.0 评审修订 #10）：两字段同时出现 → 同成同败，不留
// 「新 groups + 旧 exclude」的半应用档案。正常路径双落；注入第二步失败（drop
// prefs_exclude 表令 exclude 写入报错）→ 第一步必须回滚、custom 保持旧值
func TestUpsertPrefsAtomic(t *testing.T) {
	st := openTestStore(t)
	k := normalizeSubURL("https://sub.example.com/l")
	g1 := `[{"name":"专线","keywords":["专线"],"urlTest":false}]`
	e1 := `["🌍 国外媒体"]`

	// 正常路径：两字段同一事务双落
	if err := st.upsertPrefs(k, "default", &g1, &e1); err != nil {
		t.Fatal(err)
	}
	if got, found, _ := st.getPrefsCustom(k); !found || got != g1 {
		t.Errorf("prefs_custom = %q found=%v，want %q", got, found, g1)
	}
	if got, found, _ := st.getPrefsExclude(k, "default"); !found || got != e1 {
		t.Errorf("prefs_exclude = %q found=%v，want %q", got, found, e1)
	}

	// 两者皆 nil = 字段均未出现：no-op 不报错、档案不动
	if err := st.upsertPrefs(k, "default", nil, nil); err != nil {
		t.Errorf("双 nil 应 no-op：%v", err)
	}
	if got, _, _ := st.getPrefsCustom(k); got != g1 {
		t.Errorf("双 nil no-op 不应动档案：got=%q", got)
	}

	// 注入第二步失败：prefs_exclude 表不存在 → 整个事务回滚
	if _, err := st.db.Exec("DROP TABLE prefs_exclude"); err != nil {
		t.Fatal(err)
	}
	g2 := `[{"name":"新组","keywords":["新"],"urlTest":true}]`
	e2 := `["💬 Ai平台"]`
	if err := st.upsertPrefs(k, "default", &g2, &e2); err == nil {
		t.Fatal("exclude 写入失败应整体报错")
	}
	if got, found, _ := st.getPrefsCustom(k); !found || got != g1 {
		t.Errorf("事务回滚后 prefs_custom 应保持旧值：got=%q found=%v（不得出现半应用档案）", got, found)
	}

	// 单字段出现（exclude nil）：只写 custom，正常提交
	g3 := `[]`
	if err := st.upsertPrefs(k, "default", &g3, nil); err != nil {
		t.Fatalf("单字段写应成功：%v", err)
	}
	if got, found, _ := st.getPrefsCustom(k); !found || got != g3 {
		t.Errorf("单字段写后 prefs_custom = %q found=%v，want %q", got, found, g3)
	}
}
