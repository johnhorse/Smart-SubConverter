import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import PresetChips from './PresetChips.vue'
import { FALLBACK_PRESETS, PRESET_DESCRIPTIONS } from '../constants'

describe('PresetChips', () => {
  it('渲染三档 chips（契约顺序，default 第一），default 选中带 ✓（radiogroup 单选语义）', () => {
    const wrapper = mount(PresetChips, {
      props: { presets: FALLBACK_PRESETS, modelValue: 'default' },
    })
    const chips = wrapper.findAll('.chip')
    expect(chips).toHaveLength(FALLBACK_PRESETS.length)
    expect(chips.map((c) => c.text())).toEqual([
      'ACL4SSR 默认 ✓',
      '地区分组 + AI 分流',
      '全量 + 去广告',
    ])
    const active = chips.filter((c) => c.classes('active'))
    expect(active).toHaveLength(1)
    expect(active[0].attributes('aria-checked')).toBe('true')
    expect(wrapper.find('.chips').attributes('role')).toBe('radiogroup')
  })

  it('点击其他 chip emit update:modelValue（单选切换，无「未选」态）', async () => {
    const wrapper = mount(PresetChips, {
      props: { presets: FALLBACK_PRESETS, modelValue: 'default' },
    })
    const second = wrapper.findAll('.chip')[1]
    await second.trigger('click')
    expect(wrapper.emitted('update:modelValue')).toEqual([['full_multimode']])
  })

  it('选中档下方显示对应描述文案，随选中切换', async () => {
    const wrapper = mount(PresetChips, {
      props: { presets: FALLBACK_PRESETS, modelValue: 'default' },
    })
    expect(wrapper.find('.preset-desc').text()).toBe(PRESET_DESCRIPTIONS.default)

    await wrapper.setProps({ modelValue: 'full_multimode' })
    expect(wrapper.find('.preset-desc').text()).toBe(PRESET_DESCRIPTIONS.full_multimode)

    await wrapper.setProps({ modelValue: 'full_adblockplus' })
    expect(wrapper.find('.preset-desc').text()).toBe(PRESET_DESCRIPTIONS.full_adblockplus)
  })
})
