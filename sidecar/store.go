// 短链存储层（v1.2.0）：SQLite（modernc.org/sqlite 纯 Go，无 cgo），库文件
// ${DATA_DIR}/state.db 权限 0600；首次启动建目录建库，PRAGMA user_version 幂等迁移。
// 库损坏（非「文件不存在」）→ 启动失败，绝不自动清空（契约「存储」节）。
package main

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const base62Alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// newCode 生成 8 字符 base62 短码（crypto/rand；拒绝采样消 256%62 模偏差）
func newCode() (string, error) {
	var out [8]byte
	for n := 0; n < len(out); {
		var r [1]byte
		if _, err := rand.Read(r[:]); err != nil {
			return "", err
		}
		if int(r[0]) >= 248 { // 4*62=248
			continue
		}
		out[n] = base62Alphabet[int(r[0])%62]
		n++
	}
	return string(out[:]), nil
}

// normalizeSubURLParts 订阅 URL 规范化分段（契约级钉死，OH-8）：两端 trim、scheme/host
// 小写、去默认端口（http:80 / https:443）、path 空补 /、query 原样保留、fragment 丢弃。
// 解析失败（scheme/host 缺失）→ 全空段（调用方各自兜底）。shortFingerprint 与
// normalizeSubURL 共用此函数——指纹与偏好 sub_key 的规范化规则恒同源。
func normalizeSubURLParts(subURL string) (scheme, host, path, query string) {
	if u, err := url.Parse(strings.TrimSpace(subURL)); err == nil && u.Scheme != "" && u.Host != "" {
		scheme = strings.ToLower(u.Scheme)
		hostname := strings.ToLower(u.Hostname())
		port := u.Port()
		if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
			port = ""
		}
		host = hostname
		if port != "" {
			if strings.Contains(hostname, ":") { // IPv6 字面量补回方括号
				host = "[" + hostname + "]:" + port
			} else {
				host = hostname + ":" + port
			}
		}
		path = u.Path
		if path == "" {
			path = "/"
		}
		query = u.RawQuery
	}
	return
}

// normalizeSubURL 订阅 URL 规范化整串（v1.6.0 偏好档案 sub_key 用）：与指纹的 URL 段
// 同一规则（normalizeSubURLParts）拼回 URL 形态；解析失败返回空串（偏好路径的 URL 已过
// validateURL 预校验，不会走到）
func normalizeSubURL(subURL string) string {
	scheme, host, path, query := normalizeSubURLParts(subURL)
	if scheme == "" || host == "" {
		return ""
	}
	out := scheme + "://" + host + path
	if query != "" {
		out += "?" + query
	}
	return out
}

// shortFingerprint 短链指纹 = SHA-256 of
// "v3|{scheme}|{host}|{path}|{rawquery}|{target}|{config_url}|{groupsJSON}|{excludeJSON}"。
// v1.5.0 增补升 v3：尾段追加 excludeJSON（去重升序 JSON 数组、无空白、空为 "[]"；空串
// 等价 "[]"）——「改删减=新短链」；v1/v2 前缀差异使存量指纹不再命中（契约「幂等与指纹」：
// 升级后同参数首建换新码属预期一次性影响，存量码照常 hit）。
// 订阅 URL 规范化见 normalizeSubURLParts（v1.6.0 提取共用，规则零变化）。
// target 为请求原样值（auto 或指定）；config_url 用解析后的档位 URL（同 id 被
// PRESET_URL 覆盖时指纹自然区分）。已通过预校验的 URL 解析不会失败，防御性兜底为全空段。
func shortFingerprint(subURL, target, configURL, groupsJSON, excludeJSON string) string {
	scheme, host, path, query := normalizeSubURLParts(subURL)
	if groupsJSON == "" {
		groupsJSON = "[]"
	}
	if excludeJSON == "" {
		excludeJSON = "[]"
	}
	sum := sha256.Sum256([]byte("v3|" + scheme + "|" + host + "|" + path + "|" + query + "|" + target + "|" + configURL + "|" + groupsJSON + "|" + excludeJSON))
	return hex.EncodeToString(sum[:])
}

// schemaMigrations 按序幂等迁移（user_version 递增；只追加不改历史条目。
// v1/v2/v3 同事务执行（SQLite DDL 事务性）：v2/v3 的 ALTER TABLE 无 IF NOT EXISTS
// 形态，非原子落盘时崩溃在 DDL 与 user_version 之间会让重启重放报 duplicate column
// 卡死启动。v4（v1.6.0 分组偏好记忆）：prefs_custom + prefs_exclude 两表，单条目两语句
// 同事务，均为 CREATE TABLE IF NOT EXISTS 天然幂等
var schemaMigrations = []string{
	`CREATE TABLE IF NOT EXISTS links (
		code        TEXT PRIMARY KEY,
		fingerprint TEXT NOT NULL UNIQUE,
		source_url  TEXT NOT NULL,
		target      TEXT NOT NULL,
		preset_id   TEXT NOT NULL,
		config_url  TEXT NOT NULL,
		created_at  INTEGER NOT NULL
	)`,
	`ALTER TABLE links ADD COLUMN groups TEXT`,
	`ALTER TABLE links ADD COLUMN exclude TEXT`,
	`CREATE TABLE IF NOT EXISTS prefs_custom (
		sub_key    TEXT PRIMARY KEY,
		groups     TEXT NOT NULL,
		updated_at TEXT NOT NULL
	);
	CREATE TABLE IF NOT EXISTS prefs_exclude (
		sub_key    TEXT NOT NULL,
		preset_id  TEXT NOT NULL,
		exclude    TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		PRIMARY KEY (sub_key, preset_id)
	)`,
}

type store struct {
	db      *sql.DB
	genCode func() (string, error) // 码生成器；测试注入固定序列用，默认 crypto/rand
}

// openStore 打开（或首次创建）${dataDir}/state.db 并跑幂等迁移。
// 库损坏（能打开失败/校验失败，非「文件不存在」）→ 返回错误，调用方拒绝启动。
func openStore(dataDir string) (*store, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("创建 DATA_DIR %s 失败: %w", dataDir, err)
	}
	path := filepath.Join(dataDir, "state.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("打开 %s 失败: %w", path, err)
	}
	db.SetMaxOpenConns(1) // 单连接串行化读写，规避多连接 SQLITE_BUSY
	st := &store{db: db, genCode: newCode}
	if err := st.migrate(path); err != nil {
		db.Close()
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil { // 凭据库文件权限契约钉死 0600
		db.Close()
		return nil, fmt.Errorf("设置 %s 权限 0600 失败: %w", path, err)
	}
	return st, nil
}

// migrate 按 PRAGMA user_version 应用未执行的迁移。读元数据失败即库损坏：
// 错误信息指引「先备份再排查，勿直接删除」，绝不自动清空。
func (st *store) migrate(path string) error {
	var v int
	if err := st.db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		return fmt.Errorf("读取 SQLite 库元数据失败（%s 可能已损坏）: %w；请先备份 %s 再排查，勿直接删除", path, err, path)
	}
	for i := v; i < len(schemaMigrations); i++ {
		tx, err := st.db.Begin()
		if err != nil {
			return fmt.Errorf("迁移 v%d 开事务失败: %w", i+1, err)
		}
		if _, err := tx.Exec(schemaMigrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("迁移 v%d 失败: %w；请先备份 %s 再排查，勿直接删除", i+1, err, path)
		}
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", i+1)); err != nil {
			tx.Rollback()
			return fmt.Errorf("写 user_version=%d 失败: %w", i+1, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("迁移 v%d 提交失败: %w", i+1, err)
		}
	}
	return nil
}

func (st *store) close() error { return st.db.Close() }

type storedLink struct {
	SourceURL string
	Target    string
	ConfigURL string
	Groups    string // v1.5.0 冻结的规范化 groups JSON（""=无组；同码冻结语义）
	Exclude   string // v1.5.0 增补冻结的规范化 exclude JSON（""=不删减；同码冻结语义）
}

func (st *store) codeByFingerprint(fp string) (string, error) {
	var code string
	err := st.db.QueryRow("SELECT code FROM links WHERE fingerprint = ?", fp).Scan(&code)
	return code, err
}

func (st *store) linkByCode(code string) (storedLink, bool, error) {
	var l storedLink
	var groups, exclude sql.NullString
	err := st.db.QueryRow("SELECT source_url, target, config_url, groups, exclude FROM links WHERE code = ?", code).
		Scan(&l.SourceURL, &l.Target, &l.ConfigURL, &groups, &exclude)
	if errors.Is(err, sql.ErrNoRows) {
		return storedLink{}, false, nil
	}
	if err != nil {
		return storedLink{}, false, err
	}
	l.Groups = groups.String
	l.Exclude = exclude.String
	return l, true, nil
}

// getOrCreateLink 幂等取码（契约「幂等与指纹」）：同指纹永远复用既有码；删除后同参数
// 重建 → 指纹相同但插入新随机码（即轮换语义）。插入的两类唯一约束冲突分开处理（OH-9）：
// 指纹冲突（并发下他人已插同参数）→ 重查返回既有码；码碰撞 → 重新生成再插（≤5 次）。
// 不解析驱动错误串定性：插入失败后重查指纹，命中即指纹冲突，未命中按码碰撞换码。
// groupsJSON / excludeJSON 为规范化序列化（""=无 → 列存 NULL；指纹侧空等价 "[]"）。
func (st *store) getOrCreateLink(subURL, target, presetID, configURL, groupsJSON, excludeJSON string) (string, error) {
	fp := shortFingerprint(subURL, target, configURL, groupsJSON, excludeJSON)
	if code, err := st.codeByFingerprint(fp); err == nil {
		return code, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	for i := 0; i < 5; i++ {
		code, err := st.genCode()
		if err != nil {
			return "", err
		}
		var groupsVal, excludeVal any // NULL=无（契约 links 表注）
		if groupsJSON != "" {
			groupsVal = groupsJSON
		}
		if excludeJSON != "" {
			excludeVal = excludeJSON
		}
		_, err = st.db.Exec(
			"INSERT INTO links (code, fingerprint, source_url, target, preset_id, config_url, created_at, groups, exclude) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
			code, fp, subURL, target, presetID, configURL, time.Now().Unix(), groupsVal, excludeVal)
		if err == nil {
			return code, nil
		}
		if existing, err2 := st.codeByFingerprint(fp); err2 == nil {
			return existing, nil
		}
	}
	return "", fmt.Errorf("短码插入重试 5 次仍失败")
}

type linkEntry struct {
	Code      string `json:"code"`
	Target    string `json:"target"`
	Preset    string `json:"preset"`
	SourceURL string `json:"sourceUrl"`
	CreatedAt int64  `json:"createdAt"`
}

// listLinks 管理端点数据：按 createdAt 降序（同秒并列按 code 倒序保证稳定序）
func (st *store) listLinks() ([]linkEntry, error) {
	rows, err := st.db.Query("SELECT code, target, preset_id, source_url, created_at FROM links ORDER BY created_at DESC, code DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []linkEntry{}
	for rows.Next() {
		var e linkEntry
		if err := rows.Scan(&e.Code, &e.Target, &e.Preset, &e.SourceURL, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// deleteLink 幂等删除（码不存在不报错；删除后该短链立即 404，同参数重建得新码）
func (st *store) deleteLink(code string) error {
	_, err := st.db.Exec("DELETE FROM links WHERE code = ?", code)
	return err
}

// ---- 分组偏好档案（v1.6.0）：两把钥匙——custom 按 sub_key（跨档/跨 target 共享），
// exclude 按 sub_key+preset_id（档位独立）；同钥匙单份覆盖（最后提交赢）。sub_key 为
// normalizeSubURL 规范化值；groups/exclude 存规范化 JSON（与 links.groups / links.exclude
// 同构；列 NOT NULL，空档案存 "[]"——「字段出现（含空数组）= 记录意图」）

// execer 匹配 *sql.DB 与 *sql.Tx 的 Exec：upsert 语句单份定义，单写与事务复合写共用
// （v1.6.0 评审修订 #10 —— saveGroupPrefs 两类档案同事务，杜绝半应用档案）
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func upsertPrefsCustomExec(e execer, subKey, groupsJSON string) error {
	if groupsJSON == "" {
		groupsJSON = "[]"
	}
	_, err := e.Exec(
		`INSERT INTO prefs_custom (sub_key, groups, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(sub_key) DO UPDATE SET groups = excluded.groups, updated_at = excluded.updated_at`,
		subKey, groupsJSON, prefsTimestamp())
	return err
}

func upsertPrefsExcludeExec(e execer, subKey, presetID, excludeJSON string) error {
	if excludeJSON == "" {
		excludeJSON = "[]"
	}
	_, err := e.Exec(
		`INSERT INTO prefs_exclude (sub_key, preset_id, exclude, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(sub_key, preset_id) DO UPDATE SET exclude = excluded.exclude, updated_at = excluded.updated_at`,
		subKey, presetID, excludeJSON, prefsTimestamp())
	return err
}

// upsertPrefsCustom 覆盖写自定义组档案（groupsJSON "" 视为 "[]"）
func (st *store) upsertPrefsCustom(subKey, groupsJSON string) error {
	return upsertPrefsCustomExec(st.db, subKey, groupsJSON)
}

// upsertPrefsExclude 覆盖写模板组删减档案（excludeJSON "" 视为 "[]"）
func (st *store) upsertPrefsExclude(subKey, presetID, excludeJSON string) error {
	return upsertPrefsExcludeExec(st.db, subKey, presetID, excludeJSON)
}

// upsertPrefs 单事务复合写（v1.6.0 评审修订 #10，对齐 deletePrefs 事务语义）：
// groupsJSON / excludeJSON 非 nil 即「字段出现」，两类档案同成同败——故障不会留下
// 「新 groups + 旧 exclude」的半应用档案。两者皆 nil = 字段均未出现，no-op。
func (st *store) upsertPrefs(subKey, presetID string, groupsJSON, excludeJSON *string) error {
	if groupsJSON == nil && excludeJSON == nil {
		return nil
	}
	tx, err := st.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if groupsJSON != nil {
		if err := upsertPrefsCustomExec(tx, subKey, *groupsJSON); err != nil {
			return err
		}
	}
	if excludeJSON != nil {
		if err := upsertPrefsExcludeExec(tx, subKey, presetID, *excludeJSON); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// getPrefsCustom 读自定义组档案（groups JSON；found=false = 无存档）
func (st *store) getPrefsCustom(subKey string) (groupsJSON string, found bool, err error) {
	err = st.db.QueryRow("SELECT groups FROM prefs_custom WHERE sub_key = ?", subKey).Scan(&groupsJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return groupsJSON, err == nil, err
}

// getPrefsExclude 读指定档的删减档案（exclude JSON；found=false = 该档无存档）
func (st *store) getPrefsExclude(subKey, presetID string) (excludeJSON string, found bool, err error) {
	err = st.db.QueryRow("SELECT exclude FROM prefs_exclude WHERE sub_key = ? AND preset_id = ?", subKey, presetID).Scan(&excludeJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return excludeJSON, err == nil, err
}

// deletePrefs 清该订阅的全部偏好档案（custom 一份 + exclude 全档）：单事务防半删；
// 幂等（无档案时返回 false/0 不报错）
func (st *store) deletePrefs(subKey string) (delCustom bool, delExclude int, err error) {
	tx, err := st.db.Begin()
	if err != nil {
		return false, 0, err
	}
	defer tx.Rollback()
	rc, err := tx.Exec("DELETE FROM prefs_custom WHERE sub_key = ?", subKey)
	if err != nil {
		return false, 0, err
	}
	re, err := tx.Exec("DELETE FROM prefs_exclude WHERE sub_key = ?", subKey)
	if err != nil {
		return false, 0, err
	}
	if err := tx.Commit(); err != nil {
		return false, 0, err
	}
	if n, err := rc.RowsAffected(); err == nil {
		delCustom = n > 0
	}
	if n, err := re.RowsAffected(); err == nil {
		delExclude = int(n)
	}
	return delCustom, delExclude, nil
}

// prefsTimestamp updated_at 列的统一形态（RFC3339 UTC 秒粒度，人读运维友好）
func prefsTimestamp() string {
	return time.Now().UTC().Format(time.RFC3339)
}
