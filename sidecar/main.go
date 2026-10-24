// ssc-sidecar：Smart SubConverter 镜像内 API 服务（仅监听容器回环，由 nginx 反代 /api/）。
// 契约 truth source：docs/implementation/api-contract.md（字段名、错误码、判定序以此为准）。
package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"
)

// default 档远端配置 URL（env PRESET_URL 仅覆盖 default 档 url，冒烟测试用）
const defaultPresetURL = "https://raw.githubusercontent.com/ACL4SSR/ACL4SSR/master/Clash/config/ACL4SSR_Online.ini"

// 探测转换的 sidecar→Extended 超时（契约钉死 10s；测试注入短值）
const defaultProbeTimeout = 10 * time.Second

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

type presetRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type previewResponse struct {
	OK        bool       `json:"ok"`
	Target    string     `json:"target"`
	Preset    *presetRef `json:"preset"`
	ErrorCode *string    `json:"errorCode"`
	Message   string     `json:"message"`
	Link      string     `json:"link"`
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

func isRegisteredTarget(t string) bool {
	for _, c := range registry {
		if c.Target == t {
			return true
		}
	}
	return false
}

// 内置三档规则模板（契约 GET /api/clients presets 冻结值：顺序即返回顺序，default 永远第一）
var presetTable = []presetEntry{
	{"default", "ACL4SSR 默认", defaultPresetURL},
	{"full_multimode", "地区分组 + AI 分流", "https://raw.githubusercontent.com/ACL4SSR/ACL4SSR/master/Clash/config/ACL4SSR_Online_Full_MultiMode.ini"},
	{"full_adblockplus", "全量 + 去广告", "https://raw.githubusercontent.com/ACL4SSR/ACL4SSR/master/Clash/config/ACL4SSR_Online_Full_AdblockPlus.ini"},
}

// resolvePresets 拷贝内置三档；PRESET_URL 仅覆盖 default 档 url（id/name 与其余两档不受影响）
func resolvePresets(presetURL string) []presetEntry {
	ps := make([]presetEntry, len(presetTable))
	copy(ps, presetTable)
	if presetURL != "" && presetURL != ps[0].URL {
		ps[0].URL = presetURL
	}
	return ps
}

// config 汇总 sidecar 全部环境变量（main 启动时读定；变量表见契约「环境变量」节）
type config struct {
	extendedBase  string
	publicBaseURL string
	subToken      string
	presetURL     string
	probeTimeout  time.Duration
	pollInterval  time.Duration
}

func loadConfigFromEnv() config {
	getOr := func(name, def string) string {
		if v := os.Getenv(name); v != "" {
			return v
		}
		return def
	}
	c := config{
		extendedBase:  getOr("EXTENDED_BASE", "http://127.0.0.1:25500"),
		publicBaseURL: os.Getenv("PUBLIC_BASE_URL"),
		subToken:      os.Getenv("SUB_TOKEN"),
		presetURL:     getOr("PRESET_URL", defaultPresetURL),
		probeTimeout:  defaultProbeTimeout,
		pollInterval:  15 * time.Minute,
	}
	if raw := os.Getenv("POLL_INTERVAL"); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			c.pollInterval = d
		} else {
			log.Printf("POLL_INTERVAL %q 解析失败，回退默认 15m", raw)
		}
	}
	return c
}

type server struct {
	cfg         config
	presets     []presetEntry
	mon         *monitor
	probeClient *http.Client
}

func newServer(cfg config) *server {
	return &server{
		cfg:         cfg,
		presets:     resolvePresets(cfg.presetURL),
		mon:         newMonitor(cfg),
		probeClient: &http.Client{Timeout: cfg.probeTimeout},
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("write json: %v", err)
	}
}

func (s *server) handleClients(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, clientsResponse{
		Clients: registry,
		Presets: s.presets,
	})
}

func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.mon.snapshot())
}

func (s *server) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/clients", s.handleClients)
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("POST /api/preview", s.handlePreview)
	return mux
}

func main() {
	// 不能用 PORT：该 env 已被 Extended 占用（assignment-findings）
	port := os.Getenv("SIDECAR_PORT")
	if port == "" {
		port = "8081"
	}
	s := newServer(loadConfigFromEnv())
	go s.mon.pollLoop(context.Background(), s.cfg.pollInterval)
	addr := "127.0.0.1:" + port
	log.Printf("ssc-sidecar listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, s.routes()))
}
