# Assignment 核实结果（2026-10-23，实现前事实基线）

设计文档 `docs/designs/smart-subconverter-v1.md` 的 Assignment ①–⑤ 全部核实。本文档是实现期的 truth source：与设计文档冲突处，以本文实测为准并在 brief 中显式引用。

## ① 上游镜像架构（arm64 实证）

`docker manifest inspect aethersailor/subconverter-extended:latest`：

- `linux/amd64` ✓、`linux/arm64` ✓、`linux/arm/v7` ✓
- **arm64 可承诺**（设计文档 Dependencies 的前提条件已满足；CI 构建双架构）

## ② /version 与 /healthz

- `/healthz` → HTTP 200（纯状态码，可直接用）
- `/version` → **返回 HTML 页面，不是纯文本**。版本号嵌在 `<div class="info-value">v1.10.5</div>`（当前 v1.10.5，commit 7a75c14，构建 2026-10-22）
- **sidecar 的 backendVersion 需解析 HTML**：正则 `info-value[^>]*>(v[\d.]+)<` 或等价实现；解析失败 → `"unknown"`
- 页面还有 `/inspect` 诊断台入口（Extended 自带，与本项目无关，不依赖）

## ③ 预设远端配置 URL 的响应头（/api/health 自采依据）

- `https://raw.githubusercontent.com/ACL4SSR/ACL4SSR/master/Clash/config/ACL4SSR_Online.ini` → 200，**有 `etag`（content hash），无 `last-modified` 头**
- `.../ACL4SSR_Online_MultiMode.ini` → **404（路径不存在，勿用）**
- `https://raw.githubusercontent.com/Aethersailor/Custom_OpenClash_Rules/main/config/mihomo/lite.yaml` → 404（猜测路径，勿用）
- **结论**：`lastModifiedAt` 常态为 null（设计契约的 unknown 语义正好生效）；新鲜度变更检测以 **ETag 对比**为主，lastModifiedAt 仅在有 Last-Modified 头时填充
- V1 默认预设定为 `ACL4SSR_Online.ini`（唯一实测 200 的候选；设计文档倾向的「进阶 MultiMode」URL 已 404，落 V1.5 时再选新档）

## ④ 镜像结构与 s6 接管兼容性

- `Entrypoint=null`，`Cmd=["/usr/local/bin/start-subconverter"]`，`User=""`（root），`WorkDir=/base`
- `RepoDigests: aethersailor/subconverter-extended@sha256:2da8f8d9d139de111606afe7beb2860f9bccd5c13de66ae0ee3cb9506c9dc9b7`（**lock 文件初始 pin**）
- `start-subconverter` 全文（s6 run 脚本需复刻其行为）：
  ```sh
  #!/bin/sh
  set -e
  ARCH="$(uname -m)"
  case "$ARCH" in
    x86_64) LIB_ARCH="x86_64-linux-gnu" ;;
    aarch64|arm64) LIB_ARCH="aarch64-linux-gnu" ;;
    *) LIB_ARCH="" ;;
  esac
  if [ -n "$LIB_ARCH" ]; then
    export LD_LIBRARY_PATH="/lib/${LIB_ARCH}:/usr/lib/${LIB_ARCH}:/lib64:/usr/lib"
  fi
  CONF="${PREF_PATH:-/base/pref.toml}"
  CONF_DIR="$(dirname "$CONF")"
  mkdir -p "$CONF_DIR"
  if [ ! -f "$CONF" ] && [ -f /base/pref.example.toml ]; then
    cp /base/pref.example.toml "$CONF"
  fi
  exec /usr/bin/subconverter -f "$CONF"
  ```
- 基础系统：os-release 自称 **Alpine Linux**，包管理器 `apk`；但 subconverter 二进制是 **glibc** 构建靠 `LD_LIBRARY_PATH=/lib/<arch>-linux-gnu` 运行（镜像内自带 glibc 库目录）。**叠加进程（nginx、s6-overlay、sidecar）装 musl 原生版即可，互不影响**
- `/base` 内容：`base cache config generate.ini gistconf.ini pref.example.{ini,toml,yml} pref.toml profiles snippets third-party-licenses`
- `/base/config/`：`example_external_config.{ini,toml,yml}`（内置 external config 示例）
- **容器内无可用 HTTP 客户端**：无 curl；wget 二进制损坏（`Error relocating libssl.so.3: __memcpy_chk: symbol not found`）——sidecar 的健康自采/探测必须自带 HTTP 实现（fetch / net/http），不能 shell out curl/wget

## ⑤ 容器网络与转换链路（真实请求实测）

- **全链路 200**：`GET /sub?target=clash&url=<内联 ss 节点>&config=<ACL4SSR_Online.ini>` + clash UA → 200，14323B 合法 Clash YAML；fixture 节点入 proxies；远端 ACL4SSR 规则组（🚀 节点选择 等）成功嵌入 → **容器出网抓 GitHub raw 成功，转换器工作正常**
- **Surge 关键坑**：surge 输出头部 `#!MANAGED-CONFIG http://127.0.0.1:25500/sub?... interval=86400 strict=false` —— 回指 `managed_config_prefix`（默认 `http://127.0.0.1:25500`）。朋友导入后 Surge 的自动更新会指向朋友自己的回环地址，**更新链路断裂**
  - **对策（实现必做）**：镜像 entrypoint 把 `PUBLIC_BASE_URL`（若设置）同时导出为 Extended 的 `MANAGED_CONFIG_PREFIX`（Extended 原生支持该 env 覆盖，见 Wiki 配置参考「常用环境变量」表）；README 局域网档示例命令必须含 `-e PUBLIC_BASE_URL=http://<部署机IP>:<宿主端口>`。sidecar 的 link origin 推导与 Extended 的 managed prefix 用同一个 env，一处配置两处生效
- **`target=auto` + 无特征 UA（浏览器 UA）→ HTTP 400**，body 双语：`Invalid request: unsupported target value. 无效请求：不支持的 target 参数值。` —— 错误映射表需收录（auto 识别失败场景；sidecar 校验用固定 UA 不会踩，但朋友客户端踩到时要能翻译）
- Extended `/sub` 对未知 query 参数宽容（token 参数注入不会 4xx）

## Token 机制核实（R3-5 定论）

- **Extended 无原生 /sub 访问 token**：Configuration wiki 全部字段 + 常用环境变量表（PORT / MANAGED_CONFIG_PREFIX / MANAGED_PREFIX / SUBCONVERTER_SECURITY_PROFILE / SUBCONVERTER_ALLOW_PUBLIC_UPLOAD / 并发与缓存类）均无鉴权项；Security-and-Privacy 页只有 `profile` 档位（lan/public/strict）与日志脱敏
- **公网档 token 校验点 = 反代层**（Caddy/nginx query matcher 或 basic auth），写进 README 公网附录的 Caddy 配置；sidecar 仅负责把 `SUB_TOKEN` env 注入生成的 /sub 链接（Extended 忽略未知参数，无害）
- `SUBCONVERTER_SECURITY_PROFILE=public` 应在公网档 README 中一并给出（Extended 侧 SSRF 纵深，见下）

## SSRF 现状（D7 的第二层）

- Extended `security.profile`：`lan`（默认，可抓私网/fake-ip）→ `public`（阻止回环/私网/fake-ip 字面量）。**与 sidecar 的 D7 预校验语义同源**：局域网档 lan + sidecar 预校验；公网档 public + token，双层纵深
- sidecar 预校验仍按设计文档实现（scheme 白名单 + 私网字面量拒绝），不依赖 Extended 档位

## Wiki 三页 + 配置/安全两页已索引（ctx 知识库）

source 标签：`ext-wiki-docker` / `ext-wiki-compatibility` / `ext-wiki-rules` / `ext-wiki-config` / `ext-wiki-security`。要点：

- 官方部署约定：只挂载 `pref.toml`（ro）与 `stats/`，**不要挂载整个 /base**（会遮盖镜像内随版本更新的规则/模板）；本项目镜像内置默认 pref，部署者零挂载
- target 全矩阵（compatibility 页）：clash / clashr / surge / quan / quanx / loon / surfboard / stash / mellow / singbox / ss / ssd / ssr / sssub / v2ray / v2rayn / v2rayng / shadowrocket / trojan / vless / hysteria2 / mixed —— 客户端 chips 的候选集来源
- `target=auto` 三限制：反代改写 UA / 未知客户端 / 同名不同内核（与设计前提 4 一致）
- Surge/QX/Loon/Surfboard/Stash 走「客户端原生远程资源」（配置直接引用原订阅 URL，由客户端更新）；clash 系走 proxy-providers
