import type { NodeGroup } from './types'

// v1.6.0 分组偏好记忆——本地草稿层（localStorage，管「刷新不丢」）
// key `ssc.draft.v1`，value：{"subs": {"<trim 后订阅URL>": {snapshot}}}（spec 修订 8：last 字段已删）
// 写入门槛：快照只由用户对面板的编辑产生（spec 修订 1/3）——粘贴订阅本身不落盘，
// 否则「该 key 无快照」的服务端回填门会被空快照永久关死
export const DRAFT_KEY = 'ssc.draft.v1'
export const DRAFT_MAX_SUBS = 5
export const DRAFT_DEBOUNCE_MS = 300

export interface DraftSnapshot {
  target: string
  preset: string
  groups: NodeGroup[]
  excluded: string[]
  // 未确认的关键词 chips（GroupPanel 打词步骤中已加未成组的词；刷新后原样带出）
  draftKeywords: string[]
  // v1.6.1 增补：当前匹配模式与未成组的正则草稿（字段缺席 = 旧快照，按关键词模式降级）
  draftMode?: 'keyword' | 'regex'
  draftPattern?: string
  updatedAt: string
}

interface DraftStore {
  subs: Record<string, DraftSnapshot>
}

function isStringArray(v: unknown): v is string[] {
  return Array.isArray(v) && v.every((x) => typeof x === 'string')
}

function isNodeGroupArray(v: unknown): v is NodeGroup[] {
  return (
    Array.isArray(v) &&
    v.every(
      (g) =>
        !!g &&
        typeof g === 'object' &&
        typeof (g as Record<string, unknown>).name === 'string' &&
        isStringArray((g as Record<string, unknown>).keywords) &&
        // v1.6.1：可选 pattern，缺席或 string 均合法（正则组）
        ((g as Record<string, unknown>).pattern === undefined ||
          typeof (g as Record<string, unknown>).pattern === 'string') &&
        typeof (g as Record<string, unknown>).urlTest === 'boolean',
    )
  )
}

function isValidSnapshot(v: unknown): v is DraftSnapshot {
  if (!v || typeof v !== 'object' || Array.isArray(v)) return false
  const s = v as Record<string, unknown>
  return (
    typeof s.target === 'string' &&
    typeof s.preset === 'string' &&
    isNodeGroupArray(s.groups) &&
    isStringArray(s.excluded) &&
    isStringArray(s.draftKeywords) &&
    // v1.6.1：可选 draftMode / draftPattern，缺席合法；类型不对按坏条目剔除
    (s.draftMode === undefined || s.draftMode === 'keyword' || s.draftMode === 'regex') &&
    (s.draftPattern === undefined || typeof s.draftPattern === 'string') &&
    typeof s.updatedAt === 'string'
  )
}

function writeStore(store: DraftStore): void {
  try {
    localStorage.setItem(DRAFT_KEY, JSON.stringify(store))
  } catch {
    // 隐私模式写抛异常：静默丢弃（本会话内状态照常，只是不持久）
  }
}

// 读取自愈（spec 修订 6）：坏 JSON / subs 为数组或非对象 / 快照字段缺失或类型不对 →
// 剔除坏条目回写，不永久卡死该订阅的恢复
function readStore(): DraftStore {
  try {
    const raw = localStorage.getItem(DRAFT_KEY)
    if (!raw) return { subs: {} }
    const parsed: unknown = JSON.parse(raw)
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
      writeStore({ subs: {} })
      return { subs: {} }
    }
    const subsRaw = (parsed as Record<string, unknown>).subs
    if (!subsRaw || typeof subsRaw !== 'object' || Array.isArray(subsRaw)) {
      writeStore({ subs: {} })
      return { subs: {} }
    }
    const subs: Record<string, DraftSnapshot> = {}
    let dropped = false
    for (const [key, value] of Object.entries(subsRaw)) {
      if (isValidSnapshot(value)) subs[key] = value
      else dropped = true
    }
    if (dropped) writeStore({ subs })
    return { subs }
  } catch {
    return { subs: {} }
  }
}

// trim 后命中 sub key → 该订阅快照；无 → null（坏条目由 readStore 剔除）
export function getSnapshot(url: string): DraftSnapshot | null {
  const key = url.trim()
  if (!key) return null
  return readStore().subs[key] ?? null
}

// 写入/覆盖一订阅快照；超出容量按 updatedAt 淘汰最旧
export function saveSnapshot(url: string, snap: DraftSnapshot): void {
  const key = url.trim()
  if (!key) return
  const store = readStore()
  store.subs[key] = snap
  const keys = Object.keys(store.subs)
  if (keys.length > DRAFT_MAX_SUBS) {
    keys
      .sort((a, b) => (store.subs[a].updatedAt < store.subs[b].updatedAt ? -1 : 1))
      .slice(0, keys.length - DRAFT_MAX_SUBS)
      .forEach((k) => delete store.subs[k])
  }
  writeStore(store)
}

// 删除一订阅快照
export function removeSnapshot(url: string): void {
  const key = url.trim()
  if (!key) return
  const store = readStore()
  if (!(key in store.subs)) return
  delete store.subs[key]
  writeStore(store)
}
