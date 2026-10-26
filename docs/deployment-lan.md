# 局域网部署详解

> [README 快速开始](../README.md) 的展开版：`PUBLIC_BASE_URL` 原理、宿主机代理配置、端口绑定与版本升级注意事项。

## PUBLIC_BASE_URL 为什么必设

这个环境变量决定「朋友拿到的转换链接以什么地址开头」，一处配置三处生效：

1. **页面生成的短链**：sidecar 用它拼接短链（`/s/…`）的 origin。不设则按入站请求头推导，容易拼出朋友设备打不开的地址
2. **客户端自动更新地址**：镜像把它同步导出为转换内核的 `MANAGED_CONFIG_PREFIX`——Surge 等客户端导入订阅后，会按这个地址自动更新。实测不设时该地址回指 `127.0.0.1:25500`（朋友自己的设备），自动更新链路直接断裂
3. **规则集引用地址**（v1.3.1 修复确认）：Quantumult X / Surge 类客户端的配置里，规则列表引用的是服务的 `getruleset` 端点——不设时同样回指 `127.0.0.1:25500`，**Quantumult X 导入直接报 Response invalid**（严格校验），Clash 系则表现为规则悄悄加载失败

忘设的症状：链接在页面上能复制，但朋友设备上打不开；或客户端「更新订阅」一直失败；或 QX 导入报 Response invalid。**局域网部署建议总是显式设置。**

## 从 v1.2.x 升级到 v1.3.0（挂载点变更）

挂载点从 `/data` 改为 `/base/data`——`docker run` 里改这一个词即可，卷名与卷内数据不变（`state.db` 原样认账，v1.3.0 起规则快照与它同居一卷）。若只换镜像没改挂载点，sidecar 会在新默认路径找不到旧数据库、表现为短链全部失效——数据没丢，把挂载点改对重建容器即恢复。

## 案例：Linux 宿主机有代理，让规则刷新与订阅代拉走代理（docker compose）

场景：部署机在国内，宿主机上已跑着 Clash/mihomo 类代理（HTTP 端口 `7890`）。`SNAPSHOT_PROXY` 让 sidecar 拉取 GitHub 上的 ACL4SSR 规则与模板时走这个代理；`SUBSCRIPTION_PROXY` 让 sidecar 代拉机场订阅时也走它（机场 WAF 拦服务器直连的请求形态时需要；判据：机场对 curl 返回 200 但转换服务拉不动，日志见 `twostage=1/2 upstream=400`）。两者独立配置、可同可异，默认均直连（见 README env 表）。

```yaml
services:
  smart-subconverter:
    image: johnhorse/smart-subconverter:latest
    container_name: smart-subconverter
    ports:
      - "8080:80"
    volumes:
      - smart-sub-data:/base/data
    environment:
      - PUBLIC_BASE_URL=http://192.168.1.100:8080
      - SNAPSHOT_PROXY=http://host.docker.internal:7890       # 规则/模板刷新走代理
      - SUBSCRIPTION_PROXY=http://host.docker.internal:7890   # v1.3.8：代拉订阅走代理（机场 WAF 拦直连时）
    extra_hosts:
      - "host.docker.internal:host-gateway"   # Linux 必加：Docker 原生不认识这个域名，此行把它解析到宿主网关
    restart: unless-stopped

volumes:
  smart-sub-data:
```

两个常见坑：

- **代理要监听容器够得着的地址**：Clash/mihomo 默认只监听 `127.0.0.1`，容器从网关 IP 连过去是 `connection refused`（日志表现为规则快照一直刷新失败）。把代理的监听地址改为 `0.0.0.0`，或至少包含 Docker 网桥地址（通常 `172.17.0.1`）
- **`0.0.0.0` 监听会把代理暴露到局域网**：确认代理配置里 `allow-lan` / 绑定地址的取值你是有意为之，或用防火墙把 7890 端口限制为仅 Docker 网段访问

**ShellCrash / TUN 类宿主代理的三个坑（2026-10 实战，每个都踩过）**：

- **端口保护默认不认 docker 自定义网段**：ShellCrash（CrashCore/mihomo TUN 模式）的 nftables 防火墙只放行「本地子网集合」访问代理端口，compose 创建的自定义网络网段（如 `192.168.32.0/24`）不在集合内——容器连代理被 reject，症状是代拉 502 / `fetch_err`。在宿主 iptables 加 ACCEPT **没用**（不同 nft 表、reject 先命中），正确解法是把容器网段加进 ShellCrash 的「虚拟机/容器网段」：编辑 `/etc/ShellCrash/configs/ShellCrash.cfg` 的 `vm_ipv4='... 192.168.32.0/24 '`，`systemctl restart shellcrash`。普通 Clash/mihomo（非 ShellCrash）对应检查 `allow-lan` 与绑定地址
- **网段会被 compose 重建换掉**：`docker compose down/up` 重建网络时可能分到新网段，上面的放行就悄悄失效（症状复发）。在 compose 顶层把网段钉死：

  ```yaml
  networks:
    default:
      ipam:
        config:
          - subnet: 192.168.32.0/24
  ```

- **先分辨机场拦的是 IP 还是请求形态，再谈对策**：宿主机进程能拉、容器拉不动时，先确认两者**出口 IP 是否相同**——宿主跑着 ShellCrash/WireGuard 等策略路由时，宿主自身流量走隧道出口、docker 转发流量直出机房 IP，机场按 IP 拦（拦机房、放海外/住宅）的症状与「按 TLS 指纹拦」完全一样。分辨：`curl cip.cc` 分别看宿主与容器出口；对齐出口后再下结论。实战案例：机场拦机房 IP，`SUBSCRIPTION_PROXY` 指向宿主 ShellCrash（放行容器网段后）即全链路恢复，无需任何代码改动。注意这类拦截常是**速率/临时型**（实测：停止直连约一小时后封锁自行解除、直连恢复 200）——本服务的代拉重试会不断续期封锁窗口，形成「看起来永久 403」的死循环；所以即使直连暂时通了，也别撤掉代理配置。另外放行后 ShellCrash 的透明代理会同时接管容器全部出站（「直连」也走代理出口），`SUBSCRIPTION_PROXY` 成为代理进程停用时的兜底双保险

> 小注：docker 20.10 的 `host-gateway` 注入的是 docker0 网关（`172.17.0.1`）而非 compose 网络的网关；跨网桥访问宿主没问题，但若你的代理只绑定单一接口，留意这个解析值。

> macOS / Windows（Docker Desktop）不需要 `extra_hosts` 那行——`host.docker.internal` 原生可用。`docker run` 等价写法：`--add-host=host.docker.internal:host-gateway -e SNAPSHOT_PROXY=http://host.docker.internal:7890`。

### 消费端在受限网络：provider 形态死锁与 clash 两段式根治（v1.4.0）

上面三个坑都在说「**服务端**怎么拉到机场」。还有一个镜像问题：**客户端**拉到订阅后，自己够不够得到机场？2026-10-26 实战（VPS 部署，消费端 ShellCrash）：

- **机场的封锁判据可能是 ASN，不是单个 IP**：机场域名挂 Cloudflare，对腾讯云 AS45090 整段返回 error 1005——换 IP 没用，只要出口 ASN 是被ban的云厂商就拒。判别方法：`curl -sS https://机场域名/ -o /dev/null -w '%{http_code}'` 分别从消费端网络与其他网络（家庭宽带、手机热点）跑，一 403/1005 一 200 即坐实
- **mihomo 系客户端自拉 proxy-providers 走宿主直连、不经自身代理链**：转换引擎的 clash 输出若是 provider 形态（0 内联节点、节点列表 URL 指向机场原始订阅，由客户端自拉），那么宿主网络够不到机场时节点永远 0——**配置能导入、分组全空转（COMPATIBLE）**，且服务端配了代理也救不了它（代拉的只是转换，客户端拉 provider 是另一条路）。这是「看起来转换成功了但客户端不可用」的最隐蔽形态
- **v1.4.0 起对 clash 显式链与 auto+clash 系 UA 一律走两段式内联**（与 Quantumult X v1.3.3 对称）：sidecar 代拉机场（`SUBSCRIPTION_PROXY` 生效）→ 转通用节点列表 → 再转 clash 输出内联 `proxies`——客户端拿到即用、零二次拉取，上面两个封锁面（服务端出口、客户端出口）只要**服务端**能到机场就全部绕开。验证口径：拉取输出应含大量内联节点、不含 `proxy-providers`，sidecar 日志见 `twostage target=clash nodes=N`


## 端口绑定范围警告

README 快速开始里的 `-p 8080:80` 会把服务绑到**所有网卡**。对纯局域网机器这没问题，但**如果部署机有公网 IP，照抄这条命令等于把整个服务裸奔到公网**——任何扫到端口的人都能用你的转换服务，还能查看短链列表、删码（见[使用前须知](security.md#使用前须知)与[管理端点信任边界](security.md#短链数据与备份)）。公网 IP 主机请任选其一：

- 绑定到内网网卡：`-p 192.168.1.100:8080:80`（只写内网 IP）
- 或直接走[公网部署](deployment-public.md)：Caddy 反代 + 强制 token
