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
)

// nodeGroup 用户组意图（契约三字段）；keyword 为字面量 substring 语义，非正则
// （派生侧 regexp.QuoteMeta 转义后写入，计数与前端字面高亮等价——闸门例 8）
type nodeGroup struct {
	Name    string `json:"name"`
	Keyword string `json:"keyword"`
	URLTest bool   `json:"urlTest"`
}

// normalizeGroups 请求组归一化：name/keyword 两端 trim（trim 后落定，校验/存储/
// groupsJSON 都用规范化值）
func normalizeGroups(raw []nodeGroup) []nodeGroup {
	out := make([]nodeGroup, 0, len(raw))
	for _, g := range raw {
		g.Name = strings.TrimSpace(g.Name)
		g.Keyword = strings.TrimSpace(g.Keyword)
		out = append(out, g)
	}
	return out
}

// validateGroups 契约校验（不过 → 上层 bad_groups，不调上游）：name/keyword trim 后
// 非空、≤32 rune、禁反引号与控制字符（U+0000–U+001F、U+007F）、name 组内不重复、≤10 项
func validateGroups(gs []nodeGroup) error {
	if len(gs) > maxGroups {
		return fmt.Errorf("groups 超过 %d 项", maxGroups)
	}
	seen := map[string]bool{}
	for _, g := range gs {
		for _, f := range []struct{ what, val string }{
			{"name", g.Name},
			{"keyword", g.Keyword},
		} {
			if f.val == "" {
				return fmt.Errorf("%s trim 后为空", f.what)
			}
			if utf8.RuneCountInString(f.val) > maxGroupRunes {
				return fmt.Errorf("%s 超过 %d 字", f.what, maxGroupRunes)
			}
			if strings.ContainsRune(f.val, '`') {
				return fmt.Errorf("%s 含反引号", f.what)
			}
			for _, r := range f.val {
				if r <= 0x1F || r == 0x7F {
					return fmt.Errorf("%s 含控制字符", f.what)
				}
			}
		}
		if seen[g.Name] {
			return fmt.Errorf("组名重复")
		}
		seen[g.Name] = true
	}
	return nil
}

// canonicalGroupsJSON 规范化序列化（指纹与列存共用）：键序 name→keyword→urlTest
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
