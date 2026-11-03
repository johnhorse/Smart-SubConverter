import { computed, getCurrentScope, nextTick, onScopeDispose, ref, watch } from 'vue'
import {
  ApiUnreachableError,
  deletePreferences,
  fetchClients,
  fetchPreferences,
  fetchPreview,
} from './api'
import { FALLBACK_CLIENTS, FALLBACK_PRESETS, PRECHECK_ERROR_CODES, errorText } from './constants'
import {
  DRAFT_DEBOUNCE_MS,
  getSnapshot,
  removeSnapshot,
  saveSnapshot,
  type DraftSnapshot,
} from './prefs'
import type { ClientEntry, NodeGroup, PreviewResponse, PresetInfo } from './types'

export type Phase = 'idle' | 'converting' | 'result'

// probe（节点名单探测）状态：failed 时分组面板显示不可用态，不阻塞主流程
export type ProbeState = 'idle' | 'loading' | 'ready' | 'failed'

const URL_REPROBE_DEBOUNCE_MS = 400

export function useConverter() {
  const phase = ref<Phase>('idle')
  const inputUrl = ref('')
  const selectedTarget = ref('auto')
  // 模板无「未选」态：default 永远默认选中（契约保证 default 永远第一）
  const selectedPreset = ref('default')
  const advancedOpen = ref(false)
  // 状态②的请求中标志：出错停驻时置回 false，按钮恢复可点（可重试）
  const submitting = ref(false)
  const convertError = ref<string | null>(null)
  const result = ref<PreviewResponse | null>(null)
  const clients = ref<ClientEntry[]>(FALLBACK_CLIENTS)
  const presets = ref<PresetInfo[]>(FALLBACK_PRESETS)
  const capabilities = ref<{ customGroups?: boolean } | undefined>(undefined)

  // v1.5.0 自定义分组：面板展开态、组意图、probe 结果
  const groupsOpen = ref(false)
  const groups = ref<NodeGroup[]>([])
  const nodes = ref<string[]>([])
  const probeState = ref<ProbeState>('idle')

  // v1.5.0 增补：模板组删减（excluded 保持加入顺序、去重）+ probe 回传的模板组命中数
  const excluded = ref<string[]>([])
  const groupHits = ref<Record<string, number>>({})
  // v1.6.0 分组偏好记忆：未确认关键词 chips（GroupPanel 受控）+ 服务端回填提示条（可撤销）
  const draftKeywords = ref<string[]>([])
  // v1.6.1 正则匹配：当前模式（GroupPanel 受控）与未成组的正则草稿
  const draftMode = ref<'keyword' | 'regex'>('keyword')
  const draftPattern = ref('')
  const restoreNotice = ref<{ groups: number; excluded: number } | null>(null)
  // 当前选中档的模板自带组清单（模板原序；字段缺席按 [] 降级——豁免档/旧 sidecar）
  const templateGroups = computed(
    () => presets.value.find((p) => p.id === selectedPreset.value)?.groups ?? [],
  )

  // 入口显隐 = 部署能力位 && 选中档能力位（契约 R1/D2；字段缺席按 false 降级，老 sidecar 不显示入口）
  const customGroupsAvailable = computed(
    () =>
      (capabilities.value?.customGroups ?? false) &&
      (presets.value.find((p) => p.id === selectedPreset.value)?.customGroups ?? false),
  )

  async function loadClients() {
    try {
      const res = await fetchClients()
      if (Array.isArray(res.clients) && res.clients.length > 0) clients.value = res.clients
      if (Array.isArray(res.presets) && res.presets.length > 0) presets.value = res.presets
      capabilities.value = res.capabilities
    } catch {
      // 静默降级：保持 FALLBACK_CLIENTS / FALLBACK_PRESETS，页面仍可用
    }
  }

  let probeSeq = 0
  // 上次成功 probe 的 url / 模板档：同 url 同档重新展开面板时复用结果不重发（F6）；
  // 任一不匹配（含换档、清空 URL）都重新探测，不复用旧档结果
  let probedUrl = ''
  let probedPreset = ''

  // 清空探针状态并作废在途响应（F2/F5）：旧链接/旧模板的名单与命中数一律不得残留
  function resetProbe() {
    probeSeq++
    nodes.value = []
    groupHits.value = {}
    probeState.value = 'idle'
    probedUrl = ''
    probedPreset = ''
  }

  async function probeNodes() {
    if (!groupsOpen.value) return // 双保险：收起/restart 清了定时器后，在途回调不得再发
    if (!inputUrl.value.trim()) {
      resetProbe() // 空 URL 重开面板：上一订阅的 ready 结果残留会误导面板（F5）
      return
    }
    const seq = ++probeSeq
    const url = inputUrl.value // F3：归属按请求时的 URL 记账，不取响应时的当前值
    const preset = selectedPreset.value
    probeState.value = 'loading'
    groupHits.value = {} // 旧命中数属旧名单/旧模板，探测期间不展示
    try {
      const res = await fetchPreview(url, selectedTarget.value, preset, { probe: true })
      if (seq !== probeSeq) return // 过期响应丢弃（换 url / 换档后旧 probe 慢回）
      if (res.ok && Array.isArray(res.nodes)) {
        nodes.value = res.nodes
        groupHits.value = res.groupHits && typeof res.groupHits === 'object' ? res.groupHits : {}
        probeState.value = 'ready'
        probedUrl = url
        probedPreset = preset
        // v1.6.0：probe 成功后尝试服务端偏好回填（本地无快照才问服务端，内部静默）
        void maybeRestoreFromServer(seq, url, preset)
      } else {
        probeState.value = 'failed'
        groupHits.value = {}
      }
    } catch {
      if (seq !== probeSeq) return
      probeState.value = 'failed'
      groupHits.value = {}
    }
  }

  // ---- v1.6.0 分组偏好记忆 ----（真相源：group-preferences.md，含 2026-11-03 评审修订）

  // 面板状态当前归属的订阅 key（trim 后）：同 key（含尾部空格差异）不动作，防顶掉未落盘编辑
  let currentKey = ''
  // 程序化路径（本地恢复/换订阅清空/回填/撤销）跳过草稿落盘与提示条收起判定
  let suppressDraft = false
  // 程序化恢复中：watch(selectedPreset) 的清空 + 重 probe 跳过，由 applyPanelState 显式接管
  let restoring = false
  // 本会话被清除过记忆的订阅：禁用服务端自动回填（防「清了又回来」；DELETE 失败同样生效）
  const restoreDisabled = new Set<string>()
  // 回填响应作废纪元：清除记忆时递增，在途回填响应一律丢弃（不误伤在途 probe/节点名单）
  let prefsEpoch = 0
  // 同 (url, preset) 在途回填查询去重
  const pendingQueries = new Set<string>()
  let preRestoreState: { groups: NodeGroup[]; excluded: string[] } | null = null

  // 草稿写入流（spec 修订 1）：watch 源不含 inputUrl——粘贴订阅本身不产生快照，否则服务端
  // 回填门「该 key 无快照」会被空快照永久关死。武装时捕获 key，触发时 key 已变则丢弃本次
  // 保存（防 A 的面板状态写进 B 的 key/档案）。
  let draftTimer: ReturnType<typeof setTimeout> | undefined
  function scheduleDraftSave() {
    clearTimeout(draftTimer)
    const key = inputUrl.value.trim()
    draftTimer = setTimeout(() => {
      if (inputUrl.value.trim() !== key) return
      if (!key) return
      saveSnapshot(key, {
        target: selectedTarget.value,
        preset: selectedPreset.value,
        groups: [...groups.value],
        excluded: [...excluded.value],
        draftKeywords: [...draftKeywords.value],
        draftMode: draftMode.value,
        draftPattern: draftPattern.value,
        updatedAt: new Date().toISOString(),
      })
    }, DRAFT_DEBOUNCE_MS)
  }
  watch(
    [selectedTarget, selectedPreset, groups, excluded, draftKeywords, draftMode, draftPattern],
    () => {
      if (suppressDraft) return
      if (restoreNotice.value) {
        // 用户已接手编辑（spec 修订 4）：提示条自动收起、回滚点作废——「不用了」不得吞掉新编辑
        restoreNotice.value = null
        preRestoreState = null
      }
      scheduleDraftSave()
    },
    { deep: true, flush: 'sync' },
  )
  // 组件卸载时取消 pending 落盘（测试直接调用 useConverter 无 scope：跳过）
  if (getCurrentScope()) {
    onScopeDispose(() => clearTimeout(draftTimer))
  }

  // 程序化清空面板组状态（换订阅无快照，spec 修订 2）：target/preset 保留；
  // 正则草稿一并清空并回到关键词模式（草稿不跨订阅）
  function clearPanelGroups() {
    suppressDraft = true
    try {
      groups.value = []
      excluded.value = []
      draftKeywords.value = []
      draftPattern.value = ''
      draftMode.value = 'keyword'
    } finally {
      suppressDraft = false
    }
  }

  // 应用一整份本地快照（spec 修订 2/4）：target/preset 先校验仍在注册表，无效字段跳过保持
  // 当前值。suppressDraft 只包本次同步赋值（窗口内用户真实编辑照常落盘）；restoring 撑到
  // nextTick——让 pre-flush 的切档 watcher 在恢复期间跳过
  async function applyPanelState(snap: DraftSnapshot) {
    restoring = true
    try {
      suppressDraft = true
      try {
        if (clients.value.some((c) => c.target === snap.target)) selectedTarget.value = snap.target
        const presetValid = presets.value.some((p) => p.id === snap.preset)
        const presetChanged = presetValid && selectedPreset.value !== snap.preset
        if (presetValid) selectedPreset.value = snap.preset
        groups.value = snap.groups.map((g) => ({ ...g, keywords: [...g.keywords] }))
        excluded.value = [...snap.excluded]
        draftKeywords.value = [...snap.draftKeywords]
        // v1.6.1：恢复匹配模式与正则草稿（字段缺席按关键词模式/空正则降级）
        draftMode.value = snap.draftMode === 'regex' ? 'regex' : 'keyword'
        draftPattern.value = snap.draftPattern ?? ''
        // 不依赖调度次序（spec 修订 4）：面板开着且档位真变了 → 显式重 probe 刷新 groupHits
        if (presetChanged && groupsOpen.value && customGroupsAvailable.value) void probeNodes()
      } finally {
        suppressDraft = false
      }
      await nextTick() // 让 watch(selectedPreset) 在本轮 pre flush 里于 restoring 有效期内跳过
    } finally {
      restoring = false
    }
  }

  // 换订阅处理（spec 修订 2）：trim 后 key 变化 → 有本地快照则整份恢复；无快照则程序化清空
  // 组状态（target/preset 保留）。同 key（含尾部空格差异）不动作。
  // hadKey 守卫：「空 → 首个订阅」是首次绑定不是换订阅，用户先配的组/删减随订阅带走（不清空）
  watch(
    inputUrl,
    (url) => {
      const key = url.trim()
      if (key === currentKey) return
      const hadKey = currentKey !== ''
      currentKey = key
      restoreNotice.value = null // 换订阅：旧回填提示条失效
      if (key) {
        const snap = getSnapshot(key)
        if (snap) {
          void applyPanelState(snap)
          return
        }
      }
      if (hadKey) clearPanelGroups()
    },
    { flush: 'sync' },
  )

  // 服务端回填（spec 修订 3）：probe 成功 && 能力位真 && 该订阅无草稿快照（快照仅由用户编辑
  // 产生，等价「本地无用户编辑」——草稿优先不变）→ POST /api/preferences/query。
  // exclude 与当前档模板组列表求交集（防悬空组名）。
  async function maybeRestoreFromServer(seq: number, url: string, preset: string) {
    if (!customGroupsAvailable.value) return
    const key = url.trim()
    if (!key || restoreDisabled.has(key) || getSnapshot(key)) return
    const queryKey = `${key}\u0000${preset}`
    if (pendingQueries.has(queryKey)) return // 同 (url, preset) 在途去重
    pendingQueries.add(queryKey)
    const epoch = prefsEpoch
    let res
    try {
      res = await fetchPreferences(url, preset)
    } catch {
      return // 偏好接口失败静默：不影响主流程，下次 probe 再试
    } finally {
      pendingQueries.delete(queryKey)
    }
    // 过期响应丢弃（切档/清空后 probeSeq 已递增；期间被清除则 epoch 已变）；URL 已变不得落到新订阅头上
    if (epoch !== prefsEpoch) return
    if (seq !== probeSeq || probeState.value !== 'ready' || inputUrl.value.trim() !== key) return
    if (restoreDisabled.has(key)) return
    const customGroups = res.custom_groups ?? []
    const templateNames = new Set(templateGroups.value.map((g) => g.name))
    const exclude = (res.exclude ?? []).filter((n) => templateNames.has(n))
    if (customGroups.length === 0 && exclude.length === 0) return // 无档案（或被交集滤光）：不动面板
    suppressDraft = true // 程序化回填不落盘：快照只由用户编辑产生
    try {
      preRestoreState = { groups: [...groups.value], excluded: [...excluded.value] }
      groups.value = customGroups
      excluded.value = exclude
      restoreNotice.value = { groups: customGroups.length, excluded: exclude.length }
    } finally {
      suppressDraft = false
    }
  }

  // 撤销回填：恢复回填前的面板状态（不清任何存档；程序化路径不落盘）
  function undoRestore() {
    if (!preRestoreState) return
    suppressDraft = true
    try {
      groups.value = preRestoreState.groups
      excluded.value = preRestoreState.excluded
    } finally {
      suppressDraft = false
    }
    preRestoreState = null
    restoreNotice.value = null
  }

  const restoreNoticeText = computed(() => {
    const n = restoreNotice.value
    if (!n) return ''
    const parts: string[] = []
    if (n.groups > 0) parts.push(`已恢复 ${n.groups} 个自定义分组`)
    if (n.excluded > 0) parts.push(`剔除 ${n.excluded} 个模板组`)
    return parts.join('、')
  })

  // 清除本订阅记忆（spec 修订 5）：取消 pending 落盘、作废在途回填响应，删本地快照 +
  // DELETE 服务端档案；本会话对该订阅禁自动回填（无论 DELETE 成败，防「清了又回来」）
  async function clearPreferences(): Promise<boolean> {
    const key = inputUrl.value.trim()
    if (!key) return false
    clearTimeout(draftTimer)
    prefsEpoch++
    removeSnapshot(key)
    restoreDisabled.add(key)
    restoreNotice.value = null
    preRestoreState = null
    try {
      await deletePreferences(inputUrl.value)
      return true
    } catch {
      return false // 服务端删失败：本地已清；返回 false 让 UI 提示可重试
    }
  }

  // 展开分组面板 → probe（url 非空才发；同 url 同档已 ready 复用不重发）；收起 → 清
  // pending 防抖（收起后 400ms 内不得再触发 probe / 重置面板状态）
  let reprobeTimer: ReturnType<typeof setTimeout> | undefined
  watch(groupsOpen, (open) => {
    if (open) {
      if (
        probeState.value === 'ready' &&
        probedUrl === inputUrl.value &&
        probedPreset === selectedPreset.value
      ) {
        return
      }
      void probeNodes()
      return
    }
    clearTimeout(reprobeTimer)
  })

  // 面板展开期间改 url → 防抖重 probe（节点名单要跟订阅一致）；清空 url 回 idle。
  // flush:'sync'：回调在赋值瞬间取 groupsOpen 真值——「先粘贴 url 后展开面板」时
  // url 变化发生在面板关闭态，不埋防抖（展开 probe 已覆盖），避免展开后 400ms
  // 又重复 probe 一次（批量 flush 下回调会误读到已翻转的 groupsOpen）
  watch(
    inputUrl,
    () => {
      if (!groupsOpen.value) return
      clearTimeout(reprobeTimer)
      reprobeTimer = setTimeout(() => {
        if (!inputUrl.value.trim()) {
          resetProbe()
          return
        }
        void probeNodes()
      }, URL_REPROBE_DEBOUNCE_MS)
    },
    { flush: 'sync' },
  )

  // 切换模板 → 剔除清单与命中数按新模板重置（不按组名继承）；作废在途 probe——旧模板
  // 的命中数不得落到新档；面板开着且新档支持分组则重 probe，让 0 节点标记/一键剔除跟随新模板。
  // restoring（程序化本地恢复）时跳过：清空与重 probe 由 applyPanelState 显式接管（spec 修订 4）
  watch(selectedPreset, () => {
    if (restoring) return
    excluded.value = []
    groupHits.value = {}
    probedUrl = ''
    probedPreset = ''
    probeSeq++
    if (groupsOpen.value && customGroupsAvailable.value) void probeNodes()
  })

  async function submit() {
    if (submitting.value) return
    submitting.value = true
    phase.value = 'converting'
    advancedOpen.value = true
    convertError.value = null
    try {
      // 空组不带第 4 参（存量请求字节不变）；能力位失效（切到不支持档）时剔除 groups / exclude
      // 发送——等价无组转换正常成功，残留组不会撞 groups_disabled 锁死（F1）
      const sendGroups = customGroupsAvailable.value ? groups.value : []
      const sendExclude = customGroupsAvailable.value ? excluded.value : []
      const options: { groups?: NodeGroup[]; exclude?: string[] } = {}
      if (sendGroups.length > 0) options.groups = sendGroups
      if (sendExclude.length > 0) options.exclude = sendExclude
      const res =
        Object.keys(options).length > 0
          ? await fetchPreview(inputUrl.value, selectedTarget.value, selectedPreset.value, options)
          : await fetchPreview(inputUrl.value, selectedTarget.value, selectedPreset.value)
      // 预校验类错误（bad_url_* / bad_preset / bad_groups / groups_disabled）：输入本身有问题，
      // 给链接无意义——停留状态②，展示文案可修改重试
      if (!res.ok && res.errorCode && PRECHECK_ERROR_CODES.has(res.errorCode)) {
        convertError.value = errorText(res.errorCode)
        return
      }
      // 上游类错误与成功：进状态③（ok:false 时结果页警示条 + 链接 + 「未通过校验，仍可试用」）
      result.value = res
      phase.value = 'result'
    } catch (e) {
      // fetchPreview 只抛 ApiUnreachableError（fetch 异常 / 15s abort / 非 200 / 坏 JSON）
      // 停留状态②，可重试
      convertError.value = e instanceof ApiUnreachableError
        ? errorText('api_unreachable')
        : errorText('api_unreachable')
      phase.value = 'converting'
    } finally {
      submitting.value = false
    }
  }

  function selectClient(target: string) {
    selectedTarget.value = target
  }

  function selectPreset(id: string) {
    selectedPreset.value = id
  }

  // 逃生口：一键锁定 clash 重新发起，不询问
  async function escapeToClash() {
    selectedTarget.value = 'clash'
    await submit()
  }

  function restart() {
    phase.value = 'idle'
    result.value = null
    convertError.value = null
    restoreNotice.value = null // 回填提示条属于上一次转换，回来不残留
    // 节点名单属旧链接：清掉并收起面板（重开即对新链接重 probe）；组意图与剔除清单保留
    //（换链接打词/删减仍适用）
    resetProbe()
    groupsOpen.value = false
    clearTimeout(reprobeTimer) // pending 防抖不得在 restart 后触发、重置已清空的状态
  }

  return {
    phase,
    inputUrl,
    selectedTarget,
    selectedPreset,
    advancedOpen,
    submitting,
    convertError,
    result,
    clients,
    presets,
    customGroupsAvailable,
    groupsOpen,
    groups,
    nodes,
    probeState,
    excluded,
    groupHits,
    templateGroups,
    draftKeywords,
    draftMode,
    draftPattern,
    restoreNotice,
    restoreNoticeText,
    loadClients,
    probeNodes,
    submit,
    selectClient,
    selectPreset,
    escapeToClash,
    undoRestore,
    clearPreferences,
    restart,
  }
}
