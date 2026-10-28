import { beforeEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import App from './App.vue'
import { fetchClients, fetchPreview } from './api'
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

describe('App 自定义分组接线（v1.5.0）', () => {
  beforeEach(() => {
    vi.mocked(fetchPreview).mockReset()
    vi.mocked(fetchClients).mockReset().mockResolvedValue({ clients: [], presets: [] })
  })

  // 能力位全真的 /api/clients 响应（default 档支持、full_multimode 不支持）
  const capableClients = {
    clients: [],
    presets: [
      { id: 'default', name: 'ACL4SSR 默认', url: 'https://a.ini', customGroups: true },
      { id: 'full_multimode', name: '地区分组 + AI 分流', url: 'https://b.ini' },
    ],
    capabilities: { customGroups: true },
  }

  it('能力位缺席（旧 sidecar / 接口降级）：loadClients 完成后仍无分组入口（主流程零漂移）', async () => {
    const wrapper = await mountApp()
    await vi.waitFor(() => expect(fetchClients).toHaveBeenCalled())
    await vi.waitFor(() => expect(wrapper.find('.groups-entry').exists()).toBe(false))
  })

  it('能力位全真：url 空入口置灰 → 粘贴后可点 → 展开触发 probe → 打词输入出现', async () => {
    vi.mocked(fetchClients).mockResolvedValueOnce(capableClients)
    vi.mocked(fetchPreview).mockResolvedValue({ ...okResponse, nodes: ['香港专线 01'] })
    const wrapper = await mountApp()

    await vi.waitFor(() => expect(wrapper.find('.groups-entry').exists()).toBe(true))
    expect(wrapper.find('.groups-entry').attributes('disabled')).toBeDefined()

    await wrapper.find('input[aria-label="订阅链接"]').setValue('https://example.com/sub')
    await vi.waitFor(() => expect(wrapper.find('.groups-entry').attributes('disabled')).toBeUndefined())

    await wrapper.find('.groups-entry').trigger('click')
    await vi.waitFor(() => {
      expect(fetchPreview).toHaveBeenCalledWith('https://example.com/sub', 'auto', 'default', { probe: true })
    })
    await vi.waitFor(() => {
      expect(wrapper.find('input[aria-label="分组关键词"]').exists()).toBe(true)
    })
  })

  it('端到端：展开 → probe → 打词「专线」→ 已命中计数 → 成组 → 命名（默认拼接）→ 转换请求带 groups → 结果页', async () => {
    vi.mocked(fetchClients).mockResolvedValueOnce(capableClients)
    vi.mocked(fetchPreview)
      .mockResolvedValueOnce({ ...okResponse, nodes: ['香港专线 01', '日本专线 02', '美国 IEPL 1.0'] })
      .mockResolvedValueOnce(okResponse)
    const wrapper = await mountApp()

    await wrapper.find('input[aria-label="订阅链接"]').setValue('https://example.com/sub')
    await vi.waitFor(() => expect(wrapper.find('.groups-entry').exists()).toBe(true))
    await wrapper.find('.groups-entry').trigger('click')
    await vi.waitFor(() => expect(wrapper.find('input[aria-label="分组关键词"]').exists()).toBe(true))

    await wrapper.find('input[aria-label="分组关键词"]').setValue('专线')
    // 打词防抖 250ms（real timers，waitFor 轮询等到计数出现）
    await vi.waitFor(() => expect(wrapper.find('.match-count').text()).toBe('已命中 2'))
    await wrapper.find('.group-confirm').trigger('click') // 成组 → 命名步（默认名=词拼接）
    await vi.waitFor(() => expect(wrapper.find('input[aria-label="分组名称"]').exists()).toBe(true))
    expect((wrapper.find('input[aria-label="分组名称"]').element as HTMLInputElement).value).toBe('专线')
    await wrapper.find('.group-confirm').trigger('click') // 命名确认 → 落组
    expect(wrapper.find('.group-chip').text()).toBe('专线 ×2')

    await wrapper.find('button[type="submit"]').trigger('submit')
    await vi.waitFor(() => {
      expect(fetchPreview).toHaveBeenLastCalledWith('https://example.com/sub', 'auto', 'default', {
        groups: [{ name: '专线', keywords: ['专线'], urlTest: false }],
      })
    })
    await vi.waitFor(() => expect(wrapper.find('.result').exists()).toBe(true))
  })

  it('能力位全真但未建组：转换请求不带 groups（不展开面板时与存量行为一致）', async () => {
    vi.mocked(fetchClients).mockResolvedValueOnce(capableClients)
    vi.mocked(fetchPreview).mockResolvedValue(okResponse)
    const wrapper = await mountApp()

    await wrapper.find('input[aria-label="订阅链接"]').setValue('https://example.com/sub')
    await wrapper.find('button[type="submit"]').trigger('submit')
    await vi.waitFor(() => {
      expect(fetchPreview).toHaveBeenLastCalledWith('https://example.com/sub', 'auto', 'default')
    })
    await vi.waitFor(() => expect(wrapper.find('.result').exists()).toBe(true))
  })

  // ---- 评审 F1：切到不支持档后组残留不锁死 ----

  it('F1 切到不支持档：面板隐藏、残留组提示行出现；转换请求不带 groups 正常成功；切回后恢复', async () => {
    vi.mocked(fetchClients).mockResolvedValueOnce(capableClients)
    vi.mocked(fetchPreview).mockResolvedValue({ ...okResponse, nodes: ['香港专线 01'] })
    const wrapper = await mountApp()

    // 建一个组
    await wrapper.find('input[aria-label="订阅链接"]').setValue('https://example.com/sub')
    await vi.waitFor(() => expect(wrapper.find('.groups-entry').exists()).toBe(true))
    await wrapper.find('.groups-entry').trigger('click')
    await vi.waitFor(() => expect(wrapper.find('input[aria-label="分组关键词"]').exists()).toBe(true))
    await wrapper.find('input[aria-label="分组关键词"]').setValue('专线')
    await vi.waitFor(() => expect(wrapper.find('.match-count').text()).toBe('已命中 1'))
    await wrapper.find('.group-confirm').trigger('click') // 成组 → 命名步
    await vi.waitFor(() => expect(wrapper.find('input[aria-label="分组名称"]').exists()).toBe(true))
    await wrapper.find('.group-confirm').trigger('click') // 命名确认 → 落组
    expect(wrapper.find('.group-chip').exists()).toBe(true)

    // 切到不支持档：GroupPanel 整体隐藏、提示行出现
    await wrapper.find('.advanced-toggle').trigger('click')
    const presetChips = wrapper.findAll('[aria-label="规则模板"] .chip')
    await presetChips[1].trigger('click')
    expect(wrapper.find('.groups-entry').exists()).toBe(false)
    expect(wrapper.find('.groups-kept-note').text()).toContain('本次转换不含分组')
    expect(wrapper.find('.groups-kept-note').text()).toContain('分组已保留，切换模板后可用')

    // 转换请求不带 groups（等价无组转换，不撞 groups_disabled）
    vi.mocked(fetchPreview).mockResolvedValueOnce(okResponse)
    await wrapper.find('button[type="submit"]').trigger('submit')
    await vi.waitFor(() => {
      expect(fetchPreview).toHaveBeenLastCalledWith('https://example.com/sub', 'auto', 'full_multimode')
    })
    await vi.waitFor(() => expect(wrapper.find('.result').exists()).toBe(true))
  })

  it('F1 切回支持档：提示行消失、面板入口恢复且组仍在（再次转换正常携带 groups）', async () => {
    vi.mocked(fetchClients).mockResolvedValueOnce(capableClients)
    vi.mocked(fetchPreview).mockResolvedValue({ ...okResponse, nodes: ['香港专线 01'] })
    const wrapper = await mountApp()

    await wrapper.find('input[aria-label="订阅链接"]').setValue('https://example.com/sub')
    await vi.waitFor(() => expect(wrapper.find('.groups-entry').exists()).toBe(true))
    await wrapper.find('.groups-entry').trigger('click')
    await vi.waitFor(() => expect(wrapper.find('input[aria-label="分组关键词"]').exists()).toBe(true))
    await wrapper.find('input[aria-label="分组关键词"]').setValue('专线')
    await vi.waitFor(() => expect(wrapper.find('.match-count').text()).toBe('已命中 1'))
    await wrapper.find('.group-confirm').trigger('click') // 成组 → 命名步
    await vi.waitFor(() => expect(wrapper.find('input[aria-label="分组名称"]').exists()).toBe(true))
    await wrapper.find('.group-confirm').trigger('click') // 命名确认 → 落组

    await wrapper.find('.advanced-toggle').trigger('click')
    const presetChips = wrapper.findAll('[aria-label="规则模板"] .chip')
    await presetChips[1].trigger('click') // 切走 → 提示行出现
    expect(wrapper.find('.groups-kept-note').exists()).toBe(true)

    await presetChips[0].trigger('click') // 切回 → 提示消失、面板恢复且组保留
    expect(wrapper.find('.groups-kept-note').exists()).toBe(false)
    // groupsOpen 仍 true（切档前展开过，切档只卸载面板不清状态）→ 展开态直接回归
    await vi.waitFor(() => expect(wrapper.find('.group-chip').exists()).toBe(true))
    expect(wrapper.find('.group-chip').text()).toBe('专线 ×1')

    vi.mocked(fetchPreview).mockResolvedValueOnce(okResponse)
    await wrapper.find('button[type="submit"]').trigger('submit')
    await vi.waitFor(() => {
      expect(fetchPreview).toHaveBeenLastCalledWith('https://example.com/sub', 'auto', 'default', {
        groups: [{ name: '专线', keywords: ['专线'], urlTest: false }],
      })
    })
  })
})
