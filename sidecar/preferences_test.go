// 分组偏好记忆接口 + preview 保存钩子单测（v1.6.0）：query 回 custom（跨档共享）与
// exclude（仅当档）、delete 清该订阅全部、错误族（bad_url_* / bad_preset，HTTP 400）、
// 出树态（snap=nil）接口照常、preview 钩子「出现才写 / 未出现不动 / 空数组也是出现 /
// probe 不写 / 失败不写」。
package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// postPrefs 直调偏好 handler：返回状态码与响应体（错误族断言用原始 body，形态口径同
// preview 测试的字符串断言风格）
func postPrefs(t *testing.T, s *server, method, path, body string) (int, string) {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	return w.Code, w.Body.String()
}

// query 回填：custom 跨档共享（同 custom_groups）、exclude 仅回当档；无存档两字段 null；
// 出树态（mustServer 未 enableSnap，分组能力位 false）接口照常工作（纯存取）
func TestPreferencesQueryRoundtrip(t *testing.T) {
	s := mustServer(t, testConfig(t, "http://unused"))
	subKey := normalizeSubURL("https://sub.example.com/l")
	gJSON := `[{"name":"专线","keywords":["专线"],"urlTest":false}]`
	if err := s.store.upsertPrefsCustom(subKey, gJSON); err != nil {
		t.Fatal(err)
	}
	if err := s.store.upsertPrefsExclude(subKey, "default", `["🌍 国外媒体"]`); err != nil {
		t.Fatal(err)
	}
	if err := s.store.upsertPrefsExclude(subKey, "full_multimode", `["💬 Ai平台"]`); err != nil {
		t.Fatal(err)
	}

	// default 档（preset_id 缺省 resolve 到 default）；sub_url 用等价变体（大写 host +
	// 默认端口 + fragment）证 handler 侧走同一规范化
	code, body := postPrefs(t, s, http.MethodPost, "/api/preferences/query",
		`{"sub_url":"HTTPS://SUB.EXAMPLE.COM:443/l#frag"}`)
	if code != http.StatusOK {
		t.Fatalf("query 状态码 = %d body=%s", code, body)
	}
	var resp struct {
		CustomGroups json.RawMessage `json:"custom_groups"`
		Exclude      json.RawMessage `json:"exclude"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatal(err)
	}
	if string(resp.CustomGroups) != gJSON {
		t.Errorf("custom_groups = %s，want %s", resp.CustomGroups, gJSON)
	}
	if string(resp.Exclude) != `["🌍 国外媒体"]` {
		t.Errorf("default 档 exclude = %s", resp.Exclude)
	}

	// full_multimode 档：custom 同一份（跨档共享），exclude 换当档
	code, body = postPrefs(t, s, http.MethodPost, "/api/preferences/query",
		`{"sub_url":"https://sub.example.com/l","preset_id":"full_multimode"}`)
	if code != http.StatusOK {
		t.Fatalf("query 状态码 = %d body=%s", code, body)
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatal(err)
	}
	if string(resp.CustomGroups) != gJSON {
		t.Errorf("custom_groups 跨档应共享 = %s", resp.CustomGroups)
	}
	if string(resp.Exclude) != `["💬 Ai平台"]` {
		t.Errorf("full_multimode 档 exclude = %s，应仅回当档", resp.Exclude)
	}

	// 无存档订阅：两字段 null（独立 miss 语义）
	code, body = postPrefs(t, s, http.MethodPost, "/api/preferences/query",
		`{"sub_url":"https://none.example/z","preset_id":"default"}`)
	if code != http.StatusOK {
		t.Fatalf("query 状态码 = %d body=%s", code, body)
	}
	if !strings.Contains(body, `"custom_groups":null`) || !strings.Contains(body, `"exclude":null`) {
		t.Errorf("无存档应双 null：%s", body)
	}
}

// 错误族沿用 preview（bad_url_* / bad_preset + errorCode/message 形态），HTTP 400；
// 判定序 bad_url_* 先于 bad_preset；body 非法 JSON 按 bad_url_empty
func TestPreferencesQueryErrors(t *testing.T) {
	s := mustServer(t, testConfig(t, "http://unused"))
	cases := []struct {
		name, body, wantCode string
	}{
		{"空 url", `{"sub_url":""}`, "bad_url_empty"},
		{"非法 JSON", `{not json`, "bad_url_empty"},
		{"非 http(s)", `{"sub_url":"ftp://example.com/s"}`, "bad_url_format"},
		{"私网", `{"sub_url":"http://127.0.0.1:8080/s"}`, "bad_url_private"},
		{"bad preset", `{"sub_url":"https://sub.example.com/l","preset_id":"nope"}`, "bad_preset"},
		{"url 先于 preset", `{"sub_url":"","preset_id":"nope"}`, "bad_url_empty"},
	}
	for _, c := range cases {
		code, body := postPrefs(t, s, http.MethodPost, "/api/preferences/query", c.body)
		if code != http.StatusBadRequest {
			t.Errorf("%s：状态码 = %d，应 400（body=%s）", c.name, code, body)
		}
		var resp prefsErrorResponse
		if err := json.Unmarshal([]byte(body), &resp); err != nil {
			t.Errorf("%s：响应非合法 JSON: %v（%s）", c.name, err, body)
			continue
		}
		if resp.OK || resp.ErrorCode == nil || *resp.ErrorCode != c.wantCode {
			t.Errorf("%s：errorCode = %v，want %s（body=%s）", c.name, resp.ErrorCode, c.wantCode, body)
		}
		if resp.Message == "" {
			t.Errorf("%s：message 不应为空", c.name)
		}
	}
}

// delete 清该订阅全部档案（custom + exclude 全档）、他订阅不动、幂等；错误族同 query
func TestPreferencesDelete(t *testing.T) {
	s := mustServer(t, testConfig(t, "http://unused"))
	kA := normalizeSubURL("https://sub.example.com/l")
	kB := normalizeSubURL("https://other.example/s")
	if err := s.store.upsertPrefsCustom(kA, `[]`); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"default", "full_multimode"} {
		if err := s.store.upsertPrefsExclude(kA, p, `["g"]`); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.store.upsertPrefsCustom(kB, `[]`); err != nil {
		t.Fatal(err)
	}

	code, body := postPrefs(t, s, http.MethodDelete, "/api/preferences", `{"sub_url":" https://SUB.example.com/l "}`)
	if code != http.StatusOK {
		t.Fatalf("delete 状态码 = %d body=%s", code, body)
	}
	var resp struct {
		DeletedCustom  bool `json:"deleted_custom"`
		DeletedExclude int  `json:"deleted_exclude"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.DeletedCustom || resp.DeletedExclude != 2 {
		t.Errorf("delete = (%v, %d)，want (true, 2)", resp.DeletedCustom, resp.DeletedExclude)
	}
	if _, found, _ := s.store.getPrefsCustom(kB); !found {
		t.Error("他订阅档案不应被动")
	}
	// 幂等
	code, body = postPrefs(t, s, http.MethodDelete, "/api/preferences", `{"sub_url":"https://sub.example.com/l"}`)
	if code != http.StatusOK || !strings.Contains(body, `"deleted_custom":false`) || !strings.Contains(body, `"deleted_exclude":0`) {
		t.Errorf("delete 幂等应回 false/0：code=%d body=%s", code, body)
	}
	// 错误族
	code, body = postPrefs(t, s, http.MethodDelete, "/api/preferences", `{"sub_url":"bad"}`)
	if code != http.StatusBadRequest || !strings.Contains(body, `"bad_url_format"`) {
		t.Errorf("delete 非法 url 应 400 bad_url_format：code=%d body=%s", code, body)
	}
}

// preview 保存钩子（非 probe 成功路径）：groups/exclude 字段出现 → upsert（规范化 JSON、
// exclude 落当档 preset）；能查回（端到端：preview 写 → query 读）
func TestPreviewSavesGroupPrefs(t *testing.T) {
	ext := stubExtended(t, 200, "[General]\nx", 0)
	defer ext.Close()
	cfg := testConfig(t, ext.URL)
	cfg.presetURL = defaultPresetURL // 不豁免 default 档，能力位测试需要
	s := mustServer(t, cfg)
	enableSnap(t, s)

	resp, _ := postPreview(t, s,
		`{"url":"https://sub.example.com/l","target":"surge","preset":"full_multimode",`+
			`"groups":[{"name":"专线","keywords":["美国","专线"],"urlTest":false}],"exclude":["🌍 国外媒体"]}`,
		"h:1", "")
	if !resp.OK || resp.ErrorCode != nil {
		t.Fatalf("preview 应成功：%+v", resp)
	}
	subKey := normalizeSubURL("https://sub.example.com/l")
	wantGroups := `[{"name":"专线","keywords":["美国","专线"],"urlTest":false}]`
	if got, found, _ := s.store.getPrefsCustom(subKey); !found || got != wantGroups {
		t.Errorf("prefs_custom = %q found=%v，want %s", got, found, wantGroups)
	}
	if got, found, _ := s.store.getPrefsExclude(subKey, "full_multimode"); !found || got != `["🌍 国外媒体"]` {
		t.Errorf("prefs_exclude(full_multimode) = %q found=%v", got, found)
	}
	if _, found, _ := s.store.getPrefsExclude(subKey, "default"); found {
		t.Error("exclude 应只落请求档位 full_multimode，default 档应 miss")
	}
	// 端到端：query 读回同一份
	code, body := postPrefs(t, s, http.MethodPost, "/api/preferences/query",
		`{"sub_url":"https://sub.example.com/l","preset_id":"full_multimode"}`)
	if code != http.StatusOK || !strings.Contains(body, `"name":"专线"`) || !strings.Contains(body, `"exclude":["🌍 国外媒体"]`) {
		t.Errorf("query 应读回 preview 写入的偏好：code=%d body=%s", code, body)
	}
}

// 字段未出现 = 不动档案：无 groups/exclude 字段的成功 preview 不写不删既有档案
func TestPreviewPrefsFieldAbsentKeepsArchive(t *testing.T) {
	ext := stubExtended(t, 200, "[General]\nx", 0)
	defer ext.Close()
	cfg := testConfig(t, ext.URL)
	cfg.presetURL = defaultPresetURL
	s := mustServer(t, cfg)
	enableSnap(t, s)
	subKey := normalizeSubURL("https://sub.example.com/l")
	gJSON := `[{"name":"旧组","keywords":["旧"],"urlTest":false}]`
	if err := s.store.upsertPrefsCustom(subKey, gJSON); err != nil {
		t.Fatal(err)
	}
	if err := s.store.upsertPrefsExclude(subKey, "default", `["🚫 旧删减"]`); err != nil {
		t.Fatal(err)
	}

	if resp, _ := postPreview(t, s, `{"url":"https://sub.example.com/l","target":"surge"}`, "h:1", ""); !resp.OK {
		t.Fatalf("preview 应成功：%+v", resp)
	}
	if got, found, _ := s.store.getPrefsCustom(subKey); !found || got != gJSON {
		t.Errorf("字段未出现不应动档案：got=%q found=%v", got, found)
	}
	if got, found, _ := s.store.getPrefsExclude(subKey, "default"); !found || got != `["🚫 旧删减"]` {
		t.Errorf("字段未出现不应动档案：got=%q found=%v", got, found)
	}
}

// 空数组也是「出现」：groups:[] / exclude:[] 显式携带 → 档案记 []（与「未出现不动」相对）
func TestPreviewPrefsEmptyArrayWrites(t *testing.T) {
	ext := stubExtended(t, 200, "[General]\nx", 0)
	defer ext.Close()
	cfg := testConfig(t, ext.URL)
	cfg.presetURL = defaultPresetURL
	s := mustServer(t, cfg)
	enableSnap(t, s)

	if resp, _ := postPreview(t, s,
		`{"url":"https://sub.example.com/l","target":"surge","groups":[],"exclude":[]}`, "h:1", ""); !resp.OK {
		t.Fatalf("preview 应成功：%+v", resp)
	}
	subKey := normalizeSubURL("https://sub.example.com/l")
	if got, found, _ := s.store.getPrefsCustom(subKey); !found || got != "[]" {
		t.Errorf("groups:[] 出现应记空档案：got=%q found=%v", got, found)
	}
	if got, found, _ := s.store.getPrefsExclude(subKey, "default"); !found || got != "[]" {
		t.Errorf("exclude:[] 出现应记空档案：got=%q found=%v", got, found)
	}
}

// probe 模式绝不写档（带 groups/exclude 也不写；links 零副作用已有既有断言，此处补 prefs）
func TestPreviewPrefsProbeNoWrite(t *testing.T) {
	subBody := "ss://YWJjLTEyOjEuMi4zLjQ6NDQz#%E9%A6%99%E6%B8%AF%E4%B8%93%E7%BA%BF\n"
	subURL := publicAirportURL(t, subBody)
	ext := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("target") == "mixed" {
			content, err := fetchBody(q.Get("url"))
			if err != nil {
				http.Error(w, err.Error(), 502)
				return
			}
			_, _ = w.Write([]byte(base64.StdEncoding.EncodeToString(content)))
			return
		}
		http.NotFound(w, r)
	}))
	defer ext.Close()
	cfg := testConfig(t, ext.URL)
	cfg.presetURL = defaultPresetURL
	s := mustServer(t, cfg)
	enableSnap(t, s)
	ts := httptest.NewServer(s.routes())
	defer ts.Close()
	s.cfg.sidecarBase = ts.URL

	resp := postProbe(t, s, `{"url":"`+subURL+`","probe":true,`+
		`"groups":[{"name":"专线","keywords":["专线"],"urlTest":false}],"exclude":["🌍 国外媒体"]}`)
	if !resp.OK {
		t.Fatalf("probe 应成功：%+v", resp)
	}
	var n int
	if err := s.store.db.QueryRow("SELECT COUNT(*) FROM prefs_custom").Scan(&n); err != nil || n != 0 {
		t.Errorf("probe 零副作用：prefs_custom 行数 = %d (err %v)，应 0", n, err)
	}
	if err := s.store.db.QueryRow("SELECT COUNT(*) FROM prefs_exclude").Scan(&n); err != nil || n != 0 {
		t.Errorf("probe 零副作用：prefs_exclude 行数 = %d (err %v)，应 0", n, err)
	}
}

// 失败路径绝不写档：上游错（upstream_error）与预校验错（groups_disabled）都带 groups 提交，
// prefs 两表必须仍空
func TestPreviewPrefsFailureNoWrite(t *testing.T) {
	// 上游 400 → upstream_error
	ext := stubExtended(t, 400, "boom", 0)
	defer ext.Close()
	cfg := testConfig(t, ext.URL)
	cfg.presetURL = defaultPresetURL
	s := mustServer(t, cfg)
	enableSnap(t, s)
	resp, _ := postPreview(t, s,
		`{"url":"https://sub.example.com/l","target":"surge","groups":[{"name":"专线","keywords":["专线"],"urlTest":false}]}`,
		"h:1", "")
	if resp.OK || resp.ErrorCode == nil || *resp.ErrorCode != "upstream_error" {
		t.Fatalf("应 upstream_error：%+v", resp)
	}
	assertPrefsEmpty(t, s)

	// 能力位关（未 enableSnap = 出树态）+ 非空 groups → groups_disabled 预校验错，不写档
	s2 := mustServer(t, testConfig(t, ext.URL))
	resp2, _ := postPreview(t, s2,
		`{"url":"https://sub.example.com/l","target":"surge","groups":[{"name":"专线","keywords":["专线"],"urlTest":false}]}`,
		"h:1", "")
	if resp2.OK || resp2.ErrorCode == nil || *resp2.ErrorCode != "groups_disabled" {
		t.Fatalf("应 groups_disabled：%+v", resp2)
	}
	assertPrefsEmpty(t, s2)
}

func assertPrefsEmpty(t *testing.T, s *server) {
	t.Helper()
	for _, table := range []string{"prefs_custom", "prefs_exclude"} {
		var n int
		if err := s.store.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil || n != 0 {
			t.Errorf("%s 行数 = %d (err %v)，应 0（失败路径绝不写档）", table, n, err)
		}
	}
}
