// 与 docs/implementation/api-contract.md 严格一致（字段名、可空性）

export interface ClientEntry {
  id: string
  label: string
  target: string
  verified: boolean
}

export interface PresetInfo {
  id: string
  name: string
  url: string
}

export interface ClientsResponse {
  clients: ClientEntry[]
  presets: PresetInfo[]
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
}

export type PresetStatus = 'fresh' | 'stale' | 'unknown'

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
