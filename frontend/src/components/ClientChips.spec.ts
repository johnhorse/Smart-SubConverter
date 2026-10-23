import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import ClientChips from './ClientChips.vue'
import { FALLBACK_CLIENTS } from '../constants'

describe('ClientChips', () => {
  it('渲染注册表 chips，auto 默认选中带 ✓（radiogroup 单选语义）', () => {
    const wrapper = mount(ClientChips, {
      props: { clients: FALLBACK_CLIENTS, modelValue: 'auto' },
    })
    const chips = wrapper.findAll('.chip')
    expect(chips).toHaveLength(FALLBACK_CLIENTS.length)
    const active = chips.filter((c) => c.classes('active'))
    expect(active).toHaveLength(1)
    expect(active[0].text()).toBe('自动识别 ✓')
    expect(active[0].attributes('aria-checked')).toBe('true')
    expect(wrapper.attributes('role')).toBe('radiogroup')
  })

  it('点击其他 chip emit update:modelValue（单选切换）', async () => {
    const wrapper = mount(ClientChips, {
      props: { clients: FALLBACK_CLIENTS, modelValue: 'auto' },
    })
    const surge = wrapper.findAll('.chip').find((c) => c.text().includes('Surge'))!
    await surge.trigger('click')
    expect(wrapper.emitted('update:modelValue')).toEqual([['surge']])
  })
})
