import { beforeEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import App from './App.vue'
import { fetchPreview } from './api'
import type { PreviewResponse } from './types'

vi.mock('./api', () => ({
  ApiUnreachableError: class extends Error {
    readonly errorCode = 'api_unreachable'
  },
  fetchPreview: vi.fn(),
  fetchClients: vi.fn().mockResolvedValue({ clients: [], presets: [] }),
  fetchHealth: vi.fn().mockRejectedValue(new Error('down')),
}))

vi.mock('qrcode', () => ({
  default: { toDataURL: vi.fn().mockResolvedValue('data:image/png;base64,fakeqr') },
}))

const okResponse: PreviewResponse = {
  ok: true,
  target: 'clash',
  errorCode: null,
  message: '',
  link: 'http://192.168.1.10:8080/sub?target=auto&url=abc',
}

async function mountApp() {
  const wrapper = mount(App)
  await vi.dynamicImportSettled()
  return wrapper
}

describe('App 状态流转（组件层）', () => {
  beforeEach(() => {
    vi.mocked(fetchPreview).mockReset()
  })

  it('状态①首屏：输入框 + 转换按钮 + 高级折叠区默认收起 + 页脚徽章占位', async () => {
    const wrapper = await mountApp()
    expect(wrapper.find('input[aria-label="订阅链接"]').exists()).toBe(true)
    const btn = wrapper.find('button[type="submit"]')
    expect(btn.text()).toBe('转换')
    expect(wrapper.find('.advanced-body').isVisible()).toBe(false)
    // health 接口失败静默降级为「规则库状态未知」
    await vi.waitFor(() => {
      expect(wrapper.find('.footer .badge.unknown').text()).toContain('规则库状态未知')
    })
  })

  it('点「转换」：状态②（按钮锁死 + 折叠区自动展开 + auto 默认选中）→ 成功进状态③', async () => {
    let resolvePreview!: (v: PreviewResponse) => void
    vi.mocked(fetchPreview).mockReturnValue(
      new Promise((r) => {
        resolvePreview = r
      }),
    )
    const wrapper = await mountApp()

    await wrapper.find('input[aria-label="订阅链接"]').setValue('https://example.com/sub')
    await wrapper.find('button[type="submit"]').trigger('submit')
    await vi.dynamicImportSettled()

    // 状态②：按钮禁用锁死，折叠区展开，chips 里 auto 选中带 ✓
    const submitBtn = wrapper.find('button[type="submit"]')
    expect(submitBtn.attributes('disabled')).toBeDefined()
    expect(submitBtn.text()).toBe('转换中…')
    expect(wrapper.find('.advanced-body').isVisible()).toBe(true)
    expect(wrapper.find('.chip.active').text()).toContain('自动识别 ✓')

    resolvePreview(okResponse)
    await vi.waitFor(() => {
      expect(wrapper.find('.result').exists()).toBe(true)
    })
    // 状态③：主输入区被结果页替换
    expect(wrapper.find('input[aria-label="订阅链接"]').exists()).toBe(false)
    expect(wrapper.text()).toContain(okResponse.link)
  })

  it('状态③逃生口：点「？我是 Clash 用户」自动改选 clash 重新请求', async () => {
    vi.mocked(fetchPreview).mockResolvedValue(okResponse)
    const wrapper = await mountApp()

    await wrapper.find('input[aria-label="订阅链接"]').setValue('https://example.com/sub')
    await wrapper.find('button[type="submit"]').trigger('submit')
    await vi.waitFor(() => expect(wrapper.find('.result').exists()).toBe(true))

    const escapeBtn = wrapper.findAll('button').find((b) => b.text().includes('我是 Clash 用户'))
    expect(escapeBtn).toBeTruthy()
    await escapeBtn!.trigger('click')
    await vi.waitFor(() => {
      expect(fetchPreview).toHaveBeenLastCalledWith('https://example.com/sub', 'clash')
    })
    // 改选 clash 后逃生口消失
    await vi.waitFor(() => {
      expect(wrapper.text()).not.toContain('我是 Clash 用户')
    })
  })
})
