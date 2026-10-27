import { beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'
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

describe('useConverter 自定义分组（v1.5.0）', () => {
  beforeEach(() => {
    vi.mocked(fetchPreview).mockReset()
    vi.mocked(fetchClients).mockReset().mockResolvedValue({ clients: [], presets: [] })
  })

  it('初始：面板收起、无组、probeState idle；能力位缺席（降级数据）→ 入口不可用', () => {
    const conv = setup()
    expect(conv.groupsOpen.value).toBe(false)
    expect(conv.groups.value).toEqual([])
    expect(conv.probeState.value).toBe('idle')
    expect(conv.nodes.value).toEqual([])
    expect(conv.customGroupsAvailable.value).toBe(false)
  })

  it('展开面板 → probe：请求形状带 probe:true（不带 groups），成功 ready + nodes 落位', async () => {
    const conv = setup()
    conv.inputUrl.value = 'https://example.com/sub'
    vi.mocked(fetchPreview).mockResolvedValueOnce({ ...okResponse, nodes: ['香港专线 01'] })

    conv.groupsOpen.value = true
    await vi.waitFor(() => expect(conv.probeState.value).toBe('ready'))
    expect(fetchPreview).toHaveBeenCalledWith('https://example.com/sub', 'auto', 'default', { probe: true })
    expect(conv.nodes.value).toEqual(['香港专线 01'])
  })

  it('url 空：展开面板不发 probe', async () => {
    const conv = setup()
    conv.groupsOpen.value = true
    await nextTick()
    await nextTick()
    expect(fetchPreview).not.toHaveBeenCalled()
  })

  it('probe ok:false（如 bad_url_format）→ failed，静默不写主流程错误', async () => {
    const conv = setup()
    conv.inputUrl.value = 'not-a-url'
    vi.mocked(fetchPreview).mockResolvedValueOnce({
      ...okResponse,
      ok: false,
      errorCode: 'bad_url_format',
      nodes: [],
    })

    conv.groupsOpen.value = true
    await vi.waitFor(() => expect(conv.probeState.value).toBe('failed'))
    expect(conv.convertError.value).toBeNull()
  })

  it('probe 网络异常 → failed 不阻塞主流程，随后的 submit 照常成功', async () => {
    const conv = setup()
    conv.inputUrl.value = 'https://example.com/sub'
    vi.mocked(fetchPreview)
      .mockRejectedValueOnce(new ApiUnreachableError())
      .mockResolvedValueOnce(okResponse)

    conv.groupsOpen.value = true
    await vi.waitFor(() => expect(conv.probeState.value).toBe('failed'))
    expect(conv.convertError.value).toBeNull()

    await conv.submit()
    expect(conv.phase.value).toBe('result')
  })

  it('面板展开期间改 url → 防抖 400ms 重 probe（新 url）；清空 url → idle + 清 nodes', async () => {
    vi.useFakeTimers()
    try {
      const conv = setup()
      conv.inputUrl.value = 'https://example.com/sub'
      vi.mocked(fetchPreview).mockResolvedValue({ ...okResponse, nodes: ['A'] })
      conv.groupsOpen.value = true
      await vi.waitFor(() => expect(conv.probeState.value).toBe('ready'))

      conv.inputUrl.value = 'https://example.com/sub2'
      await vi.advanceTimersByTimeAsync(400)
      expect(fetchPreview).toHaveBeenCalledTimes(2)
      expect(fetchPreview).toHaveBeenLastCalledWith('https://example.com/sub2', 'auto', 'default', { probe: true })

      conv.inputUrl.value = ''
      await vi.advanceTimersByTimeAsync(400)
      expect(conv.probeState.value).toBe('idle')
      expect(conv.nodes.value).toEqual([])
      expect(fetchPreview).toHaveBeenCalledTimes(2)
    } finally {
      vi.useRealTimers()
    }
  })

  it('带组提交：fetchPreview 第 4 参 {groups: 组意图}；空组不带第 4 参（存量形状）', async () => {
    // F1 后：能力位失效时组被剔除不发送——本用例验证「组意图随请求携带」需能力位全真
    vi.mocked(fetchClients).mockResolvedValueOnce({
      clients: [],
      presets: [{ id: 'default', name: '默认', url: 'https://a.ini', customGroups: true }],
      capabilities: { customGroups: true },
    })
    const conv = setup()
    await conv.loadClients()
    conv.inputUrl.value = 'https://example.com/sub'
    const intent = [{ name: '专线', keyword: '专线', urlTest: false }]

    vi.mocked(fetchPreview).mockResolvedValueOnce(okResponse)
    await conv.submit()
    expect(fetchPreview).toHaveBeenLastCalledWith('https://example.com/sub', 'auto', 'default')

    conv.groups.value = intent
    vi.mocked(fetchPreview).mockResolvedValueOnce(okResponse)
    await conv.submit()
    expect(fetchPreview).toHaveBeenLastCalledWith('https://example.com/sub', 'auto', 'default', {
      groups: intent,
    })
  })

  it.each([
    ['bad_groups', ERROR_MESSAGES.bad_groups],
    ['groups_disabled', ERROR_MESSAGES.groups_disabled],
  ])('%s：预校验错停留状态②展示定稿文案，不进结果页', async (code, text) => {
    // 能力位全真（groups_disabled 用例的服务端兜底场景：手改请求绕过 UI 才点得出）
    vi.mocked(fetchClients).mockResolvedValueOnce({
      clients: [],
      presets: [{ id: 'default', name: '默认', url: 'https://a.ini', customGroups: true }],
      capabilities: { customGroups: true },
    })
    const conv = setup()
    await conv.loadClients()
    conv.inputUrl.value = 'https://example.com/sub'
    conv.groups.value = [{ name: '专线', keyword: '专线', urlTest: false }]
    vi.mocked(fetchPreview).mockResolvedValueOnce({ ...okResponse, ok: false, errorCode: code })

    await conv.submit()
    expect(conv.phase.value).toBe('converting')
    expect(conv.result.value).toBeNull()
    expect(conv.convertError.value).toBe(text)
  })

  it('能力位矩阵：部署位 × 选中档位，任一缺席或 false → 入口不可用', async () => {
    const conv = setup()
    expect(conv.customGroupsAvailable.value).toBe(false) // 降级数据无能力位

    vi.mocked(fetchClients).mockResolvedValueOnce({
      clients: [],
      presets: [
        { id: 'default', name: '默认', url: 'https://a.ini', customGroups: true },
        { id: 'full_multimode', name: '分组', url: 'https://b.ini', customGroups: false },
      ],
      capabilities: { customGroups: true },
    })
    await conv.loadClients()
    expect(conv.customGroupsAvailable.value).toBe(true)

    conv.selectPreset('full_multimode')
    expect(conv.customGroupsAvailable.value).toBe(false)
  })

  it('部署位 false 时选中档 true 也不可用', async () => {
    vi.mocked(fetchClients).mockResolvedValueOnce({
      clients: [],
      presets: [{ id: 'default', name: '默认', url: 'https://a.ini', customGroups: true }],
      capabilities: { customGroups: false },
    })
    const conv = setup()
    await conv.loadClients()
    expect(conv.customGroupsAvailable.value).toBe(false)
  })

  it('restart：清 probe 结果并收起面板，组意图保留（换链接打词仍适用）', async () => {
    const conv = setup()
    conv.inputUrl.value = 'https://example.com/sub'
    vi.mocked(fetchPreview).mockResolvedValueOnce({ ...okResponse, nodes: ['N1'] })
    conv.groupsOpen.value = true
    const intent = [{ name: '专线', keyword: '专线', urlTest: false }]
    conv.groups.value = intent
    await vi.waitFor(() => expect(conv.probeState.value).toBe('ready'))

    conv.restart()
    expect(conv.groupsOpen.value).toBe(false)
    expect(conv.probeState.value).toBe('idle')
    expect(conv.nodes.value).toEqual([])
    expect(conv.groups.value).toEqual(intent)
  })

  // ---- 评审 F1：切到不支持分组的档位不锁死 ----

  it('F1 切到不支持档后 submit 剔除 groups 字段（无组转换正常成功）；切回支持档后组恢复且正常携带', async () => {
    vi.mocked(fetchClients).mockResolvedValueOnce({
      clients: [],
      presets: [
        { id: 'default', name: '默认', url: 'https://a.ini', customGroups: true },
        { id: 'full_multimode', name: '分组', url: 'https://b.ini', customGroups: false },
      ],
      capabilities: { customGroups: true },
    })
    const conv = setup()
    await conv.loadClients()
    conv.inputUrl.value = 'https://example.com/sub'
    const intent = [{ name: '专线', keyword: '专线', urlTest: false }]
    conv.groups.value = intent

    conv.selectPreset('full_multimode') // 能力位变 false，组残留
    expect(conv.customGroupsAvailable.value).toBe(false)
    vi.mocked(fetchPreview).mockResolvedValueOnce(okResponse)
    await conv.submit()
    // 请求不带第 4 参（无 groups），不撞 groups_disabled，正常成功
    expect(fetchPreview).toHaveBeenLastCalledWith('https://example.com/sub', 'auto', 'full_multimode')
    expect(conv.phase.value).toBe('result')
    // 组保留未清
    expect(conv.groups.value).toEqual(intent)

    conv.selectPreset('default') // 切回支持档
    expect(conv.customGroupsAvailable.value).toBe(true)
    vi.mocked(fetchPreview).mockResolvedValueOnce(okResponse)
    await conv.submit()
    expect(fetchPreview).toHaveBeenLastCalledWith('https://example.com/sub', 'auto', 'default', {
      groups: intent,
    })
  })

  // ---- 评审 F4：收起/restart 不清 reprobe 定时器 ----

  it('F4 面板展开期间改 url 后 400ms 内收起：pending 防抖不触发 probe', async () => {
    vi.useFakeTimers()
    try {
      const conv = setup()
      conv.inputUrl.value = 'https://example.com/sub'
      vi.mocked(fetchPreview).mockResolvedValue({ ...okResponse, nodes: ['A'] })
      conv.groupsOpen.value = true
      await vi.waitFor(() => expect(conv.probeState.value).toBe('ready'))
      expect(fetchPreview).toHaveBeenCalledTimes(1)

      conv.inputUrl.value = 'https://example.com/sub2' // pending 防抖 400ms
      conv.groupsOpen.value = false // 立即收起
      await vi.advanceTimersByTimeAsync(400)
      expect(fetchPreview).toHaveBeenCalledTimes(1) // 定时器已清，未重发
      expect(conv.probeState.value).toBe('ready') // 未被 pending 回调重置
    } finally {
      vi.useRealTimers()
    }
  })

  it('F4 restart 后 pending 防抖不触发（不重置已清空的 probe 状态）', async () => {
    vi.useFakeTimers()
    try {
      const conv = setup()
      conv.inputUrl.value = 'https://example.com/sub'
      vi.mocked(fetchPreview).mockResolvedValue({ ...okResponse, nodes: ['A'] })
      conv.groupsOpen.value = true
      await vi.waitFor(() => expect(conv.probeState.value).toBe('ready'))

      conv.inputUrl.value = 'https://example.com/sub2' // pending 防抖 400ms
      conv.restart()
      expect(conv.probeState.value).toBe('idle')
      expect(conv.nodes.value).toEqual([])

      await vi.advanceTimersByTimeAsync(400)
      expect(fetchPreview).toHaveBeenCalledTimes(1)
      expect(conv.probeState.value).toBe('idle') // pending 回调没有重置状态
      expect(conv.nodes.value).toEqual([])
    } finally {
      vi.useRealTimers()
    }
  })

  // ---- 评审 F6：开关面板无条件 reprobe ----

  it('F6 同 url 二次展开复用 ready 结果零请求；换 url 后展开重新 probe', async () => {
    const conv = setup()
    conv.inputUrl.value = 'https://example.com/sub'
    vi.mocked(fetchPreview).mockResolvedValue({ ...okResponse, nodes: ['N1'] })
    conv.groupsOpen.value = true
    await vi.waitFor(() => expect(conv.probeState.value).toBe('ready'))
    expect(fetchPreview).toHaveBeenCalledTimes(1)

    conv.groupsOpen.value = false
    await nextTick()
    conv.groupsOpen.value = true
    await nextTick()
    await nextTick()
    expect(fetchPreview).toHaveBeenCalledTimes(1) // 同 url 复用，零新请求
    expect(conv.probeState.value).toBe('ready')
    expect(conv.nodes.value).toEqual(['N1'])

    conv.groupsOpen.value = false
    await nextTick()
    conv.inputUrl.value = 'https://example.com/sub2'
    await nextTick()
    conv.groupsOpen.value = true
    await vi.waitFor(() => expect(fetchPreview).toHaveBeenCalledTimes(2))
    expect(fetchPreview).toHaveBeenLastCalledWith('https://example.com/sub2', 'auto', 'default', { probe: true })
  })
})
