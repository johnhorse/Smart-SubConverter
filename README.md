# Smart SubConverter

[![CI](https://github.com/johnhorse/Smart-SubConverter/actions/workflows/build.yml/badge.svg)](https://github.com/johnhorse/Smart-SubConverter/actions/workflows/build.yml)
[![Docker Pulls](https://img.shields.io/docker/pulls/johnhorse/smart-subconverter)](https://hub.docker.com/r/johnhorse/smart-subconverter)
[![License: GPL-3.0](https://img.shields.io/badge/license-GPL--3.0-blue)](LICENSE)

自托管的「零选项」订阅转换服务：朋友打开页面 → 粘贴机场订阅链接 → 点一下 → 拿走能直接用的转换订阅。全程不需要理解 target、远程配置、编码这些术语。

**与同类工具的定位差别**：subweb 等经典前端把转换器的十几个参数字段原样端给用户，对小白不友好；Sub-Store 是玩机党向的「订阅管理器」，文件/脚本/覆写一堆概念。本项目只做一件事——把「粘贴、点一下、拿走」做到不需要解释。代价是功能面窄：没有节点管理、没有多用户、没有托管库。

单容器内含三进程（s6 托管）：转换内核 [SubConverter-Extended](https://github.com/Aethersailor/SubConverter-Extended)、Go sidecar（预校验 / 短链生成与存储 / 订阅代理 / 健康自采）、nginx 反代 + [Vue 3](https://vuejs.org/) 单页前端。V1 的官方部署路径是**局域网**（见下节）；公网 HTTPS 见[公网部署](docs/deployment-public.md)。

## ✨ 功能特点

- **零门槛**：粘贴订阅链接 → 点「转换」→ 复制或扫码，朋友不需要懂任何术语
- **自动识别客户端**：按客户端 UA 自动返回对应格式（Clash / Mihomo、Surge、Quantumult X、Loon、Stash、Shadowrocket、sing-box、v2rayN），也可手动指定
- **短链不内嵌凭据**：页面生成 `/s/xxxxxxxx` 短链，订阅地址存在服务端卷里；泄露可单条撤销，不必让所有人重置订阅
- **规则快照内置**：ACL4SSR 模板与规则集随镜像出厂、每日自动刷新、上游拉不动沿用旧快照——「更新订阅」不被 GitHub 网络状况卡住
- **订阅代拉**：服务端代朋友拉订阅，支持走代理（`SUBSCRIPTION_PROXY`）与 curl 降级重试，机场 WAF 拦直连场景有解
- **clash / Quantumult X 两段式内联**：服务端代拉机场并转通用节点列表再转换，输出内联节点——客户端网络够不到机场（机场按出口 IP/ASN 封禁）也能直接用，不再把订阅 URL 塞回客户端自拉
- **单容器即起**：`docker run` 一条命令；amd64 / arm64 双架构镜像
- **数据一个卷**：短链数据库与规则快照都在 `/base/data`，升级、重建容器不丢已发链接
- **可观测**：健康自采 + 页脚规则新鲜度徽章（fresh / stale / unknown）

## 🚀 快速开始（局域网）

### 前置条件

- Docker（amd64 / arm64 双架构镜像均受支持，arm64 已实证）
- **容器需能直连订阅源**（机场订阅 URL）：转换是服务端抓取。浏览器里能打开 ≠ 容器能抓到；网络受限环境请先确认容器出网正常（规则模板不在此列——走镜像内置快照）
- 宿主机能被局域网内朋友的设备访问到（同一个 Wi-Fi / 内网）

### 构建并运行

```bash
docker run -d --name smart-subconverter \
  -p 8080:80 \
  -v smart-sub-data:/base/data \
  -e PUBLIC_BASE_URL=http://192.168.1.100:8080 \
  johnhorse/smart-subconverter:latest
```

把 `192.168.1.100` 换成**部署机的局域网 IP**（`ip addr` / `ipconfig getifaddr en0` 查看），`8080` 换成想用的宿主机端口。浏览器打开 `http://192.168.1.100:8080`，把地址发给朋友即可。镜像同步发布在 GHCR（`ghcr.io/johnhorse/smart-subconverter`，能直连时可用）；需固定版本时用不可变版本 tag（如 `:v1.3.8`）；想本地构建则 `git clone` 后 `make build-image`，镜像名换 `smart-subconverter:dev`。

三个部署细节：

- **`-v smart-sub-data:/base/data` 别省**：已发出去的短链全靠这个卷认账，删卷 = 全部失效；备份方法见[安全须知](docs/security.md#短链数据与备份)
- **`PUBLIC_BASE_URL` 必设**：它决定短链 origin、客户端自动更新地址、规则集引用地址；忘设的症状是朋友设备打不开链接 / 更新订阅失败 / QX 导入报 Response invalid，原理详见[局域网部署详解](docs/deployment-lan.md)
- **部署机有公网 IP 时**：`-p 8080:80` 会绑到所有网卡，等于裸奔公网——绑定内网网卡（`-p 192.168.1.100:8080:80`）或走[公网部署](docs/deployment-public.md)

docker-compose 等价示例：

```yaml
services:
  smart-subconverter:
    image: johnhorse/smart-subconverter:latest   # 或 build: . 本地构建
    container_name: smart-subconverter
    ports:
      - "8080:80"
    volumes:
      - smart-sub-data:/base/data
    environment:
      - PUBLIC_BASE_URL=http://192.168.1.100:8080
    restart: unless-stopped

volumes:
  smart-sub-data:
```

进阶场景（[局域网部署详解](docs/deployment-lan.md)）：国内宿主机跑着 Clash/mihomo，想让规则刷新与订阅代拉走代理（`SNAPSHOT_PROXY` / `SUBSCRIPTION_PROXY`）；从 v1.2.x 升级的挂载点变更说明。

## 📖 使用说明（给朋友看的）

不需要懂任何术语，四步：

1. 打开发给你的网址
2. 把你的**订阅链接**（机场给你的那串 `https://...`）整个粘贴进输入框
3. 点「转换」，稍等几秒
4. 出现结果后：点「复制链接」，或者用手机扫码；然后把链接添加进你的 App 当订阅地址

几个可能遇到的点：

- **「自动识别」是什么**：默认就是它。你的 App 来取订阅时会自动认出它、给它要的格式。如果转换出错、或导入后格式不对，展开「指定客户端」，选中你正在用的 App，再点一次转换
- **链接是活的**：以后机场更新了节点、规则模板更新了内容，你在 App 里点一下「更新订阅」就会拿到最新的，不用重新转换
- **Quantumult X 用户注意**：拿到的链接要添加在「配置文件 → 导入」里，别填进「订阅」——「订阅」栏只吃节点链接，转换产物是整份配置文件
- 转换结果页有一条小字提醒：**链接就是你的订阅钥匙，仅自用勿转发**（原因见[安全须知](docs/security.md#1-短链即订阅钥匙转发等于交出订阅)）

## ⚙️ 环境变量

| 变量 | 默认值 | 用途 |
|---|---|---|
| `PUBLIC_BASE_URL` | 无 | 转换链接 origin 覆盖 + 客户端自动更新地址 + 规则集引用地址。**局域网部署必设** |
| `SNAPSHOT_PROXY` | 空（直连） | 规则/模板上游（GitHub）访问代理（`http://` 或 `socks5://`），仅作用于快照每日刷新 |
| `SUBSCRIPTION_PROXY` | 空（直连） | sidecar 代拉**订阅**时的代理（Go+curl 两路）。机场 WAF 拦服务器直连时配置（症状：日志 `twostage=1/2 upstream=400`） |
| `SUB_TOKEN` | 空 | 非空时：sidecar 调 `/sub` 自动带 token（朋友无感知）+ 管理端点（列表 / 删码）强制 `?token=` 鉴权；公网档必设 |
| `PRESET_URL` | ACL4SSR_Online.ini 的上游 URL | 覆盖 default 档规则模板，覆盖即豁免快照本地化（网络自担）；主要为冒烟测试用 |
| `DATA_DIR` | `/base/data` | 短链 SQLite 与规则快照目录，即 volume 挂载点；**须位于容器内 `/base` 树内** |
| `PRESET_REFRESH_INTERVAL` | `24h` | 规则快照刷新周期（徽章 fresh 阈值 = 2× 此值） |
| `PROBE_TIMEOUT` | `30s` | preview 预检（sidecar → 内核）超时 |
| `SUB_PROXY_TIMEOUT` | `120s` | 短链订阅代理超时；勿低于 10s |
| `POLL_INTERVAL` | `15m` | 健康轮询周期（仅豁免档对上游发 HEAD） |
| `SIDECAR_PORT` | `8081` | sidecar 容器内监听端口（勿用 `PORT`，已被内核占用） |
| `EXTENDED_BASE` | `http://127.0.0.1:25500` | 转换内核容器内地址 |

另外 `SUBCONVERTER_SECURITY_PROFILE`（默认 `lan`）透传给转换内核：`public` 档会阻止内核抓取回环/私网地址，公网部署的纵深防护。

## 🔒 安全须知

- **短链即订阅钥匙**：谁拿到短链，谁就能取这份订阅——转发到群里等于把机场账号散出去。泄露时不必重置机场订阅：部署者可[单条撤销](docs/security.md#3-生命周期链接何时失效)（找码 → 删码 → 朋友重新生成）
- **preview 是「转发型抓取」**：你贴什么 URL，服务端就抓什么。有基本预校验（仅 http/https、拒内网地址字面量）但不是完整 SSRF 防护——公网部署必须配 token
- **管理端点即授权边界**：局域网档（未配 `SUB_TOKEN`）下列表 / 删码接口对局域网开放，信任边界就是你的局域网
- **`state.db` 就是凭据库**：每条记录含朋友的完整订阅地址，敏感性与密码同级——定期备份，备份文件按密码规格保管

细则全文（生命周期、泄露应对三步、日志卫生、备份命令、规则快照机制）：[docs/security.md](docs/security.md)

## 🌐 公网部署（非官方）

V1 架构与验收只覆盖局域网，公网 HTTPS 是非官方路径：Caddy 反代自动 HTTPS + token 校验（scope 收窄到 `/sub*`）+ 转换内核 `public` 安全档 + 容器端口只绑回环，共三层防护。完整步骤见[docs/deployment-public.md](docs/deployment-public.md)。

## 🧩 客户端支持

Clash / Mihomo · Surge · Quantumult X · Loon · Stash · Shadowrocket · sing-box · v2rayN（按 UA 自动识别，也可手动指定）。Clash Verge 与 Quantumult X 已真机验证通过（QX 注意用「配置文件 → 导入」，见上方使用说明）；实测矩阵随真机回填：[docs/compatibility.md](docs/compatibility.md)

## 🛠 开发

本地开发全走 docker（宿主机无需 Go / Node 环境）：

| 命令 | 作用 |
|---|---|
| `make build-sidecar` | sidecar：go vet + test + build（docker 化） |
| `make build-image` | 构建镜像 `smart-subconverter:dev`（含前端真实构建） |
| `make run-local` | `127.0.0.1:18080` 起容器 |
| `make smoke-local` | 三步冒烟（发布门禁，与 CI 共用脚本） |
| `make stop-local` | 停本地容器 |

前端（Vue 3 + Vite + vitest）：`cd frontend && npm ci && npm test`。三步冒烟（基础可用 → fixture 转换 → 端到端短链）不依赖外网；CI 中 push tag `v*` 触发构建 → 冒烟全绿后才推送镜像（Docker Hub 与 GHCR 双源，amd64 / arm64），base 镜像 digest pin 在 `image/base.lock`，`upstream-watch` workflow 每日自动跟进上游与规则漂移。

## 📄 许可与致谢

- [Aethersailor/SubConverter-Extended](https://github.com/Aethersailor/SubConverter-Extended) —— 转换内核（GPL-3.0 血统）。本项目不 fork，直接使用上游官方镜像并以 digest pin 跟随其 release
- [ACL4SSR](https://github.com/ACL4SSR/ACL4SSR) —— 规则模板，v1.3.0 起随镜像分发出厂快照（CC-BY-SA-4.0）
- [s6-overlay](https://github.com/just-containers/s6-overlay)、[nginx](https://nginx.org/)、[Vue 3](https://vuejs.org/)

本项目许可证延用上游：**GPL-3.0**（全文见 [LICENSE](LICENSE)）；镜像包含 GPL-3.0 的 SubConverter-Extended，分发的镜像整体按 GPL-3.0 提供。
