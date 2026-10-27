import type { ClientEntry, HealthPreset, HealthResponse, PresetInfo } from './types'

// 错误码 → 展示文案（api-contract.md 错误映射表，前端硬编码）
export const ERROR_MESSAGES: Record<string, string> = {
  bad_url_empty: '先粘贴你的订阅链接，再点「转换」',
  bad_url_format: '这不像一个订阅链接——检查一下是不是复制全了？',
  bad_url_private: '这个链接指向内网地址，转换服务够不着它',
  bad_preset: '这个模板不可用，重新选一个试试',
  bad_groups: '分组信息有误——组名和关键词不超过 32 字，别用特殊符号，同名分组只留一个',
  groups_disabled: '当前部署或所选模板不支持自定义分组',
  upstream_error: '转换没成功——可以试试展开「指定客户端」换一个，或稍后再来',
  upstream_timeout: '等太久没等到结果，再试一次看看？',
  backend_down: '转换引擎暂时没响应，稍后再试',
  store_error: '短链生成失败了——这次给的是普通链接（含订阅信息，注意别转发）；请部署者检查 /data 卷',
  api_unreachable: '服务暂时够不着，稍后再试',
}

export function errorText(code: string | null | undefined): string {
  if (code && ERROR_MESSAGES[code]) return ERROR_MESSAGES[code]
  // 契约外 code：按最通用的失败文案兜底
  return ERROR_MESSAGES.upstream_error
}

// 预校验类错误（D4 仲裁：给链接无意义）：停留状态②可修改重试，不进结果页
// bad_preset 同属预校验（不调上游）：停留状态②换模板重试，与文案「重新选一个试试」呼应
export const PRECHECK_ERROR_CODES = new Set([
  'bad_url_empty',
  'bad_url_format',
  'bad_url_private',
  'bad_preset',
  // v1.5.0：分组预校验错（不调上游、丢 groups 照常建短链），停留状态②可改组重试
  'bad_groups',
  'groups_disabled',
])

// v1.5.0 分组上限与字段校验（对齐契约 bad_groups 规则：trim 非空 / ≤32 rune / 禁反引号与控制字符）
export const GROUPS_MAX = 10
const GROUP_BAD_CHARS = /[\u0000-\u001F\u007F`]/

export function groupFieldInvalid(value: string): boolean {
  const t = value.trim()
  return t === '' || [...t].length > 32 || GROUP_BAD_CHARS.test(t)
}

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

// /api/clients 不可用时的降级数据（与契约 presets 三档同构）；default 永远第一
export const FALLBACK_PRESETS: PresetInfo[] = [
  {
    id: 'default',
    name: 'ACL4SSR 默认',
    url: 'https://raw.githubusercontent.com/ACL4SSR/ACL4SSR/master/Clash/config/ACL4SSR_Online.ini',
  },
  {
    id: 'full_multimode',
    name: '地区分组 + AI 分流',
    url: 'https://raw.githubusercontent.com/ACL4SSR/ACL4SSR/master/Clash/config/ACL4SSR_Online_Full_MultiMode.ini',
  },
  {
    id: 'full_adblockplus',
    name: '全量 + 去广告',
    url: 'https://raw.githubusercontent.com/ACL4SSR/ACL4SSR/master/Clash/config/ACL4SSR_Online_Full_AdblockPlus.ini',
  },
]

// 选中档下方的描述文案（前端硬编码 id→文案，规划定案第 4 条）
export const PRESET_DESCRIPTIONS: Record<string, string> = {
  default: '按用途分组：国外媒体 / 电报 / 微软 / 苹果…最简',
  full_multimode: '按用途 + 地区（港日美台新韩）分组，含 AI 平台组——ChatGPT / Claude / Gemini 可定向走指定地区',
  full_adblockplus: '规则最全，附带广告拦截',
}

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
  // 快照档：新鲜度由快照刷新时间驱动；lastRefreshedAt 未实装前回退 lastSuccessAt（本地化档两字段同义）
  if (preset.source === 'snapshot') {
    const refreshedAt = preset.lastRefreshedAt ?? preset.lastSuccessAt
    if (preset.status === 'fresh') {
      const x = refreshedAt ? relativeDuration(refreshedAt, now) : '刚刚'
      return { tone: 'fresh', text: `规则快照 · ${x}前更新${backend}` }
    }
    if (refreshedAt) {
      return { tone: 'stale', text: `规则快照已 ${relativeDuration(refreshedAt, now)}未刷新${backend}` }
    }
    return { tone: 'stale', text: `规则源暂时够不着${backend}` }
  }
  // 豁免档（source: remote；字段缺席 = v1.3.0 前旧 sidecar 同样落此分支）：沿用 v1.0 远端文案，
  // {X} 取 lastModifiedAt——豁免档 status 判定本就以它为准
  if (preset.status === 'fresh') {
    const x = preset.lastModifiedAt ? relativeDuration(preset.lastModifiedAt, now) : '刚刚'
    return { tone: 'fresh', text: `规则内容 ${x}前更新${backend}` }
  }
  // stale：能给出「X 未更新」就说过期；拿不到 lastModifiedAt 视为抓取失败
  if (preset.lastModifiedAt) {
    return { tone: 'stale', text: `规则库可能过期（${relativeDuration(preset.lastModifiedAt, now)}未更新）${backend}` }
  }
  return { tone: 'stale', text: `规则源暂时够不着${backend}` }
}
