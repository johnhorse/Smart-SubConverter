import { ref } from 'vue'
import { fetchHealth } from './api'
import { badgeInfo, type BadgeInfo } from './constants'

export function useHealth() {
  const badge = ref<BadgeInfo | null>(null)

  async function init() {
    try {
      const health = await fetchHealth()
      badge.value = badgeInfo(health)
    } catch {
      // 接口失败静默降级为状态未知
      badge.value = { tone: 'unknown', text: '规则库状态未知' }
    }
  }

  return { badge, init }
}
