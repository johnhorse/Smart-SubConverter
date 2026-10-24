# /api 契约（v1，冻结版）

前端与 sidecar 的唯一接口。本文档是两端的 truth source：字段名、错误码、文案以此为准，改动需经主会话（双向同步改）。

> 增量修订（2026-10-24，规则模板切换）：`presets` 内置三档；`POST /api/preview` 增可选 `preset` 请求字段与响应 `preset` 回显；新增错误码 `bad_preset`；`PRESET_URL` 语义收窄为仅覆盖 default 档。其余形状不变。

> 增量修订（2026-10-24，导入深链）：`POST /api/preview` 响应新增 `deepLink`（string | null，sidecar 拼装的一键导入深链，拼装规则见该节）；`GET /api/clients` 形状不变。其余形状不变。

> 增量修订（2026-10-24，v1.2.0 短链）：`POST /api/preview` 响应 `link` 从 `/sub` 长链接改为 `/s/{code}` 短链（deepLink 与二维码自动随嵌）；新增 `GET /s/{code}` 订阅代理端点、管理端点 `GET /api/links` + `DELETE /api/links/{code}`（`SUB_TOKEN` 非空时强制鉴权）；`/sub` 直通路由降级为 **deprecated 遗留兼容路由**（存量链接不断）；新增错误码 `store_error`；新增 env `DATA_DIR` / `SUB_PROXY_TIMEOUT`；`SUB_TOKEN` 用途扩展。详见「短链（v1.2.0）」节。

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
  "link": "http://192.168.1.10:8080/s/Ab3xY9qZ",
  "deepLink": null
}
```

- **`link` 永远非空**（D4 定案）：`ok:false` 时照常返回，前端仍展示并标注「未通过校验，仍可试用」。v1.2.0 起 link 为短链（见「短链」节）；`store_error` 时降级为 `/sub` 长链接（唯一保留长链返回的场景）
- `preset`：本次生效的档位（id + name）。`bad_preset` 时为 `null`；其余情况（含 `ok:false`）恒回显解析后的合法档位，前端结果页据此标注「模板：{name}」
- `ok:false` 时 `target` 为探测用的 target（auto → "clash"），`errorCode` 与 `message` 必填
- `link` 由 sidecar 服务端拼接，前端一律直接使用该字段，不自行拼接
- `deepLink`：选中客户端的一键导入深链（sidecar 拼装，前端一律直接使用）。`target` 为 `auto`、`clash`、`singbox`、`v2rayn` 时为 `null`；其余五家按下表拼装。与 `link` 同源派生：`link` 非空且客户端有模板时恒非空（`ok:false`、`bad_preset` 场景同样照给）

| target | deepLink 拼装 |
|---|---|
| `surge` | `surge://install-config?url=` + `urlencode(link)` |
| `loon` | `loon://import?sub=` + `urlencode(link)` |
| `stash` | `stash://install-profile?url=` + `urlencode(link)` |
| `quanx` | `quantumult-x:///add-resource?remote-resource=` + `urlencode(JSON)`，JSON = `{"server_remote":["<link>, tag=Smart SubConverter"]}` |
| `shadowrocket` | `shadowrocket://add/sub://` + `base64_std(link)` |

口径来源：Surge / Loon / Quantumult X 官方文档，Stash 官方跳链 `link.stash.ws` 模板推断，Shadowrocket 社区口径（机场面板通用）；真机验证状态随 README 兼容矩阵「深链」列回填

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
3. **link 生成（v1.2.0 短链，与探测结果无关，永远执行）**：对规范化参数算指纹 → 查 `links` 表命中则复用既有短码；未命中则生成 8 字符 base62 短码插入（详见「短链（v1.2.0）」节），返回 `${origin}/s/${code}`。存储失败 → `errorCode:"store_error"`，link 降级为 `/sub` 长链接拼接（`${origin}/sub?target=…&url=…&config=…`，`SUB_TOKEN` 非空时追加 `&token=…`），保持非空
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
| `store_error` | 短链写入存储失败（磁盘满/库损坏等运维态） | 短链生成失败了——这次给的是普通链接（含订阅信息，注意别转发）；请部署者检查 /data 卷 |
| `api_unreachable` | **前端侧专用**：fetch /api/preview 失败/超时（15s AbortController）/非 200 | 服务暂时够不着，稍后再试 |

Extended 错误体识别要点（映射 `upstream_error` 细节，Assignment ⑤ 实测）：未知 UA + `target=auto` → 400 + body「Invalid request: unsupported target value. 无效请求：不支持的 target 参数值。」——该文案出现时前端提示优先给「展开指定客户端」指引。

## 短链（v1.2.0）

短链唯一输出：preview 生成的 `link` 为 `${origin}/s/{code}`，凭据只存在容器内 SQLite；`/sub` 降级为 deprecated 遗留兼容路由（nginx 照旧直通，存量长链接不断，契约不再为其新增行为）。

### 存储

- SQLite，`modernc.org/sqlite`（纯 Go 无 cgo）；文件 `${DATA_DIR}/state.db`，权限 0600；首次启动自动建目录建库
- `links` 表：`code TEXT PRIMARY KEY`、`fingerprint TEXT UNIQUE`、`source_url TEXT`、`target TEXT`、`preset_id TEXT`、`config_url TEXT`、`created_at INTEGER`（unix 秒）
- schema 版本用 `PRAGMA user_version`（初值 1），启动时按序幂等迁移；**同码冻结创建时参数**：`source_url` / `target` / `config_url` 存的是创建时刻的解析结果，代理时永远用存的值，不重新解析（防 env 改 `PRESET_URL` 后同码内容漂移）
- **库损坏（打不开/校验失败，非「文件不存在」）→ sidecar 拒绝启动**，错误信息指引「先备份 /data/state.db 再排查，勿直接删除」；绝不自动清空

### 幂等与指纹

- 指纹 = SHA-256 of `v1|{scheme}|{host}|{path}|{rawquery}|{target}|{config_url}`，其中订阅 URL 规范化规则：两端 trim、scheme/host 转小写、去默认端口（http:80 / https:443）、path 为空补 `/`、query 原样保留、fragment 丢弃；`target` 为请求原样值（auto 或指定）；`config_url` 为**解析后的档位 URL**（不用 preset id——同 id 被 `PRESET_URL` 覆盖时指纹自然区分）
- 同指纹 → 永远复用既有短码；删除后同参数重建 → 指纹相同但插入新随机码（即轮换语义）
- 短码：8 字符 base62（大小写字母+数字），`crypto/rand`；插入的两类唯一约束失败分开处理：**指纹冲突 → 重查返回既有码**；**码碰撞 → 重新生成码再插**（上限 5 次）；单测覆盖两类

### GET /s/{code}（订阅代理端点，nginx 路由到 sidecar）

1. 查表 miss → HTTP 404，body `{"error":"not_found"}`（客户端能展示，朋友侧表现为「订阅更新失败」）
2. hit → 容器内 GET `${EXTENDED_BASE}/sub?target={存的target}&url={enc source_url}&config={enc config_url}`（`SUB_TOKEN` 非空时追加 `&token=…`——token 只在容器内注入，朋友的短链不带 token），响应**流式透传**给客户端（不整包缓冲）
3. **UA 透传**：客户端 UA 原样传给 Extended（`target=auto` 识别依赖它）。UA 显式 Set（空 UA 设为空串——Go Transport 对未设置 UA 会补 `Go-http-client/1.1`，破坏识别）；已知边界：直连 /sub 的「无 UA header」与空串 header 在 Extended 侧行为可能有差异，未知客户端本就 fallback
4. 超时 `${SUB_PROXY_TIMEOUT}`（默认 30s，订阅生成可能慢于 preview 的 10s）；Extended 非 200 / 连接失败 / 超时 → HTTP 502，body `{"error":"upstream_unavailable"}`
5. **日志卫生（D6 延续 + OH-2）**：nginx 对 `/s/` `access_log off`（短码即 bearer 凭据，不落 access log）；sidecar 自身日志只记脱敏摘要 `s/ hit code=Ab3*** status=200 latency=1.2s`（code 前 3 位 + 状态 + 耗时，不记完整码、不记订阅 URL）

### 管理端点（GET /api/links、DELETE /api/links/{code}）

- `GET /api/links` → HTTP 200 `{ "links": [ { "code": "Ab3xY9qZ", "target": "surge", "preset": "full_multimode", "sourceUrl": "https://…含凭据…", "createdAt": 1761300000 } ] }`，按 createdAt 降序；**响应即凭据库**，仅部署者自用
- `DELETE /api/links/{code}` → 删除成功 204（幂等：码不存在也 204）；删除后该短链立即 404
- **鉴权**：`SUB_TOKEN` 非空时两者必须带 `?token=${SUB_TOKEN}`（与 /sub 的 Caddy 校验同值同带法），缺失/不符 → HTTP 401；`SUB_TOKEN` 为空（局域网档）→ 开放，信任边界 = 局域网，README 写明「码即授权：持有码者可拉订阅亦可删码，删码可由同参数重建恢复」
- 公网档 Caddyfile（README 附录 A）同步修订：token 校验 scope 到 `path /sub*`，显式豁免 `/`、`/api/`、`/s/`（OH-1——原全局 matcher 会把不带 token 的短链 403）

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
| `SUB_TOKEN` | 空 | 非空时：① 注入 `/s/` 代理与 `store_error` 降级长链的容器内 `/sub` 调用（朋友无感知）② 管理端点（GET /api/links、DELETE）强制鉴权；公网档必配（README 附录） |
| `PRESET_URL` | ACL4SSR_Online.ini | 仅覆盖 `default` 档 url（冒烟用；其余档不受影响） |
| `POLL_INTERVAL` | `15m` | 健康轮询周期 |
| `DATA_DIR` | `/data` | SQLite `state.db` 所在目录（volume 挂载点；单测指向 tmp） |
| `SUB_PROXY_TIMEOUT` | `30s` | `/s/` 代理 sidecar→Extended 超时（订阅生成慢于 preview，勿低于 10s） |

## nginx 路由（镜像层，与契约相关部分）

- `/` → 前端静态文件；`/api/` → sidecar；`/s/` → sidecar（短链代理端点）；`/sub`、`/version`、`/healthz` → Extended(25500)。`/sub` 为 **deprecated 遗留兼容路由**（v1.2.0 起契约不再为其新增行为）
- 对 sidecar 的反代：`proxy_set_header Host $http_host; proxy_set_header X-Forwarded-Proto $scheme;`（**注意用 `$http_host` 而非 `$host`**：nginx 的 `$host` 永远去掉端口，非 80 端口部署时 sidecar 推导的 link 会丢端口不可用——S2 三步冒烟实测抓出后修正）
- `/sub`、`/api/`、`/s/` 关闭 access_log（D6 日志卫生 + OH-2：短码即 bearer 凭据，与订阅 URL 同级对待）

### 契约未定义场景的既定处理（S2 实现既成语义）

- `target` 非注册表值：探测按 clash 进行，link 原样透传该 target，由 Extended 自然拒绝（不发明新错误码）；deepLink 为 `null`（无 scheme 可拼，同透传口径）
- POST body 非法 JSON / 缺 url 字段：按 `bad_url_empty` 处理（响应恒 200 原则不变）
