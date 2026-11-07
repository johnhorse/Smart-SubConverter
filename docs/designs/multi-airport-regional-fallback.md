# 多机场同区容灾与替补规则设计规范（v1.9.0）

## 1. 背景与痛点
v1.8.0 引入了多机场合并与全局 `🔯 机场容灾`（fallback：主力全部节点 -> 备用全部节点）。
在实际使用中存在严重隐患：
1. **跨区乱串**：机场节点通常覆盖多个国家/地区（港/美/日/新等）。全局 `🔯 机场容灾` 测速若选到低延迟的香港节点，当用于 OpenAI/Claude 或特定区域服务时，直接触发 403 被拒或账号被封。
2. **缺乏针对性替补**：不同机场的节点命名规则差异很大（如主力叫 `🇺🇸 美国 01`，备用叫 `SiliconValley-01`），需要支持为备用机场指定专属匹配规则。
3. **模板自带地区组偷跑备用**：模板原生的 `(美|US)` 测速组，如果备用延迟低，在主力没挂时流量也会偷跑到备用。

## 2. 核心架构设计

### 2.1 数据模型扩展（NodeGroup）
在 `NodeGroup`（`sidecar/groups.go` 及 `frontend/src/types.ts`）中增加可选字段：
```json
{
  "name": "🇺🇸 美国节点",
  "keywords": ["美", "US"],
  "pattern": "",
  "urlTest": true,
  "backupRules": {
    "备用": "SiliconValley|Portland|美国"
  }
}
```
- `backupRules`：`map[string]string`，key 为备用机场 Tag（如 `"备用"`），value 为该备用机场的自定义匹配模式（正则或关键词）。
- 校验规则：
  - key ≤ 32 runes，禁反引号与控制字符；
  - value ≤ 256 runes，禁反引号与控制字符；
  - 留空或 key 不存在：自动继承主力的 `pattern` 或 `keywords`。

### 2.2 派生引擎（sidecar/derive.go）规则

#### A. 自定义分组（Custom Groups）容灾升级
当存在多个机场（`len(airportTags) >= 2`）时，每个自定义分组 `G` 生成：
1. **主力测速子组**：
   - 组名：`⚡ [主力] G`
   - 正则：`(?i)(?=.*\[{主力Tag}\])` + 主力匹配表达式
   - 模式：`url-test`（如果 G.URLTest 为 true）或 `select`
2. **各备用测速子组**：
   - 组名：`⚡ [{备用Tag}] G`
   - 规则：优先取 `G.BackupRules[备用Tag]`，若未提供或为空，则继承主力的 `pattern`/`keywords`
   - 正则：`(?i)(?=.*\[{备用Tag}\])` + 替补匹配表达式
   - 模式：`url-test`
3. **顶层 Fallback 容灾组**（对外暴露的原组名 `G`）：
   - `custom_proxy_group=G`fallback`[]⚡ [主力] G`[]⚡ [备用1] G`...`urlTestTail
   - 若备用未配置或动态剪枝（匹配 0 节点），仅保留有节点的子组；若仅主力有效，退化为纯主力子组或直通。
4. **注入各 select 组**：
   - `G` 作为 Fallback 组原样注入到模板各 select 组（如 `💬 Ai平台` 等），业务层完全无感。

#### B. 模板自带地区组（Template Region Groups）自动容灾
识别模板原生的地区测速组（如 `🇭🇰 香港节点`、`🇯🇵 日本节点`、`🇺🇲 美国节点`、`🇨🇳 台湾节点`、`🇸🇬 狮城节点`、`🇰🇷 韩国节点`）：
- 原始行形式：`custom_proxy_group={Region}`url-test`{Pattern}`{Tail}
- 在多机场下改写为：
  1. `custom_proxy_group=⚡ [{主力Tag}] {Region}`url-test`(?i)(?=.*\[{主力Tag}\]){Pattern}`{Tail}
  2. `custom_proxy_group=⚡ [{备用Tag}] {Region}`url-test`(?i)(?=.*\[{备用Tag}\]){Pattern}`{Tail}
  3. `custom_proxy_group={Region}`fallback`[]⚡ [{主力Tag}] {Region}`[]⚡ [{备用Tag}] {Region}`{Tail}
- 效果：主力未挂时绝不偷跑备用；主力故障同区自动秒切备用。

#### C. 全局混合「🔯 机场容灾」收敛
- `🔯 机场容灾`（包含所有节点的 fallback 组）保留生成；
- **注入收窄**：只注入到 `🚀 节点选择`（即名字含 `节点选择` 的顶层总入口），从所有具体的分流业务组（如 `💬 Ai平台`、`🌍 国外媒体`、`📲 电报信息` 等）中剔除，彻底杜绝跨区串流导致的封号风险。

### 2.3 前端交互（Frontend）
1. **多订阅时展开替补规则**：
   - 当 `extraSubs.length > 0` 时，在自定义分组编辑/查看卡片中展示「⚙️ 替补规则（可选）」；
   - 展开后展示各备用机场 Tag 的输入框，Placeholder 标明 `留空自动继承主力规则`；
2. **偏好记忆与回填**：
   - 偏好存储与回填携带 `backupRules`；
   - 清除记忆时一并清除。
3. **探针节点提示**：
   - 若备用节点中对某规则匹配数为 0，给出黄色温和提示：`⚠️ [{Tag}] 未匹配到节点，该组仅主力生效`。

### 2.4 测试验证规范
- `sidecar/derive_test.go`：覆盖多机场下自定义组 fallback 升级、模板地区组 fallback 升级、全局容灾组仅限节点选择注入；
- `sidecar/groups_test.go`：覆盖 `backupRules` 校验与归一化；
- `frontend/src/useConverter.spec.ts`：覆盖多订阅与 `backupRules` 的组合与持久化；
- `frontend/src/components/GroupPanel.spec.ts`：覆盖替补规则展开、输入、回填渲染与更新。
