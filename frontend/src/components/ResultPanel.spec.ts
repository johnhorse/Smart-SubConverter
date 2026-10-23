import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import ResultPanel from './ResultPanel.vue'
import type { PreviewResponse } from '../types'

vi.mock('qrcode', () => ({
  default: { toDataURL: vi.fn().mockResolvedValue('data:image/png;base64,fakeqr') },
}))

const okResult: PreviewResponse = {
  ok: true,
  target: 'clash',
  errorCode: null,
  message: '',
  link: 'http://192.168.1.10:8080/sub?target=auto&url=abc',
}

const badResult: PreviewResponse = {
  ok: false,
  target: 'clash',
  errorCode: 'upstream_error',
  message: 'Invalid request',
  link: 'http://192.168.1.10:8080/sub?target=auto&url=abc',
}

function mountPanel(result: PreviewResponse, showEscape = true) {
  return mount(ResultPanel, { props: { result, showEscape } })
}

describe('ResultPanel', () => {
  it('ok:false 时渲染警示条：errorCode 文案 + 「未通过校验，仍可试用」标注，链接照常展示', () => {
    const wrapper = mountPanel(badResult)
    const banner = wrapper.find('[role="alert"]')
    expect(banner.exists()).toBe(true)
    expect(banner.text()).toContain('转换没成功——可以试试展开「指定客户端」换一个，或稍后再来')
    expect(banner.text()).toContain('未通过校验，仍可试用')
    expect(wrapper.text()).toContain(badResult.link)
    // 修订后的结果态主文案（不使用线框图原文）
    expect(wrapper.text()).toContain('导入你的 App 时会自动适配格式')
  })

  it('ok:true 时无警示条，含「链接是活的」说明与复制小字（D6）', () => {
    const wrapper = mountPanel(okResult)
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('链接是活的')
    expect(wrapper.text()).toContain('链接含订阅信息，仅自用勿转发')
  })

  it('点击复制成功后按钮短暂变「已复制 ✓」', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(navigator, 'clipboard', {
      value: { writeText },
      configurable: true,
    })
    try {
      const wrapper = mountPanel(okResult)
      const btn = wrapper.findAll('button').find((b) => b.text() === '复制')!
      expect(btn).toBeTruthy()
      await btn.trigger('click')
      expect(writeText).toHaveBeenCalledWith(okResult.link)
      expect(wrapper.findAll('button').some((b) => b.text() === '已复制 ✓')).toBe(true)
    } finally {
      Object.defineProperty(navigator, 'clipboard', { value: undefined, configurable: true })
    }
  })

  it('逃生口按钮 emit escape；showEscape=false 时不渲染', async () => {
    const wrapper = mountPanel(okResult, true)
    const escapeBtn = wrapper.findAll('button').find((b) => b.text().includes('我是 Clash 用户'))
    expect(escapeBtn).toBeTruthy()
    await escapeBtn!.trigger('click')
    expect(wrapper.emitted('escape')).toHaveLength(1)

    const wrapper2 = mountPanel(okResult, false)
    expect(wrapper2.text()).not.toContain('我是 Clash 用户')
  })

  it('二维码渲染（qrcode.toDataURL）', async () => {
    const wrapper = mountPanel(okResult)
    await vi.waitFor(() => {
      expect(wrapper.find('img[alt="订阅链接二维码"]').attributes('src')).toBe(
        'data:image/png;base64,fakeqr',
      )
    })
  })
})
