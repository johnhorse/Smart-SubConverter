// 与 docs/implementation/api-contract.md 严格一致（字段名、可空性）

export interface ClientEntry {
  id: string
  label: string
  target: string
  verified: boolean
}

// v1.5.0 增补：模板自带组清单条目（locked = 路由骨架/安全组/回退目标，不可删）
export interface PresetGroup {
  name: string
  locked: boolean
}

export interface PresetInfo {
  id: string
  name: string
  url: string
  // v1.5.0 能力位：快照本地化档 true / 豁免档 false；字段缺席 = 旧 sidecar，按 false 降级
  customGroups?: boolean
  // v1.5.0 增补：模板自带组清单（模板原序）；豁免档或快照不可用 → []；字段缺席按 [] 降级
  groups?: PresetGroup[]
}

export interface ClientsResponse {
  clients: ClientEntry[]
  presets: PresetInfo[]
  // v1.5.0 部署级能力位：出树 DATA_DIR 为 false；字段缺席 = 旧 sidecar，按 false 降级
  capabilities?: { customGroups?: boolean }
}

// v1.5.0 分组意图（groupsJSON 键序 name→keywords→urlTest 由序列化保证）
// v1.6.1 增补：可选 pattern（正则组 keywords 为 []；字段缺席 = 关键词组，存量兼容）
export interface NodeGroup {
  name: string
  keywords: string[]
  pattern?: string
  urlTest: boolean
}

// preview 响应回显的生效档位（bad_preset 时为 null）
export interface AppliedPreset {
  id: string
  name: string
}

export interface PreviewResponse {
  ok: boolean
  target: string
  preset: AppliedPreset | null
  errorCode: string | null
  message: string
  link: string
  deepLink: string | null
  // v1.5.0 probe 模式返回节点名列表；非 probe 恒 []；旧 sidecar 字段缺席按 [] 容错
  nodes?: string[]
  // v1.5.0 增补：probe 模式模板组命中数（仅自带节点正则的组）；ok:false/能力位 false 恒 {}；
  // 旧 sidecar 字段缺席按 {} 容错
  groupHits?: Record<string, number>
}

export type PresetStatus = 'fresh' | 'stale' | 'unknown'

// v1.6.0 偏好查询响应（group-preferences.md 接口节）：null = 无存档；
// exclude 只回请求 preset 档的（模板组删减按 sub_key + preset 记）
export interface PreferencesQueryResponse {
  custom_groups: NodeGroup[] | null
  exclude: string[] | null
}

// v1.6.0 偏好删除响应：该订阅全部档案（自定义组 + 各档 exclude）
export interface PreferencesDeleteResponse {
  deleted_custom: boolean
  deleted_exclude: number
}

export type PresetSource = 'snapshot' | 'remote'

export interface HealthPreset {
  name: string
  url: string
  // 档位标识：本地化档 snapshot / 豁免档 remote；字段缺席 = v1.3.0 前旧 sidecar，按 remote 处理
  source?: PresetSource
  lastModifiedAt: string | null
  // v1.3.0 快照刷新时刻；豁免档（PRESET_URL 覆盖）与未实装 sidecar 的响应里无此字段
  lastRefreshedAt?: string | null
  // 从未成功抓取/刷新时为 null（sidecar 实测输出形态）
  lastSuccessAt: string | null
  status: PresetStatus
}

export interface HealthResponse {
  backendVersion: string
  presets: HealthPreset[]
}
