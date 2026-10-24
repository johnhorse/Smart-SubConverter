import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiUnreachableError, fetchClients, fetchPreview } from './api'
import { ERROR_MESSAGES, FALLBACK_CLIENTS, FALLBACK_PRESETS } from './constants'
import type { PreviewResponse } from './types'
import { useConverter } from './useConverter'

vi.mock('./api', () => ({
  ApiUnreachableError: class extends Error {
    readonly errorCode = 'api_unreachable'
  },
  fetchPreview: vi.fn(),
  fetchClients: vi.fn().mockResolvedValue({ clients: [], presets: [] }),
}))

const okResponse: PreviewResponse = {
  ok: true,
  target: 'clash',
  preset: { id: 'default', name: 'ACL4SSR 默认' },
  errorCode: null,
  message: '',
  link: 'http://192.168.1.10:8080/s/Ab3xY9qZ',
  deepLink: null,
}

const badResponse: PreviewResponse = {
  ok: false,
  target: 'clash',
  preset: { id: 'default', name: 'ACL4SSR 默认' },
  errorCode: 'upstream_error',
  message: 'Invalid request',
  link: 'http://192.168.1.10:8080/s/Ab3xY9qZ',
  deepLink: null,
}

function setup() {
  const conv = useConverter()
  return conv
}

describe('useConverter 状态机', () => {
  beforeEach(() => {
    vi.mocked(fetchPreview).mockReset()
  })

  it('初始为状态①（idle），chips 默认选中 auto，模板默认选中 default，高级区收起', () => {
    const conv = setup()
    expect(conv.phase.value).toBe('idle')
    expect(conv.selectedTarget.value).toBe('auto')
    expect(conv.selectedPreset.value).toBe('default')
    expect(conv.advancedOpen.value).toBe(false)
    expect(conv.clients.value).toEqual(FALLBACK_CLIENTS)
    expect(conv.presets.value).toEqual(FALLBACK_PRESETS)
  })

  it('三状态流转：idle → converting（submitting 锁死）→ result', async () => {
    const conv = setup()
    conv.inputUrl.value = 'https://example.com/sub'
    let resolvePreview!: (v: PreviewResponse) => void
    vi.mocked(fetchPreview).mockReturnValue(
      new Promise((r) => {
        resolvePreview = r
      }),
    )

    const pending = conv.submit()
    expect(conv.phase.value).toBe('converting')
    expect(conv.submitting.value).toBe(true)
    // 请求中折叠区自动展开（状态② chips 展开态）
    expect(conv.advancedOpen.value).toBe(true)

    resolvePreview(okResponse)
    await pending
    expect(conv.phase.value).toBe('result')
    expect(conv.result.value).toEqual(okResponse)
    expect(conv.submitting.value).toBe(false)
    expect(conv.convertError.value).toBeNull()
  })

  it('请求中重复 submit 不发第二个请求（按钮锁死兜底）', async () => {
    const conv = setup()
    vi.mocked(fetchPreview).mockReturnValue(new Promise(() => {}))
    const first = conv.submit()
    void conv.submit()
    void conv.submit()
    expect(fetchPreview).toHaveBeenCalledTimes(1)
    void first
  })

  it('请求失败（api_unreachable）停留状态②，显示对应文案，可重试成功', async () => {
    const conv = setup()
    conv.inputUrl.value = 'https://example.com/sub'
    vi.mocked(fetchPreview)
      .mockRejectedValueOnce(new ApiUnreachableError())
      .mockResolvedValueOnce(okResponse)

    await conv.submit()
    expect(conv.phase.value).toBe('converting')
    expect(conv.submitting.value).toBe(false) // 可重试：按钮恢复可点
    expect(conv.convertError.value).toBe(ERROR_MESSAGES.api_unreachable)

    await conv.submit()
    expect(conv.phase.value).toBe('result')
    expect(conv.convertError.value).toBeNull()
  })

  it('上游类 ok:false 也进入状态③，errorCode 原样保留（结果页展示警示条）', async () => {
    const conv = setup()
    conv.inputUrl.value = 'https://example.com/broken'
    vi.mocked(fetchPreview).mockResolvedValue(badResponse)

    await conv.submit()
    expect(conv.phase.value).toBe('result')
    expect(conv.result.value?.ok).toBe(false)
    expect(conv.result.value?.errorCode).toBe('upstream_error')
    expect(conv.result.value?.link).toContain('/s/') // 链接照常展示（D4）
  })

  it.each([
    ['bad_url_empty', ''],
    ['bad_url_format', 'not-a-url'],
    ['bad_url_private', 'http://192.168.1.5/sub'],
  ])('预校验错误 %s 停留状态②，不进结果页不展示链接（D4 仲裁）', async (code, url) => {
    const conv = setup()
    conv.inputUrl.value = url
    vi.mocked(fetchPreview).mockResolvedValue({
      ok: false,
      target: 'clash',
      preset: null,
      errorCode: code,
      message: '',
      link: 'http://192.168.1.10:8080/s/xK9badQm',
      deepLink: null,
    })

    await conv.submit()
    expect(conv.phase.value).toBe('converting')
    expect(conv.submitting.value).toBe(false)
    expect(conv.result.value).toBeNull()
    expect(conv.convertError.value).toBe(ERROR_MESSAGES[code])
    expect(conv.advancedOpen.value).toBe(true) // chips 保持展开，可修改后重试
  })

  it('bad_preset 停留状态②：模板文案提示重选，不进结果页', async () => {
    const conv = setup()
    conv.inputUrl.value = 'https://example.com/sub'
    conv.selectPreset('full_multimode')
    vi.mocked(fetchPreview).mockResolvedValue({
      ok: false,
      target: 'clash',
      preset: null,
      errorCode: 'bad_preset',
      message: '',
      link: 'http://192.168.1.10:8080/s/xK9badQm',
      deepLink: null,
    })

    await conv.submit()
    expect(conv.phase.value).toBe('converting')
    expect(conv.result.value).toBeNull()
    expect(conv.convertError.value).toBe(ERROR_MESSAGES.bad_preset)
    expect(conv.convertError.value).toBe('这个模板不可用，重新选一个试试')
    expect(conv.advancedOpen.value).toBe(true) // 模板 chips 展开可重选
  })

  it('逃生口：一键改选 target=clash 并自动重新发起请求', async () => {
    const conv = setup()
    conv.inputUrl.value = 'https://example.com/sub'
    vi.mocked(fetchPreview).mockResolvedValue(okResponse)

    await conv.submit()
    expect(conv.phase.value).toBe('result')
    expect(conv.selectedTarget.value).toBe('auto')

    await conv.escapeToClash()
    expect(conv.selectedTarget.value).toBe('clash')
    expect(fetchPreview).toHaveBeenLastCalledWith('https://example.com/sub', 'clash', 'default')
    expect(conv.phase.value).toBe('result')
  })

  it('chips 单选：selectClient 切换 target', () => {
    const conv = setup()
    expect(conv.selectedTarget.value).toBe('auto')
    conv.selectClient('surge')
    expect(conv.selectedTarget.value).toBe('surge')
    conv.selectClient('clash')
    expect(conv.selectedTarget.value).toBe('clash')
  })

  it('选了客户端后 submit 带该 target', async () => {
    const conv = setup()
    conv.inputUrl.value = 'https://example.com/sub'
    conv.selectClient('singbox')
    vi.mocked(fetchPreview).mockResolvedValue(okResponse)
    await conv.submit()
    expect(fetchPreview).toHaveBeenCalledWith('https://example.com/sub', 'singbox', 'default')
  })

  it('模板单选：selectPreset 切换 preset，submit 带该 preset id', async () => {
    const conv = setup()
    expect(conv.selectedPreset.value).toBe('default')
    conv.selectPreset('full_multimode')
    expect(conv.selectedPreset.value).toBe('full_multimode')

    conv.inputUrl.value = 'https://example.com/sub'
    vi.mocked(fetchPreview).mockResolvedValue(okResponse)
    await conv.submit()
    expect(fetchPreview).toHaveBeenCalledWith('https://example.com/sub', 'auto', 'full_multimode')
  })

  it('loadClients 拉取 /api/clients 的 presets 数组（不前端硬编码）', async () => {
    const remotePresets = [
      { id: 'default', name: '远端默认', url: 'https://example.com/a.ini' },
      { id: 'full_multimode', name: '远端分组', url: 'https://example.com/b.ini' },
    ]
    vi.mocked(fetchClients).mockResolvedValueOnce({ clients: [], presets: remotePresets })
    const conv = setup()
    await conv.loadClients()
    expect(conv.presets.value).toEqual(remotePresets)
    expect(conv.clients.value).toEqual(FALLBACK_CLIENTS) // clients 空数组时保持降级
  })

  it('restart 从状态③回到状态①，保留输入与选择', async () => {
    const conv = setup()
    conv.inputUrl.value = 'https://example.com/sub'
    vi.mocked(fetchPreview).mockResolvedValue(okResponse)
    await conv.submit()
    expect(conv.phase.value).toBe('result')

    conv.restart()
    expect(conv.phase.value).toBe('idle')
    expect(conv.result.value).toBeNull()
    expect(conv.inputUrl.value).toBe('https://example.com/sub')
  })
})
