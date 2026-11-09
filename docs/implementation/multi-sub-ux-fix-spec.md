# 多机场订阅 UX 修复规范（v1.10.1）

## 1. 目标与背景

用户反馈前端交互问题：
1. **主力订阅 placeholder 提示支持多行/竖线分隔，但实际测试并不支持**：
   - 多行：HTML 单行 `<input type="url">` 在原生 paste 时自动被浏览器剔除 `\r\n` 或替换为空格，导致 `rawText.includes('\n')` 永远为 false；
   - 竖线：若直接把包含 `|` 的订阅传到后端，sidecar 的 `parseSubscriptions` 与 `validateURL` 没有按 `|` 拆分，`url.Parse` 报 `bad_url_format`（不是有效的 http(s) 链接）。
2. **输入主力订阅并加载节点分组后，再添加备用订阅，节点未重新拉取且无前缀**：
   - `useConverter.ts` 仅 watch 了 `inputUrl`，没有 watch `extraSubs` 或 `allSubscriptions`；
   - 缓存标识 `probedUrl` 仅存了 `inputUrl` 单个字符串，即使收起再打开面板也会误判为命中缓存，导致节点列表没有重新拉取，节点名缺少 `[主力]`、`[备用]` 前缀，备用节点也不在候选列表内。

## 2. 决策方案

经用户确认均采用**选项 A**：
1. **多行/竖线分割**：
   - 前端通过 `@paste` 拦截原生剪贴板内容（读取 `e.clipboardData.getData('text')`），支持 `\r\n`、`\n`、`|` 及空白分隔多链接，自动拆分为主力 + 备用卡片；
   - 后端 sidecar `parseSubscriptions` 增加 `\n` 与 `|` 拆分兜底，防止直接调用 API 时报 `bad_url_format`。
2. **备用订阅变更重探测**：
   - 探测缓存从 `probedUrl` 升级为 `probedSignature`（基于所有有效订阅的 `tag:url`）；
   - 在面板展开态下，监听所有订阅（主力与备用），400ms 防抖自动 re-probe；
   - 探测完成后实时刷新 `nodes` 与 `groupHits`。

## 3. 具体修改点

### 3.1 前端：`frontend/src/App.vue`
- 为单订阅输入框和多订阅主力输入框添加 `@paste="onMainPaste"`。
- `onMainPaste(e: ClipboardEvent)`：
  - 获取 `const text = e.clipboardData?.getData('text') ?? ''`
  - 若 `handleUrlBatch(text)` 返回 `true`，调用 `e.preventDefault()` 阻止浏览器单行清洗。

### 3.2 前端：`frontend/src/useConverter.ts`
- 优化 `handleUrlBatch(rawText: string): boolean`：
  - 检查是否包含 `\n`、`|`，或包含多于一个 `https?://`；
  - 正则拆分：首先按 `[\r\n|]+` 拆分并 trim，若只有 1 项且包含多个 `https?://`，按空白 `\s+` 再次提取；
  - 过滤空项；若项数 > 1，第一项入 `inputUrl`，其余项入 `extraSubs`，生成 tag，返回 `true`。
- 探测缓存与重探升级：
  - 定义 `getSubscriptionsSignature(): string`：将 `allSubscriptions.value` 格式化为 `tag:url` 用 `|` 连接的签名。
  - 将 `probedUrl` 替换为 `probedSignature`。
  - `watch(groupsOpen)`：使用 `getSubscriptionsSignature()` 与 `probedSignature` 比较。
  - 增加对所有订阅变更的监听：
    ```ts
    watch(
      [inputUrl, extraSubs, mainTag],
      () => {
        if (!groupsOpen.value) return
        clearTimeout(reprobeTimer)
        reprobeTimer = setTimeout(() => {
          if (!inputUrl.value.trim()) {
            resetProbe()
            return
          }
          const sig = getSubscriptionsSignature()
          if (sig !== probedSignature || selectedPreset.value !== probedPreset) {
            void probeNodes()
          }
        }, URL_REPROBE_DEBOUNCE_MS)
      },
      { deep: true, flush: 'sync' }
    )
    ```
  - `probeNodes()` 中：请求成功后记录 `probedSignature = getSubscriptionsSignature()` 与 `probedPreset = preset`。

### 3.3 后端：`sidecar/store.go`
- 在 `parseSubscriptions(raw string) []subscriptionItem` 中：
  - 若非 `[` 开头的 JSON，检查是否包含 `\n` 或 `|`；
  - 若包含，按 `\n`、`\r`、`|` 拆分并 trim；
  - 若拆分出多项有效 URL，返回多个 `subscriptionItem{URL: u, Tag: ""}`；
  - 若只有 1 项，保持原有逻辑返回单项。

### 3.4 测试覆盖
1. `sidecar/store_test.go`：测试 `parseSubscriptions` 对 `url1|url2`、多行 `url1\nurl2` 的解析。
2. `frontend/src/useConverter.spec.ts`：
   - 测试 paste 批量链接（带换行、竖线、空格）；
   - 测试分组面板开启时，添加/修改 `extraSubs` 触发 400ms 防抖 re-probe；
   - 测试 `probedSignature` 缓存匹配逻辑。
3. `frontend/src/App.spec.ts`：测试 paste 事件触发自动拆分卡片。

## 4. 验证标准
- 前端测试：`npm --prefix frontend test` 全部通过。
- Go 测试：按照 `sidecar-go-tests-via-docker-cp.md`，启动 `golang:1.23-alpine` 容器，`docker cp` 源码进去执行 `go vet ./... && go test ./...` 全部通过。
