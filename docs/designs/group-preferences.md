# 分组偏好记忆（Group Preferences）设计

版本定位：v1.6.0。状态：已与用户敲定（2026-11-03），待实施。

## 背景与目标

v1.5.0/v1.5.1 交付了自定义分组与模板组删减，但所有面板状态只存在前端内存
（`frontend/src/useConverter.ts` 的 ref），服务端 links 表虽然存了已提交的
groups/exclude（随短链指纹落库），却没有「按订阅链接查回来」的入口。

三个痛点：

| 场景 | 现状 |
|---|---|
| 配到一半刷新页面 | 全丢（含未确认的关键词 chips） |
| 换浏览器/换设备继续配 | 页面空白重来（后端有数据，拿不回） |
| 同订阅换 target（clash→QX）/换档位 | 指纹含 target 与 config_url，组不带过去 |

## 用户拍板的五条决策

1. **两层存储**：浏览器 localStorage 记面板状态（管刷新）；服务端按订阅链接存
   「已提交偏好档案」（管跨端跨平台）。
2. **两把钥匙**：自定义组只按订阅链接（sub_key）记；模板组删减（exclude）按
   （sub_key + preset 档位）记。target 不进任何钥匙——同订阅换 target 自动套用。
3. **自动带出**：节点探测成功后自动回填，顶部轻提示「已恢复 N 个自定义分组、
   剔除 M 个模板组［不用了］」可一键撤销。
4. **本地草稿优先**：本地有该订阅的快照用本地；没有才问服务端要。配到一半
   刷新不会被旧存档顶掉。
5. **POST + 不认证**：偏好接口走 POST/DELETE + body 传订阅链接（不进访问日志）；
   不加认证——能查到某订阅偏好的前提是已持有该订阅链接，而持有链接本来就能
   直接拉全部节点，不算新增暴露。同一订阅多人共用时最后提交者覆盖（可接受）。

默认项（未单独问，按常规处理）：

- 存档时机：`/api/preview` 非探测提交成功即顺手 upsert 偏好，不加新保存按钮；
  本地草稿每次变更防抖写入。
- 订阅链接明文进 localStorage（本机自用；服务端 links 表本就存 source_url）。
- 不做存档管理列表 UI，仅在分组面板给「清除本订阅记忆」入口。

## 非目标（本版不做）

- 发版/CHANGELOG/VERSION（走发版流程时另做）
- 偏好档案的管理列表页、多套命名存档（同一钥匙单份覆盖）
- 后端偏好与短链的联动清理（删短链不动偏好档案）
- 服务端草稿（未提交内容只存本地）

## 服务端设计（sidecar）

### 存储（store.go 迁移 v4，沿用 schemaMigrations 只追加惯例）

```sql
CREATE TABLE IF NOT EXISTS prefs_custom (
  sub_key    TEXT PRIMARY KEY,  -- 订阅 URL 规范化（复用 shortFingerprint 的规范化逻辑，提取为可复用函数）
  groups     TEXT NOT NULL,     -- JSON 数组，与 links.groups 同构
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS prefs_exclude (
  sub_key    TEXT NOT NULL,
  preset_id  TEXT NOT NULL,
  exclude    TEXT NOT NULL,     -- JSON 数组
  updated_at TEXT NOT NULL,
  PRIMARY KEY (sub_key, preset_id)
);
```

### 接口（main.go 注册）

```
POST /api/preferences/query
  Body: {"sub_url": "...", "preset_id": "default"}
  200: {"custom_groups": <NodeGroup[]|null>, "exclude": <string[]|null>}
       （null = 无存档；exclude 只回该 preset 档的）
  400: bad_url_* / bad_preset（沿用 /api/preview 的错误族与响应形态）
DELETE /api/preferences
  Body: {"sub_url": "..."}
  200: {"deleted_custom": <bool>, "deleted_exclude": <int>}  -- 该订阅全部档案
```

### 保存钩子（handlePreview 成功路径，非 probe）

- 请求 JSON 里 **groups 字段出现** → upsert prefs_custom（sub_key, groups）。
- 请求 JSON 里 **exclude 字段出现** → upsert prefs_exclude（sub_key, preset, exclude）。
- **字段未出现 = 不动档案**（现状前端仅在非空时携带：`useConverter.ts` 的
  `if (sendGroups.length > 0)`——边界语义「删光所有组再生成短链不会清档案，
  要清用清除记忆入口」，写入契约「幂等与偏好」节）。
- probe 模式（探测）绝不写档；preview 失败（含 derive_error）不写档。
- 前端能力位失效档剔除 groups/exclude 发送（F1 现状）天然落入「字段未出现」。

### 边界

- 出树 DATA_DIR（分组禁用）：偏好接口照常工作（纯存取，无引擎副作用）。
- sub_key 规范化与指纹同规则：两端 trim、scheme/host 小写、去默认端口、
  path 空补 /、query 原样、fragment 丢弃。

## 前端设计（frontend）

### localStorage 草稿

- key `ssc.draft.v1`，value JSON：
  `{"subs": {"<trim 后订阅URL>": {snapshot}}, "last": "<trim 后订阅URL>"}`。
- snapshot：`{target, preset, groups, excluded, draftKeywords, updatedAt}`——
  必须包含**未确认的关键词 chips**（现状 chips 输入态在 GroupPanel.vue 内部，
  需提升或暴露给持久层，实现方式自定）。
- 写入：面板状态变更 debounce（~300ms）；恢复：`inputUrl` trim 后命中某 sub key
  即恢复整份（含 target/preset——「回到刚才的样子」语义）。
- 容量：最多保留最近 5 个订阅的快照，超出淘汰最旧。

### 服务端回填（痛点②③的解）

- 触发：probe 成功 && 本地无该订阅快照 && customGroupsAvailable。
- 动作：POST /api/preferences/query（preset= 当前档）→ groups 回填；
  exclude 与**当前档模板组列表求交集**后回填（防悬空组名）。
- 提示条：「已恢复 N 个自定义分组、剔除 M 个模板组［不用了］」；
  撤销 = 恢复回填前的面板状态（不清任何存档）。
- 回填成功后该状态照常进入本地草稿写入流。

### 清除记忆

- 分组面板底部「清除本订阅记忆」：删本地快照 + DELETE /api/preferences
  （该订阅全部档案），轻提示确认，防误触。

## 任务拆分

- T1 sidecar：store v4 迁移 + prefs 两表 + sub_key 规范化提取复用 + CRUD + 单测
- T2 sidecar：query/delete 两接口 + 路由注册 + 契约文档（api-contract.md）增补 + 单测
- T3 sidecar：handlePreview 保存钩子（字段出现才 upsert；probe/失败不写）+ 单测
- T4 frontend：api.ts/types + localStorage 草稿存取 + inputUrl 命中恢复 + 单测
- T5 frontend：服务端回填 + 提示条/撤销 + exclude 交集 + 清除入口 + 单测
- T6 冒烟（主会话）：本机起服务端到端——存档→查回→换档 exclude 不串→
  换 target 组跟走→DELETE 清除→probe 无副作用
- T7 独立 code review（主会话组织）

## 验收标准

- `go test ./...`（sidecar）全绿；前端 vitest 全绿；`make build` 产物正常。
- 冒烟场景表逐条过（T6）。
- 契约文档含「幂等与偏好」边界语义。
- 真机验收（用户执行，不阻塞合入）：刷新不丢、新浏览器粘贴订阅自动带出、
  换 target 组跟走。

## 评审修订（2026-11-03，code review 后定稿，优先级高于上文冲突处）

1. **草稿落盘只由用户面板编辑触发**：watch 源 = target/preset/groups/excluded/draftKeywords，
   **不含 inputUrl**；武装时捕获 key，触发时 key 已变则丢弃本次保存。程序化路径（本地恢复、
   换订阅清空、清除）用 suppress 标志跳过落盘。修「粘贴 300ms 后写空快照 → 服务端回填门
   永久关闭」的阻断级 bug（原实现真实时序下回填永不触发）。
2. **换订阅处理**（trim 后 key 变化）：有本地快照 → 整份恢复（先校验 target 在 clients、
   preset 在 presets 中，无效字段跳过保持当前值）；无快照 → 程序化清空
   groups/excluded/draftKeywords（target/preset 保留）。同 key（含尾部空格差异）不动作。
3. **服务端回填门改语义**：probe 成功 && customGroupsAvailable && 该 key **无草稿快照**
   （快照仅由用户编辑产生 → 等价「本地无用户编辑」，草稿优先不变）。取消 restoredKeys
   会话级永久阻断：切档重 probe 时按新档 preset 重新 query（无本地快照时两把钥匙的
   分档删减可回填）。同一 (url, preset) 在途查询去重。
4. **提示条生命周期**：回填后用户首次编辑 → 自动收起并作废回滚点（「不用了」只在无后续
   编辑时回滚）。防「撤销把用户新加的组一起吞掉」。
5. **清除记忆**：clearTimeout(draftTimer)、probeSeq++ 作废在途回填；任何一次清除尝试
   （无论 DELETE 成败）后，本会话对该订阅禁用服务端自动回填（防「清了又回来」；DELETE
   失败 toast 保持可重试口径）。
6. **草稿层自愈**：坏 JSON / subs 为数组 / 快照字段缺失或类型不对 → 当无草稿处理并剔除
   坏条目回写（不永久卡死该订阅的恢复，不产生 unhandled rejection）。
7. **GroupPanel 的 draftKeywords 改必传受控 prop**（去掉半受控双模式——未请求的灵活性）。
8. **DraftStore.last** 若产品代码无读取处则删除。
9. **测试**：回填用例改真实 timers（必须覆盖「粘贴后 >300ms 才 probe 仍能回填」）；各 spec
   文件 beforeEach 清 localStorage；新增回归：清除后 300ms 不复活、换订阅不污染
   （A 的组不进 B 的 key/档案）、撤销后编辑不被回滚、坏快照自愈。修正既有 App.spec 跨用例泄漏。
10. **sidecar**：saveGroupPrefs 的两次 upsert 并入单事务（对齐 deletePrefs 事务语义）。

## 实施约束（给两条 lane）

- 分两 lane 并行：sidecar（T1-T3）∥ frontend（T4-T5），契约 JSON 形态以本文件
  为准（两 lane 不互相等待）。
- lane 不 commit、不 push；主会话验收后统一收口。
- 未覆盖的设计问题：停下，写进最终报告交主会话仲裁，不自作主张。
