import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { nextTick } from 'vue'
import GroupPanel from './GroupPanel.vue'
import type { NodeGroup } from '../types'

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
      url: 'https://example.com/sub',
      open: true,
      modelValue: [],
      probeState: 'ready',
      nodes: NODES,
      ...props,
    } as PanelProps,
  })
}

describe('GroupPanel 入口与 probe 态', () => {
  it('能力位 false：整块不渲染（部署或所选档不支持，入口不出现）', () => {
    const wrapper = mountPanel({ available: false })
    expect(wrapper.find('.groups').exists()).toBe(false)
  })

  it('入口矩阵：url 空 → 渲染但置灰；url 非空 → 可点并 emit update:open true', async () => {
    const empty = mountPanel({ open: false, url: '' })
    const entry = empty.find('.groups-entry')
    expect(entry.exists()).toBe(true)
    expect(entry.attributes('disabled')).toBeDefined()

    const filled = mountPanel({ open: false })
    const btn = filled.find('.groups-entry')
    expect(btn.attributes('disabled')).toBeUndefined()
    await btn.trigger('click')
    expect(filled.emitted('update:open')).toEqual([[true]])
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

describe('GroupPanel 打词匹配与成组', () => {
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
  ])('校验拒绝 %s：成组禁用', async (_label, value) => {
    const wrapper = mountPanel()
    await wrapper.find('input[aria-label="分组关键词"]').setValue(value)
    vi.advanceTimersByTime(250)
    await nextTick()
    expect(wrapper.find('.group-confirm').attributes('disabled')).toBeDefined()
  })

  it('同名组拒绝：已有「专线」再打同词，成组禁用并提示', async () => {
    const wrapper = mountPanel({
      modelValue: [{ name: '专线', keyword: '专线', urlTest: false }],
    })
    await wrapper.find('input[aria-label="分组关键词"]').setValue('专线')
    vi.advanceTimersByTime(250)
    await nextTick()
    expect(wrapper.find('.group-confirm').attributes('disabled')).toBeDefined()
    expect(wrapper.find('.group-kw-hint').text()).toContain('同名分组只留一个')
  })

  it('确认成组：emit 新组 {name=keyword, urlTest:false}，输入清空', async () => {
    const wrapper = mountPanel()
    await wrapper.find('input[aria-label="分组关键词"]').setValue('专线')
    vi.advanceTimersByTime(250)
    await nextTick()
    await wrapper.find('.group-confirm').trigger('click')
    expect(wrapper.emitted('update:modelValue')).toEqual([
      [[{ name: '专线', keyword: '专线', urlTest: false }]],
    ])
    expect((wrapper.find('input[aria-label="分组关键词"]').element as HTMLInputElement).value).toBe('')
  })

  it('trim：首尾空格输入成组时归一（name/keyword 为 trim 后值）', async () => {
    const wrapper = mountPanel()
    await wrapper.find('input[aria-label="分组关键词"]').setValue('  专线  ')
    vi.advanceTimersByTime(250)
    await nextTick()
    await wrapper.find('.group-confirm').trigger('click')
    expect(wrapper.emitted('update:modelValue')![0][0]).toEqual([
      { name: '专线', keyword: '专线', urlTest: false },
    ])
  })

  it('probeState 非 ready 时成组禁用（无名单无从判定命中）', () => {
    const loading = mountPanel({ probeState: 'loading' })
    expect(loading.find('.group-confirm').exists()).toBe(false)
  })
})

describe('GroupPanel 组管理与上限', () => {
  const twoGroups: NodeGroup[] = [
    { name: '专线', keyword: '专线', urlTest: false },
    { name: 'IEPL', keyword: 'IEPL', urlTest: true },
  ]

  it('组 chips 显示 name ×实时计数（随 nodes 变化反映自动进组）', async () => {
    const wrapper = mountPanel({ modelValue: twoGroups })
    const chips = wrapper.findAll('.group-chip')
    expect(chips.map((c) => c.text())).toEqual(['专线 ×3', 'IEPL ×2'])

    await wrapper.setProps({ nodes: [...NODES, '德国专线 09'] })
    expect(wrapper.findAll('.group-chip')[0].text()).toBe('专线 ×4')
  })

  it('「自动选最快」开关两态：点击 emit 翻转该组 urlTest，不动其他组', async () => {
    const wrapper = mountPanel({ modelValue: twoGroups })
    const switches = wrapper.findAll('.group-switch input[type="checkbox"]')
    expect((switches[0].element as HTMLInputElement).checked).toBe(false)
    expect((switches[1].element as HTMLInputElement).checked).toBe(true)

    await switches[0].setValue(true)
    expect(wrapper.emitted('update:modelValue')![0][0]).toEqual([
      { name: '专线', keyword: '专线', urlTest: true },
      { name: 'IEPL', keyword: 'IEPL', urlTest: true },
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
      keyword: `组${i}`,
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
