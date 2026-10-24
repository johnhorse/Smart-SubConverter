import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiUnreachableError, fetchPreview } from './api'
import { ERROR_MESSAGES, errorText } from './constants'

// mock fetch：pending 直到 abort signal 触发 reject（模拟真实 fetch 对 signal 的响应）
function stubFetchWithPending() {
  return vi.fn(
    (_input: RequestInfo | URL, init?: RequestInit) =>
      new Promise<Response>((_resolve, reject) => {
        init?.signal?.addEventListener('abort', () =>
          reject(new DOMException('The operation was aborted.', 'AbortError')),
        )
      }),
  )
}

describe('fetchPreview', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })
  afterEach(() => {
    vi.useRealTimers()
    vi.unstubAllGlobals()
  })

  it('200 时解析并返回 JSON，body 带 url 与 target 与 preset', async () => {
    const body = {
      ok: true,
      target: 'clash',
      preset: { id: 'full_multimode', name: '地区分组 + AI 分流' },
      errorCode: null,
      message: '',
      link: 'http://x/sub?target=auto',
    }
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify(body), { status: 200 })))
    const res = await fetchPreview('https://example.com/sub', 'auto', 'full_multimode')
    expect(res).toEqual(body)
    expect(vi.mocked(fetch)).toHaveBeenCalledWith(
      '/api/preview',
      expect.objectContaining({
        method: 'POST',
        body: JSON.stringify({
          url: 'https://example.com/sub',
          target: 'auto',
          preset: 'full_multimode',
        }),
      }),
    )
  })

  it('非 200 抛 ApiUnreachableError', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('Bad Gateway', { status: 502 })))
    await expect(fetchPreview('https://example.com/sub', 'auto', 'default')).rejects.toBeInstanceOf(ApiUnreachableError)
  })

  it('fetch 网络异常抛 ApiUnreachableError', async () => {
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new TypeError('Failed to fetch')))
    await expect(fetchPreview('https://example.com/sub', 'auto', 'default')).rejects.toBeInstanceOf(ApiUnreachableError)
  })

  it('15s 超时 abort 抛 ApiUnreachableError', async () => {
    vi.stubGlobal('fetch', stubFetchWithPending())
    const p = fetchPreview('https://example.com/sub', 'auto', 'default')
    vi.advanceTimersByTime(15_000)
    await expect(p).rejects.toBeInstanceOf(ApiUnreachableError)
  })

  it('成功后不再保留 abort 定时器（14.9s 内完成不触发 abort）', async () => {
    const body = { ok: true, target: 'clash', preset: null, errorCode: null, message: '', link: 'http://x/sub' }
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify(body), { status: 200 })))
    const res = await fetchPreview('https://example.com/sub', 'auto', 'default')
    expect(res.ok).toBe(true)
    vi.advanceTimersByTime(15_000) // 若定时器未清理，此处可能抛未处理 rejection
  })
})

describe('错误码 → 前端文案映射（契约全表）', () => {
  it.each([
    ['bad_url_empty', '先粘贴你的订阅链接，再点「转换」'],
    ['bad_url_format', '这不像一个订阅链接——检查一下是不是复制全了？'],
    ['bad_url_private', '这个链接指向内网地址，转换服务够不着它'],
    ['bad_preset', '这个模板不可用，重新选一个试试'],
    ['upstream_error', '转换没成功——可以试试展开「指定客户端」换一个，或稍后再来'],
    ['upstream_timeout', '等太久没等到结果，再试一次看看？'],
    ['backend_down', '转换引擎暂时没响应，稍后再试'],
    ['api_unreachable', '服务暂时够不着，稍后再试'],
  ])('%s → %s', (code, text) => {
    expect(ERROR_MESSAGES[code]).toBe(text)
  })

  it('契约外 code 兜底为通用失败文案', () => {
    expect(errorText('what_ever')).toBe(ERROR_MESSAGES.upstream_error)
    expect(errorText(null)).toBe(ERROR_MESSAGES.upstream_error)
  })
})
