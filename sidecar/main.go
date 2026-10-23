// ssc-sidecar：Smart SubConverter 镜像内 API 服务（仅监听容器回环，由 nginx 反代 /api/）。
// 本文件是 V1 脚手架：/api/clients 完整实现；/api/health、/api/preview 为形状符合契约的桩，
// 真实实现（Wave 2）以 docs/implementation/api-contract.md 为准。
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
)

// 预设远端配置 URL（契约默认值，可被 env PRESET_URL 覆盖，冒烟测试用）
const defaultPresetURL = "https://raw.githubusercontent.com/ACL4SSR/ACL4SSR/master/Clash/config/ACL4SSR_Online.ini"

type clientEntry struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Target   string `json:"target"`
	Verified bool   `json:"verified"`
}

type presetEntry struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	URL  string `json:"url"`
}

type clientsResponse struct {
	Clients []clientEntry `json:"clients"`
	Presets []presetEntry `json:"presets"`
}

type presetStatus struct {
	Name           string  `json:"name"`
	URL            string  `json:"url"`
	LastModifiedAt *string `json:"lastModifiedAt"`
	LastSuccessAt  *string `json:"lastSuccessAt"`
	Status         string  `json:"status"`
}

type healthResponse struct {
	BackendVersion string         `json:"backendVersion"`
	Presets        []presetStatus `json:"presets"`
}

type previewResponse struct {
	OK        bool   `json:"ok"`
	Target    string `json:"target"`
	ErrorCode string `json:"errorCode"`
	Message   string `json:"message"`
	Link      string `json:"link"`
}

// 客户端注册表（契约冻结：顺序即 chips 顺序，auto 永远第一；verified 随真机实测置 true）
var registry = []clientEntry{
	{"auto", "自动识别", "auto", false},
	{"clash", "Clash / Mihomo", "clash", false},
	{"surge", "Surge", "surge", false},
	{"quanx", "Quantumult X", "quanx", false},
	{"loon", "Loon", "loon", false},
	{"stash", "Stash", "stash", false},
	{"shadowrocket", "Shadowrocket", "shadowrocket", false},
	{"singbox", "sing-box", "singbox", false},
	{"v2rayn", "v2rayN", "v2rayn", false},
}

func presetURL() string {
	if u := os.Getenv("PRESET_URL"); u != "" {
		return u
	}
	return defaultPresetURL
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("write json: %v", err)
	}
}

func handleClients(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, clientsResponse{
		Clients: registry,
		Presets: []presetEntry{{ID: "default", Name: "ACL4SSR Online", URL: presetURL()}},
	})
}

// 桩：真实实现（Wave 2）= 解析 ${EXTENDED_BASE}/version 的 HTML 得 backendVersion
// + 按 POLL_INTERVAL 周期对每个预设 URL 发 HEAD 自采 lastModifiedAt/lastSuccessAt/status 三态
func handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{
		BackendVersion: "unknown",
		Presets: []presetStatus{{
			Name:           "ACL4SSR Online",
			URL:            presetURL(),
			LastModifiedAt: nil,
			LastSuccessAt:  nil,
			Status:         "unknown",
		}},
	})
}

// 桩：真实实现（Wave 2）= URL 预校验（D7）+ 探测转换（10s 超时）+ link 服务端拼接，
// 形状必须符合契约「POST /api/preview」节（响应恒 200，link 永远非空）
func handlePreview(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, previewResponse{
		OK:        false,
		Target:    "clash",
		ErrorCode: "backend_down",
		Message:   "stub",
		Link:      "http://stub/sub?target=auto",
	})
}

func main() {
	// 不能用 PORT：该 env 已被 Extended 占用（assignment-findings）
	port := os.Getenv("SIDECAR_PORT")
	if port == "" {
		port = "8081"
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/clients", handleClients)
	mux.HandleFunc("GET /api/health", handleHealth)
	mux.HandleFunc("POST /api/preview", handlePreview)
	addr := "127.0.0.1:" + port
	log.Printf("ssc-sidecar listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}
