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

// shortFingerprint 短链指纹 = SHA-256 of
// "v3|{scheme}|{host}|{path}|{rawquery}|{target}|{config_url}|{groupsJSON}|{excludeJSON}"。
// v1.5.0 增补升 v3：尾段追加 excludeJSON（去重升序 JSON 数组、无空白、空为 "[]"；空串
// 等价 "[]"）——「改删减=新短链」；v1/v2 前缀差异使存量指纹不再命中（契约「幂等与指纹」：
// 升级后同参数首建换新码属预期一次性影响，存量码照常 hit）。
// 订阅 URL 规范化（契约级钉死，OH-8）：两端 trim、scheme/host 小写、去默认端口
// （http:80 / https:443）、path 空补 /、query 原样保留、fragment 丢弃。
// target 为请求原样值（auto 或指定）；config_url 用解析后的档位 URL（同 id 被
// PRESET_URL 覆盖时指纹自然区分）。已通过预校验的 URL 解析不会失败，防御性兜底为全空段。
func shortFingerprint(subURL, target, configURL, groupsJSON, excludeJSON string) string {
	var scheme, host, path, query string
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
// 卡死启动
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
