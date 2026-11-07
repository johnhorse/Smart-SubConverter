package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// BackupRules 归一化：key 与 value 做 trim，剔除空 value
func TestNormalizeGroupsBackupRules(t *testing.T) {
	raw := []nodeGroup{
		{
			Name:     "美专线",
			Keywords: []string{"美"},
			BackupRules: map[string]string{
				"  备用1  ": "  SiliconValley|Portland  ",
				"备用2":     "   ", // 空 value 剔除
				"备用3":     "",    // 空 value 剔除
				"   ":      "rule", // 空 key 剔除
			},
		},
		{
			Name:     "日专线",
			Keywords: []string{"日"},
			BackupRules: map[string]string{
				"备用": "   ", // 全空剔除后变为 nil
			},
		},
	}

	norm := normalizeGroups(raw)
	if len(norm) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(norm))
	}

	// 组 1：留下的只有 备用1
	g1 := norm[0]
	if len(g1.BackupRules) != 1 {
		t.Fatalf("expected 1 backup rule, got %d: %+v", len(g1.BackupRules), g1.BackupRules)
	}
	if val, ok := g1.BackupRules["备用1"]; !ok || val != "SiliconValley|Portland" {
		t.Errorf("unexpected backup rule for 备用1: %q", val)
	}

	// 组 2：全部为空，BackupRules 应为 nil
	g2 := norm[1]
	if g2.BackupRules != nil {
		t.Errorf("expected nil BackupRules, got %+v", g2.BackupRules)
	}
}

// BackupRules 校验：key <= 32 runes，禁反引号与控制字符；value <= 256 runes，禁反引号与控制字符
func TestValidateGroupsBackupRules(t *testing.T) {
	// 合法用例
	valid := []nodeGroup{
		{
			Name:     "香港节点",
			Keywords: []string{"HK"},
			BackupRules: map[string]string{
				"备用":                   "HongKong|HK",
				strings.Repeat("k", 32): strings.Repeat("v", 256), // 边界值 32 / 256
			},
		},
	}
	if err := validateGroups(valid); err != nil {
		t.Errorf("expected valid backup rules to pass, got: %v", err)
	}

	// key 超长（33 runes）
	if err := validateGroups([]nodeGroup{{
		Name:     "n",
		Keywords: []string{"k"},
		BackupRules: map[string]string{
			strings.Repeat("k", 33): "valid",
		},
	}}); err == nil {
		t.Errorf("expected error for key > 32 runes")
	}

	// key 含反引号
	if err := validateGroups([]nodeGroup{{
		Name:     "n",
		Keywords: []string{"k"},
		BackupRules: map[string]string{
			"tag`bad": "valid",
		},
	}}); err == nil {
		t.Errorf("expected error for key with backtick")
	}

	// key 含控制字符
	if err := validateGroups([]nodeGroup{{
		Name:     "n",
		Keywords: []string{"k"},
		BackupRules: map[string]string{
			"tag\nbad": "valid",
		},
	}}); err == nil {
		t.Errorf("expected error for key with newline")
	}

	// key 为空
	if err := validateGroups([]nodeGroup{{
		Name:     "n",
		Keywords: []string{"k"},
		BackupRules: map[string]string{
			"": "valid",
		},
	}}); err == nil {
		t.Errorf("expected error for empty key")
	}

	// value 超长（257 runes）
	if err := validateGroups([]nodeGroup{{
		Name:     "n",
		Keywords: []string{"k"},
		BackupRules: map[string]string{
			"tag": strings.Repeat("v", 257),
		},
	}}); err == nil {
		t.Errorf("expected error for value > 256 runes")
	}

	// value 含反引号
	if err := validateGroups([]nodeGroup{{
		Name:     "n",
		Keywords: []string{"k"},
		BackupRules: map[string]string{
			"tag": "a`b",
		},
	}}); err == nil {
		t.Errorf("expected error for value with backtick")
	}

	// value 含控制字符
	if err := validateGroups([]nodeGroup{{
		Name:     "n",
		Keywords: []string{"k"},
		BackupRules: map[string]string{
			"tag": "a\x00b",
		},
	}}); err == nil {
		t.Errorf("expected error for value with control char")
	}
}

// BackupRules JSON 序列化与反序列化（omitempty 与指纹兼容）
func TestNodeGroupJSONWithBackupRules(t *testing.T) {
	// 1. 无 BackupRules 时 omitempty（存量兼容）
	g1 := nodeGroup{Name: "专线", Keywords: []string{"专线"}, URLTest: true}
	b1, err := json.Marshal(g1)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b1), "backupRules") {
		t.Errorf("expected backupRules to be omitted when nil: %s", string(b1))
	}

	// 2. 携带 BackupRules 序列化与反序列化还原
	g2 := nodeGroup{
		Name:     "美专线",
		Keywords: []string{"美"},
		URLTest:  false,
		BackupRules: map[string]string{
			"备用": "SiliconValley",
		},
	}
	b2, err := json.Marshal(g2)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b2), `"backupRules":{"备用":"SiliconValley"}`) {
		t.Errorf("unexpected json: %s", string(b2))
	}

	var parsed nodeGroup
	if err := json.Unmarshal(b2, &parsed); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if parsed.Name != "美专线" || parsed.BackupRules["备用"] != "SiliconValley" {
		t.Errorf("unexpected unmarshaled: %+v", parsed)
	}
}
