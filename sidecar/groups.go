// 自定义分组（v1.5.0）共享件：组意图类型、请求校验、规范化序列化。契约
// 「groups 请求字段与归一化」节；probe 节点名解析在 preview.go，拉取期派生在 derive.go。
package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	maxGroups       = 10
	maxGroupRunes   = 32
	maxKeywords     = 5
	maxPatternRunes = 256
	maxExclude      = 64
	maxExcludeRunes = 64
)

// nodeGroup 用户组意图（契约四字段，v1.6.1 前三字段，v1.9.0 增补 backupRules）；keywords 为字面量 substring
// AND 语义，非正则（派生侧每词 regexp.QuoteMeta 转义后拼前瞻正则，计数与前端字面
// 高亮等价——gate2）；pattern 为正则模式（前端正则 Tab，pattern 优先于 keywords 生效，
// 自带 PCRE 语法由引擎侧解释）。两者至少一个有效。backupRules 为备用机场自定义替补匹配规则。
type nodeGroup struct {
	Name        string            `json:"name"`
	Keywords    []string          `json:"keywords"`
	Pattern     string            `json:"pattern,omitempty"` // 空 = 关键词组（序列化省略，存量指纹逐字节不变）
	URLTest     bool              `json:"urlTest"`
	BackupRules map[string]string `json:"backupRules,omitempty"` // v1.9.0: key 为备用机场 Tag，value 为替补匹配规则
}

// UnmarshalJSON 旧格式容错（试运行期存量）：keyword 单字段且无 keywords → 当单词组；
// 请求解码与 parseGroupsJSON（库存旧 JSON）共用此路径。写入侧只写新格式（默认 Marshal）
func (g *nodeGroup) UnmarshalJSON(b []byte) error {
	var aux struct {
		Name        string            `json:"name"`
		Keywords    []string          `json:"keywords"`
		Pattern     string            `json:"pattern"`
		URLTest     bool              `json:"urlTest"`
		Keyword     *string           `json:"keyword"`
		BackupRules map[string]string `json:"backupRules"`
	}
	if err := json.Unmarshal(b, &aux); err != nil {
		return err
	}
	*g = nodeGroup{
		Name:        aux.Name,
		Keywords:    aux.Keywords,
		Pattern:     aux.Pattern,
		URLTest:     aux.URLTest,
		BackupRules: aux.BackupRules,
	}
	if len(g.Keywords) == 0 && aux.Keyword != nil {
		g.Keywords = []string{*aux.Keyword}
	}
	return nil
}

// normalizeGroups 请求组归一化：name/每词/pattern 两端 trim（trim 后落定，校验/存储/
// groupsJSON 都用规范化值）；v1.9.0: BackupRules key 与 value 做 trim，剔除空 value
func normalizeGroups(raw []nodeGroup) []nodeGroup {
	out := make([]nodeGroup, 0, len(raw))
	for _, g := range raw {
		g.Name = strings.TrimSpace(g.Name)
		kw := make([]string, len(g.Keywords))
		for i, w := range g.Keywords {
			kw[i] = strings.TrimSpace(w)
		}
		g.Keywords = kw
		g.Pattern = strings.TrimSpace(g.Pattern)
		if len(g.BackupRules) > 0 {
			norm := make(map[string]string, len(g.BackupRules))
			for k, v := range g.BackupRules {
				tk := strings.TrimSpace(k)
				tv := strings.TrimSpace(v)
				if tk != "" && tv != "" {
					norm[tk] = tv
				}
			}
			if len(norm) > 0 {
				g.BackupRules = norm
			} else {
				g.BackupRules = nil
			}
		}
		out = append(out, g)
	}
	return out
}

// validateGroups 契约校验（不过 → 上层 bad_groups，不调上游）：name/每词 trim 后
// 非空、各 ≤32 rune、禁反引号与控制字符（U+0000–U+001F、U+007F）、keywords 1..5 词、
// name 组内不重复、≤10 项；v1.6.1：keywords 与 pattern 至少一个有效，pattern ≤256 rune、
// 禁反引号与控制字符（\r \n 属控制字符自然涵盖，防破坏 ini 组行格式）。
// v1.9.0：BackupRules 每项校验：key ≤32 rune、value ≤256 rune，禁反引号与控制字符。
// 铁律：绝不用 regexp.Compile 校验 pattern——Go regexp 是 RE2，不支持 Perl 前瞻
// (?=...) / (?!...)，Compile 会误拒引擎侧完全合法的 PCRE；语法合法性由前端
// JS RegExp 实时校验把关，服务端只做长度与非法字符
func validateGroups(gs []nodeGroup) error {
	if len(gs) > maxGroups {
		return fmt.Errorf("groups 超过 %d 项", maxGroups)
	}
	seen := map[string]bool{}
	for _, g := range gs {
		check := func(what, val string, maxRunes int) error {
			if val == "" {
				return fmt.Errorf("%s trim 后为空", what)
			}
			if utf8.RuneCountInString(val) > maxRunes {
				return fmt.Errorf("%s 超过 %d 字", what, maxRunes)
			}
			if strings.ContainsRune(val, '`') {
				return fmt.Errorf("%s 含反引号", what)
			}
			for _, r := range val {
				if r <= 0x1F || r == 0x7F {
					return fmt.Errorf("%s 含控制字符", what)
				}
			}
			return nil
		}
		if err := check("name", g.Name, maxGroupRunes); err != nil {
			return err
		}
		if len(g.Keywords) == 0 && g.Pattern == "" {
			return fmt.Errorf("keywords 与 pattern 均为空")
		}
		if len(g.Keywords) > maxKeywords {
			return fmt.Errorf("keywords 超过 %d 词", maxKeywords)
		}
		for _, w := range g.Keywords {
			if err := check("keyword", w, maxGroupRunes); err != nil {
				return err
			}
		}
		if g.Pattern != "" {
			if err := check("pattern", g.Pattern, maxPatternRunes); err != nil {
				return err
			}
		}
		for k, v := range g.BackupRules {
			if err := check("backupRule tag", k, maxGroupRunes); err != nil {
				return err
			}
			if err := check("backupRule pattern", v, maxPatternRunes); err != nil {
				return err
			}
		}
		if seen[g.Name] {
			return fmt.Errorf("组名重复")
		}
		seen[g.Name] = true
	}
	return nil
}

// canonicalGroupsJSON 规范化序列化（指纹与列存共用）：键序 name→keywords→
// (pattern)→urlTest（struct 声明序；pattern omitempty——关键词组不含该键，存量
// 指纹逐字节不变）、无空白、空组 "[]"
func canonicalGroupsJSON(gs []nodeGroup) string {
	if len(gs) == 0 {
		return "[]"
	}
	b, err := json.Marshal(gs)
	if err != nil {
		return "[]" // nodeGroup 三字段恒可序列化，防御性兜底
	}
	return string(b)
}

// parseGroupsJSON 列存 JSON 还原（建链期已校验，此处只解包；失败 → 上层 derive_error）。
// ""（列存 NULL 读回）等价无组
func parseGroupsJSON(s string) ([]nodeGroup, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var gs []nodeGroup
	if err := json.Unmarshal([]byte(s), &gs); err != nil {
		return nil, fmt.Errorf("groups JSON 解析失败: %w", err)
	}
	return gs, nil
}

// normalizeExclude 模板组删减名单归一化（契约 POST /api/preview「v1.5.0 增补字段」）：
// 每名两端 trim 后落定、≤64 项、单名 ≤64 rune、禁反引号与控制字符（U+0000–U+001F、
// U+007F）；重复名去重后升序排序（指纹 v3 规范化：名单集合与输入顺序无关）。
// 违规 → 上层 bad_groups
func normalizeExclude(raw []string) ([]string, error) {
	if len(raw) > maxExclude {
		return nil, fmt.Errorf("exclude 超过 %d 项", maxExclude)
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(raw))
	for _, name := range raw {
		v := strings.TrimSpace(name)
		if v == "" {
			return nil, fmt.Errorf("exclude 名单名 trim 后为空")
		}
		if utf8.RuneCountInString(v) > maxExcludeRunes {
			return nil, fmt.Errorf("exclude 名单名超过 %d 字", maxExcludeRunes)
		}
		if strings.ContainsRune(v, '`') {
			return nil, fmt.Errorf("exclude 名单名含反引号")
		}
		for _, r := range v {
			if r <= 0x1F || r == 0x7F {
				return nil, fmt.Errorf("exclude 名单名含控制字符")
			}
		}
		if seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	sort.Strings(out)
	return out, nil
}

// canonicalExcludeJSON 规范化序列化（指纹 v3 与 links.exclude 列共用）：去重升序 JSON
// 数组、无空白、空为 "[]"（空串等价 "[]"）
func canonicalExcludeJSON(ex []string) string {
	if len(ex) == 0 {
		return "[]"
	}
	b, err := json.Marshal(ex)
	if err != nil {
		return "[]" // []string 恒可序列化，防御性兜底
	}
	return string(b)
}

// parseExcludeJSON links.exclude 列还原（建链期已校验；"" 等价不删减）
func parseExcludeJSON(s string) ([]string, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var ex []string
	if err := json.Unmarshal([]byte(s), &ex); err != nil {
		return nil, fmt.Errorf("exclude JSON 解析失败: %w", err)
	}
	return ex, nil
}
