# Smart SubConverter

自托管的「零选项」订阅转换服务：朋友打开页面 → 粘贴机场订阅链接 → 点一下 → 拿走能直接用的转换订阅。全程不需要理解 target、远程配置、编码这些术语。

单容器内含三进程（s6 托管）：转换内核 [SubConverter-Extended](https://github.com/Aethersailor/SubConverter-Extended)、Go sidecar（预校验 / 链接拼接 / 健康自采）、nginx 反代 + [Vue 3](https://vuejs.org/) 单页前端。

**与同类工具的定位差别**：subweb 等经典前端把转换器的十几个参数字段原样端给用户，对小白不友好；Sub-Store 是玩机党向的「订阅管理器」，文件/脚本/覆写一堆概念。本项目只做一件事——把上面那行「粘贴、点一下、拿走」做到不需要解释。代价是功能面窄：没有节点管理、没有多用户、没有托管库。

V1 的官方部署路径是**局域网**（见下节）；公网 HTTPS 是非官方附录（[附录 A](#附录-a公网部署非官方)）。

## 快速开始：局域网部署（V1 官方路径）

### 前置条件

- Docker（构建 amd64 / arm64 双架构镜像均受支持，arm64 已实证）
- **容器需能直连订阅源与规则上游**（如 `raw.githubusercontent.com`）。转换是服务端抓取：朋友粘贴什么订阅 URL、规则模板从哪拉，容器就要能访问到哪。浏览器里能打开 ≠ 容器能抓到；网络受限环境请先确认容器出网正常
- 宿主机能被局域网内朋友的设备访问到（同一个 Wi-Fi / 内网）

### 构建并运行

```bash
git clone <本仓库地址> && cd Smart-SubConverter
make build-image        # 构建本地镜像 smart-subconverter:dev

docker run -d --name smart-subconverter \
  -p 8080:80 \
  -e PUBLIC_BASE_URL=http://192.168.1.100:8080 \
  smart-subconverter:dev
```

把 `192.168.1.100` 换成**部署机的局域网 IP**（`ip addr` / `ipconfig getifaddr en0` 查看），`8080` 换成你想用的宿主机端口。然后浏览器打开 `http://192.168.1.100:8080`，把地址发给朋友即可。

镜像发布后（push tag `v*` 触发 CI 冒烟门禁，通过后推送 GHCR / Docker Hub，tag 形如 `ghcr.io/<owner>/smart-subconverter:latest`）可直接 `docker run` 对应镜像名，跳过本地构建。

### PUBLIC_BASE_URL 为什么必设

这个环境变量决定「朋友拿到的转换链接以什么地址开头」，一处配置两处生效：

1. **页面生成的 /sub 链接**：sidecar 用它拼接链接 origin。不设则按入站请求头推导，容易拼出朋友设备打不开的地址
2. **客户端自动更新地址**：镜像把它同步导出为转换内核的 `MANAGED_CONFIG_PREFIX`——Surge 等客户端导入订阅后，会按这个地址自动更新。实测不设时该地址回指 `127.0.0.1:25500`（朋友自己的设备），自动更新链路直接断裂

忘设的症状：链接在页面上能复制，但朋友设备上打不开，或客户端「更新订阅」一直失败。**局域网部署建议总是显式设置。**

### docker-compose 等价示例

```yaml
services:
  smart-subconverter:
    build: .                     # 或 image: ghcr.io/<owner>/smart-subconverter:latest
    container_name: smart-subconverter
    ports:
      - "8080:80"
    environment:
      - PUBLIC_BASE_URL=http://192.168.1.100:8080
    restart: unless-stopped
```

### 端口绑定范围警告

上面的 `-p 8080:80` 会把服务绑到**所有网卡**。对纯局域网机器这没问题，但**如果部署机有公网 IP，照抄这条命令等于把 /sub 裸奔到公网**——任何扫到端口的人都能用你的转换服务（详见[链接属性](#链接的三个属性使用前必读)）。公网 IP 主机请任选其一：

- 绑定到内网网卡：`-p 192.168.1.100:8080:80`（只写内网 IP）
- 或直接走[附录 A 公网部署](#附录-a公网部署非官方)：Caddy 反代 + 强制 token

## 环境变量

| 变量 | 默认值 | 用途 |
|---|---|---|
| `PUBLIC_BASE_URL` | 无 | 转换链接 origin 覆盖 + 客户端自动更新地址（转 `MANAGED_CONFIG_PREFIX`）。**局域网部署必设**，见上文 |
| `SUB_TOKEN` | 空 | 非空时注入页面生成的每条 /sub 链接（`&token=...`），配合反代层校验用。公网档必设（附录 A） |
| `PRESET_URL` | ACL4SSR_Online.ini | 规则模板预设覆盖，主要为冒烟测试用；日常部署无需设置 |
| `POLL_INTERVAL` | `15m` | 健康自采轮询周期（对规则模板上游发 HEAD 的间隔） |
| `SIDECAR_PORT` | `8081` | sidecar 容器内监听端口（勿用 `PORT`，该名已被转换内核占用） |
| `EXTENDED_BASE` | `http://127.0.0.1:25500` | 转换内核容器内地址 |

另外 `SUBCONVERTER_SECURITY_PROFILE`（默认 `lan`）透传给转换内核：`public` 档会阻止内核抓取回环/私网地址，作为公网部署的纵深防护（附录 A 使用）。

## 使用说明（给朋友看的）

不需要懂任何术语，四步：

1. 打开发给你的网址
2. 把你的**订阅链接**（机场给你的那串 `https://...`）整个粘贴进输入框
3. 点「转换」，稍等几秒
4. 出现结果后：点「复制链接」，或者用手机扫码；然后把链接添加进你的 App 当订阅地址

几个可能遇到的点：

- **「自动识别」是什么**：默认就是它。你的 App 来取订阅时会自动认出它、给它要的格式。如果转换出错、或导入后格式不对，展开「指定客户端」，选中你正在用的 App，再点一次转换
- **链接是活的**：这不是一次性文件。以后机场更新了节点、规则模板更新了内容，你在 App 里点一下「更新订阅」就会拿到最新的，不用重新转换
- 转换结果页有一条小字提醒：**链接里带着你的订阅信息，仅自用勿转发**（原因见下节）

## 链接的三个属性（使用前必读）

### 1. 凭据内嵌——链接即凭据，仅自用勿转发

转换链接以 URL 编码内嵌着朋友的原始订阅地址。谁拿到这根链接，谁就能直接用这份订阅——发群里等于把机场账号散出去。这是 subconverter 生态的普遍属性（subweb 亦然）。本项目的应对：页面与本文档明示 + 服务端日志卫生（对 `/sub`、`/api/*` 关闭含订阅参数的访问日志，sidecar 不记录订阅 URL）。服务端短链（不内嵌凭据）在 V2 候选中。

### 2. preview 是「转发型抓取」——你贴什么，服务器抓什么

页面上粘贴什么 URL，服务端（经转换内核）就会去抓取什么——这是转换的工作方式，无法绕开。服务端有基本预校验（只允许 http/https、拒绝内网地址字面量），但它不是完整的 SSRF 防护。因此**公网部署必须启用 token**（附录 A），让能触发抓取的人至少受访问控制约束。

### 3. 生命周期——链接何时失效

- **链接寿命 = 源订阅寿命**。机场侧重置订阅链接（常见于手动「重置订阅」操作），基于旧链接的所有转换链接同时失效
- **公网档轮换 `SUB_TOKEN` = 所有已分发的链接同时失效**。这是部署级单值 token 的爆破半径：轮换前请提前告知朋友，或选择低峰时段操作
- **泄露应对**：怀疑链接被转发/泄露时，去机场侧「重置订阅链接」——旧转换链接立即作废，用新订阅链接重新转换即可，无需动服务器

## 附录 A：公网部署（非官方）

V1 架构与验收只覆盖局域网。以下路径**不在 V1 验收范围内**，供有公网 HTTPS 需求的部署者参考。

公网档的三层防护：

1. **token 强制校验加在反代层（Caddy）**。实测结论：SubConverter-Extended 无原生 /sub 访问控制（配置与安全文档均无鉴权项），校验只能做在这层。`SUB_TOKEN` 设为非空后，sidecar 会把 `&token=<值>` 注入页面生成的每条链接——朋友无感知、无需手动输入
2. **转换内核安全档位**：`SUBCONVERTER_SECURITY_PROFILE=public` 阻止内核抓取回环/私网地址字面量
3. **容器端口只绑回环**（`127.0.0.1:18080:80`），对外只暴露 Caddy 的 443

容器侧：

```bash
docker run -d --name smart-subconverter \
  -p 127.0.0.1:18080:80 \
  -e PUBLIC_BASE_URL=https://sub.example.com \
  -e SUB_TOKEN=CHANGE_ME_TO_A_LONG_RANDOM_SECRET \
  -e SUBCONVERTER_SECURITY_PROFILE=public \
  smart-subconverter:dev
```

Caddy 侧（`Caddyfile`，自动 HTTPS 由 Caddy ACME 处理）：

```caddyfile
sub.example.com {
	# token 校验：query 里 token 参数不等于设定值 → 403
	@notoken not query token=CHANGE_ME_TO_A_LONG_RANDOM_SECRET
	respond @notoken "Forbidden" 403

	reverse_proxy 127.0.0.1:18080
}
```

说明：

- 两处 `CHANGE_ME_TO_A_LONG_RANDOM_SECRET` 必须同值（生成建议：`openssl rand -hex 32`）
- token 防的是盲猜 /sub URL 与链接长期滥用，**不是页面级鉴权**：能打开页面的人就能取得带 token 的链接，这是预期设计。页面本身不提供 token 的查看/输入入口
- 上述 Caddyfile 已通过 `caddy:2-alpine` 的 `caddy adapt` 编译验证（语法与 matcher 语义正确），但未在真实域名 + TLS 环境运行验证；上线前请自行确认证书签发与 token 403 行为

## 客户端兼容矩阵

按「客户端 × 协议 × 模板」实测标注，**未实测 = 未验证**——本表不承诺「同一链接总能返回正确格式」（UA 被改写、未知客户端、同名不同内核是已知限制）。真机实测随时间回填。

| 客户端 | target | 状态 | 实测记录 |
|---|---|---|---|
| 自动识别（按客户端 UA） | auto | 未验证 | — |
| Clash / Mihomo | clash | 未验证 | — |
| Surge | surge | 未验证 | — |
| Quantumult X | quanx | 未验证 | — |
| Loon | loon | 未验证 | — |
| Stash | stash | 未验证 | — |
| Shadowrocket | shadowrocket | 未验证 | — |
| sing-box | singbox | 未验证 | — |
| v2rayN | v2rayn | 未验证 | — |

回填格式示例：`| Clash Verge | clash | 已验证 | 2026-10-24 · Clash Verge v1.7.7 · ACL4SSR_Online.ini · amd64 |`

## 规则新鲜度徽章

页脚徽章反映默认规则模板（ACL4SSR Online）上游的可观测状态，三态语义：

| 状态 | 含义 |
|---|---|
| fresh | 远端 `Last-Modified` 头存在且距今 < 24h |
| unknown | 从未成功抓取过，或远端不返回 `Last-Modified` 头 |
| stale | 最近一次抓取失败，或 `Last-Modified` 距今 ≥ 24h |

**GitHub raw 的常态就是 unknown**（实测有 ETag、无 Last-Modified 头）——这是诚实显示，不是故障；内容是否真的变更由 ETag 对比检测，只是不体现在徽章上。

**上游不可达时的行为**：本项目不镜像、不拷贝规则内容，也不做本地兜底缓存。规则模板上游拉不到时，朋友客户端这次「更新订阅」会失败或沿用其本地缓存（客户端自身行为），徽章转 stale。

## 开发

本地开发全走 docker（宿主机无需 Go / Node 环境）：

```bash
make build-sidecar   # sidecar: go vet + test + build（docker 化）
make build-image     # 构建镜像 smart-subconverter:dev（含前端真实构建）
make run-local       # 127.0.0.1:18080 起容器（仅验证「镜像能起」）
make smoke-local     # 三步冒烟（发布门禁）
make stop-local      # 停掉本地容器
```

前端（Vue 3 + Vite + vitest）：`cd frontend && npm ci && npm test`。

**三步冒烟**（`image/smoke.sh`，本地 `make smoke-local` 与 CI 共用）：

1. 基础可用：`/`、`/version`、`/api/health`（真实自采）、`/api/clients` 结构断言
2. fixture 转换：本地 fixture 经 `/sub` 以 Clash / Surge 双 UA 直转，断言输出格式
3. 端到端：`/api/preview` 取返回链接 → 双 UA 经 nginx 入口拉取该链接，断言格式——证明「生成的链接可用」而不只是「/sub 能转」

全流程不依赖外网（fixture 由本地静态服务提供，节点为 RFC 5737 测试地址）。

**发布门禁语义**：冒烟不过不发布。CI 中 push tag `v*` 触发构建 → 三步冒烟 → 全绿后才推送镜像（GHCR 主源，Docker Hub 第二源；`linux/amd64` + `linux/arm64`）。tag 策略：`vX.Y.Z` 版本 tag 不可变，`latest` 只在冒烟通过后移动。base 镜像以 digest pin 在 `image/base.lock`（构建不可变）；`upstream-watch` workflow 每日比对上游 digest，漂移则自动更新 lock 并开 PR。

## 致谢与许可

- [Aethersailor/SubConverter-Extended](https://github.com/Aethersailor/SubConverter-Extended) —— 转换内核（GPL-3.0 血统）。本项目**不 fork**：仓库零 C++ 代码，直接使用上游官方镜像并以 digest pin 跟随其 release
- [ACL4SSR](https://github.com/ACL4SSR/ACL4SSR) —— 规则模板，仅作远程引用（不镜像、不拷贝）
- [s6-overlay](https://github.com/just-containers/s6-overlay)、[nginx](https://nginx.org/)、[Vue 3](https://vuejs.org/)

本仓库尚未选定许可证（上游 GPL-3.0 血统意味着选择需审慎，见仓库 issue/后续讨论）。
