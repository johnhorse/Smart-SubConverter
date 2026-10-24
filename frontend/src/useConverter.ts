import { ref } from 'vue'
import { ApiUnreachableError, fetchClients, fetchPreview } from './api'
import { FALLBACK_CLIENTS, FALLBACK_PRESETS, PRECHECK_ERROR_CODES, errorText } from './constants'
import type { ClientEntry, PreviewResponse, PresetInfo } from './types'

export type Phase = 'idle' | 'converting' | 'result'

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

  async function loadClients() {
    try {
      const res = await fetchClients()
      if (Array.isArray(res.clients) && res.clients.length > 0) clients.value = res.clients
      if (Array.isArray(res.presets) && res.presets.length > 0) presets.value = res.presets
    } catch {
      // 静默降级：保持 FALLBACK_CLIENTS / FALLBACK_PRESETS，页面仍可用
    }
  }

  async function submit() {
    if (submitting.value) return
    submitting.value = true
    phase.value = 'converting'
    advancedOpen.value = true
    convertError.value = null
    try {
      const res = await fetchPreview(inputUrl.value, selectedTarget.value, selectedPreset.value)
      // 预校验类错误（bad_url_*）：输入本身有问题，给链接无意义——停留状态②，展示文案可修改重试
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
    loadClients,
    submit,
    selectClient,
    selectPreset,
    escapeToClash,
    restart,
  }
}
