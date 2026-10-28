// 自定义分组（v1.5.0）共享件：组意图类型、请求校验、规范化序列化。契约
// 「groups 请求字段与归一化」节；probe 节点名解析在 preview.go，拉取期派生在 derive.go。
package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	maxGroups     = 10
	maxGroupRunes = 32
	maxKeywords   = 5
)

// nodeGroup 用户组意图（契约三字段）；keywords 为字面量 substring AND 语义，非正则
// （派生侧每词 regexp.QuoteMeta 转义后拼前瞻正则，计数与前端字面高亮等价——gate2）
type nodeGroup struct {
	Name     string   `json:"name"`
	Keywords []string `json:"keywords"`
	URLTest  bool     `json:"urlTest"`
}

// UnmarshalJSON 旧格式容错（试运行期存量）：keyword 单字段且无 keywords → 当单词组；
// 请求解码与 parseGroupsJSON（库存旧 JSON）共用此路径。写入侧只写新格式（默认 Marshal）
func (g *nodeGroup) UnmarshalJSON(b []byte) error {
	var aux struct {
		Name     string    `json:"name"`
		Keywords []string  `json:"keywords"`
		URLTest  bool      `json:"urlTest"`
		Keyword  *string   `json:"keyword"`
	}
	if err := json.Unmarshal(b, &aux); err != nil {
		return err
	}
	*g = nodeGroup{Name: aux.Name, Keywords: aux.Keywords, URLTest: aux.URLTest}
	if len(g.Keywords) == 0 && aux.Keyword != nil {
		g.Keywords = []string{*aux.Keyword}
	}
	return nil
}

// normalizeGroups 请求组归一化：name/每词两端 trim（trim 后落定，校验/存储/
// groupsJSON 都用规范化值）
func normalizeGroups(raw []nodeGroup) []nodeGroup {
	out := make([]nodeGroup, 0, len(raw))
	for _, g := range raw {
		g.Name = strings.TrimSpace(g.Name)
		kw := make([]string, len(g.Keywords))
		for i, w := range g.Keywords {
			kw[i] = strings.TrimSpace(w)
		}
		g.Keywords = kw
		out = append(out, g)
	}
	return out
}

// validateGroups 契约校验（不过 → 上层 bad_groups，不调上游）：name/每词 trim 后
// 非空、各 ≤32 rune、禁反引号与控制字符（U+0000–U+001F、U+007F）、keywords 1..5 词、
// name 组内不重复、≤10 项
func validateGroups(gs []nodeGroup) error {
	if len(gs) > maxGroups {
		return fmt.Errorf("groups 超过 %d 项", maxGroups)
	}
	seen := map[string]bool{}
	for _, g := range gs {
		check := func(what, val string) error {
			if val == "" {
				return fmt.Errorf("%s trim 后为空", what)
			}
			if utf8.RuneCountInString(val) > maxGroupRunes {
				return fmt.Errorf("%s 超过 %d 字", what, maxGroupRunes)
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
		if err := check("name", g.Name); err != nil {
			return err
		}
		if len(g.Keywords) == 0 {
			return fmt.Errorf("keywords 为空")
		}
		if len(g.Keywords) > maxKeywords {
			return fmt.Errorf("keywords 超过 %d 词", maxKeywords)
		}
		for _, w := range g.Keywords {
			if err := check("keyword", w); err != nil {
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

// canonicalGroupsJSON 规范化序列化（指纹与列存共用）：键序 name→keywords→urlTest
// （struct 声明序）、无空白、空组 "[]"
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

// parseGroupsJSON 列存 JSON 还原（建链期已校验，此处只解包；失败 → 上层 derive_error）
func parseGroupsJSON(s string) ([]nodeGroup, error) {
	var gs []nodeGroup
	if err := json.Unmarshal([]byte(s), &gs); err != nil {
		return nil, fmt.Errorf("groups JSON 解析失败: %w", err)
	}
	return gs, nil
}
