import { computed, ref, watch } from 'vue'
import { ApiUnreachableError, fetchClients, fetchPreview } from './api'
import { FALLBACK_CLIENTS, FALLBACK_PRESETS, PRECHECK_ERROR_CODES, errorText } from './constants'
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
  // 上次成功 probe 的 url：同 url 重新展开面板时复用结果不重发（F6）
  let probedUrl = ''
  async function probeNodes() {
    if (!groupsOpen.value) return // 双保险：收起/restart 清了定时器后，在途回调不得再发
    if (!inputUrl.value.trim()) return
    const seq = ++probeSeq
    probeState.value = 'loading'
    try {
      const res = await fetchPreview(inputUrl.value, selectedTarget.value, selectedPreset.value, { probe: true })
      if (seq !== probeSeq) return // 过期响应丢弃（换 url 后旧 probe 慢回）
      if (res.ok && Array.isArray(res.nodes)) {
        nodes.value = res.nodes
        probeState.value = 'ready'
        probedUrl = inputUrl.value
      } else {
        probeState.value = 'failed'
      }
    } catch {
      if (seq !== probeSeq) return
      probeState.value = 'failed'
    }
  }

  // 展开分组面板 → probe（url 非空才发；同 url 已 ready 复用不重发）；收起 → 清
  // pending 防抖（收起后 400ms 内不得再触发 probe / 重置面板状态）
  let reprobeTimer: ReturnType<typeof setTimeout> | undefined
  watch(groupsOpen, (open) => {
    if (open) {
      if (probeState.value === 'ready' && probedUrl === inputUrl.value) return
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
          nodes.value = []
          probeState.value = 'idle'
          return
        }
        void probeNodes()
      }, URL_REPROBE_DEBOUNCE_MS)
    },
    { flush: 'sync' },
  )

  async function submit() {
    if (submitting.value) return
    submitting.value = true
    phase.value = 'converting'
    advancedOpen.value = true
    convertError.value = null
    try {
      // 空组不带第 4 参（存量请求字节不变）；能力位失效（切到不支持档）时剔除 groups
      // 发送——等价无组转换正常成功，残留组不会撞 groups_disabled 锁死（F1）
      const sendGroups = customGroupsAvailable.value ? groups.value : []
      const res =
        sendGroups.length > 0
          ? await fetchPreview(inputUrl.value, selectedTarget.value, selectedPreset.value, {
              groups: sendGroups,
            })
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
    // 节点名单属旧链接：清掉并收起面板（重开即对新链接重 probe）；组意图保留（换链接打词仍适用）
    nodes.value = []
    probeState.value = 'idle'
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
    loadClients,
    probeNodes,
    submit,
    selectClient,
    selectPreset,
    escapeToClash,
    restart,
  }
}
