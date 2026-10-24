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
}

export type PresetStatus = 'fresh' | 'stale' | 'unknown'

export interface HealthPreset {
  name: string
  url: string
  lastModifiedAt: string | null
  lastSuccessAt: string
  status: PresetStatus
}

export interface HealthResponse {
  backendVersion: string
  presets: HealthPreset[]
}
