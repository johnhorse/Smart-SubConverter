import { ref } from 'vue'
import { ApiUnreachableError, fetchClients, fetchPreview } from './api'
import { FALLBACK_CLIENTS, errorText } from './constants'
import type { ClientEntry, PreviewResponse } from './types'

export type Phase = 'idle' | 'converting' | 'result'

export function useConverter() {
  const phase = ref<Phase>('idle')
  const inputUrl = ref('')
  const selectedTarget = ref('auto')
  const advancedOpen = ref(false)
  // 状态②的请求中标志：出错停驻时置回 false，按钮恢复可点（可重试）
  const submitting = ref(false)
  const convertError = ref<string | null>(null)
  const result = ref<PreviewResponse | null>(null)
  const clients = ref<ClientEntry[]>(FALLBACK_CLIENTS)

  async function loadClients() {
    try {
      const res = await fetchClients()
      if (Array.isArray(res.clients) && res.clients.length > 0) clients.value = res.clients
    } catch {
      // 静默降级：保持 FALLBACK_CLIENTS，页面仍可用
    }
  }

  async function submit() {
    if (submitting.value) return
    submitting.value = true
    phase.value = 'converting'
    advancedOpen.value = true
    convertError.value = null
    try {
      const res = await fetchPreview(inputUrl.value, selectedTarget.value)
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
    advancedOpen,
    submitting,
    convertError,
    result,
    clients,
    loadClients,
    submit,
    selectClient,
    escapeToClash,
    restart,
  }
}
