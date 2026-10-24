# /api 契约（v1，冻结版）

前端与 sidecar 的唯一接口。本文档是两端的 truth source：字段名、错误码、文案以此为准，改动需经主会话（双向同步改）。

> 增量修订（2026-10-24，规则模板切换）：`presets` 内置三档；`POST /api/preview` 增可选 `preset` 请求字段与响应 `preset` 回显；新增错误码 `bad_preset`；`PRESET_URL` 语义收窄为仅覆盖 default 档。其余形状不变。

Sidecar 由 nginx 反代挂载在 `/api/*`；监听 `SIDECAR_PORT`（默认 8081，仅容器内回环）。注意：**不能用 `PORT`**——该 env 已被 Extended（subconverter）占用。

## GET /api/clients

客户端注册表 + 预设模板。HTTP 200：

```json
{
  "clients": [
    { "id": "auto", "label": "自动识别", "target": "auto", "verified": false },
    { "id": "clash", "label": "Clash / Mihomo", "target": "clash", "verified": false },
    { "id": "surge", "label": "Surge", "target": "surge", "verified": false },
    { "id": "quanx", "label": "Quantumult X", "target": "quanx", "verified": false },
    { "id": "loon", "label": "Loon", "target": "loon", "verified": false },
    { "id": "stash", "label": "Stash", "target": "stash", "verified": false },
    { "id": "shadowrocket", "label": "Shadowrocket", "target": "shadowrocket", "verified": false },
    { "id": "singbox", "label": "sing-box", "target": "singbox", "verified": false },
    { "id": "v2rayn", "label": "v2rayN", "target": "v2rayn", "verified": false }
  ],
  "presets": [
    { "id": "default", "name": "ACL4SSR 默认", "url": "https://raw.githubusercontent.com/ACL4SSR/ACL4SSR/master/Clash/config/ACL4SSR_Online.ini" },
    { "id": "full_multimode", "name": "地区分组 + AI 分流", "url": "https://raw.githubusercontent.com/ACL4SSR/ACL4SSR/master/Clash/config/ACL4SSR_Online_Full_MultiMode.ini" },
    { "id": "full_adblockplus", "name": "全量 + 去广告", "url": "https://raw.githubusercontent.com/ACL4SSR/ACL4SSR/master/Clash/config/ACL4SSR_Online_Full_AdblockPlus.ini" }
  ]
}
```

- chips 顺序即返回顺序（`auto` 永远第一，前端默认选中）
- `verified`：真机实测通过后置 true（V1 发布时全部 false，兼容矩阵随实测更新）
- presets 顺序即返回顺序（`default` 永远第一，前端默认选中）；三个 url 均为 2026-10-24 实测 200。`full_multimode` 含按地区分组（港日美台新韩）+ 💬 Ai平台 组，Ai平台候选含全部地区组——ChatGPT / Claude / Gemini 等可定向走指定地区（在客户端的 Ai平台组里选地区即可，无需重新生成订阅）
- env `PRESET_URL` 仅覆盖 `default` 档的 url（冒烟测试用），覆盖时 `id/name` 不变，其余两档不受影响

## POST /api/preview

请求体：

```json
{ "url": "https://…订阅链接…", "target": "auto", "preset": "full_multimode" }
```

`target` 缺省、空串或 `"auto"` = 自动；否则必须是注册表中的 target 值。

`preset` 可选，缺省、空串或 `"default"` = 默认档；否则必须是 `/api/clients` presets 中的 id。非注册表 id → `bad_preset`（**不做静默回落**——防「以为选了地区分组、实际拿到默认模板」的链接流出）。选中的档位烙进探测请求与生成 link 的 `config=` 参数。

响应恒为 HTTP 200（sidecar 处理的错误都体现在字段里）：

```json
{
  "ok": true,
  "target": "clash",
  "preset": { "id": "full_multimode", "name": "地区分组 + AI 分流" },
  "errorCode": null,
  "message": "",
  "link": "http://192.168.1.10:8080/sub?target=auto&url=…&config=…"
}
```

- **`link` 永远非空**（D4 定案）：`ok:false` 时照常返回，前端仍展示并标注「未通过校验，仍可试用」
- `preset`：本次生效的档位（id + name）。`bad_preset` 时为 `null`；其余情况（含 `ok:false`）恒回显解析后的合法档位，前端结果页据此标注「模板：{name}」
- `ok:false` 时 `target` 为探测用的 target（auto → "clash"），`errorCode` 与 `message` 必填
- `link` 由 sidecar 服务端拼接，前端一律直接使用该字段，不自行拼接

### sidecar 行为

1. **预校验（D7 + 规则模板）**，不过返回对应 errorCode，不做上游调用：
   - 空串 / 纯空白 → `bad_url_empty`
   - 非 URL 或 scheme ∉ {http, https} → `bad_url_format`
   - 主机名为私网/回环字面量（localhost、127.0.0.0/8、10.0.0.0/8、172.16.0.0/12、192.168.0.0/16、169.254.0.0/16、0.0.0.0、[::1]）→ `bad_url_private`
   - `preset` 非注册表 id → `bad_preset`（此时不调上游；`link` 仍以 default 档生成以维持「link 永远非空」，`preset` 回显 `null`）
2. **探测转换**：GET `${EXTENDED_BASE}/sub?target=<探测target>&url=<enc>&config=<enc 所选档位url>`，超时 **10s**：
   - 探测 target：用户显式指定 → 该 target；auto → `clash`（设计定案的启发式）
   - UA：按 target 家族给代表性 UA（clash→`clash-verge/v1.7.7`，surge→`Surge Mac/2398`，其余给通用 UA；探测 target 显式时 UA 影响很小）
   - 200 且格式粗验通过（clash 系：body 含 `proxies:`；surge 系：body 含 `[General]` 或以 `#!MANAGED-CONFIG` 开头；singbox：可 JSON 解析且含 `outbounds`；其余 target：body 非空且不含 `Invalid request`）→ `ok:true`
   - 200 但粗验不过 → `ok:false`，`errorCode:"upstream_error"`
   - 非 200 → `ok:false`，`errorCode:"upstream_error"`，message 由 Extended 响应体映射（见错误码表）
   - 连接失败 → `backend_down`；超时 → `upstream_timeout`
3. **link 拼接**（与探测结果无关，永远执行）：
   ```
   ${origin}/sub?target=${请求target原样：auto或指定值}&url=${encodeURIComponent(用户url)}&config=${encodeURIComponent(所选档位url)}
   ```
   `SUB_TOKEN` env 非空时追加 `&token=${encodeURIComponent(SUB_TOKEN)}`
4. **origin 推导（R3-1 定案，优先级从高到低）**：
   - env `PUBLIC_BASE_URL`（去尾部 `/`）
   - `${X-Forwarded-Proto 或 "http"}://${Host 头}`（nginx 必须显式 `proxy_set_header Host $host` + `X-Forwarded-Proto $scheme`，否则 sidecar 看到的是内部地址）

### 错误码 → 前端文案（错误映射表，sidecar 返回 code，前端负责最终展示文案；同表也供 sidecar 单测断言 code 语义）

| errorCode | 场景 | 展示文案（前端硬编码，中文） |
|---|---|---|
| `bad_url_empty` | 没贴链接 | 先粘贴你的订阅链接，再点「转换」 |
| `bad_url_format` | 不是 http(s) 链接 | 这不像一个订阅链接——检查一下是不是复制全了？ |
| `bad_url_private` | 私网/回环地址 | 这个链接指向内网地址，转换服务够不着它 |
| `bad_preset` | preset 非注册表 id（仅手改请求/前后端错配会出现，UI 选不出非法值） | 这个模板不可用，重新选一个试试 |
| `upstream_error` | Extended 返回错误/格式不对 | 转换没成功——可以试试展开「指定客户端」换一个，或稍后再来 |
| `upstream_timeout` | sidecar→Extended 10s 超时 | 等太久没等到结果，再试一次看看？ |
| `backend_down` | Extended 连不上 | 转换引擎暂时没响应，稍后再试 |
| `api_unreachable` | **前端侧专用**：fetch /api/preview 失败/超时（15s AbortController）/非 200 | 服务暂时够不着，稍后再试 |

Extended 错误体识别要点（映射 `upstream_error` 细节，Assignment ⑤ 实测）：未知 UA + `target=auto` → 400 + body「Invalid request: unsupported target value. 无效请求：不支持的 target 参数值。」——该文案出现时前端提示优先给「展开指定客户端」指引。

## GET /api/health

```json
{
  "backendVersion": "v1.10.5",
  "presets": [
    {
      "name": "ACL4SSR 默认",
      "url": "https://raw.githubusercontent.com/…/ACL4SSR_Online.ini",
      "lastModifiedAt": null,
      "lastSuccessAt": "2026-10-23T15:00:00Z",
      "status": "unknown"
    }
  ]
}
```

- `backendVersion`：解析 `${EXTENDED_BASE}/version` 的 **HTML**（正则 `info-value[^>]*>\s*(v[\d.]+)`，Assignment ② 实测返回页面非纯文本）；失败 → `"unknown"`
- 数据由 sidecar **周期自采**：每 15 min（env `POLL_INTERVAL` 可调）对每个预设 url 发 HEAD，读 `etag` + `last-modified`（Assignment ③：GitHub raw 有 etag 无 last-modified）
- `lastModifiedAt`：远端 Last-Modified 头，缺头为 `null`；ETag 仅内部做变更检测，不进契约
- **status 三态语义（Success Criteria 定案，按序判定）**：
  1. 从未成功抓取 → `unknown`
  2. 最近一次抓取失败 → `stale`
  3. `lastModifiedAt == null` → `unknown`（头缺失不冒充 fresh）
  4. `lastModifiedAt` 距今 < 24h → `fresh`
  5. ≥ 24h → `stale`
- 前端文案：fresh → 「规则内容 {X} 前更新」；stale → 「规则库可能过期（{X} 未更新）」或最近抓取失败时「规则源暂时够不着」；unknown → 「规则库状态未知」；`backendVersion == "unknown"` → 后端版本显示 unknown

## 环境变量（sidecar）

| env | 默认 | 用途 |
|---|---|---|
| `SIDECAR_PORT` | `8081` | 监听端口（勿用 `PORT`，被 Extended 占用） |
| `EXTENDED_BASE` | `http://127.0.0.1:25500` | Extended 内部地址 |
| `PUBLIC_BASE_URL` | 无 | link origin 覆盖 + 镜像层转 `MANAGED_CONFIG_PREFIX`（见 assignment-findings ⑤） |
| `SUB_TOKEN` | 空 | 非空时注入生成链接 |
| `PRESET_URL` | ACL4SSR_Online.ini | 仅覆盖 `default` 档 url（冒烟用；其余档不受影响） |
| `POLL_INTERVAL` | `15m` | 健康轮询周期 |

## nginx 路由（镜像层，与契约相关部分）

- `/` → 前端静态文件；`/api/` → sidecar；`/sub`、`/version`、`/healthz` → Extended(25500)
- 对 sidecar 的反代：`proxy_set_header Host $http_host; proxy_set_header X-Forwarded-Proto $scheme;`（**注意用 `$http_host` 而非 `$host`**：nginx 的 `$host` 永远去掉端口，非 80 端口部署时 sidecar 推导的 link 会丢端口不可用——S2 三步冒烟实测抓出后修正）
- `/sub`、`/api/` 关闭 access_log（D6 日志卫生；nginx 默认 access_log 含 query string）

### 契约未定义场景的既定处理（S2 实现既成语义）

- `target` 非注册表值：探测按 clash 进行，link 原样透传该 target，由 Extended 自然拒绝（不发明新错误码）
- POST body 非法 JSON / 缺 url 字段：按 `bad_url_empty` 处理（响应恒 200 原则不变）
