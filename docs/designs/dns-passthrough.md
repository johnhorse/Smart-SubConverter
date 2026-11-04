# 机场 DNS 透传（DNS Passthrough）设计（v1.7.0）

## 背景与痛点

真实场景：部分机场的 Clash 订阅自带 `dns:` 块（通常是机场自家 DoH，专门用于正确解析其节点域名）。现状两条链路都把它丢了：

- **两段式**（clash/quanx 家族，`sidecar/subproxy.go` 的 `stage1Mixed`）：第一段 `target=mixed` 把订阅压成节点 URI 列表，dns 块在此丢失；
- **单段**（surge/loon/stash 及 auto 的其余 UA）：引擎自拉订阅，同样只取节点。

产物里的 dns 全部来自引擎内置 base 模板（与机场无关）→ 客户端拉到节点但节点域名解析不对，连不通（2026-11-04 用户实测反馈）。

关键有利条件：两段式的 stage 0（`internalSubStore.fetchAndRegister`）本来就代拉机场原始内容，dns 块字节已在 sidecar 手里，只差捞出来喂回去。

## 用户拍板的决策项（2026-11-04）

| 决策点 | 定案 | 被否掉的备选 |
|---|---|---|
| 覆盖范围 | clash 系 + QX + Surge/Loon/Stash，一个版本全做 | 分期交付（用户明确一把梭） |
| clash 系透传策略 | 机场 dns 块**整块替换**产物 dns（含 fake-ip 等模式字段） | 只换 nameserver 字段（机场 nameserver-policy 等精细配置会丢，可能仍连不通） |
| QX/Surge/Loon 透传策略 | **翻译解析服务器**到各家 DNS 配置（格式所限，模式类字段不跨家） | — |
| 生效形态 | 默认全生效；存量短链下次拉取即受益；前端零改动；部署级 env 逃生口（off = 所有路径**逐字节回退**现状行为） | 每链可选开关（要动前端+建链 API+库表，存量链不受益） |
| 无 dns 机场 | 自动 no-op（含 base64 节点列表订阅、解析失败） | — |

主会话代定并已向用户报备的常规项：

- 单段链统一改走 sidecar 代拉（复用 `fetchAndRegister`，v1.3.5 为绕机场 WAF 而生，clash/QX 已全量在用，属加固）；
- preview 探测与拉取同路（探测也走代拉，避免「探测绿、拉取挂」的路径分叉）；
- 引擎 base 模板**运行时读取、不 vendor**（对齐 Dockerfile「不 vendor 模板」既有纪律）。

## 服务端设计（sidecar）

### 提取（拟新 `dns.go`）

- `fetchAndRegister` 需扩展为同时返回订阅内容（fetch 路径与 60s 缓存命中路径字节都在 `internalSubStore.entries` 手里）；
- 从原始字节解析：
  - Clash YAML `dns:` 块（core/meta 两种 schema：`nameserver` / `default-nameserver` / `nameserver-policy` / `enhanced-mode` / `fake-ip-range` / `use-hosts` 等）→ 保真持有（map/结构体，透传时原样回写，不解释语义）；
  - 机场按 UA 给 QX/Surge 原生格式时，解析对应 `[dns]` / `[DNS]` 段；
  - 无 dns / 解析失败 → no-op。

### 注入（具体形态以闸门实验为准）

- 首选：派生 ini `[custom]` 段加 `clash_rule_base` / `quan_rule_base` / `surge_rule_base`（Loon 视闸门结果；无对应机制则 Loon 走输出文本后处理 DNS 段兜底）；
- 派生 base = **运行时读引擎默认 base**（闸门 G2 定位落位）+ dns 整块替换（clash 系）/ 翻译（QX/Surge），落 `{DATA_DIR}/derived/`，回环 URL 供引擎（引擎 config= 只认 http，见 `derive.go` 头注）；
- 与既有 groups/exclude 派生**叠加**：同一根派生 ini 同时承载组行 + rule_base 行；
- 翻译口径（样本驱动，闸门 G4 校准）：
  - QX：DoH URL → `doh-server=`，plain UDP → `dns-server=`；
  - Surge：`dns-server` / `doh-server` / `doh-suffix`；
- 两段式第二段与单段链同样生效（都改传派生 ini）；一个未知 UA 的 auto 链可在一根派生 ini 里同时挂多家 rule_base（引擎按输出格式取用）。

### 失败语义（与 groups 派生的 fail-closed 相反）

- dns **提取失败 → fail-open**（no-op + 脱敏日志一行 `dns_passthrough=absent/error`）：dns 透传是尽力增强，不能因它挂掉拉取；
- 派生 base **写入前自检**（YAML/conf 合法性）：宁可漏透传，不可让引擎拉到坏 base 挂掉整次转换。

### 链路改造

- `handleShortLink` 单段分支 + `probeConvert` 单段分支：`url=` 从 `lnk.SourceURL` 改为 `fetchAndRegister` 回环 URL（UA 原样透传，保留机场按 UA 的内容协商）；
- env 逃生口（大写蛇形、默认 on，如 `PASS_THROUGH_DNS`）：off → 单段照旧传原始 URL、不提取不注入（逐字节回退）。

### 契约与文档

- `api-contract.md` 增量修订（v1.7.0）：/s/ 拉取行为变化 + 新 env；**无 API 形状变化、无前端改动**；
- `compatibility.md` 矩阵加 DNS 透传行（真机验收前标未验证）。

## 前端

零改动（决策定案）。

## 非目标

- singbox / shadowrocket / v2ray 等其余 target 不透传（用户未选；shadowrocket 本就是链接列表无配置体）；
- 不做多订阅合并仲裁（单链单源，现状即如此）；
- 只透传 dns，不透传机场的 rules / proxy-groups。

## 闸门实验（实现前必过，真引擎实测）

- **G1**：external config ini `[custom]` 加 `clash_rule_base=<回环 URL 指完整 base YAML>` → clash 输出的 dns 是否整块随之变；
- **G2**：引擎默认 clash base 的落位（pref.toml 指向 / /base 树布局），sidecar 同容器运行时读取可行性；
- **G3**：`quan_rule_base` / `surge_rule_base` 同 G1；Loon 有无 base 覆盖机制（无则文本后处理兜底）；
- **G4**：真机场样本（用户提供或测试构造）：core/meta schema 提取健壮性；机场按 UA 变格式的行为。

任一闸门假设不成立 → 停下，把实测结果与备选方案写进报告待用户/主会话仲裁，不自行改设计。

## 闸门实验记录（2026-11-04，全部通过）

环境：`docker run -d --name ssc-gate-engine -p 127.0.0.1:25500:25500 aethersailor/subconverter-extended@sha256:145eeaaf...`（base.lock digest），宿主 python http.server serve fixture（`/tmp/ssc-gate/`），订阅节点为 RFC 5737 测试地址。**结论：G1–G4 全部成立，无需仲裁、无需文本后处理改道。**

### G1 clash_rule_base 整块替换 —— ✅ 成立

- **假设**：external config ini `[custom]` 加 `clash_rule_base=<http URL 指完整 base tpl>` → clash 输出的 dns 整块随之变。
- **实验**：ext ini（`[custom]` + `clash_rule_base=http://host/my-clash-base.tpl` + 2 条内联 ruleset）；my-clash-base.tpl = 容器内 `/base/base/all_base.tpl` 复制、clash 段 dns 块替换为机场 dns（去掉模板条件）；`curl /sub?target=clash&url=sub-clash.yaml&config=ext.ini`。
- **结果**：输出 dns 块与派生 base 的 dns 块逐字段一致（enable / enhanced-mode / fake-ip-range / nameserver / default-nameserver 全透传）；meta schema（nameserver-policy 嵌套 map、proxy-server-nameserver）同样保真——引擎仅做 YAML 规范化（列表缩进 4 空格、键去引号），无字段白名单。基线对照（无 rule_base）：**默认 clash 输出根本没有 dns 块**（all_base.tpl 的 dns 块包在 `{% if default(request.clash.dns, "") == "1" %}...{% endif %}` 里，默认关）。
- **推论**：整块替换策略可行；派生 tpl 必须去掉该条件渲染（无条件嵌机场 dns 块）。「拉到节点但 dns 全丢」的痛点在默认模板下比预想更彻底——透传是从无到有。
- **附加实测**：`clash_rule_base` 的值**只认 http URL**（本地路径 `/base/data/xxx.tpl` 实测不生效——与 config= 认本地路径不同）；URL 参数直传 `clash_rule_base=` 也不认（必须写进 external config ini）。→ 派生 base 必须落 `{DATA_DIR}/derived/` 并回环 serve。

### G2 引擎默认 base 落位与运行时读取 —— ✅ 成立

- **假设**：pref.toml 指向可定位、sidecar 同容器可读。
- **实验**：`docker exec` 读 `/base/pref.toml` 与 `/proc/1/cwd`。
- **结果**：引擎 cwd=`/base`；pref.toml `base_path = "base"` + `clash_rule_base = "base/all_base.tpl"` → 绝对落位 **`/base/base/all_base.tpl`**；stash 独立模板 **`/base/base/stash.yaml`**。/base 树运行时可读已有先例（snapshot 的 mapFrozenConfig 引擎读本地 ini）。
- **推论**：sidecar `os.ReadFile("/base/base/all_base.tpl")` 与 `/base/base/stash.yaml` 直接可行，不 vendor、运行时读成立。Dockerfile 既有 `sed` 补丁（v1.3.2 QX 修复）同路径先例。

### G3 quanx / surge / loon / stash rule_base —— ✅ 全部成立（含 Loon，无需文本后处理兜底）

- **假设**：`quan_rule_base` / `surge_rule_base` 同 G1；Loon 有无 base 覆盖机制未知。
- **实验**：同 G1 方法逐家替换 all_base.tpl 对应段（surge 段 `[General]` dns-server/doh-server/doh-suffix、loon 段 dns-server/doh-server、quanx 段 `[dns]` server= 行）。
- **结果**：
  - `surge_rule_base` ✅：dns-server / doh-server / doh-suffix 行全部透传生效；
  - `loon_rule_base` ✅：**Loon 有 base 覆盖机制**，dns-server / doh-server 替换生效、旧值无残留——设计预留的「Loon 走输出文本后处理兜底」不需要；
  - `quanx_rule_base` ✅：**键名坑**——`quan_rule_base` 对 target=quanx 不生效（那是 Quantumult 原版的键），必须用 `quanx_rule_base`；`[pref]` 段写法不认；
  - `stash_rule_base` ✅：**值必须是纯 YAML**（喂 tpl 报 400 "the selected Stash base template must be a YAML mapping"）；stash.yaml 顶层 `dns:` 块行级替换生效、干净无残留；
  - 多家 rule_base 同挂一根 ext ini + auto target：clash UA / Loon UA 均正确取用对应键（单段 auto 链一根派生 ini 挂全家的设计成立）。
- **推论**：四家全部走 ext ini + rule_base，无一家需要文本后处理。派生物两份：tpl（clash/quanx/surge/loon 四段混合替换，单文件）+ stash 的纯 YAML（dns 顶层块替换）。
- **附带发现**：引擎 auto 把 "Quantumult X" UA 识别为 quan（原版）而非 quanx（输出 `[SERVER]` 段）——生产链路无影响（sidecar 对 QX UA 恒走两段式显式 quanx），但不可依赖引擎 auto 认 QX；引擎 auto 不识别 Stash UA（400 unsupported target，与现状一致，stash 需显式 target）。

### G4 提取健壮性与 UA 内容协商 —— ✅ 成立（测试构造）

- **假设**：core/meta schema 可提取；机场按 UA 变格式不影响提取（按内容嗅探）。
- **实验**：构造 core schema（nameserver/plain IP 混排）与 meta schema（nameserver-policy 嵌套 map / proxy-server-nameserver）两份订阅；`target=mixed` 展开验证 dns 丢失；两段式第二端到端（URI|URI + ext ini）。
- **结果**：两种 schema 的 dns 块均按「顶层 `dns:` 键 + 缩进块」边界可稳定切出（与 stash.yaml 同法）；`target=mixed` 输出（base64 URI 列表）确认 dns 丢弃（痛点成立）；两段式第二段（URI|URI + rule_base ext ini）dns 整块注入 + 纯内联节点共存——端到端打通。base64 节点列表订阅无 dns → no-op。
- **推论**：提取函数按内容嗅探（先 clash YAML `dns:` 块、再 QX `[dns]` 段、再 Surge `[General]` dns 行），不按 UA 硬走；真机场按 UA 变格式的行为无法离线实测，留真机验收校准（compatibility.md 标未验证）。
- **翻译口径定稿（真机验收前按官方文档口径）**：QX `server=<ip>`（plain，每行一个）+ `doh-server=<url1>, <url2>`（单行逗号分隔）；Surge/Loon `dns-server = <plain...>` + `doh-server = <doh...>`（逗号分隔）；`doh-suffix` 机场语义未知不造。引擎对 [dns]/[General] 段只透传不校验，语法正确性归客户端。

### 闸门期实现修正（对设计「注入」节的落实细节）

- 派生 base 与 ext ini 均 serve 回环 URL（G1 附加实测 rule_base 只认 http）；
- QX 用 `quanx_rule_base` 键（G3 键名坑）；
- stash 派生物是独立纯 YAML（非 tpl），ext ini 另挂 `stash_rule_base`；
- 豁免档（PRESET_URL 覆盖、无本地 rewritten ini 作底）dns 透传 no-op（fail-open）——与自定义分组能力位同款边界，无法在远端 ini 上叠加 rule_base 行。
