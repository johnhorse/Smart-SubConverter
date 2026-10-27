import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiUnreachableError, fetchPreview, type PreviewOptions } from './api'
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
      link: 'http://x/s/Ab3xY9qZ',
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
    const body = { ok: true, target: 'clash', preset: null, errorCode: null, message: '', link: 'http://x/s/Ab3xY9qZ' }
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
    ['bad_groups', '分组信息有误——组名和关键词不超过 32 字，别用特殊符号，同名分组只留一个'],
    ['groups_disabled', '当前部署或所选模板不支持自定义分组'],
    ['upstream_error', '转换没成功——可以试试展开「指定客户端」换一个，或稍后再来'],
    ['upstream_timeout', '等太久没等到结果，再试一次看看？'],
    ['backend_down', '转换引擎暂时没响应，稍后再试'],
    ['store_error', '短链生成失败了——这次给的是普通链接（含订阅信息，注意别转发）；请部署者检查 /data 卷'],
    ['api_unreachable', '服务暂时够不着，稍后再试'],
  ])('%s → %s', (code, text) => {
    expect(ERROR_MESSAGES[code]).toBe(text)
  })

  it('契约外 code 兜底为通用失败文案', () => {
    expect(errorText('what_ever')).toBe(ERROR_MESSAGES.upstream_error)
    expect(errorText(null)).toBe(ERROR_MESSAGES.upstream_error)
  })
})

describe('fetchPreview options（v1.5.0 probe / groups 请求形状）', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })
  afterEach(() => {
    vi.useRealTimers()
    vi.unstubAllGlobals()
  })

  function stubOk(body: object) {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify(body), { status: 200 })))
  }

  it('options.probe → body 带 probe:true，probe 响应 nodes 透传（link 契约上恒 null，前端不读）', async () => {
    stubOk({ ok: true, target: 'auto', preset: null, errorCode: null, message: '', link: '', deepLink: null, nodes: ['香港专线 01'] })
    const res = await fetchPreview('https://example.com/sub', 'auto', 'default', { probe: true })
    expect(res.nodes).toEqual(['香港专线 01'])
    expect(vi.mocked(fetch)).toHaveBeenCalledWith(
      '/api/preview',
      expect.objectContaining({
        method: 'POST',
        body: JSON.stringify({
          url: 'https://example.com/sub',
          target: 'auto',
          preset: 'default',
          probe: true,
        }),
      }),
    )
  })

  it('options.groups 非空 → body 带 groups（键序 name→keyword→urlTest，与 groupsJSON 规范一致）', async () => {
    stubOk({ ok: true, target: 'clash', preset: null, errorCode: null, message: '', link: 'http://x/s/Ab3xY9qZ', deepLink: null })
    await fetchPreview('https://example.com/sub', 'auto', 'default', {
      groups: [{ name: '专线', keyword: '专线', urlTest: false }],
    })
    expect(vi.mocked(fetch)).toHaveBeenCalledWith(
      '/api/preview',
      expect.objectContaining({
        body: JSON.stringify({
          url: 'https://example.com/sub',
          target: 'auto',
          preset: 'default',
          groups: [{ name: '专线', keyword: '专线', urlTest: false }],
        }),
      }),
    )
  })

  it.each([
    ['无 options', undefined],
    ['空 groups', { groups: [] }],
  ])('%s → body 恰三字段（存量请求字节不变，v1.4.0 逐字节回归路径）', async (_label, options) => {
    stubOk({ ok: true, target: 'clash', preset: null, errorCode: null, message: '', link: 'http://x/s/Ab3xY9qZ', deepLink: null })
    await fetchPreview('https://example.com/sub', 'auto', 'default', options as PreviewOptions | undefined)
    expect(vi.mocked(fetch)).toHaveBeenCalledWith(
      '/api/preview',
      expect.objectContaining({
        body: JSON.stringify({ url: 'https://example.com/sub', target: 'auto', preset: 'default' }),
      }),
    )
  })
})
