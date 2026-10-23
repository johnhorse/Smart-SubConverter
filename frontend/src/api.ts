import type { ClientsResponse, HealthResponse, PreviewResponse } from './types'

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

export function fetchPreview(url: string, target: string): Promise<PreviewResponse> {
  return fetchJson<PreviewResponse>(
    '/api/preview',
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ url, target }),
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
