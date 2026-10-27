import type { ClientsResponse, HealthResponse, NodeGroup, PreviewResponse } from './types'

export const PREVIEW_TIMEOUT_MS = 15_000
export const HEALTHY_TIMEOUT_MS = 8_000

// /api/preview 本身失败（fetch 异常 / 超时 abort / 非 200 / 响应不是合法 JSON）统一归类
export class ApiUnreachableError extends Error {
  readonly errorCode = 'api_unreachable'
  constructor() {
    super('api_unreachable')
  }
}

async function fetchJson<T>(path: string, init: RequestInit, timeoutMs: number): Promise<T> {
  const controller = new AbortController()
  const timer = setTimeout(() => controller.abort(), timeoutMs)
  let res: Response
  try {
    res = await fetch(path, { ...init, signal: controller.signal })
  } catch {
    throw new ApiUnreachableError()
  } finally {
    clearTimeout(timer)
  }
  if (!res.ok) throw new ApiUnreachableError()
  try {
    return (await res.json()) as T
  } catch {
    throw new ApiUnreachableError()
  }
}

export interface PreviewOptions {
  // probe:true = 探测模式：不建链零副作用，响应 nodes 为节点名列表（link/deepLink 恒 null）
  probe?: boolean
  // 分组意图；空组不带字段（存量请求字节不变）
  groups?: NodeGroup[]
}

export function fetchPreview(
  url: string,
  target: string,
  preset: string,
  options: PreviewOptions = {},
): Promise<PreviewResponse> {
  const body: Record<string, unknown> = { url, target, preset }
  if (options.probe) body.probe = true
  if (options.groups && options.groups.length > 0) body.groups = options.groups
  return fetchJson<PreviewResponse>(
    '/api/preview',
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    },
    PREVIEW_TIMEOUT_MS,
  )
}

export function fetchClients(): Promise<ClientsResponse> {
  return fetchJson<ClientsResponse>('/api/clients', { method: 'GET' }, HEALTHY_TIMEOUT_MS)
}

export function fetchHealth(): Promise<HealthResponse> {
  return fetchJson<HealthResponse>('/api/health', { method: 'GET' }, HEALTHY_TIMEOUT_MS)
}
