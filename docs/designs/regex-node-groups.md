# 正则表达式节点分组（Regex Node Groups）设计

版本定位：v1.6.1。状态：已敲定（2026-11-03），待实施。

## 背景与痛点

v1.5.0 支持了多关键词 AND 筛选成组，但遇到以下场景无能为力：
- **排除特定节点**：例如“选所有香港节点，但排除带 [维护] 或 0.1x 节点”；
- **或条件筛选**：例如“同时匹配香港或新加坡节点”；
- **前缀/后缀正则提取**。

## 用户拍板的决策项

1. **界面交互：二选一切换**：打词区上方提供 `[ 关键词匹配 | 正则匹配 ]` 双 Tab 切换（默认关键词模式保持傻瓜体验）。
2. **数据结构：新增可选 pattern 字段**：
   ```ts
   export interface NodeGroup {
     name: string
     keywords: string[]    // 关键词模式下使用（字面量）
     pattern?: string      // 正则模式下使用
     urlTest: boolean
   }
   ```
   存量短链、数据库 `links` 表、服务端偏好档案 `prefs_custom` 无需改表或洗库，平滑兼容。
3. **输入辅助与实时校验**：
   - 前端输入时实时 `new RegExp(pattern, 'i')` 校验；
   - 语法错误时实时红字提示，阻止成组；
   - 提供常用语法模板 chip（如 `(?=.*A)(?!.*B)`、`(A|B)`、`^(?!.*A)`），点击填入改词即用；
   - 下方节点列表实时高亮命中节点，右侧实时显示 `已命中 X`。
4. **默认忽略大小写**：
   - 前端匹配带 `'i'` 标志；
   - 后端 sidecar 派生 `custom_proxy_group=` 时，若 pattern 未以 `(?i)` 开头，自动拼接 `(?i)` 前缀（引擎实测完美支持）。
5. **成组命名与展示**：
   - 正则模式点击「成组」后，默认组名为空，自动聚焦命名框，引导用户输入自然组名（如「香港纯净」）；
   - 已建组列表在组名前后标注 `[正则]` 微标，鼠标悬浮时 tooltip 显示完整 pattern。
6. **草稿与偏好衔接（对齐 v1.6.0）**：
   - 本地草稿记录当前模式（关键词/正则）和未成组的 `draftPattern`，刷新不丢；
   - 偏好档案按 `NodeGroup[]` 序列化保存，换端自动带出。

## 服务端设计（sidecar）

### 契约与校验
- `nodeGroup` struct 增加 `Pattern string \`json:"pattern,omitempty"\``
- `groupsJSON` 规范化：若 `Pattern != ""`，对两端 trim。
- 校验规则：
  - pattern 与 keywords 至少有一个有效（pattern 非空 或 len(keywords) > 0）；
  - pattern 长度不超过 256；
  - pattern 禁止包含控制字符、换行符 `\r` / `\n`，以及反引号 `` ` ``（防止破坏 ini 格式）；
  - 注意：Go 标准库 `regexp` 不支持 Perl 前瞻语法 `(?=...)` / `(?!...)`，**不得**在 Go 侧使用 `regexp.Compile` 去拒绝包含前瞻语法的合法 PCRE 正则！Go 侧只做长度、非法字符及基本成对校验。

### 派生逻辑（derive.go）
- 如果 `g.Pattern != ""`：
  - 若未以 `(?i)` 开头，拼装为 `(?i)` + `g.Pattern`；
  - 组行格式：
    `custom_proxy_group=` + name + "`" + (url-test / select) + "`" + pattern + ("`" + urlTestTail / "")
- 如果 `g.Pattern == ""`：
  - 走原有的 `Keywords` 拼装 `(?=.*QuoteMeta(w))` 逻辑。

## 前端设计（frontend）

### 草稿与状态（prefs.ts / useConverter.ts）
- `DraftSnapshot`:
  - 增加可选 `draftMode?: 'keyword' | 'regex'`
  - 增加可选 `draftPattern?: string`
- `prefs.ts`:
  - `isValidSnapshot`: 允许 `pattern` 字段，允许可选的 `draftMode` 和 `draftPattern`。
- `useConverter.ts`:
  - 暴露 `draftMode` 与 `draftPattern`；
  - watch 保存流包含这两个新字段；
  - 恢复流同步恢复模式与草稿正则；
  - 换订阅清空组时，同时清空 `draftPattern`。

### UI（GroupPanel.vue）
- 顶部 Tab：关键词模式 / 正则模式；
- 正则模式输入区：
  - 实时正则编译与报错提示；
  - 快捷模板 chips；
  - 节点列表实时计算匹配；
- 命名确认区；
- 已建分组卡片展示：如果是正则组，显示 `[正则]` chip，悬浮展示 pattern。

## 任务划分

- **Lane A (sidecar-regex)**：
  - `sidecar/groups.go`：结构体扩展、UnmarshalJSON 兼容、归一化与 groupsJSON 序列化；
  - `sidecar/derive.go`：派生逻辑分支（pattern 优先、注入 `(?i)`、防换行反引号）；
  - `sidecar/preview.go`：校验逻辑调整（支持 pattern 组，放行前瞻语法）；
  - `docs/implementation/api-contract.md`：契约更新；
  - 单测：单测覆盖 pattern 组派生、前瞻排除断言、非法字符拒绝、大小写忽略前缀等。
- **Lane B (frontend-regex)**：
  - `frontend/src/types.ts`：NodeGroup 接口扩展；
  - `frontend/src/prefs.ts`：草稿结构与字段类型宽容；
  - `frontend/src/useConverter.ts`：draftMode / draftPattern 接入防抖保存与恢复；
  - `frontend/src/components/GroupPanel.vue`：Tab 切换、正则输入框、实时校验与高亮、模板 chip、空名引导、`[正则]` 微标展示；
  - 单测：覆盖正则匹配高亮、语法错误阻断、Tab 切换、草稿保存恢复等。
