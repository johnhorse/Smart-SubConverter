# Smart SubConverter

自托管的「零选项」订阅转换服务：朋友打开页面 → 粘贴机场订阅链接 → 点一下 → 拿走能直接用的转换订阅。全程不需要理解 target、远程配置、编码这些术语。

单容器内含三进程（s6 托管）：转换内核 [SubConverter-Extended](https://github.com/Aethersailor/SubConverter-Extended)、Go sidecar（预校验 / 短链生成与存储 / 订阅代理 / 健康自采）、nginx 反代 + [Vue 3](https://vuejs.org/) 单页前端。v1.2.0 起页面生成的转换链接是短链（`/s/xxxxxxxx`）——朋友的订阅地址存在服务端 `/base/data` 卷里，链接本身不再内嵌凭据。

**与同类工具的定位差别**：subweb 等经典前端把转换器的十几个参数字段原样端给用户，对小白不友好；Sub-Store 是玩机党向的「订阅管理器」，文件/脚本/覆写一堆概念。本项目只做一件事——把上面那行「粘贴、点一下、拿走」做到不需要解释。代价是功能面窄：没有节点管理、没有多用户、没有托管库。

V1 的官方部署路径是**局域网**（见下节）；公网 HTTPS 是非官方附录（[附录 A](#附录-a公网部署非官方)）。

## 快速开始：局域网部署（V1 官方路径）

### 前置条件

- Docker（amd64 / arm64 双架构镜像均受支持，arm64 已实证）
- **容器需能直连订阅源**（机场订阅 URL）：转换是服务端抓取，朋友粘贴什么订阅 URL，容器就要能访问到哪。浏览器里能打开 ≠ 容器能抓到；网络受限环境请先确认容器出网正常。规则模板不在此列——v1.3.0 起走镜像内置快照（上游拉不动只是刷新延后，不影响转换）
- 宿主机能被局域网内朋友的设备访问到（同一个 Wi-Fi / 内网）

### 构建并运行

```bash
git clone https://github.com/johnhorse/Smart-SubConverter.git && cd Smart-SubConverter
make build-image        # 构建本地镜像 smart-subconverter:dev

docker run -d --name smart-subconverter \
  -p 8080:80 \
  -v smart-sub-data:/base/data \
  -e PUBLIC_BASE_URL=http://192.168.1.100:8080 \
  smart-subconverter:dev
```

把 `192.168.1.100` 换成**部署机的局域网 IP**（`ip addr` / `ipconfig getifaddr en0` 查看），`8080` 换成你想用的宿主机端口。然后浏览器打开 `http://192.168.1.100:8080`，把地址发给朋友即可。

`-v smart-sub-data:/base/data` 别省：短链数据库（`/base/data/state.db`）存在这个卷里，已发给朋友的短链全靠它认账。不挂卷时数据只活在容器层，`docker rm` 后旧短链全部失效（朋友表现为「更新订阅」失败）；挂了命名卷，升级镜像、重建容器旧短链照常可用。

**从 v1.2.x 升级到 v1.3.0**：挂载点从 `/data` 改为 `/base/data`——`docker run` 里改这一个词即可，卷名与卷内数据不变（`state.db` 原样认账，v1.3.0 起规则快照与它同居一卷）。若只换镜像没改挂载点，sidecar 会在新默认路径找不到旧数据库、表现为短链全部失效——数据没丢，把挂载点改对重建容器即恢复。

镜像已发布在 GHCR（amd64 / arm64，CI 冒烟门禁通过后推送）：把上面 `docker run` 的镜像名换成 `ghcr.io/johnhorse/smart-subconverter:latest` 即可跳过本地构建；需固定版本时用不可变版本 tag（如 `ghcr.io/johnhorse/smart-subconverter:v1.0.0`）。

### PUBLIC_BASE_URL 为什么必设

这个环境变量决定「朋友拿到的转换链接以什么地址开头」，一处配置三处生效：

1. **页面生成的短链**：sidecar 用它拼接短链（`/s/…`）的 origin。不设则按入站请求头推导，容易拼出朋友设备打不开的地址
2. **客户端自动更新地址**：镜像把它同步导出为转换内核的 `MANAGED_CONFIG_PREFIX`——Surge 等客户端导入订阅后，会按这个地址自动更新。实测不设时该地址回指 `127.0.0.1:25500`（朋友自己的设备），自动更新链路直接断裂
3. **规则集引用地址**（v1.3.1 修复确认）：Quantumult X / Surge 类客户端的配置里，规则列表引用的是服务的 `getruleset` 端点——不设时同样回指 `127.0.0.1:25500`，**Quantumult X 导入直接报 Response invalid**（严格校验），Clash 系则表现为规则悄悄加载失败

忘设的症状：链接在页面上能复制，但朋友设备上打不开；或客户端「更新订阅」一直失败；或 QX 导入报 Response invalid。**局域网部署建议总是显式设置。**

### docker-compose 等价示例

```yaml
services:
  smart-subconverter:
    build: .                     # 或 image: ghcr.io/johnhorse/smart-subconverter:latest
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

### 案例：Linux 宿主机有代理，让规则快照刷新走代理（docker compose）

场景：部署机在国内，宿主机上已跑着 Clash/mihomo 类代理（HTTP 端口 `7890`）。`SNAPSHOT_PROXY` 让 sidecar 拉取 GitHub 上的 ACL4SSR 规则与模板时走这个代理；**朋友的订阅拉取不走代理**（始终直连，见 env 表）。

```yaml
services:
  smart-subconverter:
    image: ghcr.io/johnhorse/smart-subconverter:latest
    container_name: smart-subconverter
    ports:
      - "8080:80"
    volumes:
      - smart-sub-data:/base/data
    environment:
      - PUBLIC_BASE_URL=http://192.168.1.100:8080
      - SNAPSHOT_PROXY=http://host.docker.internal:7890
    extra_hosts:
      - "host.docker.internal:host-gateway"   # Linux 必加：Docker 原生不认识这个域名，此行把它解析到宿主网关
    restart: unless-stopped

volumes:
  smart-sub-data:
```

两个常见坑：

- **代理要监听容器够得着的地址**：Clash/mihomo 默认只监听 `127.0.0.1`，容器从网关 IP 连过去是 `connection refused`（日志表现为规则快照一直刷新失败）。把代理的监听地址改为 `0.0.0.0`，或至少包含 Docker 网桥地址（通常 `172.17.0.1`）
- **`0.0.0.0` 监听会把代理暴露到局域网**：确认代理配置里 `allow-lan` / 绑定地址的取值你是有意为之，或用防火墙把 7890 端口限制为仅 Docker 网段访问

> macOS / Windows（Docker Desktop）不需要 `extra_hosts` 那行——`host.docker.internal` 原生可用。`docker run` 等价写法：`--add-host=host.docker.internal:host-gateway -e SNAPSHOT_PROXY=http://host.docker.internal:7890`。

### 端口绑定范围警告

上面的 `-p 8080:80` 会把服务绑到**所有网卡**。对纯局域网机器这没问题，但**如果部署机有公网 IP，照抄这条命令等于把整个服务裸奔到公网**——任何扫到端口的人都能用你的转换服务，还能查看短链列表、删码（见[使用前须知](#四条使用前须知)与[管理端点信任边界](#短链数据与备份base-data-卷)）。公网 IP 主机请任选其一：

- 绑定到内网网卡：`-p 192.168.1.100:8080:80`（只写内网 IP）
- 或直接走[附录 A 公网部署](#附录-a公网部署非官方)：Caddy 反代 + 强制 token

## 环境变量

| 变量 | 默认值 | 用途 |
|---|---|---|
| `PUBLIC_BASE_URL` | 无 | 转换链接 origin 覆盖 + 客户端自动更新地址 + 规则集引用地址（均转 `MANAGED_CONFIG_PREFIX`）。**局域网部署必设**，见上文 |
| `SNAPSHOT_PROXY` | 空（直连） | 规则/模板上游（GitHub）访问代理，`http://` 或 `socks5://`：仅作用于快照每日刷新，**朋友的订阅拉取不走它**（始终直连）。国内服务器上 GitHub 直连慢/超时时配置，如 `-e SNAPSHOT_PROXY=http://host.docker.internal:7890`（代理跑在宿主机时） |
| `SUB_TOKEN` | 空 | 非空时：① sidecar 容器内调用 `/sub` 自动带 token（朋友无感知）② 管理端点 `GET /api/links` / `DELETE /api/links/{code}` 强制 `?token=` 鉴权（缺失/不符 401）。v1.2.0 起短链**不携带** token；公网档必设（附录 A） |
| `PRESET_URL` | ACL4SSR_Online.ini | 仅覆盖 default 档规则模板，**覆盖即豁免快照本地化**——该档原样透传此 URL 给转换内核（网络自担）。主要为冒烟测试用，日常部署无需设置 |
| `DATA_DIR` | `/base/data` | 短链 SQLite（`state.db`）与运行时规则快照（`rules/`）所在目录，即 volume 挂载点。**须位于容器内 `/base` 树内**（引擎本地读取硬限），否则规则快照本地化整体禁用（启动告警，不拒启动） |
| `PRESET_REFRESH_INTERVAL` | `24h` | 规则快照刷新周期（启动先刷一轮；徽章 fresh 阈值 = 2× 此值，默认 48h） |
| `PROBE_TIMEOUT` | `30s` | preview 预检（sidecar → 转换内核）超时 |
| `SUB_PROXY_TIMEOUT` | `120s` | 短链订阅代理（sidecar → 转换内核）超时；规则集已本地化，主要兜订阅源本身慢的情况，勿低于 10s |
| `POLL_INTERVAL` | `15m` | 健康轮询周期（仅豁免档对规则上游发 HEAD；本地化档由快照刷新驱动） |
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
- 转换结果页有一条小字提醒：**链接就是你的订阅钥匙，仅自用勿转发**（原因见下节）

## 四条使用前须知

### 1. 短链即订阅钥匙——转发等于交出订阅

v1.2.0 起页面生成的是短链（`/s/xxxxxxxx`）：朋友的原始订阅地址存在部署机的 `/base/data` 卷里，链接本身不再内嵌它。但别因此放松——**短码本身就是那把钥匙**：谁拿到这根短链，谁就能直接取这份订阅，转发到群里等于把机场账号散出去。泄露时也**不必**再靠机场重置（那会让所有人的链接一起换）：找部署者单条撤销即可，三步操作见下节「泄露应对」。存量 v1.0 / v1.1 长链接（`/sub?...`）仍以内嵌凭据的老方式照常可用，同样仅自用勿转发。

服务端日志卫生不变且加强：`/sub`、`/api/*`、`/s/*` 均关闭访问日志（短码与订阅 URL 同级对待），sidecar 对短链命中只记脱敏摘要（码前 3 位 + 状态 + 耗时）。

### 2. preview 是「转发型抓取」——你贴什么，服务器抓什么

页面上粘贴什么 URL，服务端（经转换内核）就会去抓取什么——这是转换的工作方式，无法绕开。服务端有基本预校验（只允许 http/https、拒绝内网地址字面量），但它不是完整的 SSRF 防护。因此**公网部署必须启用 token**（附录 A），让能触发抓取的人至少受访问控制约束。短链模式下还要注意**持久重放**：贴进去的 URL 会存进服务端，之后朋友每次「更新订阅」都会按它重新抓取；想让某条彻底停掉，用管理端点删码即可（见下节）。

### 3. 生命周期——链接何时失效

- **短链寿命 = `/base/data` 卷寿命 + 显式撤销**。短码映射存在 `/base/data/state.db`：卷在、码在。没有过期时间、不做自动清理——要作废某条，删码（管理端点）或销毁卷
- **机场侧重置订阅 → 短码不会自动消失**。码还在，但代理抓不到新订阅了，朋友侧表现为「更新订阅」一直失败。应对：删掉旧码，让朋友在页面用新订阅链接重新生成（同配置自动得到新码）
- **公网档轮换 `SUB_TOKEN` 不再波及短链**。v1.2.0 起朋友的短链不带 token：轮换只影响管理端点访问（部署者自己换个 URL 参数）和存量 `/sub` 长链接（v1.1 及之前分发的会同时失效）；已分发短链不受影响
- **泄露应对（首要）**——怀疑某条短链被转发/泄露时，部署者三步撤销：
  1. 找码：`curl "http://192.168.1.100:8080/api/links"`（局域网档直接可用；公网档加 `?token=<SUB_TOKEN>`），按 `sourceUrl` / 创建时间定位泄露的那条
  2. 删码：`curl -X DELETE "http://192.168.1.100:8080/api/links/{code}"`（公网档同样带 `?token=`）→ 返回 204，该短链立即失效
  3. 重建：让朋友在页面用同样配置再点一次「转换」——同参数自动生成新短码（这就是单条轮换），把新链接发给他
- **订阅本身泄露**（而非转换链接泄露）：去机场侧重置订阅链接——这仍是正确应对，随后按上一条把对应短码删掉重建即可

### 4. 规则模板是内置快照——出厂兜底、每日刷新、拉不动沿用旧的

三档规则模板来自 [ACL4SSR](https://github.com/ACL4SSR/ACL4SSR)（按 [CC-BY-SA-4.0](https://creativecommons.org/licenses/by-sa/4.0/deed.zh) 分发，在此署名致谢）：v1.3.0 起 .ini 与其引用的全部规则集文件在构建时随镜像出厂（`/base/rules-snapshot/`，拉取时间与逐文件校验和见镜像内 `SNAPSHOT_META`），运行时 sidecar 每日（`PRESET_REFRESH_INTERVAL`，默认 24h）从上游逐文件刷新，单个文件拉不动就沿用旧快照。朋友的「更新订阅」因此不再被 GitHub 网络状况卡住；上游真改了规则，最迟一两轮刷新后自动跟上。

## 短链数据与备份（`/base/data` 卷）

短链映射存在 `${DATA_DIR}/state.db`（默认 `/base/data`，SQLite，权限 0600）。**这个文件就是凭据库**——每条记录都含朋友的完整订阅地址，敏感性与密码同级。

- **挂载**：部署命令里的 `-v smart-sub-data:/base/data`。升级镜像、重建容器都不丢；反过来说，删卷/换卷名 = 所有已分发短链失效
- **备份**（建议定期执行）：

  ```bash
  docker run --rm -v smart-sub-data:/base/data -v "$PWD":/backup alpine \
    cp /base/data/state.db /backup/state.db.$(date +%Y%m%d)
  ```

  **备份文件含全部订阅凭据**——按密码的规格保管，别放进网盘公开目录或聊天记录。恢复＝停容器后把文件放回卷里
- **可用性**：短链模式下朋友每次「更新订阅」都经 sidecar 代理——sidecar 挂了更新会临时中断，s6 会自动拉起，无需人工干预；`/base/data` 卷写满或数据库损坏会导致短链功能不可用（页面报 `store_error` 或容器启动失败，错误信息会指引排查方向），数据库损坏时 sidecar **拒绝启动、绝不自动清空**——先备份 `state.db` 再排查
- **管理端点与信任边界（码即授权）**：`GET /api/links` 能看到所有短码与订阅地址，`DELETE /api/links/{code}` 能删码。局域网档（未配 `SUB_TOKEN`）这两个接口**开放**——任何能打开页面的人都能看列表、删码，信任边界就是你的局域网；公网档必须配 `SUB_TOKEN`（附录 A），接口强制 `?token=` 鉴权

## 附录 A：公网部署（非官方）

V1 架构与验收只覆盖局域网。以下路径**不在 V1 验收范围内**，供有公网 HTTPS 需求的部署者参考。

公网档的三层防护：

1. **token 校验加在反代层（Caddy），scope 收窄到 `/sub*`**。实测结论：SubConverter-Extended 无原生 /sub 访问控制（配置与安全文档均无鉴权项），校验只能做在这层。v1.2.0 起语义变化：`SUB_TOKEN` 不再注入朋友拿到的链接（短链不带 token——短码即逐链接凭据，泄露走单条撤销）；它的用途是 sidecar 容器内调 `/sub` 时自动带上（朋友无感知）+ 管理端点（`GET /api/links` / `DELETE`）的 `?token=` 鉴权。Caddy 校验相应只拦 `/sub*`（存量长链直通路由），放行 `/`（页面）、`/s/`（短链）、`/api/`（sidecar 自行鉴权）——原全局 matcher 会把不带 token 的短链与页面全部 403
2. **转换内核安全档位**：`SUBCONVERTER_SECURITY_PROFILE=public` 阻止内核抓取回环/私网地址字面量
3. **容器端口只绑回环**（`127.0.0.1:18080:80`），对外只暴露 Caddy 的 443

容器侧：

```bash
docker run -d --name smart-subconverter \
  -p 127.0.0.1:18080:80 \
  -v smart-sub-data:/base/data \
  -e PUBLIC_BASE_URL=https://sub.example.com \
  -e SUB_TOKEN=CHANGE_ME_TO_A_LONG_RANDOM_SECRET \
  -e SUBCONVERTER_SECURITY_PROFILE=public \
  ghcr.io/johnhorse/smart-subconverter:latest
```

Caddy 侧（`Caddyfile`，自动 HTTPS 由 Caddy ACME 处理）：

```caddyfile
sub.example.com {
	# token 校验只拦 /sub*（遗留长链直通路由，query 必须带 token）。
	# 显式放行：/（页面与静态资源）、/s/（短链——短码即逐链接凭据，本身不带
	# token）、/api/（管理端点由 sidecar 用 SUB_TOKEN 自行鉴权）。
	# 原全局 matcher 会把不带 token 的短链与页面全部 403，故收窄。
	@notoken {
		path /sub*
		not query token=CHANGE_ME_TO_A_LONG_RANDOM_SECRET
	}
	respond @notoken "Forbidden" 403

	reverse_proxy 127.0.0.1:18080
}
```

说明：

- 两处 `CHANGE_ME_TO_A_LONG_RANDOM_SECRET` 必须同值（生成建议：`openssl rand -hex 32`）
- token 防两处：盲猜 `/sub` 直通 URL（存量长链滥用）与管理端点越权；朋友侧全程无感知（短链不带 token，容器内自动注入；管理端点是部署者自己用的）
- 上述 Caddyfile 的 matcher 写法（named matcher 内 `path` 与 `not query` 按 AND 组合）是 Caddy 标准语义，与 v1.1 已验证版本同构；上线前请照旧用 `caddy:2-alpine` 的 `caddy adapt` 自行验证，并确认无 token 时 `/sub*` 403、`/` `/s/` `/api/` 放行

## 客户端兼容矩阵

按「客户端 × 协议 × 模板」实测标注，**未实测 = 未验证**——本表不承诺「同一链接总能返回正确格式」（UA 被改写、未知客户端、同名不同内核是已知限制）。真机实测随时间回填。

| 客户端 | target | 状态 | 深链 | 实测记录 |
|---|---|---|---|---|
| 自动识别（按客户端 UA） | auto | 未验证 | — | — |
| Clash / Mihomo | clash | 未验证 | — | — |
| Surge | surge | 未验证 | 未验证 | — |
| Quantumult X | quanx | 未验证 | 未验证 | — |
| Loon | loon | 未验证 | 未验证 | — |
| Stash | stash | 未验证 | 未验证 | — |
| Shadowrocket | shadowrocket | 未验证 | 未验证 | — |
| sing-box | singbox | 未验证 | — | — |
| v2rayN | v2rayn | 未验证 | — | — |

「深链」列 = 结果页「在 XX 中打开」按钮（唤起对应 App 导入），仅 iOS 五家提供，拼装口径见 `docs/implementation/api-contract.md`；真机实测后回填。

回填格式示例：`| Clash Verge | clash | 已验证 | — | 2026-10-24 · Clash Verge v1.7.7 · ACL4SSR_Online.ini · amd64 |`

## 规则新鲜度徽章

页脚徽章反映规则模板的可观测状态，v1.3.0 起分两种语义：

**快照档（默认三档）**——由快照刷新循环驱动：

| 状态 | 含义 |
|---|---|
| fresh | 快照最近一次刷新距今 < 2× `PRESET_REFRESH_INTERVAL`（默认 48h） |
| stale | 超过上述阈值（上游连续拉不动时到达） |
| unknown | 从未刷新成功且无出厂时间戳 |

上游拉不动 ≠ 订阅失败：sidecar 沿用旧快照（含出厂兜底），徽章转 stale 只是提醒部署者「快照有点旧了」。

**豁免档（`PRESET_URL` 覆盖的 default 档）**——沿用远端 HEAD 探测（每 `POLL_INTERVAL` 一次），三态同 v1.0。GitHub raw 的常态仍是 unknown（实测有 ETag、无 Last-Modified 头），诚实显示不是故障；最近一次抓取失败转 stale——该档上游拉不到时朋友的「更新订阅」确实会受影响（覆盖即自担网络）。

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
3. 端到端（短链）：`/api/preview` 取返回短链（断言 `/s/{8 字符码}`）→ 双 UA 经 nginx 入口拉取，断言格式 + 同参数二次 preview link 相同（幂等）+ `docker restart` 后同一短链仍可用（持久化）+ 管理端点（列表含码 → 删除 204 幂等 → 删后 404）——证明「生成的链接可用」而不只是「/sub 能转」

全流程不依赖外网（fixture 由本地静态服务提供，节点为 RFC 5737 测试地址）。

**发布门禁语义**：冒烟不过不发布。CI 中 push tag `v*` 触发构建 → 三步冒烟 → 全绿后才推送镜像（GHCR 主源，Docker Hub 第二源；`linux/amd64` + `linux/arm64`）。tag 策略：`vX.Y.Z` 版本 tag 不可变，`latest` 只在冒烟通过后移动。base 镜像以 digest pin 在 `image/base.lock`（构建不可变）；`upstream-watch` workflow 每日比对上游 digest 与 ACL4SSR 规则 HEAD，漂移则自动更新（`base.lock` / `image/rules-snapshot/`）并开 PR。

## 致谢与许可

- [Aethersailor/SubConverter-Extended](https://github.com/Aethersailor/SubConverter-Extended) —— 转换内核（GPL-3.0 血统）。本项目**不 fork**：仓库零 C++ 代码，直接使用上游官方镜像并以 digest pin 跟随其 release
- [ACL4SSR](https://github.com/ACL4SSR/ACL4SSR) —— 规则模板，v1.3.0 起随镜像分发出厂快照（CC-BY-SA-4.0，署名见[使用前须知](#四条使用前须知)第 4 条）
- [s6-overlay](https://github.com/just-containers/s6-overlay)、[nginx](https://nginx.org/)、[Vue 3](https://vuejs.org/)

本项目许可证延用上游：**GPL-3.0**（全文见 [LICENSE](LICENSE)）；镜像包含 GPL-3.0 的 SubConverter-Extended，分发的镜像整体按 GPL-3.0 提供。
