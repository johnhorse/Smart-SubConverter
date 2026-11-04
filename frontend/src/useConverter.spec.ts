import { beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'
import {
  ApiUnreachableError,
  deletePreferences,
  fetchClients,
  fetchPreferences,
  fetchPreview,
} from './api'
import { ERROR_MESSAGES, FALLBACK_CLIENTS, FALLBACK_PRESETS } from './constants'
import { getSnapshot, saveSnapshot, type DraftSnapshot } from './prefs'
import type { PreviewResponse } from './types'
import { useConverter } from './useConverter'

vi.mock('./api', () => ({
  ApiUnreachableError: class extends Error {
    readonly errorCode = 'api_unreachable'
  },
  fetchPreview: vi.fn(),
  fetchClients: vi.fn().mockResolvedValue({ clients: [], presets: [] }),
  fetchPreferences: vi.fn().mockResolvedValue({ custom_groups: null, exclude: null }),
  deletePreferences: vi.fn().mockResolvedValue({ deleted_custom: false, deleted_exclude: 0 }),
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

// v1.6.0 草稿层落 localStorage：真实 timers 用例的防抖写入会跨用例残留，统一每例清空
beforeEach(() => {
  localStorage.clear()
})

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
    const intent = [{ name: '专线', keywords: ['专线'], urlTest: false }]

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
    conv.groups.value = [{ name: '专线', keywords: ['专线'], urlTest: false }]
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
    const intent = [{ name: '专线', keywords: ['专线'], urlTest: false }]
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
    const intent = [{ name: '专线', keywords: ['专线'], urlTest: false }]
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

describe('useConverter 模板组删减（v1.5.0 增补）', () => {
  const capableClients = {
    clients: [],
    presets: [
      {
        id: 'default',
        name: '默认',
        url: 'https://a.ini',
        customGroups: true,
        groups: [
          { name: '🚀 节点选择', locked: true },
          { name: '🇰🇷 韩国节点', locked: false },
        ],
      },
      {
        id: 'full_multimode',
        name: '分组',
        url: 'https://b.ini',
        customGroups: true,
        groups: [{ name: '💬 Ai平台', locked: false }],
      },
    ],
    capabilities: { customGroups: true },
  }

  beforeEach(() => {
    vi.mocked(fetchPreview).mockReset()
    vi.mocked(fetchClients).mockReset().mockResolvedValue({ clients: [], presets: [] })
  })

  it('templateGroups 取选中档 groups（模板原序）；切档跟随', async () => {
    vi.mocked(fetchClients).mockResolvedValueOnce(capableClients)
    const conv = setup()
    await conv.loadClients()
    expect(conv.templateGroups.value).toEqual([
      { name: '🚀 节点选择', locked: true },
      { name: '🇰🇷 韩国节点', locked: false },
    ])

    conv.selectPreset('full_multimode')
    expect(conv.templateGroups.value).toEqual([{ name: '💬 Ai平台', locked: false }])
  })

  it('groups 字段缺席（旧 sidecar / 豁免档）→ templateGroups 为空数组', async () => {
    vi.mocked(fetchClients).mockResolvedValueOnce({
      clients: [],
      presets: [{ id: 'default', name: '默认', url: 'https://a.ini', customGroups: true }],
      capabilities: { customGroups: true },
    })
    const conv = setup()
    await conv.loadClients()
    expect(conv.templateGroups.value).toEqual([])
  })

  it('probe 成功写入 groupHits；响应缺字段（旧 sidecar）按 {} 降级', async () => {
    const conv = setup()
    conv.inputUrl.value = 'https://example.com/sub'
    vi.mocked(fetchPreview).mockResolvedValueOnce({
      ...okResponse,
      nodes: ['A'],
      groupHits: { '🇰🇷 韩国节点': 0 },
    })
    conv.groupsOpen.value = true
    await vi.waitFor(() => expect(conv.probeState.value).toBe('ready'))
    expect(conv.groupHits.value).toEqual({ '🇰🇷 韩国节点': 0 })

    conv.groupsOpen.value = false
    await nextTick()
    conv.inputUrl.value = 'https://example.com/sub2'
    await nextTick()
    vi.mocked(fetchPreview).mockResolvedValueOnce({ ...okResponse, nodes: ['B'] })
    conv.groupsOpen.value = true
    await vi.waitFor(() => expect(conv.probeState.value).toBe('ready'))
    expect(conv.groupHits.value).toEqual({})
  })

  it('probe 失败（ok:false / 网络异常）→ groupHits 清空', async () => {
    const conv = setup()
    conv.inputUrl.value = 'not-a-url'
    conv.groupHits.value = { '🇰🇷 韩国节点': 0 }
    vi.mocked(fetchPreview).mockResolvedValueOnce({ ...okResponse, ok: false, errorCode: 'bad_url_format', nodes: [] })
    conv.groupsOpen.value = true
    await vi.waitFor(() => expect(conv.probeState.value).toBe('failed'))
    expect(conv.groupHits.value).toEqual({})
  })

  it('submit：exclude 非空才带；与 groups 可同时携带', async () => {
    vi.mocked(fetchClients).mockResolvedValueOnce(capableClients)
    const conv = setup()
    await conv.loadClients()
    conv.inputUrl.value = 'https://example.com/sub'

    conv.excluded.value = ['🇰🇷 韩国节点']
    vi.mocked(fetchPreview).mockResolvedValueOnce(okResponse)
    await conv.submit()
    expect(fetchPreview).toHaveBeenLastCalledWith('https://example.com/sub', 'auto', 'default', {
      exclude: ['🇰🇷 韩国节点'],
    })

    const intent = [{ name: '专线', keywords: ['专线'], urlTest: false }]
    conv.groups.value = intent
    vi.mocked(fetchPreview).mockResolvedValueOnce(okResponse)
    await conv.submit()
    expect(fetchPreview).toHaveBeenLastCalledWith('https://example.com/sub', 'auto', 'default', {
      groups: intent,
      exclude: ['🇰🇷 韩国节点'],
    })
  })

  it('切档：excluded 与 groupHits 清空（不按组名继承）；面板收起时不重 probe', async () => {
    vi.mocked(fetchClients).mockResolvedValueOnce(capableClients)
    const conv = setup()
    await conv.loadClients()
    conv.excluded.value = ['🇰🇷 韩国节点']
    conv.groupHits.value = { '🇰🇷 韩国节点': 0 }

    conv.selectPreset('full_multimode')
    await nextTick()
    expect(conv.excluded.value).toEqual([])
    expect(conv.groupHits.value).toEqual({})
    expect(fetchPreview).not.toHaveBeenCalled()
  })

  it('面板开着切档：以新档重 probe，groupHits 跟随新模板', async () => {
    vi.mocked(fetchClients).mockResolvedValueOnce(capableClients)
    const conv = setup()
    await conv.loadClients()
    conv.inputUrl.value = 'https://example.com/sub'
    vi.mocked(fetchPreview).mockResolvedValue({
      ...okResponse,
      nodes: ['A'],
      groupHits: { '🇰🇷 韩国节点': 0 },
    })
    conv.groupsOpen.value = true
    await vi.waitFor(() => expect(conv.probeState.value).toBe('ready'))
    expect(fetchPreview).toHaveBeenCalledTimes(1)

    vi.mocked(fetchPreview).mockResolvedValue({ ...okResponse, nodes: ['A'], groupHits: { '💬 Ai平台': 0 } })
    conv.selectPreset('full_multimode')
    await vi.waitFor(() => expect(fetchPreview).toHaveBeenCalledTimes(2))
    expect(fetchPreview).toHaveBeenLastCalledWith('https://example.com/sub', 'auto', 'full_multimode', {
      probe: true,
    })
    await vi.waitFor(() => expect(conv.groupHits.value).toEqual({ '💬 Ai平台': 0 }))
  })

  it('F1 同类：切到不支持分组的档位时 submit 不发送 exclude（残留在状态里，切回恢复）', async () => {
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
    conv.excluded.value = ['🇰🇷 韩国节点']

    conv.selectPreset('full_multimode')
    vi.mocked(fetchPreview).mockResolvedValueOnce(okResponse)
    await conv.submit()
    expect(fetchPreview).toHaveBeenLastCalledWith('https://example.com/sub', 'auto', 'full_multimode')
    expect(conv.phase.value).toBe('result')

    // 切回支持档：excluded 已被切档清空（设计：剔除清单不跨模板继承），不会误带
    conv.selectPreset('default')
    expect(conv.excluded.value).toEqual([])
  })

  // ---- 评审 F2：切档作废在途 probe ----

  it('F2 旧档 probe 在途时收起并切档：旧响应被丢弃，重开面板以新档重探', async () => {
    vi.mocked(fetchClients).mockResolvedValueOnce(capableClients)
    const conv = setup()
    await conv.loadClients()
    conv.inputUrl.value = 'https://example.com/sub'
    let resolveOld!: (v: PreviewResponse) => void
    vi.mocked(fetchPreview).mockReturnValueOnce(
      new Promise((r) => {
        resolveOld = r
      }),
    )
    conv.groupsOpen.value = true
    await vi.waitFor(() => expect(fetchPreview).toHaveBeenCalledTimes(1))

    conv.groupsOpen.value = false
    conv.selectPreset('full_multimode') // 切档：在途 probe 作废
    await nextTick()

    resolveOld({ ...okResponse, nodes: ['旧档节点'], groupHits: { '🇰🇷 韩国节点': 3 } })
    await nextTick()
    await nextTick()
    expect(conv.groupHits.value).toEqual({}) // 旧模板命中数不得写入新档
    expect(conv.nodes.value).toEqual([])
    expect(conv.probeState.value).not.toBe('ready')

    vi.mocked(fetchPreview).mockResolvedValueOnce({
      ...okResponse,
      nodes: ['新档节点'],
      groupHits: { '💬 Ai平台': 1 },
    })
    conv.groupsOpen.value = true
    await vi.waitFor(() => expect(conv.probeState.value).toBe('ready'))
    expect(fetchPreview).toHaveBeenLastCalledWith('https://example.com/sub', 'auto', 'full_multimode', {
      probe: true,
    })
    expect(conv.nodes.value).toEqual(['新档节点'])
    expect(conv.groupHits.value).toEqual({ '💬 Ai平台': 1 })
  })

  // ---- 评审 F3：probedUrl 按请求时的 URL 记账 ----

  it('F3 probe 在途改 URL 且收起：响应归属请求时的 A，重开面板对 B 重探', async () => {
    const conv = setup()
    conv.inputUrl.value = 'https://example.com/a'
    let resolveA!: (v: PreviewResponse) => void
    vi.mocked(fetchPreview).mockReturnValueOnce(
      new Promise((r) => {
        resolveA = r
      }),
    )
    conv.groupsOpen.value = true
    await vi.waitFor(() => expect(fetchPreview).toHaveBeenCalledTimes(1))

    conv.inputUrl.value = 'https://example.com/b' // 防抖待发
    conv.groupsOpen.value = false // 收起清掉防抖：B 的 probe 不会发出
    await nextTick()

    resolveA({ ...okResponse, nodes: ['A 节点'] })
    await vi.waitFor(() => expect(conv.probeState.value).toBe('ready'))
    expect(conv.nodes.value).toEqual(['A 节点'])

    vi.mocked(fetchPreview).mockResolvedValueOnce({ ...okResponse, nodes: ['B 节点'] })
    conv.groupsOpen.value = true
    await vi.waitFor(() => expect(fetchPreview).toHaveBeenCalledTimes(2))
    expect(fetchPreview).toHaveBeenLastCalledWith('https://example.com/b', 'auto', 'default', { probe: true })
    await vi.waitFor(() => expect(conv.nodes.value).toEqual(['B 节点']))
  })

  // ---- 评审 F5：空 URL 重开面板不残留旧状态 ----

  it('F5 ready 后清空 URL 并收起：重开面板状态回 idle、名单清空（面板显示空态文案）、不发新请求', async () => {
    const conv = setup()
    conv.inputUrl.value = 'https://example.com/sub'
    vi.mocked(fetchPreview).mockResolvedValueOnce({
      ...okResponse,
      nodes: ['N1'],
      groupHits: { '🇰🇷 韩国节点': 1 },
    })
    conv.groupsOpen.value = true
    await vi.waitFor(() => expect(conv.probeState.value).toBe('ready'))

    conv.inputUrl.value = '' // 防抖未到就被收起清掉：探针状态仍是旧订阅的 ready
    conv.groupsOpen.value = false
    await nextTick()
    expect(conv.probeState.value).toBe('ready') // 场景前提：残留 ready 与旧名单
    expect(conv.nodes.value).toEqual(['N1'])

    conv.groupsOpen.value = true // 重开：空 URL 分支清掉残留
    await nextTick()
    await nextTick()
    expect(conv.probeState.value).toBe('idle') // GroupPanel 据此渲染「粘贴订阅链接后可加载节点名单」
    expect(conv.nodes.value).toEqual([])
    expect(conv.groupHits.value).toEqual({})
    expect(fetchPreview).toHaveBeenCalledTimes(1) // 未发新请求
  })
})

describe('useConverter 分组偏好记忆（v1.6.0）', () => {
  // 独立 URL：与文件内其他 describe 的 'https://example.com/sub' 隔离——真实 timers 用例的
  // pending 草稿落盘可能跨用例落地，避免误命中本 describe 的快照查询
  const SUB = 'https://example.com/prefs-sub'
  const OTHER = 'https://example.com/prefs-other'

  const capableClients = {
    clients: [],
    presets: [
      {
        id: 'default',
        name: '默认',
        url: 'https://a.ini',
        customGroups: true,
        groups: [
          { name: '🚀 节点选择', locked: true },
          { name: '🇰🇷 韩国节点', locked: false },
        ],
      },
      {
        id: 'full_multimode',
        name: '分组',
        url: 'https://b.ini',
        customGroups: true,
        groups: [
          { name: '🚀 节点选择', locked: true },
          { name: '🇰🇷 韩国节点', locked: false },
        ],
      },
    ],
    capabilities: { customGroups: true },
  }

  const intent = [{ name: '专线', keywords: ['专线'], urlTest: true }]

  function makeSnap(overrides: Partial<DraftSnapshot> = {}): DraftSnapshot {
    return {
      target: 'surge',
      preset: 'full_multimode',
      groups: intent,
      excluded: ['🇰🇷 韩国节点'],
      draftKeywords: ['家宽'],
      updatedAt: '2026-11-03T00:00:00.000Z',
      ...overrides,
    }
  }

  // 微任务链驱动（watch flush → probe → 回填全是微任务）：nextTick 轮询到条件成立
  async function tickUntil(cond: () => boolean, rounds = 30) {
    for (let i = 0; i < rounds && !cond(); i++) await nextTick()
    expect(cond(), 'condition not met after ticks').toBe(true)
  }

  beforeEach(() => {
    localStorage.clear()
    vi.mocked(fetchPreview).mockReset()
    vi.mocked(fetchClients).mockReset().mockResolvedValue({ clients: [], presets: [] })
    vi.mocked(fetchPreferences).mockReset().mockResolvedValue({ custom_groups: null, exclude: null })
    vi.mocked(deletePreferences).mockReset().mockResolvedValue({ deleted_custom: true, deleted_exclude: 1 })
  })

  // ---- T4：localStorage 草稿层（落盘只由用户编辑触发，spec 修订 1）----

  it('粘贴订阅本身不落盘：真实等 >300ms 后 probe，服务端回填仍触发（回填门不被空快照关死）', async () => {
    vi.mocked(fetchClients).mockResolvedValueOnce(capableClients)
    const conv = setup()
    await conv.loadClients()
    conv.inputUrl.value = SUB
    await new Promise((resolve) => setTimeout(resolve, 350)) // 真实等过 300ms 防抖窗口
    expect(getSnapshot(SUB)).toBeNull() // 粘贴不是编辑：不产生快照

    vi.mocked(fetchPreview).mockResolvedValue({ ...okResponse, nodes: ['A'] })
    vi.mocked(fetchPreferences).mockResolvedValue({
      custom_groups: intent,
      exclude: ['🇰🇷 韩国节点'],
    })
    conv.groupsOpen.value = true
    await vi.waitFor(() => expect(conv.groups.value).toEqual(intent))
    expect(conv.excluded.value).toEqual(['🇰🇷 韩国节点'])
    expect(conv.restoreNotice.value).toEqual({ groups: 1, excluded: 1 })
  })

  it('用户编辑防抖 300ms 落盘；新会话粘贴同订阅 → 整份恢复（target/preset/groups/excluded/未确认 chips）', async () => {
    vi.useFakeTimers()
    try {
      const conv = setup()
      conv.inputUrl.value = SUB
      conv.selectClient('surge')
      conv.selectPreset('full_multimode')
      await nextTick() // 切档 watcher（清 excluded）先跑完，再配 excluded
      conv.excluded.value = ['🇰🇷 韩国节点']
      conv.groups.value = intent
      conv.draftKeywords.value = ['家宽']
      await vi.advanceTimersByTimeAsync(300)
      expect(getSnapshot(SUB)).toMatchObject({
        target: 'surge',
        preset: 'full_multimode',
        groups: intent,
        excluded: ['🇰🇷 韩国节点'],
        draftKeywords: ['家宽'],
      })

      // 模拟刷新：全新实例 + 空面板，粘贴同订阅（含首尾空白）→ 恢复
      const conv2 = useConverter()
      conv2.inputUrl.value = `  ${SUB}  `
      await tickUntil(() => conv2.selectedTarget.value === 'surge')
      await tickUntil(() => conv2.groups.value.length === 1)
      expect(conv2.selectedPreset.value).toBe('full_multimode')
      expect(conv2.groups.value).toEqual(intent)
      expect(conv2.excluded.value).toEqual(['🇰🇷 韩国节点']) // 未被切档 watcher 顶掉（restoring 守卫）
      expect(conv2.draftKeywords.value).toEqual(['家宽'])
    } finally {
      vi.useRealTimers()
    }
  })

  it('同 key（尾部补空格）不动作：不重复恢复、不清空、未落盘编辑不被顶掉', async () => {
    vi.useFakeTimers()
    try {
      saveSnapshot(SUB, makeSnap())
      const conv = setup()
      conv.inputUrl.value = SUB
      await tickUntil(() => conv.groups.value.length === 1) // 恢复完成
      const modified = [{ name: '新组', keywords: ['新'], urlTest: false }]
      conv.groups.value = modified // 防抖 pending 中
      conv.inputUrl.value = `${SUB} ` // trim 后同 key
      await nextTick()
      await nextTick()
      expect(conv.groups.value).toEqual(modified) // currentKey 守卫：不触发恢复
      await vi.advanceTimersByTimeAsync(300)
      expect(getSnapshot(SUB)?.groups).toEqual(modified) // 落盘的是修改后状态
    } finally {
      vi.useRealTimers()
    }
  })

  it('换订阅无本地快照 → 程序化清空组状态（target/preset 保留）；A 的组不写进 B 的 key/档案', async () => {
    vi.useFakeTimers()
    try {
      const conv = setup()
      conv.inputUrl.value = SUB
      conv.selectClient('surge')
      conv.groups.value = intent
      conv.excluded.value = ['🇰🇷 韩国节点']
      conv.draftKeywords.value = ['家宽']
      await vi.advanceTimersByTimeAsync(300) // A 落盘
      expect(getSnapshot(SUB)).not.toBeNull()

      conv.inputUrl.value = OTHER // B 无快照
      expect(conv.groups.value).toEqual([]) // 程序化清空（跨订阅污染修复）
      expect(conv.excluded.value).toEqual([])
      expect(conv.draftKeywords.value).toEqual([])
      expect(conv.selectedTarget.value).toBe('surge') // target/preset 保留
      await vi.advanceTimersByTimeAsync(300)
      expect(getSnapshot(OTHER)).toBeNull() // 换订阅本身不落盘
      expect(getSnapshot(SUB)?.groups).toEqual(intent) // A 的档案原样

      // 在 B 上编辑 → 只写 B
      conv.groups.value = [{ name: 'B组', keywords: ['B'], urlTest: false }]
      await vi.advanceTimersByTimeAsync(300)
      expect(getSnapshot(OTHER)?.groups).toEqual([
        { name: 'B组', keywords: ['B'], urlTest: false },
      ])
      expect(getSnapshot(SUB)?.groups).toEqual(intent)
    } finally {
      vi.useRealTimers()
    }
  })

  it('空 URL 首次粘贴（无快照）：首次绑定不清空用户先配的组/删减', async () => {
    const conv = setup()
    conv.groups.value = intent
    conv.excluded.value = ['🇰🇷 韩国节点']
    conv.inputUrl.value = SUB
    await nextTick()
    await nextTick()
    expect(conv.groups.value).toEqual(intent)
    expect(conv.excluded.value).toEqual(['🇰🇷 韩国节点'])
  })

  it('无订阅（URL 空）时编辑不落盘', async () => {
    vi.useFakeTimers()
    try {
      const conv = setup()
      conv.groups.value = intent
      conv.draftKeywords.value = ['家宽']
      await vi.advanceTimersByTimeAsync(300)
      expect(localStorage.getItem('ssc.draft.v1')).toBeNull()
    } finally {
      vi.useRealTimers()
    }
  })

  it('URL 清空（key 变化）：程序化清空面板组状态', async () => {
    vi.useFakeTimers()
    try {
      const conv = setup()
      conv.inputUrl.value = SUB
      conv.groups.value = intent
      await vi.advanceTimersByTimeAsync(300)
      expect(getSnapshot(SUB)).not.toBeNull()

      conv.inputUrl.value = ''
      expect(conv.groups.value).toEqual([]) // 无快照路径 → 清空（target/preset 保留）
    } finally {
      vi.useRealTimers()
    }
  })

  it('本地快照的 target/preset 已不在注册表 → 跳过该字段保持当前值，groups 照常恢复', async () => {
    vi.mocked(fetchClients).mockResolvedValueOnce(capableClients)
    const conv = setup()
    await conv.loadClients()
    saveSnapshot(SUB, makeSnap({ target: '不存在的target', preset: '不存在的preset' }))
    conv.inputUrl.value = SUB
    await vi.waitFor(() => expect(conv.groups.value).toEqual(intent))
    expect(conv.selectedTarget.value).toBe('auto') // 无效字段跳过
    expect(conv.selectedPreset.value).toBe('default')
  })

  // ---- T5：服务端回填（真实 timers，spec 修订 3/9）----

  it('本地优先：本地有该订阅快照 → probe 成功也不问服务端', async () => {
    saveSnapshot(SUB, makeSnap())
    const conv = setup()
    conv.inputUrl.value = SUB
    await vi.waitFor(() => expect(conv.groups.value).toEqual(intent))
    vi.mocked(fetchPreview).mockResolvedValue({ ...okResponse, nodes: ['A'] })
    conv.groupsOpen.value = true
    await vi.waitFor(() => expect(conv.probeState.value).toBe('ready'))
    await nextTick()
    await nextTick()
    expect(fetchPreferences).not.toHaveBeenCalled()
  })

  it('服务端回填：无快照 + probe 成功 + 能力位真 → query(sub_url, preset_id)，groups/exclude 交集落位 + 提示条', async () => {
    vi.mocked(fetchClients).mockResolvedValueOnce(capableClients)
    const conv = setup()
    await conv.loadClients()
    conv.inputUrl.value = SUB
    vi.mocked(fetchPreview).mockResolvedValue({ ...okResponse, nodes: ['香港专线 01'] })
    vi.mocked(fetchPreferences).mockResolvedValue({
      custom_groups: intent,
      exclude: ['🇰🇷 韩国节点', '模板里没有的组'],
    })

    conv.groupsOpen.value = true
    await vi.waitFor(() => expect(conv.groups.value).toEqual(intent))
    expect(fetchPreferences).toHaveBeenCalledWith(SUB, 'default')
    // exclude 与当前档模板组列表求交集：悬空名滤掉
    expect(conv.excluded.value).toEqual(['🇰🇷 韩国节点'])
    expect(conv.restoreNoticeText.value).toBe('已恢复 1 个自定义分组、剔除 1 个模板组')
  })

  it('回填不产生本地快照（快照只由用户编辑产生）；回填后首次编辑 → 提示条自动收起、回滚点作废', async () => {
    vi.mocked(fetchClients).mockResolvedValueOnce(capableClients)
    const conv = setup()
    await conv.loadClients()
    conv.inputUrl.value = SUB
    vi.mocked(fetchPreview).mockResolvedValue({ ...okResponse, nodes: ['A'] })
    vi.mocked(fetchPreferences).mockResolvedValue({
      custom_groups: intent,
      exclude: ['🇰🇷 韩国节点'],
    })
    conv.groupsOpen.value = true
    await vi.waitFor(() => expect(conv.groups.value).toEqual(intent))

    // 真实等过防抖窗口：回填是程序化路径，不落盘
    await new Promise((resolve) => setTimeout(resolve, 350))
    expect(getSnapshot(SUB)).toBeNull()

    // 用户编辑 → 提示条自动收起、回滚点作废（「不用了」不得吞掉新编辑）
    conv.draftKeywords.value = ['新词']
    expect(conv.restoreNotice.value).toBeNull()
    conv.undoRestore() // 回滚点已作废：无效果
    expect(conv.draftKeywords.value).toEqual(['新词'])
    expect(conv.groups.value).toEqual(intent) // 回填的组仍在，不被吞
    // 编辑照常落盘
    await vi.waitFor(() => expect(getSnapshot(SUB)?.draftKeywords).toEqual(['新词']))
  })

  it('撤销回填：无后续编辑时恢复回填前面板状态（不清存档）', async () => {
    vi.mocked(fetchClients).mockResolvedValueOnce(capableClients)
    const conv = setup()
    await conv.loadClients()
    conv.inputUrl.value = SUB
    const before = [{ name: '手配', keywords: ['手'], urlTest: false }]
    conv.groups.value = before
    vi.mocked(fetchPreview).mockResolvedValue({ ...okResponse, nodes: ['A'] })
    vi.mocked(fetchPreferences).mockResolvedValue({ custom_groups: intent, exclude: ['🇰🇷 韩国节点'] })

    conv.groupsOpen.value = true
    await vi.waitFor(() => expect(conv.excluded.value).toEqual(['🇰🇷 韩国节点']))
    expect(conv.groups.value).toEqual(intent)

    conv.undoRestore()
    expect(conv.groups.value).toEqual(before) // 回滚到回填前
    expect(conv.excluded.value).toEqual([])
    expect(conv.restoreNotice.value).toBeNull()
    expect(conv.restoreNoticeText.value).toBe('')
    expect(deletePreferences).not.toHaveBeenCalled() // 不清存档
  })

  it('切档重 probe：按新 preset 重新 query，新档 exclude 档案可回填', async () => {
    vi.mocked(fetchClients).mockResolvedValueOnce(capableClients)
    const conv = setup()
    await conv.loadClients()
    conv.inputUrl.value = SUB
    vi.mocked(fetchPreview).mockResolvedValue({ ...okResponse, nodes: ['A'] })
    vi.mocked(fetchPreferences).mockResolvedValue({ custom_groups: intent, exclude: ['🇰🇷 韩国节点'] })
    conv.groupsOpen.value = true
    await vi.waitFor(() => expect(conv.groups.value).toEqual(intent))
    expect(fetchPreferences).toHaveBeenLastCalledWith(SUB, 'default')

    // 切档（面板开着 → 以新档重 probe → 按新 preset 重查；无本地快照，门仍开着）
    conv.selectPreset('full_multimode')
    await vi.waitFor(() => expect(fetchPreferences).toHaveBeenLastCalledWith(SUB, 'full_multimode'))
    await vi.waitFor(() => expect(conv.excluded.value).toEqual(['🇰🇷 韩国节点']))
  })

  it('同 (url, preset) 在途查询去重：重复 probe 不重复发 query', async () => {
    vi.mocked(fetchClients).mockResolvedValueOnce(capableClients)
    const conv = setup()
    await conv.loadClients()
    conv.inputUrl.value = SUB
    vi.mocked(fetchPreview).mockResolvedValue({ ...okResponse, nodes: ['A'] })
    let resolveQuery!: (v: { custom_groups: typeof intent | null; exclude: string[] | null }) => void
    vi.mocked(fetchPreferences).mockReturnValue(
      new Promise((r) => {
        resolveQuery = r
      }),
    )
    conv.groupsOpen.value = true
    await vi.waitFor(() => expect(fetchPreferences).toHaveBeenCalledTimes(1))

    await conv.probeNodes() // 同 url 同档再探：在途去重
    expect(fetchPreferences).toHaveBeenCalledTimes(1)

    resolveQuery({ custom_groups: null, exclude: null })
    await nextTick()
    await nextTick()
    expect(conv.restoreNotice.value).toBeNull()
  })

  it('query 失败静默：probe 仍 ready、面板不动、无提示条', async () => {
    vi.mocked(fetchClients).mockResolvedValueOnce(capableClients)
    const conv = setup()
    await conv.loadClients()
    conv.inputUrl.value = SUB
    vi.mocked(fetchPreview).mockResolvedValue({ ...okResponse, nodes: ['A'] })
    vi.mocked(fetchPreferences).mockRejectedValue(new ApiUnreachableError())

    conv.groupsOpen.value = true
    await vi.waitFor(() => expect(fetchPreferences).toHaveBeenCalledTimes(1))
    await nextTick()
    await nextTick()
    expect(conv.probeState.value).toBe('ready')
    expect(conv.groups.value).toEqual([])
    expect(conv.restoreNotice.value).toBeNull()
  })

  it('能力位 false（部署或档位不支持）→ probe 成功不问服务端', async () => {
    const conv = setup() // fetchClients 降级 mock：无能力位 → customGroupsAvailable false
    conv.inputUrl.value = SUB
    vi.mocked(fetchPreview).mockResolvedValue({ ...okResponse, nodes: ['A'] })
    conv.groupsOpen.value = true
    await vi.waitFor(() => expect(conv.probeState.value).toBe('ready'))
    await nextTick()
    await nextTick()
    expect(fetchPreferences).not.toHaveBeenCalled()
  })

  it('回填响应慢回时 URL 已换：不落到新订阅头上', async () => {
    vi.mocked(fetchClients).mockResolvedValueOnce(capableClients)
    const conv = setup()
    await conv.loadClients()
    conv.inputUrl.value = SUB
    vi.mocked(fetchPreview).mockResolvedValue({ ...okResponse, nodes: ['A'] })
    let resolveQuery!: (v: { custom_groups: typeof intent | null; exclude: string[] | null }) => void
    vi.mocked(fetchPreferences).mockReturnValue(
      new Promise((r) => {
        resolveQuery = r
      }),
    )
    conv.groupsOpen.value = true
    await vi.waitFor(() => expect(fetchPreferences).toHaveBeenCalledTimes(1))
    conv.inputUrl.value = OTHER // query 在途时换订阅
    resolveQuery({ custom_groups: intent, exclude: null })
    await nextTick()
    await nextTick()
    await nextTick()
    expect(conv.groups.value).toEqual([]) // 旧订阅的档案不回填到新订阅
    expect(conv.restoreNotice.value).toBeNull()
  })

  // ---- T5：清除记忆（spec 修订 5）----

  it('清除入口：删本地快照 + DELETE 服务端（sub_url），成功返回 true；面板保留', async () => {
    saveSnapshot(SUB, makeSnap())
    const conv = setup()
    conv.inputUrl.value = SUB
    await vi.waitFor(() => expect(conv.groups.value).toEqual(intent))

    const ok = await conv.clearPreferences()
    expect(ok).toBe(true)
    expect(getSnapshot(SUB)).toBeNull()
    expect(deletePreferences).toHaveBeenCalledWith(SUB)
    expect(conv.groups.value).toEqual(intent) // 清除记忆 ≠ 清空面板
  })

  it('清除后 300ms 不复活：pending 落盘被取消', async () => {
    vi.useFakeTimers()
    try {
      const conv = setup()
      conv.inputUrl.value = SUB
      conv.groups.value = intent // 武装 300ms 落盘
      await conv.clearPreferences() // clearTimeout + 删快照
      await vi.advanceTimersByTimeAsync(300)
      expect(getSnapshot(SUB)).toBeNull()
    } finally {
      vi.useRealTimers()
    }
  })

  it('清除后本会话禁用服务端自动回填（防「清了又回来」）', async () => {
    vi.mocked(fetchClients).mockResolvedValueOnce(capableClients)
    const conv = setup()
    await conv.loadClients()
    conv.inputUrl.value = SUB
    vi.mocked(fetchPreview).mockResolvedValue({ ...okResponse, nodes: ['A'] })
    conv.groupsOpen.value = true
    await vi.waitFor(() => expect(conv.probeState.value).toBe('ready'))
    await conv.clearPreferences()

    vi.mocked(fetchPreferences).mockClear()
    await conv.probeNodes() // 再次 probe：不得回填
    await vi.waitFor(() => expect(conv.probeState.value).toBe('ready'))
    await nextTick()
    await nextTick()
    expect(fetchPreferences).not.toHaveBeenCalled()
  })

  it('清除作废在途回填响应：清除后慢回的档案不回填', async () => {
    vi.mocked(fetchClients).mockResolvedValueOnce(capableClients)
    const conv = setup()
    await conv.loadClients()
    conv.inputUrl.value = SUB
    vi.mocked(fetchPreview).mockResolvedValue({ ...okResponse, nodes: ['A'] })
    let resolveQuery!: (v: { custom_groups: typeof intent | null; exclude: string[] | null }) => void
    vi.mocked(fetchPreferences).mockReturnValue(
      new Promise((r) => {
        resolveQuery = r
      }),
    )
    conv.groupsOpen.value = true
    await vi.waitFor(() => expect(fetchPreferences).toHaveBeenCalledTimes(1))
    await conv.clearPreferences()
    resolveQuery({ custom_groups: intent, exclude: ['🇰🇷 韩国节点'] })
    await nextTick()
    await nextTick()
    await nextTick()
    expect(conv.groups.value).toEqual([])
    expect(conv.restoreNotice.value).toBeNull()
  })

  it('清除入口：服务端删除失败 → 本地仍已清，返回 false（UI 提示可重试）', async () => {
    saveSnapshot(SUB, makeSnap())
    const conv = setup()
    conv.inputUrl.value = SUB
    vi.mocked(deletePreferences).mockRejectedValueOnce(new ApiUnreachableError())

    const ok = await conv.clearPreferences()
    expect(ok).toBe(false)
    expect(getSnapshot(SUB)).toBeNull()
  })

  it('清除入口：无订阅链接 → false 且不调服务端', async () => {
    const conv = setup()
    const ok = await conv.clearPreferences()
    expect(ok).toBe(false)
    expect(deletePreferences).not.toHaveBeenCalled()
  })

  it('restart 清回填提示条', async () => {
    const conv = setup()
    conv.restoreNotice.value = { groups: 1, excluded: 1 }
    conv.restart()
    expect(conv.restoreNotice.value).toBeNull()
  })

  // ---- v1.6.1：正则模式草稿（draftMode / draftPattern）----

  it('正则草稿防抖落盘；新会话粘贴同订阅 → 模式与正则恢复', async () => {
    vi.useFakeTimers()
    try {
      const conv = setup()
      conv.inputUrl.value = SUB
      conv.draftMode.value = 'regex'
      conv.draftPattern.value = '(?=.*香港)(?!.*维护)'
      await vi.advanceTimersByTimeAsync(300)
      expect(getSnapshot(SUB)).toMatchObject({
        draftMode: 'regex',
        draftPattern: '(?=.*香港)(?!.*维护)',
      })

      const conv2 = useConverter()
      conv2.inputUrl.value = SUB
      await tickUntil(() => conv2.draftMode.value === 'regex')
      expect(conv2.draftPattern.value).toBe('(?=.*香港)(?!.*维护)')
    } finally {
      vi.useRealTimers()
    }
  })

  it('换订阅无快照：draftPattern 清空、draftMode 重置 keyword（程序化清空不落盘）', async () => {
    vi.useFakeTimers()
    try {
      const conv = setup()
      conv.inputUrl.value = SUB
      conv.draftMode.value = 'regex'
      conv.draftPattern.value = '(A|B)'
      await vi.advanceTimersByTimeAsync(300)
      expect(getSnapshot(SUB)).not.toBeNull()

      conv.inputUrl.value = OTHER
      expect(conv.draftPattern.value).toBe('')
      expect(conv.draftMode.value).toBe('keyword')
      await vi.advanceTimersByTimeAsync(300)
      expect(getSnapshot(OTHER)).toBeNull()
    } finally {
      vi.useRealTimers()
    }
  })

  it('旧快照（draftMode/draftPattern 缺席）：恢复降级为关键词模式 + 空正则草稿', async () => {
    saveSnapshot(SUB, makeSnap())
    const conv = setup()
    conv.inputUrl.value = SUB
    await tickUntil(() => conv.groups.value.length === 1)
    expect(conv.draftMode.value).toBe('keyword')
    expect(conv.draftPattern.value).toBe('')
  })

  it('清除记忆：不复活本地快照（动作本身不得重新武装落盘）', async () => {
    vi.useFakeTimers()
    try {
      const conv = setup()
      conv.inputUrl.value = SUB
      conv.draftPattern.value = '(A|B)' // 武装 300ms 落盘
      await conv.clearPreferences()
      await vi.advanceTimersByTimeAsync(300)
      expect(getSnapshot(SUB)).toBeNull()
    } finally {
      vi.useRealTimers()
    }
  })

  it('带 pattern 的正则组随 groups 落盘与恢复（groups 原样携带 pattern 字段）', async () => {
    vi.useFakeTimers()
    try {
      const regexIntent = [{ name: '纯净港', keywords: [], pattern: '(?=.*香港)(?!.*维护)', urlTest: false }]
      const conv = setup()
      conv.inputUrl.value = SUB
      conv.groups.value = regexIntent
      await vi.advanceTimersByTimeAsync(300)
      expect(getSnapshot(SUB)?.groups).toEqual(regexIntent)

      const conv2 = useConverter()
      conv2.inputUrl.value = SUB
      await tickUntil(() => conv2.groups.value.length === 1)
      expect(conv2.groups.value).toEqual(regexIntent)
    } finally {
      vi.useRealTimers()
    }
  })

  // ---- v1.7.1：多机场与容灾 Fallback（subscriptions）----

  describe('多机场与容灾 Fallback（v1.7.1）', () => {
    it('extractTagFromUrl 准确从 hostname 提取机场简写', async () => {
      const { extractTagFromUrl } = await import('./useConverter')
      expect(extractTagFromUrl('https://sub.airport-one.com/link')).toBe('airport-one')
      expect(extractTagFromUrl('https://www.flycloud.net/sub')).toBe('flycloud')
      expect(extractTagFromUrl('https://example.com:8443/api')).toBe('example')
      expect(extractTagFromUrl('')).toBe('')
      expect(extractTagFromUrl('not-a-url')).toBe('')
    })

    it('handleUrlBatch 支持换行或竖线粘贴自动拆分为主力与备用订阅并自动生成 tag', () => {
      const conv = setup()
      const pasted = `https://a.com/sub
https://b.com/sub|https://b.com/sub2`
      const handled = conv.handleUrlBatch(pasted)
      expect(handled).toBe(true)
      expect(conv.inputUrl.value).toBe('https://a.com/sub')
      expect(conv.mainTag.value).toBe('a')
      expect(conv.extraSubs.value.length).toBe(2)
      expect(conv.extraSubs.value[0]).toEqual({ url: 'https://b.com/sub', tag: 'b' })
      expect(conv.extraSubs.value[1]).toEqual({ url: 'https://b.com/sub2', tag: 'b-2' })
    })

    it('多订阅卡片操作：addExtraSub 与 removeExtraSub', () => {
      const conv = setup()
      expect(conv.allSubscriptions.value.length).toBe(0)
      conv.inputUrl.value = 'https://main.com/sub'
      expect(conv.allSubscriptions.value.length).toBe(1)

      conv.addExtraSub('https://backup.com/sub', '备用')
      expect(conv.extraSubs.value.length).toBe(1)
      expect(conv.allSubscriptions.value.length).toBe(2)
      expect(conv.allSubscriptions.value[0].tag).toBe('main') // 默认从 url 提取
      expect(conv.allSubscriptions.value[1].tag).toBe('备用')

      conv.removeExtraSub(0)
      expect(conv.extraSubs.value.length).toBe(0)
      expect(conv.allSubscriptions.value.length).toBe(1)
    })

    it('多订阅提交：submit 带上 subscriptions 字段', async () => {
      const conv = setup()
      conv.inputUrl.value = 'https://main.com/sub'
      conv.mainTag.value = '主力'
      conv.addExtraSub('https://backup.com/sub', '备用')

      vi.mocked(fetchPreview).mockResolvedValueOnce(okResponse)
      await conv.submit()

      expect(fetchPreview).toHaveBeenLastCalledWith(
        'https://main.com/sub',
        'auto',
        'default',
        {
          subscriptions: [
            { url: 'https://main.com/sub', tag: '主力' },
            { url: 'https://backup.com/sub', tag: '备用' },
          ],
        },
      )
    })

    it('多订阅探测：probeNodes 带上 subscriptions 字段', async () => {
      const conv = setup()
      conv.inputUrl.value = 'https://main.com/sub'
      conv.mainTag.value = '主力'
      conv.addExtraSub('https://backup.com/sub', '备用')

      vi.mocked(fetchPreview).mockResolvedValueOnce({
        ...okResponse,
        nodes: ['[主力] 香港 01', '[备用] 香港 01'],
      })

      conv.groupsOpen.value = true
      await vi.waitFor(() => expect(conv.probeState.value).toBe('ready'))

      expect(fetchPreview).toHaveBeenLastCalledWith(
        'https://main.com/sub',
        'auto',
        'default',
        {
          probe: true,
          subscriptions: [
            { url: 'https://main.com/sub', tag: '主力' },
            { url: 'https://backup.com/sub', tag: '备用' },
          ],
        },
      )
      expect(conv.nodes.value).toEqual(['[主力] 香港 01', '[备用] 香港 01'])
    })

    it('restart 清空 extraSubs 和 mainTag', () => {
      const conv = setup()
      conv.inputUrl.value = 'https://main.com/sub'
      conv.mainTag.value = '主力'
      conv.addExtraSub('https://backup.com/sub', '备用')
      expect(conv.extraSubs.value.length).toBe(1)

      conv.restart()
      expect(conv.extraSubs.value.length).toBe(0)
      expect(conv.mainTag.value).toBe('')
    })
  })
})
