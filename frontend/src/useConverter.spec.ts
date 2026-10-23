import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiUnreachableError, fetchPreview } from './api'
import { ERROR_MESSAGES, FALLBACK_CLIENTS } from './constants'
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
  errorCode: null,
  message: '',
  link: 'http://192.168.1.10:8080/sub?target=auto&url=xx',
}

const badResponse: PreviewResponse = {
  ok: false,
  target: 'clash',
  errorCode: 'bad_url_format',
  message: 'Extended said no',
  link: 'http://192.168.1.10:8080/sub?target=auto&url=xx',
}

function setup() {
  const conv = useConverter()
  return conv
}

describe('useConverter 状态机', () => {
  beforeEach(() => {
    vi.mocked(fetchPreview).mockReset()
  })

  it('初始为状态①（idle），chips 默认选中 auto，高级区收起', () => {
    const conv = setup()
    expect(conv.phase.value).toBe('idle')
    expect(conv.selectedTarget.value).toBe('auto')
    expect(conv.advancedOpen.value).toBe(false)
    expect(conv.clients.value).toEqual(FALLBACK_CLIENTS)
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

  it('ok:false 也进入状态③，errorCode 原样保留（结果页展示警示条）', async () => {
    const conv = setup()
    conv.inputUrl.value = 'not-a-url'
    vi.mocked(fetchPreview).mockResolvedValue(badResponse)

    await conv.submit()
    expect(conv.phase.value).toBe('result')
    expect(conv.result.value?.ok).toBe(false)
    expect(conv.result.value?.errorCode).toBe('bad_url_format')
    expect(conv.result.value?.link).toContain('/sub?') // 链接照常展示（D4）
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
    expect(fetchPreview).toHaveBeenLastCalledWith('https://example.com/sub', 'clash')
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
    expect(fetchPreview).toHaveBeenCalledWith('https://example.com/sub', 'singbox')
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
