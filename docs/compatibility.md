# 客户端兼容与规则新鲜度

## 客户端兼容矩阵

按「客户端 × 协议 × 模板」实测标注，**未实测 = 未验证**——本表不承诺「同一链接总能返回正确格式」（UA 被改写、未知客户端、同名不同内核是已知限制）。真机实测随时间回填。

| 客户端 | target | 状态 | 深链 | 实测记录 |
|---|---|---|---|---|
| 自动识别（按客户端 UA） | auto | 未验证 | — | — |
| Clash / Mihomo | clash | 未验证 | — | — |
| Clash Verge | clash | 已验证 | — | 2026-10-25 · 真机订阅导入成功 |
| Surge | surge | 未验证 | 未验证 | — |
| Quantumult X | quanx | 已验证 | 未验证 | 2026-10-25 · 真机导入成功（注意走「配置文件 → 导入」） |
| Loon | loon | 未验证 | 未验证 | — |
| Stash | stash | 未验证 | 未验证 | — |
| Shadowrocket | shadowrocket | 未验证 | 未验证 | — |
| sing-box | singbox | 未验证 | — | — |
| v2rayN | v2rayn | 未验证 | — | — |

「深链」列 = 结果页「在 XX 中打开」按钮（唤起对应 App 导入），仅 iOS 五家提供，拼装口径见 [docs/implementation/api-contract.md](implementation/api-contract.md)；真机实测后回填。

> **Quantumult X 的坑**：转换链接要用「配置文件」的**导入**功能添加，不要填进「订阅」栏——「订阅」只吃节点链接，转换产物是整份配置文件，填错位置表现为导入后规则/分流缺失或直接报错。

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
