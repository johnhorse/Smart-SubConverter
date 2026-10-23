import type { ClientEntry, HealthPreset, HealthResponse } from './types'

// 错误码 → 展示文案（api-contract.md 错误映射表，前端硬编码）
export const ERROR_MESSAGES: Record<string, string> = {
  bad_url_empty: '先粘贴你的订阅链接，再点「转换」',
  bad_url_format: '这不像一个订阅链接——检查一下是不是复制全了？',
  bad_url_private: '这个链接指向内网地址，转换服务够不着它',
  upstream_error: '转换没成功——可以试试展开「指定客户端」换一个，或稍后再来',
  upstream_timeout: '等太久没等到结果，再试一次看看？',
  backend_down: '转换引擎暂时没响应，稍后再试',
  api_unreachable: '服务暂时够不着，稍后再试',
}

export function errorText(code: string | null | undefined): string {
  if (code && ERROR_MESSAGES[code]) return ERROR_MESSAGES[code]
  // 契约外 code：按最通用的失败文案兜底
  return ERROR_MESSAGES.upstream_error
}

// 预校验类错误（D4 仲裁：给链接无意义）：停留状态②可修改重试，不进结果页
export const PRECHECK_ERROR_CODES = new Set(['bad_url_empty', 'bad_url_format', 'bad_url_private'])

// /api/clients 不可用时的降级数据（与契约示例同构，保证页面可用）
export const FALLBACK_CLIENTS: ClientEntry[] = [
  { id: 'auto', label: '自动识别', target: 'auto', verified: false },
  { id: 'clash', label: 'Clash / Mihomo', target: 'clash', verified: false },
  { id: 'surge', label: 'Surge', target: 'surge', verified: false },
  { id: 'quanx', label: 'Quantumult X', target: 'quanx', verified: false },
  { id: 'loon', label: 'Loon', target: 'loon', verified: false },
  { id: 'stash', label: 'Stash', target: 'stash', verified: false },
  { id: 'shadowrocket', label: 'Shadowrocket', target: 'shadowrocket', verified: false },
  { id: 'singbox', label: 'sing-box', target: 'singbox', verified: false },
  { id: 'v2rayn', label: 'v2rayN', target: 'v2rayn', verified: false },
]

// 相对时间描述：fresh/stale 文案里的 {X}
export function relativeDuration(fromIso: string, now: number = Date.now()): string {
  const ms = now - new Date(fromIso).getTime()
  if (Number.isNaN(ms)) return ''
  const minutes = Math.floor(ms / 60_000)
  if (minutes < 1) return '刚刚'
  if (minutes < 60) return `${minutes} 分钟`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours} 小时`
  const days = Math.floor(hours / 24)
  return `${days} 天`
}

export interface BadgeInfo {
  tone: 'fresh' | 'stale' | 'unknown'
  text: string
}

export function badgeInfo(health: HealthResponse, now: number = Date.now()): BadgeInfo {
  // V1 单预设，取第一个
  const preset: HealthPreset | undefined = health.presets[0]
  const backend = health.backendVersion === 'unknown' || !health.backendVersion
    ? ' · 引擎 unknown'
    : ` · 引擎 ${health.backendVersion}`
  if (!preset || preset.status === 'unknown') {
    return { tone: 'unknown', text: `规则库状态未知${backend}` }
  }
  if (preset.status === 'fresh') {
    const x = preset.lastModifiedAt ? relativeDuration(preset.lastModifiedAt, now) : '刚刚'
    return { tone: 'fresh', text: `规则内容 ${x}前更新${backend}` }
  }
  // stale：能给出「X 未更新」就说过期；拿不到 lastModifiedAt 视为抓取失败
  if (preset.lastModifiedAt) {
    const x = relativeDuration(preset.lastModifiedAt, now)
    return { tone: 'stale', text: `规则库可能过期（${x}未更新）${backend}` }
  }
  return { tone: 'stale', text: `规则源暂时够不着${backend}` }
}
