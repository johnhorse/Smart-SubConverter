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
  preset: { id: 'full_multimode', name: '地区分组 + AI 分流' },
  errorCode: null,
  message: '',
  link: 'http://192.168.1.10:8080/s/Ab3xY9qZ',
  deepLink: null,
}

const badResult: PreviewResponse = {
  ok: false,
  target: 'clash',
  preset: { id: 'default', name: 'ACL4SSR 默认' },
  errorCode: 'upstream_error',
  message: 'Invalid request',
  link: 'http://192.168.1.10:8080/s/Ab3xY9qZ',
  deepLink: null,
}

// store_error：短链写入失败，link 降级为 /sub 长链（契约唯一保留长链返回的场景）
const storeErrorResult: PreviewResponse = {
  ok: false,
  target: 'clash',
  preset: { id: 'default', name: 'ACL4SSR 默认' },
  errorCode: 'store_error',
  message: 'sqlite: database or disk is full',
  link: 'http://192.168.1.10:8080/sub?target=clash&url=abc',
  deepLink: null,
}

function mountPanel(result: PreviewResponse, showEscape = true, clientLabel = 'Surge') {
  return mount(ResultPanel, { props: { result, showEscape, clientLabel } })
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

  it('store_error：警示条用降级长链文案，链接照常展示（v1.2.0）', () => {
    const wrapper = mountPanel(storeErrorResult)
    const banner = wrapper.find('[role="alert"]')
    expect(banner.exists()).toBe(true)
    expect(banner.text()).toContain(
      '短链生成失败了——这次给的是普通链接（含订阅信息，注意别转发）；请部署者检查 /data 卷',
    )
    expect(wrapper.text()).toContain(storeErrorResult.link)
  })

  it('ok:true 时无警示条，含「链接是活的」说明与复制小字（D6）', () => {
    const wrapper = mountPanel(okResult)
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('链接是活的')
    expect(wrapper.text()).toContain('这条短链就是你的订阅钥匙——转发给谁，谁就能用你的订阅；泄露了找部署者撤销，换一条新的')
  })

  it('link 下方标注「模板：{preset.name}」（ok:true，响应回显）', () => {
    const wrapper = mountPanel(okResult)
    expect(wrapper.find('.preset-note').text()).toBe('模板：地区分组 + AI 分流')
  })

  it('ok:false 但有合法 preset 回显时照样标注模板（非 bad_preset 错误）', () => {
    const wrapper = mountPanel(badResult)
    expect(wrapper.find('.preset-note').text()).toBe('模板：ACL4SSR 默认')
  })

  it('preset 回显为 null 时（bad_preset）不渲染模板标注', () => {
    const wrapper = mountPanel({ ...okResult, preset: null })
    expect(wrapper.find('.preset-note').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('模板：')
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

  it('deepLink 非空时渲染「在 {label} 中打开」按钮，点击把 location.href 指向 deepLink', async () => {
    const locationStub = { href: '' }
    vi.stubGlobal('location', locationStub)
    try {
      const deepLink = 'surge://install-config?url=' + encodeURIComponent(okResult.link)
      const wrapper = mountPanel({ ...okResult, target: 'surge', deepLink }, true, 'Surge')
      const btn = wrapper.findAll('button').find((b) => b.text() === '在 Surge 中打开')
      expect(btn).toBeTruthy()
      await btn!.trigger('click')
      expect(locationStub.href).toBe(deepLink)
    } finally {
      vi.unstubAllGlobals()
    }
  })

  it('deepLink 为 null 时（auto/clash/singbox/v2rayn）copy-row 只剩复制按钮，无打开按钮残留', () => {
    const wrapper = mountPanel(okResult)
    const copyRow = wrapper.find('.copy-row')
    expect(copyRow.findAll('button')).toHaveLength(1)
    expect(copyRow.text()).not.toContain('中打开')
    expect(copyRow.text()).toContain('这条短链就是你的订阅钥匙——转发给谁，谁就能用你的订阅；泄露了找部署者撤销，换一条新的')
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
