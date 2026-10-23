import { afterEach, describe, expect, it, vi } from 'vitest'
import { copyText } from './copy'

describe('copyText', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
    vi.restoreAllMocks()
  })

  it('navigator.clipboard 可用时优先使用', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(navigator, 'clipboard', {
      value: { writeText },
      configurable: true,
    })
    try {
      const ok = await copyText('https://example.com/sub?token=x')
      expect(ok).toBe(true)
      expect(writeText).toHaveBeenCalledWith('https://example.com/sub?token=x')
    } finally {
      Object.defineProperty(navigator, 'clipboard', { value: undefined, configurable: true })
    }
  })

  it('clipboard 不可用（http 非安全上下文）时回落 execCommand（R3-11）', async () => {
    // jsdom 默认 navigator.clipboard 为 undefined
    const execCommand = vi.fn(() => true)
    document.execCommand = execCommand as unknown as typeof document.execCommand
    const ok = await copyText('https://example.com/sub')
    expect(ok).toBe(true)
    expect(execCommand).toHaveBeenCalledWith('copy')
  })

  it('execCommand 失败返回 false', async () => {
    document.execCommand = vi.fn(() => false) as unknown as typeof document.execCommand
    const ok = await copyText('https://example.com/sub')
    expect(ok).toBe(false)
  })
})
