// 分组偏好记忆接口（v1.6.0）：按订阅链接存取「已提交偏好档案」——POST /api/preferences/
// query 查回（custom 跨档共享 + exclude 仅当档）、DELETE /api/preferences 清该订阅全部。
// 订阅链接走 body 传（不进访问日志，隐私口径同 preview）；不加认证（持链接者本可直接拉
// 全部节点，不算新增暴露）。日志卫生（D6）：绝不记订阅 URL，只记脱敏摘要。
package main

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
)

// prefsErrorResponse 错误响应沿用 /api/preview 的错误族与字段形态（ok/errorCode/
// message，errorCode 复用 bad_url_* / bad_preset 与 validateMessages 文案）；本组接口
// 错误以 HTTP 400 呈现（非 preview 的恒 200——偏好接口没有「link 照常返回」的兜底语义）
type prefsErrorResponse struct {
	OK        bool    `json:"ok"`
	ErrorCode *string `json:"errorCode"`
	Message   string  `json:"message"`
}

func writePrefsError(w http.ResponseWriter, code string) {
	c := code
	writeJSON(w, http.StatusBadRequest, prefsErrorResponse{OK: false, ErrorCode: &c, Message: validateMessages[code]})
}

// preferencesQueryRequest：preset_id 缺省/空串/"default" 均 resolve 到 default 档（口径同 preview）
type preferencesQueryRequest struct {
	SubURL   string `json:"sub_url"`
	PresetID string `json:"preset_id"`
}

// preferencesQueryResponse：null = 无存档（两字段独立）；custom_groups 为 links.groups
// 同构的规范化 NodeGroup JSON 数组，exclude 为去重升序 string JSON 数组（原样透出库存
// 规范化形态，不重排序）
type preferencesQueryResponse struct {
	CustomGroups json.RawMessage `json:"custom_groups"`
	Exclude      json.RawMessage `json:"exclude"`
}

// handlePreferencesQuery POST /api/preferences/query。预校验判定序沿用 preview：
// bad_url_* 先于 bad_preset。出树 DATA_DIR（分组禁用）不影响本接口——纯存取无引擎副作用。
func (s *server) handlePreferencesQuery(w http.ResponseWriter, r *http.Request) {
	var req preferencesQueryRequest
	// body 解析失败按「未提供链接」处理（口径同 preview，不发明新错误码）
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req)
	primURL := primarySubURL(req.SubURL)
	if code := validateURL(primURL); code != "" {
		writePrefsError(w, code)
		return
	}
	sel, ok := resolvePreset(s.presets, req.PresetID)
	if !ok {
		log.Printf("preferences query 预校验: code=bad_preset")
		writePrefsError(w, "bad_preset")
		return
	}
	subKey := normalizeSubURL(primURL)
	groupsJSON, hasCustom, err := s.store.getPrefsCustom(subKey)
	if err != nil {
		log.Printf("preferences query 读 prefs_custom 失败: %T", err)
		writeJSON(w, http.StatusInternalServerError, struct {
			Error string `json:"error"`
		}{"internal"})
		return
	}
	excludeJSON, hasExclude, err := s.store.getPrefsExclude(subKey, sel.ID)
	if err != nil {
		log.Printf("preferences query 读 prefs_exclude 失败: %T", err)
		writeJSON(w, http.StatusInternalServerError, struct {
			Error string `json:"error"`
		}{"internal"})
		return
	}
	resp := preferencesQueryResponse{} // 两字段零值 nil → JSON null（无存档语义）
	if hasCustom {
		resp.CustomGroups = json.RawMessage(groupsJSON)
	}
	if hasExclude {
		resp.Exclude = json.RawMessage(excludeJSON)
	}
	writeJSON(w, http.StatusOK, resp)
}

// handlePreferencesDelete DELETE /api/preferences：清该订阅的全部档案（custom 一份 +
// exclude 全档）；幂等（无档案回 false/0）。target/preset 不参与——清就是全清。
func (s *server) handlePreferencesDelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SubURL string `json:"sub_url"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req)
	primURL := primarySubURL(req.SubURL)
	if code := validateURL(primURL); code != "" {
		writePrefsError(w, code)
		return
	}
	delCustom, delExclude, err := s.store.deletePrefs(normalizeSubURL(primURL))
	if err != nil {
		log.Printf("preferences delete 失败: %T", err)
		writeJSON(w, http.StatusInternalServerError, struct {
			Error string `json:"error"`
		}{"internal"})
		return
	}
	writeJSON(w, http.StatusOK, struct {
		DeletedCustom  bool `json:"deleted_custom"`
		DeletedExclude int  `json:"deleted_exclude"`
	}{delCustom, delExclude})
}
