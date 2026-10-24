# Handoff: V1 收尾 → 下版本开启

日期：2026-10-24 · 上一会话：V1 全量实施（从设计评审收口到代码交付）

## V1 现状（全部经主会话亲验后收工）

7 个本地 commit（未 push，无 remote）：

```
e8fadd2 docs(readme): S4 README——两档部署、三披露、兼容矩阵
3b84a62 docs(api-contract): Host 透传修正为 $http_host
3d2a0a8 feat(sidecar): S2 真实实现 + 三步冒烟门禁
c423a8c feat(scaffold): S1 镜像层 + s6 三进程 + Go sidecar 骨架 + CI
ac8e964 fix(frontend): 预校验错误停留状态②（D4 仲裁分流）
4771e06 feat(frontend): V1 三状态单页（Vue 3 + Vite + TS）
83e4065 docs: 设计文档 + Assignment 实测基线 + /api 冻结契约
```

- **已验证**：前端 46 单测 + sidecar 26 单测全绿；三步冒烟 10/10（本机 colima arm64 实跑，含「preview 生成 link → 经 nginx 双 UA 拉取」端到端）；日志 0 泄漏；`PUBLIC_BASE_URL→MANAGED_CONFIG_PREFIX` 联动实证（Surge `#!MANAGED-CONFIG` 正确回指）
- **未验证（下会话或用户侧）**：CI workflow 只过了语法检查，没在真实 GitHub 上跑过；amd64 构建靠 CI buildx 未验证；Caddy 附录 `caddy adapt` 已验、真实 TLS 未验；朋友真实订阅 URL 的容器可达性未测（fixture 是内联节点 + 本地 config）

## V1 遗留（用户侧，与代码无关）

1. **LICENSE**：仓库没有；上游 GPL-3.0 血统，选择需用户定夺（README 致谢节有占位说明）
2. **发布链路**：建 GitHub 仓库 → remote + push → 配 `DOCKERHUB_TOKEN` secret → 打 `v1.0.0` tag（首次 push = CI 首跑，预期会有小修）
3. **朋友闭环真机实测**（V1 核心成功标准）：一 iOS 一 Android 走「粘贴→导入→再更新」→ 回填 README 兼容矩阵 + `sidecar/main.go` 注册表的 `verified` 字段
4. 本机 `~/.docker/config.json` 的 `credsStore: desktop` 残留（memory 已记绕过法）

## 下版本第一个功能：转换规则模板切换

**用户需求原话（2026-10-24）**：「有时候想按照某个规则模板进行转换订阅，比如有的模板会把节点按地区分类……现在的功能里面不支持切换转换规则模板」。

**定位**：= 设计文档 V1.5 候选池「三档预设补齐」提前做。**需修订前提 2**（原文「三档规则预设平台内置、V1 先落一档，**不暴露给用户**」——暴露预设即修订此条）。

**讨论进度**：第一个决策问题已提出，用户未答复。选项与推荐：

- **A（已推荐）**：放「高级」折叠区（现标题「高级 · 指定客户端 ▸」扩为含规则模板），朋友路径零选项不变；选中模板烙进生成链接的 `config=` 参数，朋友拿走后更新订阅持续用该模板
- **B**：平级展示主路径（违背零选项前提与线框图基准，不推荐）

**后续待定决策（按序 grill，一次一个）**：

1. 选择器位置（上面的 A/B）
2. 模板列表来源：内置固定几档 vs 部署者自定义（env/挂载文件注入模板 URL 列表）vs 内置+自定义混合。**事实注意**：倾向的「地区分组」款 `ACL4SSR_Online_MultiMode.ini` 实测 404（见 assignment-findings ③），实施前先实测候选 URL（ACL4SSR_Online_Full / Full_AdblockPlus / Fallback / Custom_OpenClash_Rules 等），别按记忆承诺
3. 契约微调：POST `/api/preview` 加可选 `preset` 字段（缺省 = 默认预设）；`/api/clients` 的 `presets` 数组与 `/api/health` 的按预设监控**已支持多预设，形状不动**；`PRESET_URL` env 是单预设覆盖语义，多预设后要重新设计（如 `PRESETS` JSON）
4. 前端：高级区加模板选择 UI、preview 请求透传 preset、结果页可标注所用模板

## 实施模式（V1 验证有效的，直接复用）

- 冻结契约先行（api-contract.md 是唯一接口真相，两端对齐它实现）
- 分 lane 并行后台 agent（brief 含 truth sources + 已定决策 + 验收标准 + fork 停报边界），主会话只做决策/仲裁/亲验
- 主会话验收不采信 agent 自评：单测自己跑、冒烟自己跑、日志抽查自己跑
- 已知环境坑：docker credsStore（见 memory）、colima 无 BuildKit（classic builder）、/Volumes 不进 VM（构建走 tar 流，Makefile 已封装）

## 下个会话开场

1. 读本文档 + `docs/designs/smart-subconverter-v1.md` + `docs/implementation/api-contract.md`
2. 从「选择器位置」继续问用户（问题已备好，见上）
3. 决策齐后：修订前提 2 → 更新契约 → 分发实施

---

## 规划定案（2026-10-24，本会话 grill 收口）

「转换规则模板切换」四个决策全部定案，据此已修订设计文档（前提 2 / Open Questions / Next Steps）与 api-contract.md（增量修订记录见契约顶部）：

1. **选择器位置**：放「高级」折叠区，标题扩为「高级 · 客户端与规则模板 ▸」，展开后客户端 chips + 规则模板 chips 两块；主路径零选项不变
2. **模板来源**：sidecar 内置三档（随版本走，不做部署者自定义）：
   - `default` / ACL4SSR 默认 → ACL4SSR_Online.ini
   - `full_multimode` / 地区分组 + AI 分流 → ACL4SSR_Online_Full_MultiMode.ini（💬 Ai平台组候选含全部地区组，ChatGPT/Claude/Gemini 在客户端选地区即可定向；AI.list/OpenAi.list 实测覆盖三家）
   - `full_adblockplus` / 全量 + 去广告 → ACL4SSR_Online_Full_AdblockPlus.ini
   - 三 URL 均实测 200；旧记录里 MultiMode 404 是路径错（正确路径带 Full_ 前缀）
3. **契约**：preview 加可选 `preset`（缺省/空串/"default" = default 档）；非注册表 id → `bad_preset` 报错不回落（link 仍以 default 档生成保持非空，preset 回显 null）；响应回显 `preset:{id,name}`（bad_preset 时 null，其余含 ok:false 恒回显）；`PRESET_URL` 收窄为仅覆盖 default 档；`/api/clients` presets 1→3 项形状不动；health 轮询 3 URL 形状不动
4. **前端**：chips 直接显示 name（default 永远第一、默认选中，无「未选」态）；选中档下方一行描述（前端硬编码 id→描述：默认=按用途分组最简；地区分组+AI 分流=按用途+地区分组，含 AI 平台组可定向指定地区；全量+去广告=规则最全附广告拦截）；结果页 link 下标注「模板：{preset.name}」；bad_preset 文案「这个模板不可用，重新选一个试试」

实施分发：sidecar lane + frontend lane 并行后台 agent（契约即 truth source），主会话亲验单测 + 冒烟 + 日志抽查。
