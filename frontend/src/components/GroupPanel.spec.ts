import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { nextTick } from 'vue'
import GroupPanel from './GroupPanel.vue'
import type { NodeGroup, PresetGroup } from '../types'

// 文件级 fake timers：打词防抖 250ms 用 advanceTimersByTime 驱动（无时间依赖的用例不受影响）
beforeEach(() => {
  vi.useFakeTimers()
})
afterEach(() => {
  vi.useRealTimers()
})

const NODES = [
  '香港专线 01',
  '香港专线 02',
  '日本专线 01',
  '美国 IEPL 1.0',
  '新加坡 IEPL 1x0',
  '台湾家宽',
  '韩国 BGP',
]

type PanelProps = InstanceType<typeof GroupPanel>['$props']

function mountPanel(props: Partial<PanelProps> = {}) {
  return mount(GroupPanel, {
    props: {
      available: true,
      open: true,
      modelValue: [],
      probeState: 'ready',
      nodes: NODES,
      templateGroups: [],
      excluded: [],
      groupHits: {},
      ...props,
    } as PanelProps,
  })
}

// 逐词回车加 chip（chips 即时参与命中，无需防抖等待）
async function addWords(wrapper: ReturnType<typeof mountPanel>, ws: string[]) {
  for (const w of ws) {
    const input = wrapper.find('input[aria-label="分组关键词"]')
    await input.setValue(w)
    await input.trigger('keydown.enter')
  }
  await nextTick()
}

// 打词步点「成组」进入命名步（等待命名输入框渲染）
async function enterNaming(wrapper: ReturnType<typeof mountPanel>, ws: string[]) {
  await addWords(wrapper, ws)
  await wrapper.find('.group-confirm').trigger('click')
  await nextTick()
}

describe('GroupPanel 入口与 probe 态', () => {
  it('能力位 false：整块不渲染（部署或所选档不支持，入口不出现）', () => {
    const wrapper = mountPanel({ available: false })
    expect(wrapper.find('.groups').exists()).toBe(false)
  })

  it('入口不依赖订阅链接：模板组清单本地可得，未粘 URL 也可点开', async () => {
    const wrapper = mountPanel({ open: false })
    const btn = wrapper.find('.groups-entry')
    expect(btn.exists()).toBe(true)
    expect(btn.attributes('disabled')).toBeUndefined()
    expect(btn.text()).toContain('分组管理')
    await btn.trigger('click')
    expect(wrapper.emitted('update:open')).toEqual([[true]])
  })

  it('展开态 probe loading / idle / failed 分支文案（ready 之外不渲染节点列表）', () => {
    const loading = mountPanel({ probeState: 'loading' })
    expect(loading.find('.groups-status').text()).toContain('正在拉取节点名单')
    expect(loading.find('.node-list').exists()).toBe(false)

    const idle = mountPanel({ probeState: 'idle' })
    expect(idle.find('.groups-status').text()).toContain('粘贴订阅链接后可加载节点名单')

    const failed = mountPanel({ probeState: 'failed' })
    expect(failed.find('.groups-status').text()).toContain('节点名单拉取失败')
    expect(failed.find('.node-list').exists()).toBe(false)
  })

  it('probe 失败态重试按钮 emit retry', async () => {
    const wrapper = mountPanel({ probeState: 'failed' })
    await wrapper.find('.groups-retry').trigger('click')
    expect(wrapper.emitted('retry')).toHaveLength(1)
  })

  it('展开态点「收起」emit update:open false', async () => {
    const wrapper = mountPanel()
    await wrapper.find('.groups-collapse').trigger('click')
    expect(wrapper.emitted('update:open')).toEqual([[false]])
  })

  it('唯一解释文案「机场新增同类节点会自动进组」', () => {
    const wrapper = mountPanel()
    expect(wrapper.find('.groups-note').text()).toBe('机场新增同类节点会自动进组')
  })

  it('ready 渲染节点名单全文，无词时不加 hit/dim class', () => {
    const wrapper = mountPanel()
    const items = wrapper.findAll('.node-list li')
    expect(items).toHaveLength(NODES.length)
    expect(items.map((li) => li.text())).toEqual(NODES)
    expect(items.every((li) => li.classes('hit') === false && li.classes('dim') === false)).toBe(true)
  })
})

describe('GroupPanel 打词匹配与防抖', () => {
  it('防抖 250ms：输入即时不高亮，250ms 后命中项加粗、未命中降灰、计数出现', async () => {
    const wrapper = mountPanel()
    const input = wrapper.find('input[aria-label="分组关键词"]')
    await input.setValue('专线')

    // 防抖窗口内：无命中计数、无高亮
    expect(wrapper.find('.match-count').exists()).toBe(false)
    expect(wrapper.findAll('.node-list li.hit')).toHaveLength(0)

    vi.advanceTimersByTime(250)
    await nextTick()
    expect(wrapper.find('.match-count').text()).toBe('已命中 3')
    const hits = wrapper.findAll('.node-list li.hit')
    expect(hits.map((li) => li.text())).toEqual(['香港专线 01', '香港专线 02', '日本专线 01'])
    expect(wrapper.findAll('.node-list li.dim')).toHaveLength(NODES.length - 3)
  })

  it('字面量 substring：关键词「1.0」命中「IEPL 1.0」不吃「IEPL 1x0」（. 不当通配）', async () => {
    const wrapper = mountPanel()
    await wrapper.find('input[aria-label="分组关键词"]').setValue('1.0')
    vi.advanceTimersByTime(250)
    await nextTick()
    const hits = wrapper.findAll('.node-list li.hit')
    expect(hits.map((li) => li.text())).toEqual(['美国 IEPL 1.0'])
    expect(wrapper.find('.match-count').text()).toBe('已命中 1')
  })

  it('零命中：成组禁用 + 提示换词（创建期零命中不确认成组）', async () => {
    const wrapper = mountPanel()
    await wrapper.find('input[aria-label="分组关键词"]').setValue('不存在的词')
    vi.advanceTimersByTime(250)
    await nextTick()
    expect(wrapper.find('.match-count').text()).toBe('已命中 0')
    expect(wrapper.find('.group-confirm').attributes('disabled')).toBeDefined()
    expect(wrapper.find('.group-kw-hint').text()).toContain('没命中任何节点')
  })

  it.each([
    ['纯空格', '   '],
    ['超 32 字', '专'.repeat(33)],
    ['含反引号', '专线`'],
    ['含控制字符', '专\u0001线'],
  ])('草稿 %s：成组禁用', async (_label, value) => {
    const wrapper = mountPanel()
    await wrapper.find('input[aria-label="分组关键词"]').setValue(value)
    vi.advanceTimersByTime(250)
    await nextTick()
    expect(wrapper.find('.group-confirm').attributes('disabled')).toBeDefined()
  })

  it('probeState 非 ready 时成组禁用（无名单无从判定命中）', () => {
    const loading = mountPanel({ probeState: 'loading' })
    expect(loading.find('.group-confirm').exists()).toBe(false)
  })
})

describe('GroupPanel 多关键词 chip 输入与 AND 命中', () => {
  it('回车加词成 chip：输入清空，chip 即时参与命中（无防抖等待）', async () => {
    const wrapper = mountPanel()
    const input = wrapper.find('input[aria-label="分组关键词"]')
    await input.setValue('香港')
    await input.trigger('keydown.enter')
    await nextTick()
    expect(wrapper.findAll('.kw-chip')).toHaveLength(1)
    expect(wrapper.find('.kw-chip').text()).toContain('香港')
    expect((wrapper.find('input[aria-label="分组关键词"]').element as HTMLInputElement).value).toBe('')
    expect(wrapper.find('.match-count').text()).toBe('已命中 2')
  })

  it('「＋」按钮加词与回车等价', async () => {
    const wrapper = mountPanel()
    await wrapper.find('input[aria-label="分组关键词"]').setValue('日本')
    await wrapper.find('.group-add-kw').trigger('click')
    await nextTick()
    expect(wrapper.findAll('.kw-chip')).toHaveLength(1)
    expect(wrapper.find('.match-count').text()).toBe('已命中 1')
  })

  it('删词：点词 chip 的 × 移除，命中数随之放宽', async () => {
    const wrapper = mountPanel()
    await addWords(wrapper, ['香港', '专线'])
    expect(wrapper.find('.match-count').text()).toBe('已命中 2')
    await wrapper.find('button[aria-label="删除关键词 香港"]').trigger('click')
    await nextTick()
    expect(wrapper.findAll('.kw-chip')).toHaveLength(1)
    expect(wrapper.find('.match-count').text()).toBe('已命中 3')
  })

  it('满 5 词禁加：输入框禁用、回车与＋均不加词、提示上限', async () => {
    const wrapper = mountPanel()
    await addWords(wrapper, ['香港', '日本', '美国', '台湾', '韩国'])
    expect(wrapper.findAll('.kw-chip')).toHaveLength(5)
    const input = wrapper.find('input[aria-label="分组关键词"]')
    expect(input.attributes('disabled')).toBeDefined()
    await input.setValue('专线')
    await input.trigger('keydown.enter')
    await nextTick()
    expect(wrapper.findAll('.kw-chip')).toHaveLength(5)
    expect(wrapper.find('.group-kw-hint').text()).toBe('最多 5 个关键词')
  })

  it.each([
    ['超 32 字', '专'.repeat(33)],
    ['含反引号', '专`线'],
    ['含控制字符', '专\u0001线'],
    ['纯空格', '   '],
  ])('词级校验拒收 %s：回车不加 chip', async (_label, value) => {
    const wrapper = mountPanel()
    await wrapper.find('input[aria-label="分组关键词"]').setValue(value)
    await wrapper.find('input[aria-label="分组关键词"]').trigger('keydown.enter')
    await nextTick()
    expect(wrapper.findAll('.kw-chip')).toHaveLength(0)
  })

  it('重复词拒收：同词已加过，再输不加并提示', async () => {
    const wrapper = mountPanel()
    await addWords(wrapper, ['专线'])
    await wrapper.find('input[aria-label="分组关键词"]').setValue('专线')
    await nextTick()
    expect(wrapper.find('.group-kw-hint').text()).toBe('这个词已经加过了')
    await wrapper.find('input[aria-label="分组关键词"]').trigger('keydown.enter')
    await nextTick()
    expect(wrapper.findAll('.kw-chip')).toHaveLength(1)
  })

  it('多词 AND：节点名同时含全部词才命中，词序无关', async () => {
    const wrapper = mountPanel()
    await addWords(wrapper, ['香港', '专线'])
    expect(wrapper.find('.match-count').text()).toBe('已命中 2')
    expect(wrapper.findAll('.node-list li.hit').map((li) => li.text())).toEqual([
      '香港专线 01',
      '香港专线 02',
    ])
    expect(wrapper.findAll('.node-list li.dim')).toHaveLength(NODES.length - 2)

    const reversed = mountPanel()
    await addWords(reversed, ['专线', '香港'])
    expect(reversed.find('.match-count').text()).toBe('已命中 2')
    expect(reversed.findAll('.node-list li.hit').map((li) => li.text())).toEqual([
      '香港专线 01',
      '香港专线 02',
    ])
  })

  it('多词 AND 单词命中不算：「香港」+「专线」不吃只含「香港」或只含「专线」的节点', async () => {
    const wrapper = mountPanel()
    await addWords(wrapper, ['美国', 'IEPL'])
    // 只「美国 IEPL 1.0」同时含两词；「新加坡 IEPL 1x0」含 IEPL 不含美国 → 不算
    expect(wrapper.find('.match-count').text()).toBe('已命中 1')
  })

  it('草稿未回车也参与 AND 收窄（防抖后即时预览）', async () => {
    const wrapper = mountPanel()
    await addWords(wrapper, ['专线'])
    expect(wrapper.find('.match-count').text()).toBe('已命中 3')
    await wrapper.find('input[aria-label="分组关键词"]').setValue('香港')
    expect(wrapper.find('.match-count').text()).toBe('已命中 3') // 防抖窗口内不变
    vi.advanceTimersByTime(250)
    await nextTick()
    expect(wrapper.find('.match-count').text()).toBe('已命中 2')
  })

  it('AND 零命中（词间无交集节点）：成组禁用 + 换词提示', async () => {
    const wrapper = mountPanel()
    await addWords(wrapper, ['香港'])
    await wrapper.find('input[aria-label="分组关键词"]').setValue('IEPL')
    vi.advanceTimersByTime(250)
    await nextTick()
    expect(wrapper.find('.match-count').text()).toBe('已命中 0')
    expect(wrapper.find('.group-confirm').attributes('disabled')).toBeDefined()
    expect(wrapper.find('.group-kw-hint').text()).toContain('没命中任何节点')
  })
})

describe('GroupPanel 确认成组与独立命名', () => {
  it('两步确认：成组进命名步（默认名=词拼接）→ 确认 emit 新组，回打词步清空', async () => {
    const wrapper = mountPanel()
    await enterNaming(wrapper, ['日本', '专线'])
    const nameInput = wrapper.find('input[aria-label="分组名称"]')
    expect(nameInput.exists()).toBe(true)
    expect((nameInput.element as HTMLInputElement).value).toBe('日本专线')
    await wrapper.find('.group-confirm').trigger('click')
    expect(wrapper.emitted('update:modelValue')).toEqual([
      [[{ name: '日本专线', keywords: ['日本', '专线'], urlTest: false }]],
    ])
    expect((wrapper.find('input[aria-label="分组关键词"]').element as HTMLInputElement).value).toBe('')
    expect(wrapper.findAll('.kw-chip')).toHaveLength(0)
  })

  it('命名可改：改「⭐️日本专线」确认 → emit 该组名', async () => {
    const wrapper = mountPanel()
    await enterNaming(wrapper, ['日本', '专线'])
    await wrapper.find('input[aria-label="分组名称"]').setValue('⭐️日本专线')
    await nextTick()
    await wrapper.find('.group-confirm').trigger('click')
    expect(wrapper.emitted('update:modelValue')).toEqual([
      [[{ name: '⭐️日本专线', keywords: ['日本', '专线'], urlTest: false }]],
    ])
  })

  it('命名回车等价确认', async () => {
    const wrapper = mountPanel()
    await enterNaming(wrapper, ['日本', '专线'])
    await wrapper.find('input[aria-label="分组名称"]').trigger('keydown.enter')
    expect(wrapper.emitted('update:modelValue')![0][0]).toEqual([
      { name: '日本专线', keywords: ['日本', '专线'], urlTest: false },
    ])
  })

  it('trim：草稿首尾空格成组时归一（词与默认名均为 trim 后值）', async () => {
    const wrapper = mountPanel()
    await wrapper.find('input[aria-label="分组关键词"]').setValue('  专线  ')
    vi.advanceTimersByTime(250)
    await nextTick()
    await wrapper.find('.group-confirm').trigger('click') // 草稿自动固化
    expect((wrapper.find('input[aria-label="分组名称"]').element as HTMLInputElement).value).toBe('专线')
    await wrapper.find('.group-confirm').trigger('click')
    expect(wrapper.emitted('update:modelValue')![0][0]).toEqual([
      { name: '专线', keywords: ['专线'], urlTest: false },
    ])
  })

  it.each([
    ['超 32 字', '名'.repeat(33)],
    ['含反引号', '名`'],
    ['含控制字符', '名\u0001'],
    ['纯空格', '   '],
  ])('命名校验拒绝 %s：确认禁用', async (_label, value) => {
    const wrapper = mountPanel()
    await enterNaming(wrapper, ['日本', '专线'])
    await wrapper.find('input[aria-label="分组名称"]').setValue(value)
    await nextTick()
    expect(wrapper.find('.group-confirm').attributes('disabled')).toBeDefined()
  })

  it('命名重名拒：默认名与已有组同名 → 确认禁用 + 提示；改名后可落组', async () => {
    const wrapper = mountPanel({
      modelValue: [{ name: '日本专线', keywords: ['其他'], urlTest: false }],
    })
    await enterNaming(wrapper, ['日本', '专线'])
    expect(wrapper.find('.group-confirm').attributes('disabled')).toBeDefined()
    expect(wrapper.find('.group-kw-hint').text()).toContain('同名分组只留一个')

    await wrapper.find('input[aria-label="分组名称"]').setValue('日本直连')
    await nextTick()
    expect(wrapper.find('.group-confirm').attributes('disabled')).toBeUndefined()
    await wrapper.find('.group-confirm').trigger('click')
    expect(wrapper.emitted('update:modelValue')![0][0]).toEqual([
      { name: '日本专线', keywords: ['其他'], urlTest: false },
      { name: '日本直连', keywords: ['日本', '专线'], urlTest: false },
    ])
  })

  it('取消命名：回打词步，已加词保留，不 emit', async () => {
    const wrapper = mountPanel()
    await enterNaming(wrapper, ['日本', '专线'])
    await wrapper.find('.group-cancel').trigger('click')
    await nextTick()
    expect(wrapper.find('input[aria-label="分组名称"]').exists()).toBe(false)
    expect(wrapper.find('input[aria-label="分组关键词"]').exists()).toBe(true)
    expect(wrapper.findAll('.kw-chip')).toHaveLength(2)
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
  })

  it('草稿未回车直接点成组：有效草稿自动固化为词', async () => {
    const wrapper = mountPanel()
    await wrapper.find('input[aria-label="分组关键词"]').setValue('专线')
    vi.advanceTimersByTime(250)
    await nextTick()
    await wrapper.find('.group-confirm').trigger('click')
    expect((wrapper.find('input[aria-label="分组名称"]').element as HTMLInputElement).value).toBe('专线')
    await wrapper.find('.group-confirm').trigger('click')
    expect(wrapper.emitted('update:modelValue')![0][0]).toEqual([
      { name: '专线', keywords: ['专线'], urlTest: false },
    ])
  })
})

describe('GroupPanel 组管理与上限', () => {
  const twoGroups: NodeGroup[] = [
    { name: '专线', keywords: ['专线'], urlTest: false },
    { name: 'IEPL', keywords: ['IEPL'], urlTest: true },
  ]

  it('组 chips 显示 name ×AND 计数（随 nodes 变化反映自动进组）', async () => {
    const wrapper = mountPanel({ modelValue: twoGroups })
    const chips = wrapper.findAll('.group-chip')
    expect(chips.map((c) => c.text())).toEqual(['专线 ×3', 'IEPL ×2'])

    await wrapper.setProps({ nodes: [...NODES, '德国专线 09'] })
    expect(wrapper.findAll('.group-chip')[0].text()).toBe('专线 ×4')
  })

  it('多词组 chips 计数 = AND 命中数', () => {
    const wrapper = mountPanel({
      modelValue: [{ name: '香港专线', keywords: ['香港', '专线'], urlTest: false }],
    })
    expect(wrapper.find('.group-chip').text()).toBe('香港专线 ×2')
  })

  it('「自动选最快」开关两态：点击 emit 翻转该组 urlTest，不动其他组', async () => {
    const wrapper = mountPanel({ modelValue: twoGroups })
    const switches = wrapper.findAll('.group-switch input[type="checkbox"]')
    expect((switches[0].element as HTMLInputElement).checked).toBe(false)
    expect((switches[1].element as HTMLInputElement).checked).toBe(true)

    await switches[0].setValue(true)
    expect(wrapper.emitted('update:modelValue')![0][0]).toEqual([
      { name: '专线', keywords: ['专线'], urlTest: true },
      { name: 'IEPL', keywords: ['IEPL'], urlTest: true },
    ])
  })

  it('删除组：emit 过滤掉该组', async () => {
    const wrapper = mountPanel({ modelValue: twoGroups })
    await wrapper.find('button[aria-label="删除分组 专线"]').trigger('click')
    expect(wrapper.emitted('update:modelValue')![0][0]).toEqual([twoGroups[1]])
  })

  it('≤10 组：满 10 组后「＋添加分组」禁用、成组禁用', async () => {
    const ten: NodeGroup[] = Array.from({ length: 10 }, (_, i) => ({
      name: `组${i}`,
      keywords: [`组${i}`],
      urlTest: false,
    }))
    const wrapper = mountPanel({ modelValue: ten })
    expect(wrapper.find('.add-group-btn').attributes('disabled')).toBeDefined()

    await wrapper.find('input[aria-label="分组关键词"]').setValue('专线')
    vi.advanceTimersByTime(250)
    await nextTick()
    expect(wrapper.find('.group-confirm').attributes('disabled')).toBeDefined()
  })
})

describe('GroupPanel 模板自带群组删减（v1.5.0 增补）', () => {
  const TEMPLATE: PresetGroup[] = [
    { name: '🚀 节点选择', locked: true },
    { name: '♻️ 自动选择', locked: false },
    { name: '🇰🇷 韩国节点', locked: false },
    { name: '💬 Ai平台', locked: false },
  ]
  const HITS: Record<string, number> = {
    '🚀 节点选择': 0, // 锁定组即便 0 命中也不标记
    '♻️ 自动选择': 3,
    '🇰🇷 韩国节点': 0,
    '💬 Ai平台': 0,
  }

  function prunePanel(props: Partial<PanelProps> = {}) {
    return mountPanel({ templateGroups: TEMPLATE, groupHits: HITS, ...props })
  }

  // jsdom 的属性选择器不认非 BMP 字符（emoji）：按类名 + 属性比对定位 ×
  function findX(wrapper: ReturnType<typeof mountPanel>, name: string) {
    return wrapper.findAll('.tpl-x').find((b) => b.attributes('aria-label') === `剔除组 ${name}`)!
  }

  it('上区按模板原序渲染：锁定组 🔒 无删除入口 + 规则入口提示，可删组带 ×', () => {
    const wrapper = prunePanel()
    const chips = wrapper.findAll('.tpl-chip')
    expect(chips.map((c) => c.find('.tpl-chip-name').text())).toEqual([
      '🚀 节点选择',
      '♻️ 自动选择',
      '🇰🇷 韩国节点',
      '💬 Ai平台',
    ])
    const locked = chips[0]
    expect(locked.find('.tpl-lock').text()).toContain('🔒')
    expect(locked.find('.tpl-lock').attributes('aria-label')).toBe('规则入口，必须保留')
    expect(locked.attributes('title')).toBe('规则入口，必须保留')
    expect(locked.find('.tpl-x').exists()).toBe(false)
    for (const chip of chips.slice(1)) {
      expect(chip.find('.tpl-x').exists()).toBe(true)
    }
  })

  it('0 节点标记：仅可删且命中数为 0 的组标黄；锁定组与有命中的组不标', () => {
    const wrapper = prunePanel()
    const chips = wrapper.findAll('.tpl-chip')
    expect(chips[0].find('.tpl-zero').exists()).toBe(false) // locked（即便 hits 0）
    expect(chips[1].find('.tpl-zero').exists()).toBe(false) // 有命中
    expect(chips[2].find('.tpl-zero').text()).toBe('(0 节点)')
    expect(chips[3].find('.tpl-zero').text()).toBe('(0 节点)')
  })

  it('groupHits 缺席（旧 sidecar / 未 probe）：不标 0 节点', () => {
    const wrapper = mountPanel({ templateGroups: TEMPLATE })
    expect(wrapper.findAll('.tpl-zero')).toHaveLength(0)
  })

  it('点 ×：emit update:excluded 追加该组名', async () => {
    const wrapper = prunePanel()
    await findX(wrapper, '💬 Ai平台').trigger('click')
    expect(wrapper.emitted('update:excluded')).toEqual([[['💬 Ai平台']]])
  })

  it('已剔除区按模板原序渲染灰 chips；点 ＋ 加回（emit 去掉该名）', async () => {
    const wrapper = prunePanel({ excluded: ['💬 Ai平台', '♻️ 自动选择'] })
    expect(wrapper.findAll('.tpl-chip .tpl-chip-name').map((n) => n.text())).toEqual([
      '🚀 节点选择',
      '🇰🇷 韩国节点',
    ])
    const excluded = wrapper.findAll('.excluded-chip')
    expect(excluded.map((c) => c.text())).toEqual(['＋ ♻️ 自动选择', '＋ 💬 Ai平台'])
    await excluded[0].trigger('click')
    expect(wrapper.emitted('update:excluded')).toEqual([[['💬 Ai平台']]])
  })

  it('模板中已不存在的剔除名不渲染（状态里仍保留）', () => {
    const wrapper = prunePanel({ excluded: ['不存在的组'] })
    expect(wrapper.find('.excluded-zone').exists()).toBe(false)
    expect(wrapper.findAll('.tpl-chip')).toHaveLength(TEMPLATE.length)
  })

  it('一键剔除空群组：probe 未完成时禁用', () => {
    for (const probeState of ['idle', 'loading', 'failed'] as const) {
      const wrapper = prunePanel({ probeState })
      expect(wrapper.find('.prune-empty').attributes('disabled')).toBeDefined()
    }
    expect(prunePanel().find('.prune-empty').attributes('disabled')).toBeUndefined()
  })

  it('一键剔除空群组：没有空群组时禁用', () => {
    const wrapper = prunePanel({ groupHits: { '🇰🇷 韩国节点': 2, '💬 Ai平台': 1 } })
    expect(wrapper.find('.prune-empty').attributes('disabled')).toBeDefined()
  })

  it('一键剔除：静默移入全部空群组 + 轻提示，2.5s 自动消失', async () => {
    const wrapper = prunePanel()
    await wrapper.find('.prune-empty').trigger('click')
    expect(wrapper.emitted('update:excluded')).toEqual([[['🇰🇷 韩国节点', '💬 Ai平台']]])
    expect(wrapper.find('.prune-toast').text()).toBe('已剔除 2 个空群组（随时点下方标签加回）')

    vi.advanceTimersByTime(2500)
    await nextTick()
    expect(wrapper.find('.prune-toast').exists()).toBe(false)
  })

  it('一键剔除与现有 excluded 合并（不重复加入）', async () => {
    const wrapper = prunePanel({ excluded: ['💬 Ai平台'] })
    await wrapper.find('.prune-empty').trigger('click')
    expect(wrapper.emitted('update:excluded')).toEqual([[['💬 Ai平台', '🇰🇷 韩国节点']]])
  })

  it('提示定时器在组件卸载时清理（卸载后推进时间不报错且无残留定时器）', async () => {
    const wrapper = prunePanel()
    await wrapper.find('.prune-empty').trigger('click')
    wrapper.unmount()
    vi.advanceTimersByTime(2500)
    expect(vi.getTimerCount()).toBe(0)
  })

  it('恢复默认：excluded 空时禁用；非空时点击 emit 空数组', async () => {
    expect(prunePanel().find('.restore-default').attributes('disabled')).toBeDefined()
    const wrapper = prunePanel({ excluded: ['💬 Ai平台'] })
    const btn = wrapper.find('.restore-default')
    expect(btn.attributes('disabled')).toBeUndefined()
    await btn.trigger('click')
    expect(wrapper.emitted('update:excluded')).toEqual([[[]]])
  })

  it('模板组清单为空（字段缺席/豁免档降级）：上下两区都不渲染，打词区照常', () => {
    const wrapper = mountPanel()
    expect(wrapper.find('.tpl-zone').exists()).toBe(false)
    expect(wrapper.find('.excluded-zone').exists()).toBe(false)
    expect(wrapper.find('input[aria-label="分组关键词"]').exists()).toBe(true)
  })
})
