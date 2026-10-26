# /api 契约（v1，冻结版）

前端与 sidecar 的唯一接口。本文档是两端的 truth source：字段名、错误码、文案以此为准，改动需经主会话（双向同步改）。

> 增量修订（2026-10-24，规则模板切换）：`presets` 内置三档；`POST /api/preview` 增可选 `preset` 请求字段与响应 `preset` 回显；新增错误码 `bad_preset`；`PRESET_URL` 语义收窄为仅覆盖 default 档。其余形状不变。

> 增量修订（2026-10-24，导入深链）：`POST /api/preview` 响应新增 `deepLink`（string | null，sidecar 拼装的一键导入深链，拼装规则见该节）；`GET /api/clients` 形状不变。其余形状不变。

> 增量修订（2026-10-24，v1.2.0 短链）：`POST /api/preview` 响应 `link` 从 `/sub` 长链接改为 `/s/{code}` 短链（deepLink 与二维码自动随嵌）；新增 `GET /s/{code}` 订阅代理端点、管理端点 `GET /api/links` + `DELETE /api/links/{code}`（`SUB_TOKEN` 非空时强制鉴权）；`/sub` 直通路由降级为 **deprecated 遗留兼容路由**（存量链接不断）；新增错误码 `store_error`；新增 env `DATA_DIR` / `SUB_PROXY_TIMEOUT`；`SUB_TOKEN` 用途扩展。详见「短链（v1.2.0）」节。

> 增量修订（2026-10-24，v1.3.0 规则快照）：三档预设默认**快照本地化**（引擎读本地文件，对 GitHub 零网络依赖；`presets[].url` 仍为上游原始 URL 作来源披露，形状不变）；`GET /api/health` presets 新增 `lastRefreshedAt`、`status` 本地化档改快照刷新驱动（豁免档维持远端 HEAD 语义）；`PRESET_URL` 语义改为「覆盖即豁免本地化」；`/s/` 代理对存量短链的冻结 config 做内置 URL → 本地路径运行时映射；新增 env `PRESET_REFRESH_INTERVAL` / `PROBE_TIMEOUT`；`SUB_PROXY_TIMEOUT` 默认 30s→120s。详见「规则快照（v1.3.0）」节。

> 增量修订（2026-10-24，v1.3.1）：`GET /s/{code}` 的 404/502 错误响应内容协商——`Accept: text/html`（浏览器直开）返回人话 HTML 页，客户端/程序化消费维持 JSON 不变。详见「GET /s/{code}」节第 5 条。

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

- **`link` 永远非空**（D4 定案）：`ok:false` 时照常返回，前端仍展示并标注「未通过校验，仍可试用」。v1.2.0 起 link 为短链（见「短链」节）；两个降级为 `/sub` 长链的场景：① `store_error`（存储失败）② `bad_url_*`（URL 未过预校验**不落库**——私网 URL 落库后经 `/s/` 持久重放会绕过 D7 准入闸门，OH-13 放大）。`bad_preset` URL 合法，仍走短链（default 档）
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
2. **探测转换**：探测 target 为 `quanx` / `clash`（含 auto 默认的 `clash`）时，探测走与 /s/ 拉取**同一两段式路径**（v1.4.0 起，见 /s/ 第 4 条：sidecar 代拉 → mixed 转通用 → 转探测 target），所见即所得——preview 绿 = 受限网络消费端拉得到内联节点；其余 target 直通引擎：GET `${EXTENDED_BASE}/sub?target=<探测target>&url=<enc>&config=<enc 所选档位url>`，超时 `min(PROBE_TIMEOUT, SUB_PROXY_TIMEOUT)`（默认 30s；两段式路径同此预算且从代拉起算——代拉也纳入整体预算）：
   - 探测 target：用户显式指定 → 该 target；auto → `clash`（设计定案的启发式）
   - UA：按 target 家族给代表性 UA（clash→`clash-verge/v1.7.7`，surge→`Surge Mac/2398`，其余给通用 UA；探测 target 显式时 UA 影响很小）
   - 200 且格式粗验通过（两段式路径（quanx/clash）：输出**内联节点数 > 0**，替代旧「clash 系：body 含 `proxies:`」——provider 形态 body 也含空 `proxies:`，旧粗验对「节点自拉死锁」是结构性盲区；surge 系：body 含 `[General]` 或以 `#!MANAGED-CONFIG` 开头；singbox：可 JSON 解析且含 `outbounds`；其余 target：body 非空且不含 `Invalid request`）→ `ok:true`
   - 200 但粗验不过 → `ok:false`，`errorCode:"upstream_error"`
   - 非 200 → `ok:false`，`errorCode:"upstream_error"`，message 由 Extended 响应体映射（见错误码表）
   - 连接失败 → `backend_down`；超时 → `upstream_timeout`
3. **link 生成（v1.2.0 短链，与探测结果无关，永远执行）**：对规范化参数算指纹 → 查 `links` 表命中则复用既有短码；未命中则生成 8 字符 base62 短码插入（详见「短链（v1.2.0）」节），返回 `${origin}/s/${code}`。两处降级为 `/sub` 长链拼接（`${origin}/sub?target=…&url=…&config=…`，`SUB_TOKEN` 非空时追加 `&token=…`），保持非空：① 存储失败 → `errorCode:"store_error"` ② 预校验失败的 `bad_url_*`（见字段说明）。**errorCode 并存优先级（sidecar 实现定案）**：探测失败 + 存储失败 → 探测错误优先；探测成功 + 存储失败 → `store_error` 且 `ok` 仍为 `true`（转换本身成了，前端按「成功 + 短链警示」展示）；`bad_preset` + 存储失败 → `bad_preset` 优先
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

1. 查表 miss → HTTP 404，body `{"error":"not_found"}`（客户端能展示，朋友侧表现为「订阅更新失败」）；查表运行时 DB 错误 → HTTP 500，body `{"error":"internal"}`（罕见运维态，不在预定义错误内）
2. hit → 容器内 GET `${EXTENDED_BASE}/sub?target={存的target}&url={enc source_url}&config={enc config_url}`（`SUB_TOKEN` 非空时追加 `&token=…`——token 只在容器内注入，朋友的短链不带 token），响应**流式透传**给客户端（不整包缓冲；响应头只透传 `Content-Type`，长度类头由 Go 按流自动处理）
3. **UA 透传**：客户端 UA 原样传给 Extended（`target=auto` 识别依赖它）。UA 显式 Set（空 UA 设为空串——Go Transport 对未设置 UA 会补 `Go-http-client/1.1`，破坏识别）；已知边界：直连 /sub 的「无 UA header」与空串 header 在 Extended 侧行为可能有差异，未知客户端本就 fallback
4. **两段式服务端代取（v1.3.3 QX；v1.3.5 改内部回环代拉；v1.4.0 扩展到 clash）**：链 target=quanx 或 clash（显式恒走），或 target=auto 且请求 UA 宽匹配 `quantumult` / clash 系词表（`clash`、`mihomo`、`shellcrash`，小写包含；路由决策，权威识别仍在引擎）时，/s/ 代理改为：**sidecar 代拉订阅源**（Go 抓取；`SUBSCRIPTION_PROXY` 非空时走代理）→ 内容注册内部端点 `/internal/sub/{ref}`（随机 ref、60s TTL、仅容器内回环可达，nginx 不路由）→ 引擎 `target=mixed` 从回环拉内容转通用节点 URI 列表 → `target=<归一后的链 target>&url=<URI|URI>` 明文节点列表转换（显式链原样；auto 链按 UA 家族归一为 quanx/clash，与 v1.3.3 auto+quantumult 第二段固定 quanx 的既有行为同构） → 输出内联明文节点（quanx：`[server_local]`；clash：`proxies` 内联数组），客户端零二次拉取。动机：① Extended 对 quanx+http 订阅默认 native server_remote（机场原始 URL 塞给客户端自拉），而「机场输出 QX 解析不了」恰是本服务目标用户场景（pref 无 quanx 开关）；② 机场 WAF 按 **TLS 指纹**拦引擎直拉（实测：busybox/libcurl 指纹 403、curl/Go 指纹 200，UA 无关，同 IP）——代拉换 Go 指纹 + 可选代理出口；③（v1.4.0）Extended 对 clash 输出 proxy-providers 形态（0 内联节点、节点列表 url=机场原始订阅、客户端自拉）——客户端网络够不到机场时节点永远 0（2026-10-26 实证：腾讯 AS45090 被机场 CF 按 ASN 封 1005；且 mihomo 自拉 provider 走宿主直连、不经自身代理链，服务端代理通道救不了它），服务端代取内联是唯一解。`SUBSCRIPTION_PROXY` 语义（v1.3.5）：sidecar 代拉走代理（引擎 pref `proxy_subscription` 的改写保留兜底）。两段各分得 `SUB_PROXY_TIMEOUT` 一半；代拉非 200 / 第一段空节点列表 → 502 `upstream_unavailable`
5. 超时 `${SUB_PROXY_TIMEOUT}`（默认 120s，v1.3.0 起；订阅生成可能慢于 preview）；Extended 非 200 / 连接失败 / 超时 → HTTP 502，body `{"error":"upstream_unavailable"}`（两段式路径同码）
5. **错误响应内容协商（v1.3.1）**：404 `not_found` 与 502 `upstream_unavailable` 两个错误，当请求 `Accept` 含 `text/html`（浏览器直开）时返回同状态码的**人话 HTML 页**（`Content-Type: text/html; charset=utf-8`；404 = 「链接不存在或已被撤销」、502 = 「这是订阅链接，请在客户端中打开」并附「若客户端拉取仍见此页则上游暂时不可用」提示）；其余 Accept（客户端 / 程序化消费）维持 JSON error code **不变**。500 `internal` 恒 JSON（内部错误不做人话包装）。动机：浏览器 UA 不在 Extended 识别表内、直开必 400→502，冷 JSON 曾被误读为「服务故障」；浏览器不是订阅拉取客户端，人话页消除误判
6. **日志卫生（D6 延续 + OH-2）**：nginx 对 `/s/` `access_log off`（短码即 bearer 凭据，不落 access log）；sidecar 自身日志只记脱敏摘要 `s/ hit code=Ab3*** status=200 latency=1.2s`（code 前 3 位 + 状态 + 耗时，不记完整码、不记订阅 URL）

### 管理端点（GET /api/links、DELETE /api/links/{code}）

- `GET /api/links` → HTTP 200 `{ "links": [ { "code": "Ab3xY9qZ", "target": "surge", "preset": "full_multimode", "sourceUrl": "https://…含凭据…", "createdAt": 1761300000 } ] }`，按 createdAt 降序；**响应即凭据库**，仅部署者自用
- `DELETE /api/links/{code}` → 删除成功 204（幂等：码不存在也 204）；删除后该短链立即 404
- **鉴权**：`SUB_TOKEN` 非空时两者必须带 `?token=${SUB_TOKEN}`（与 /sub 的 Caddy 校验同值同带法），缺失/不符 → HTTP 401；`SUB_TOKEN` 为空（局域网档）→ 开放，信任边界 = 局域网，README 写明「码即授权：持有码者可拉订阅亦可删码，删码可由同参数重建恢复」
- 公网档 Caddyfile（README 附录 A）同步修订：token 校验 scope 到 `path /sub*`，显式豁免 `/`、`/api/`、`/s/`（OH-1——原全局 matcher 会把不带 token 的短链 403）

## 规则快照（v1.3.0）

国内服务器直连 GitHub raw 慢/超时是常态（实测：规则集 6h 缓存 TTL 过期后现场重拉超 30s → `upstream_unavailable`）。v1.3.0 起三档预设的 .ini 模板与其引用的全部规则集文件走「快照本地化」，引擎读本地文件，对 GitHub 零网络依赖；系统仅剩网络依赖 = sidecar 后台刷新快照 + 引擎拉订阅源（机场，本来就要实时）。

### 快照分层与刷新

引擎本地读取**硬限 `/base` 路径树**（canonical path 校验，pref 无配置项；`file://` 不支持、symlink 桥接被拒、范围外 ruleset 静默丢弃——2026-10-24 闸门实测，证据 /tmp/gate-probe.txt）。快照布局据此定：

- **出厂快照**：vendor 于仓库 `image/rules-snapshot/`（原始未改写文件 + 拉取时间戳 `SNAPSHOT_META`），构建时 COPY 进镜像 `/base/rules-snapshot/`（只读兜底层，不被 volume 遮蔽）
- **运行时快照**：`{DATA_DIR}/rules/`（volume 持久层；`DATA_DIR` 默认 `/base/data`，**必须位于 /base 树内**引擎才可读）。sidecar 启动时确保该目录有可用快照（无则从出厂层复制并改写），此后按 `PRESET_REFRESH_INTERVAL`（默认 24h，启动时先刷一轮）从上游逐文件刷新：**单文件成功即覆盖**（逐文件 last-known-good），失败保留旧文件；每轮刷新后重做 .ini 改写
- **.ini 改写**：sidecar 将 .ini 中引用的规则集 GitHub URL 替换为运行时快照目录内的本地绝对路径，改写产物落 `{DATA_DIR}/rules/`；引擎 `config=` 收到的是改写后 .ini 的本地路径。改写仅在 sidecar 侧发生（CI/vendor 不改写）。改写器范围：仅 URL 形态的 ruleset 行（实测全集 35 个 URL、无 basename 冲突可平铺、无 include= 行、非 URL ruleset 仅 `[]FINAL`/`[]GEOIP,CN` 内置型）
- **静默丢规则防护（实测风险，硬性要求）**：改写后必须校验该 .ini 引用的全部规则集文件路径存在，校验不过**不切换**（继续用旧改写产物）；冒烟断言本地化输出规则数 == GitHub 基线（ACL4SSR_Online 档 DOMAIN 94 / GEOIP 1，闸门实测值）
- **引擎指向**：三档预设默认解析到「运行时快照改写后 .ini」；`PRESET_URL` 覆盖的档（仅 default）**豁免本地化**，sidecar 原样透传部署者 URL 给引擎（部署者自担网络）
- **降级行为**：`DATA_DIR` 不在 /base 树内（显式自设的部署）→ 启动 log 警告 + 快照本地化整体禁用（三档全部按豁免档语义工作，health 如实反映），不拒启动
- **未就绪窗口**：改写产物不存在时（启动至首轮改写完成之间、或上述禁用态）该档 `config=` 透传原始上游 URL（渐进降级，窗口内行为同 v1.2.x），产物就绪后自动切本地路径
- **`lastRefreshedAt` 更新粒度**：仅当该档本轮实际落地新内容（.ini 或其引用规则集任一拉取成功）才更新为刷新时刻；纯旧文件重做改写或离线重启保持原时刻（不冒充新刷）

### 对既有契约面的影响

- `GET /api/clients`：**形状零变化**。`presets[].url` 仍为上游原始 GitHub URL（来源披露；豁免档为部署者 URL）
- 建链冻结与幂等指纹：本地化档建链冻结的 config = 本地改写后 .ini 路径。升级后同指纹首建会生成**新短码**（config 解析结果变化，旧码仍有效），属预期一次性影响
- **存量短链兼容**：`/s/` 代理发现冻结 config 为内置三档 GitHub URL 时，运行时映射为本地改写后 .ini 路径（库不动、存量短码原地升级为快照直读）
- `POLL_INTERVAL` 健康轮询对本地化档不再发远端 HEAD（改为读快照刷新状态）；豁免档维持远端 HEAD 自采
- 部署命令：`-v <vol>:/base/data`（原 `/data` 挂载点改至此，卷名与卷内数据不变，state.db 与规则快照同居一卷）；README 升级说明注明

### 闸门实测结论（2026-10-24 已测毕，三项全过）

① 本地路径可用但硬限 /base（上述布局依据）；volume 挂 /base 子路径实测输出与 GitHub 基线逐行一致。② 规则集全集 35 个 URL 去重 3.07 MiB（最大 1.44MB，无冲突）。③ 引擎磁盘缓存 `/base/cache/`（6h TTL，pref 默认开启；快照本地化后 config/ruleset 不再依赖，订阅源缓存走引擎内置，不进契约承诺）。完整证据：/tmp/gate-probe.txt、清单 /tmp/gate-rules/{urls.txt,MANIFEST.txt}（临时文件，缺失时从三档 .ini 重新解析）。

## GET /api/health

```json
{
  "backendVersion": "v1.10.5",
  "presets": [
    {
      "name": "ACL4SSR 默认",
      "url": "https://raw.githubusercontent.com/…/ACL4SSR_Online.ini",
      "lastModifiedAt": null,
      "lastRefreshedAt": "2026-10-24T08:41:37Z",
      "lastSuccessAt": "2026-10-24T08:41:37Z",
      "status": "fresh"
    }
  ]
}
```

- `backendVersion`：解析 `${EXTENDED_BASE}/version` 的 **HTML**（正则 `info-value[^>]*>\s*(v[\d.]+)`，Assignment ② 实测返回页面非纯文本）；失败 → `"unknown"`
- **本地化档（v1.3.0 起默认三档）**：数据源 = 快照刷新循环
  - `source: "snapshot"`（档位标识：豁免档为 `"remote"`；字段缺席 = v1.3.0 前的 sidecar，前端按 remote 旧文案回退）
  - `lastRefreshedAt`：当前所用快照的获得时间——出厂快照 = vendor 时间戳（`SNAPSHOT_META`）；运行时刷新成功 = 刷新时刻
  - `lastModifiedAt`：最近一次刷新成功时上游响应的 Last-Modified 头，缺头为 `null`（信息保真：内容本身多旧）；ETag 仅内部变更检测，不进契约
  - `lastSuccessAt`：同 `lastRefreshedAt`（保留字段名兼容前端）
  - **status 按序判定**：① `lastRefreshedAt` 距今 < 2×`PRESET_REFRESH_INTERVAL`（默认 48h）→ `fresh`；② ≥ → `stale`。「不冒充 fresh」原则延续：从未刷新成功且无出厂时间戳 → `unknown`
- **豁免档（`PRESET_URL` 覆盖的 default 档）**：维持 v1.0 远端 HEAD 自采语义（每 15 min、`POLL_INTERVAL` 可调），status 判定沿用：① 从未成功抓取 → `unknown` ② 最近一次抓取失败 → `stale` ③ `lastModifiedAt == null` → `unknown` ④ < 24h → `fresh` ⑤ ≥ 24h → `stale`
- 前端文案（按 `source` 选文案集；缺席按 remote）：
  - **snapshot**：fresh → 「规则快照 · {X} 前更新」；stale → 「规则快照已 {X} 未刷新」；unknown → 「规则库状态未知」
  - **remote**（豁免档，沿用 v1.0 文案）：fresh → 「规则内容 {X} 前更新」；stale → 「规则库可能过期（{X} 未更新）」/最近抓取失败时「规则源暂时够不着」；unknown → 「规则库状态未知」
  - `backendVersion == "unknown"` → 后端版本显示 unknown

## 环境变量（sidecar）

| env | 默认 | 用途 |
|---|---|---|
| `SIDECAR_PORT` | `8081` | 监听端口（勿用 `PORT`，被 Extended 占用） |
| `EXTENDED_BASE` | `http://127.0.0.1:25500` | Extended 内部地址 |
| `PUBLIC_BASE_URL` | 无（强烈建议设置） | link origin 覆盖 + 镜像层转 `MANAGED_CONFIG_PREFIX`（见 assignment-findings ⑤）；**同时决定输出中 getruleset/MANAGED-CONFIG 自引用前缀**——不设则回指 127.0.0.1:25500，外部客户端（Quantumult X 等）拉不到规则集导入失败（sidecar 启动打警告） |
| `SNAPSHOT_PROXY` | 空（直连） | 规则/模板上游访问代理（`http://` 或 `socks5://`）：作用于快照刷新与豁免档远端 HEAD；**订阅拉取不经它**（订阅代拉走 `SUBSCRIPTION_PROXY`，默认直连）。国内服务器 GitHub 直连慢时配置宿主代理（如 `http://host.docker.internal:7890`） |
| `SUBSCRIPTION_PROXY` | 空（直连） | v1.3.8：**sidecar 代拉订阅走代理**（Go 抓取与 curl 重试两路，`curl -x`）；v1.3.4 的引擎 pref `proxy_subscription` 改写保留兜底。场景：机场 WAF 拦服务器直连请求形态（curl 可过、引擎/Go 403 的 TLS 指纹类拦截），宿主代理出口可过时配置。与 `SNAPSHOT_PROXY` 相互独立（规则/模板走 `SNAPSHOT_PROXY`、订阅走 `SUBSCRIPTION_PROXY`，可同可异；v1.3.5~v1.3.7 曾误共用 `SNAPSHOT_PROXY`，v1.3.8 修正） |
| `SUB_TOKEN` | 空 | 非空时：① 注入 `/s/` 代理与 `store_error` 降级长链的容器内 `/sub` 调用（朋友无感知）② 管理端点（GET /api/links、DELETE）强制鉴权；公网档必配（README 附录） |
| `PRESET_URL` | ACL4SSR_Online.ini 的 GitHub URL | 仅覆盖 `default` 档：**覆盖即豁免本地化**——该档 sidecar 原样透传此 URL 给引擎（部署者自担网络；冒烟 fixture 注入沿用此口） |
| `POLL_INTERVAL` | `15m` | 健康轮询周期（仅豁免档远端 HEAD；本地化档由快照刷新循环驱动） |
| `PRESET_REFRESH_INTERVAL` | `24h` | 快照刷新周期（启动时先刷一轮；本地化档 `status` fresh 阈值 = 2× 此值） |
| `PROBE_TIMEOUT` | `30s` | preview 探测 sidecar→Extended 超时（v1.3.0 前硬编码 10s；慢网络下预检不再 10 秒判死） |
| `DATA_DIR` | `/base/data` | SQLite `state.db` 与运行时快照 `rules/` 所在目录（volume 挂载点；**必须位于 /base 树内**引擎才可读本地快照，不在树内则快照本地化禁用并告警；单测指向 tmp） |
| `SUB_PROXY_TIMEOUT` | `120s` | `/s/` 代理 sidecar→Extended 超时（v1.2.0 默认 30s；国内慢网络实测不够。快照本地化后常规请求应快，此值兜订阅源慢的情况） |

## nginx 路由（镜像层，与契约相关部分）

- `/` → 前端静态文件；`/api/` → sidecar；`/s/` → sidecar（短链代理端点）；`/sub`、`/version`、`/healthz`、**`/getruleset`** → Extended(25500)。`/sub` 为 **deprecated 遗留兼容路由**（v1.2.0 起契约不再为其新增行为）。`/getruleset`（v1.3.1）：surge/quanx 系输出 `[filter_remote]` 与 clash provider url 引用该端点，不路由会落入 `location /` 返回 index.html——严格校验客户端（Quantumult X「Response invalid」）当场失败，宽容客户端（mihomo）静默丢规则；query 仅含规则集来源（无订阅凭据），保留 access_log
- surge/quanx 类外部可达依赖：输出中 getruleset/MANAGED-CONFIG 自引用前缀 = `MANAGED_CONFIG_PREFIX`（镜像层由 `PUBLIC_BASE_URL` 联动注入）；**`PUBLIC_BASE_URL` 未设时回指 127.0.0.1:25500，外部客户端拉不到规则**——sidecar 启动时为空打警告（v1.3.1）
- 对 sidecar 的反代：`proxy_set_header Host $http_host; proxy_set_header X-Forwarded-Proto $scheme;`（**注意用 `$http_host` 而非 `$host`**：nginx 的 `$host` 永远去掉端口，非 80 端口部署时 sidecar 推导的 link 会丢端口不可用——S2 三步冒烟实测抓出后修正）
- `/sub`、`/api/`、`/s/` 关闭 access_log（D6 日志卫生 + OH-2：短码即 bearer 凭据，与订阅 URL 同级对待）

### 契约未定义场景的既定处理（S2 实现既成语义）

- `target` 非注册表值：探测按 clash 进行，link 原样透传该 target，由 Extended 自然拒绝（不发明新错误码）；deepLink 为 `null`（无 scheme 可拼，同透传口径）
- POST body 非法 JSON / 缺 url 字段：按 `bad_url_empty` 处理（响应恒 200 原则不变）
