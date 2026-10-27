// ssc-sidecar：Smart SubConverter 镜像内 API 服务（仅监听容器回环，由 nginx 反代 /api/）。
// 契约 truth source：docs/implementation/api-contract.md（字段名、错误码、判定序以此为准）。
package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"os"
	"time"
)

// default 档远端配置 URL（env PRESET_URL 仅覆盖 default 档 url，冒烟测试用）
const defaultPresetURL = "https://raw.githubusercontent.com/ACL4SSR/ACL4SSR/master/Clash/config/ACL4SSR_Online.ini"

// 探测转换的 sidecar→Extended 超时（v1.3.0 前硬编码 10s；env PROBE_TIMEOUT 可调，慢网络下预检不再 10 秒判死）
const defaultProbeTimeout = 30 * time.Second

// /s/ 订阅代理的 sidecar→Extended 超时（v1.2.0 默认 30s，国内慢网络实测不够；
// env SUB_PROXY_TIMEOUT 可调。快照本地化后常规请求应快，此值兜订阅源慢的情况）
const defaultSubProxyTimeout = 120 * time.Second

// 快照刷新周期默认值（health 本地化档 fresh 阈值 = 2× 此值）
const defaultPresetRefreshInterval = 24 * time.Hour

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

// capabilities 部署级能力位（v1.5.0：customGroups=派生能力，出树 DATA_DIR → false）
type capabilitiesInfo struct {
	CustomGroups bool `json:"customGroups"`
}

// presetWithCaps 档位级能力位（v1.5.0）：快照本地化档 true、豁免档 false（派生需读本地 ini）
type presetWithCaps struct {
	presetEntry
	CustomGroups bool `json:"customGroups"`
}

type clientsResponse struct {
	Clients      []clientEntry    `json:"clients"`
	Presets      []presetWithCaps `json:"presets"`
	Capabilities capabilitiesInfo `json:"capabilities"`
}

type presetStatus struct {
	Name            string  `json:"name"`
	URL             string  `json:"url"`
	Source          string  `json:"source"` // snapshot=快照本地化 / remote=远端 HEAD 自采（v1.3.0 起恒输出；缺席 = 旧版 sidecar，前端回退 remote）
	LastModifiedAt  *string `json:"lastModifiedAt"`
	LastRefreshedAt *string `json:"lastRefreshedAt"` // 快照获得时间（本地化档）；豁免档无此概念恒 null
	LastSuccessAt   *string `json:"lastSuccessAt"`
	Status          string  `json:"status"`
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
	DeepLink  *string    `json:"deepLink"`
	Nodes     []string   `json:"nodes"` // v1.5.0：非 probe 恒 []（节点名唯一出口是 probe）
}

// probePreviewResponse probe 模式响应（v1.5.0）：与 previewResponse 同形，唯 link /
// deepLink 恒 null（「link 永远非空」条款本模式豁免——探测建链无副作用即目的），
// nodes 为节点名列表（ok:false 时恒 []）
type probePreviewResponse struct {
	OK        bool       `json:"ok"`
	Target    string     `json:"target"`
	Preset    *presetRef `json:"preset"`
	ErrorCode *string    `json:"errorCode"`
	Message   string     `json:"message"`
	Link      *string    `json:"link"`
	DeepLink  *string    `json:"deepLink"`
	Nodes     []string   `json:"nodes"`
}

// 客户端注册表（契约冻结：顺序即 chips 顺序，auto 永远第一；verified 随真机实测置 true）
var registry = []clientEntry{
	{"auto", "自动识别", "auto", false},
	{"clash", "Clash / Mihomo", "clash", true},  // 2026-10-25 Clash Verge（家庭网络）+ 2026-10-26 ShellCrash（VPS，机场 CF 按 ASN 封禁直连，两段式内联 170 节点）
	{"surge", "Surge", "surge", false},
	{"quanx", "Quantumult X", "quanx", true},    // 2026-10-25 / 10-26 两案真机终验（两段式 [server_local]）
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
	extendedBase          string
	publicBaseURL         string
	subToken              string
	presetURL             string
	probeTimeout          time.Duration
	pollInterval          time.Duration
	dataDir               string // state.db 与运行时快照 rules/ 所在目录（volume 挂载点；单测指向 tmp）
	subProxyTimeout       time.Duration
	presetRefreshInterval time.Duration // 快照刷新周期（启动先刷一轮）
	snapshotProxy         *url.URL      // 规则/模板上游访问代理（SNAPSHOT_PROXY，v1.3.1）
	subscriptionProxy     *url.URL      // 订阅代拉专用代理（SUBSCRIPTION_PROXY，v1.3.8）；与 snapshotProxy 独立
	sidecarBase           string        // sidecar 自身回环基址（引擎拉内部订阅端点用；main 按 SIDECAR_PORT 填充）
}

func loadConfigFromEnv() config {
	getOr := func(name, def string) string {
		if v := os.Getenv(name); v != "" {
			return v
		}
		return def
	}
	c := config{
		extendedBase:          getOr("EXTENDED_BASE", "http://127.0.0.1:25500"),
		publicBaseURL:         os.Getenv("PUBLIC_BASE_URL"),
		subToken:              os.Getenv("SUB_TOKEN"),
		presetURL:             getOr("PRESET_URL", defaultPresetURL),
		probeTimeout:          defaultProbeTimeout,
		pollInterval:          15 * time.Minute,
		dataDir:               getOr("DATA_DIR", "/base/data"),
		subProxyTimeout:       defaultSubProxyTimeout,
		presetRefreshInterval: defaultPresetRefreshInterval,
	}
	parseDur := func(name, def string, dst *time.Duration) {
		if raw := os.Getenv(name); raw != "" {
			if d, err := time.ParseDuration(raw); err == nil && d > 0 {
				*dst = d
			} else {
				log.Printf("%s %q 解析失败，回退默认 %s", name, raw, def)
			}
		}
	}
	parseDur("POLL_INTERVAL", "15m", &c.pollInterval)
	parseDur("SUB_PROXY_TIMEOUT", "120s", &c.subProxyTimeout)
	parseDur("PROBE_TIMEOUT", "30s", &c.probeTimeout)
	parseDur("PRESET_REFRESH_INTERVAL", "24h", &c.presetRefreshInterval)
	if raw := os.Getenv("SNAPSHOT_PROXY"); raw != "" {
		if u, err := url.Parse(raw); err == nil && (u.Scheme == "http" || u.Scheme == "https" || u.Scheme == "socks5") && u.Host != "" {
			c.snapshotProxy = u
		} else {
			log.Printf("SNAPSHOT_PROXY %q 非法（需 http:// 或 socks5:// 且带 host），回退直连", raw)
		}
	}
	if raw := os.Getenv("SUBSCRIPTION_PROXY"); raw != "" {
		if u, err := url.Parse(raw); err == nil && (u.Scheme == "http" || u.Scheme == "https" || u.Scheme == "socks5") && u.Host != "" {
			c.subscriptionProxy = u
		} else {
			log.Printf("SUBSCRIPTION_PROXY %q 非法（需 http:// 或 socks5:// 且带 host），回退直连", raw)
		}
	}
	if c.publicBaseURL == "" {
		log.Printf("警告：PUBLIC_BASE_URL 未设置——输出的规则集引用（getruleset/MANAGED-CONFIG）将回指 127.0.0.1，外部客户端拉不到规则，严格校验的客户端（Quantumult X 等）导入会失败")
	}
	return c
}

// upstreamClient 规则/模板上游访问专用 client（快照刷新 + 豁免档远端 HEAD）：
// SNAPSHOT_PROXY 非空时全走代理；订阅代拉不共用（走 SUBSCRIPTION_PROXY，见 newInternalSubStore）
func upstreamClient(cfg config, timeout time.Duration) *http.Client {
	if cfg.snapshotProxy == nil {
		return &http.Client{Timeout: timeout}
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: &http.Transport{Proxy: http.ProxyURL(cfg.snapshotProxy)},
	}
}

type server struct {
	cfg         config
	presets     []presetEntry
	mon         *monitor
	probeClient *http.Client
	proxyClient *http.Client       // /s/ 订阅代理（超时 SUB_PROXY_TIMEOUT）
	store       *store             // 短链存储（v1.2.0；openStore 失败则启动失败）
	snap        *snapshotManager   // 规则快照本地化（v1.3.0；nil = 禁用，三档按豁免档语义）
	isub        *internalSubStore  // 内部订阅端点 + 代拉（v1.3.5；绕机场 WAF 的 TLS 指纹拦截）
}

// presetExempt：PRESET_URL 覆盖即豁免本地化（仅 default 档可被覆盖，契约 v1.3.0 语义：
// sidecar 原样透传部署者 URL，自担网络）
func (s *server) presetExempt(p presetEntry) bool {
	return p.ID == "default" && s.cfg.presetURL != defaultPresetURL
}

// engineConfig 发给引擎的 config= 值：本地化档用快照改写后 .ini 本地绝对路径
// （产物可用时——引擎读 /base 树内本地文件，契约「引擎指向」节）；否则透传该档
// 原始 URL（豁免档透传部署者 URL；快照未就绪的窗口期短暂透传 GitHub URL，
// 刷新成功后自然切换）
func (s *server) engineConfig(p presetEntry) string {
	if s.snap != nil && !s.presetExempt(p) {
		if path, ok := s.snap.rewrittenPath(p); ok {
			return path
		}
	}
	return p.URL
}

// customGroupsEnabled 部署级分组能力位（v1.5.0 R1/D2=A）：复用快照本地化启用判定
// （DATA_DIR 在 /base 树内 → true；出树 → false），与既有出树降级语义同一判定
func (s *server) customGroupsEnabled() bool { return s.snap != nil }

// presetCustomGroups 档位级分组能力位（v1.5.0）：快照本地化档 true、豁免档 false
// （派生需要读本地 ini，豁免档没有）
func (s *server) presetCustomGroups(p presetEntry) bool {
	return s.snap != nil && !s.presetExempt(p)
}

// newServer 打开短链存储（库损坏等失败 → 返回错误，main 拒绝启动）；DATA_DIR 在
// /base 树内时构造快照管理器（出树 → 禁用 + 警告，不拒启动）
func newServer(cfg config) (*server, error) {
	st, err := openStore(cfg.dataDir)
	if err != nil {
		return nil, err
	}
	s := &server{
		cfg:         cfg,
		presets:     resolvePresets(cfg.presetURL),
		mon:         newMonitor(cfg),
		probeClient: &http.Client{Timeout: cfg.probeTimeout},
		proxyClient: &http.Client{Timeout: cfg.subProxyTimeout},
		store:       st,
		isub:        newInternalSubStore(cfg),
	}
	if snapshotEnabled(cfg.dataDir) {
		s.snap = newSnapshotManager(cfg, s.presetExempt)
	} else {
		log.Printf("DATA_DIR %s 不在 %s 路径树内（引擎本地读取硬限），快照本地化整体禁用（自定义分组一并禁用，customGroups=false）——三档按豁免档语义工作（source=remote）", cfg.dataDir, engineBaseDir)
	}
	s.mon.snap = s.snap
	s.mon.exempt = s.presetExempt
	return s, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("write json: %v", err)
	}
}

func (s *server) handleClients(w http.ResponseWriter, r *http.Request) {
	ps := make([]presetWithCaps, len(s.presets))
	for i, p := range s.presets {
		ps[i] = presetWithCaps{presetEntry: p, CustomGroups: s.presetCustomGroups(p)}
	}
	writeJSON(w, http.StatusOK, clientsResponse{
		Clients:      registry,
		Presets:      ps,
		Capabilities: capabilitiesInfo{CustomGroups: s.customGroupsEnabled()},
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
	mux.HandleFunc("GET /s/{code}", s.handleShortLink)
	mux.HandleFunc("GET /api/links", s.handleLinks)
	mux.HandleFunc("DELETE /api/links/{code}", s.handleDeleteLink)
	mux.HandleFunc("GET /internal/sub/", s.isub.handleInternalSub)    // 引擎专用（回环 listener，nginx 不路由）
	mux.HandleFunc("GET /internal/derived/", s.handleInternalDerived) // 同上；serve 派生 ini（无 TTL，短链存活期内可重复拉取）
	return mux
}

func main() {
	// 不能用 PORT：该 env 已被 Extended 占用（assignment-findings）
	port := os.Getenv("SIDECAR_PORT")
	if port == "" {
		port = "8081"
	}
	cfg := loadConfigFromEnv()
	if cfg.sidecarBase == "" {
		cfg.sidecarBase = "http://127.0.0.1:" + port // 引擎拉内部订阅端点的回环基址
	}
	s, err := newServer(cfg)
	if err != nil {
		// 库损坏等运维态：拒绝启动，绝不自动清空（契约「存储」节）
		log.Fatalf("sidecar 启动失败: %v", err)
	}
	go s.mon.pollLoop(context.Background(), s.cfg.pollInterval)
	if s.snap != nil {
		go s.snap.refreshLoop(context.Background()) // 启动先刷一轮（bootstrap 后；含 .ini 改写）
	}
	addr := "127.0.0.1:" + port
	log.Printf("ssc-sidecar listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, s.routes()))
}
