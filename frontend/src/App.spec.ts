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
  preset: { id: 'default', name: 'ACL4SSR 默认' },
  errorCode: null,
  message: '',
  link: 'http://192.168.1.10:8080/s/Ab3xY9qZ',
  deepLink: null,
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

  it('预校验错误（bad_url_format）停留状态②：错误文案可修改重试，不展示链接不进结果页', async () => {
    vi.mocked(fetchPreview).mockResolvedValue({
      ok: false,
      target: 'clash',
      preset: null,
      errorCode: 'bad_url_format',
      message: '',
      link: 'http://192.168.1.10:8080/s/xK9badQm',
      deepLink: null,
    })
    const wrapper = await mountApp()

    await wrapper.find('input[aria-label="订阅链接"]').setValue('not-a-url')
    await wrapper.find('button[type="submit"]').trigger('submit')
    await vi.waitFor(() => {
      expect(wrapper.find('.error-note').text()).toContain('这不像一个订阅链接——检查一下是不是复制全了？')
    })

    // 停留状态②：主输入区仍在、无结果页、无链接文本、按钮恢复可点
    expect(wrapper.find('.result').exists()).toBe(false)
    expect(wrapper.find('input[aria-label="订阅链接"]').exists()).toBe(true)
    expect(wrapper.text()).not.toContain('192.168.1.10:8080/s/')
    const btn = wrapper.find('button[type="submit"]')
    expect(btn.attributes('disabled')).toBeUndefined()
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
      expect(fetchPreview).toHaveBeenLastCalledWith('https://example.com/sub', 'clash', 'default')
    })
    // 改选 clash 后逃生口消失
    await vi.waitFor(() => {
      expect(wrapper.text()).not.toContain('我是 Clash 用户')
    })
  })

  it('高级区：标题「客户端与规则模板」，展开后渲染三档模板 chips（default 第一且选中）+ 选中档描述', async () => {
    const wrapper = await mountApp()
    const toggle = wrapper.find('.advanced-toggle')
    expect(toggle.text()).toContain('高级 · 客户端与规则模板')

    await toggle.trigger('click')
    expect(wrapper.find('.advanced-body').isVisible()).toBe(true)

    // 客户端 chips 仍在上方（auto 第一选中）
    expect(wrapper.find('[aria-label="指定客户端"] .chip.active').text()).toBe('自动识别 ✓')

    const presetChips = wrapper.findAll('[aria-label="规则模板"] .chip')
    expect(presetChips.map((c) => c.text())).toEqual([
      'ACL4SSR 默认 ✓',
      '地区分组 + AI 分流',
      '全量 + 去广告',
    ])
    expect(presetChips[0].attributes('aria-checked')).toBe('true')
    expect(wrapper.find('.preset-desc').text()).toBe('按用途分组：国外媒体 / 电报 / 微软 / 苹果…最简')
  })

  it('切换规则模板后点转换：preview 请求带该 preset id，描述随选中切换', async () => {
    vi.mocked(fetchPreview).mockResolvedValue(okResponse)
    const wrapper = await mountApp()
    await wrapper.find('.advanced-toggle').trigger('click')

    const presetChips = wrapper.findAll('[aria-label="规则模板"] .chip')
    await presetChips[1].trigger('click')
    expect(presetChips[1].classes('active')).toBe(true)
    expect(wrapper.find('.preset-desc').text()).toContain('ChatGPT / Claude / Gemini')

    await wrapper.find('input[aria-label="订阅链接"]').setValue('https://example.com/sub')
    await wrapper.find('button[type="submit"]').trigger('submit')
    await vi.waitFor(() => {
      expect(fetchPreview).toHaveBeenLastCalledWith('https://example.com/sub', 'auto', 'full_multimode')
    })
  })

  it('结果页 link 下方标注「模板：{preset.name}」（用响应回显，不本地猜）', async () => {
    vi.mocked(fetchPreview).mockResolvedValue({
      ...okResponse,
      preset: { id: 'full_multimode', name: '地区分组 + AI 分流' },
    })
    const wrapper = await mountApp()

    await wrapper.find('input[aria-label="订阅链接"]').setValue('https://example.com/sub')
    await wrapper.find('button[type="submit"]').trigger('submit')
    await vi.waitFor(() => expect(wrapper.find('.result').exists()).toBe(true))

    expect(wrapper.find('.preset-note').text()).toBe('模板：地区分组 + AI 分流')
  })
})
