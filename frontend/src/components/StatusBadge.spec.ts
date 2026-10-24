import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import StatusBadge from './StatusBadge.vue'
import { badgeInfo, relativeDuration } from '../constants'
import type { HealthPreset, HealthResponse } from '../types'

const NOW = new Date('2026-10-23T16:00:00Z').getTime()

function health(overrides: Partial<HealthResponse> = {}): HealthResponse {
  return {
    backendVersion: 'v1.10.5',
    presets: [
      {
        name: 'ACL4SSR 默认',
        url: 'https://example.com/ACL4SSR_Online.ini',
        source: 'snapshot',
        lastModifiedAt: null,
        lastRefreshedAt: '2026-10-23T15:00:00Z',
        lastSuccessAt: '2026-10-23T15:00:00Z',
        status: 'fresh',
      },
    ],
    ...overrides,
  }
}

// 豁免档（source: remote）数据形态：远端 HEAD 语义，无 lastRefreshedAt
function remotePreset(overrides: Partial<HealthPreset> = {}): HealthPreset {
  return {
    name: 'ACL4SSR 默认',
    url: 'https://example.com/ACL4SSR_Online.ini',
    source: 'remote',
    lastModifiedAt: '2026-10-23T15:00:00Z', // 1 小时前
    lastSuccessAt: '2026-10-23T15:50:00Z',
    status: 'fresh',
    ...overrides,
  }
}

describe('badgeInfo（快照档 snapshot 文案）', () => {
  it('fresh → 「规则快照 · X 前更新」+ 引擎版本', () => {
    const info = badgeInfo(health(), NOW)
    expect(info.tone).toBe('fresh')
    expect(info.text).toContain('规则快照 · 1 小时前更新')
    expect(info.text).toContain('引擎 v1.10.5')
  })

  it('fresh 且无 lastRefreshedAt（sidecar 未实装过渡期）→ 回退 lastSuccessAt', () => {
    const h = health()
    delete h.presets[0].lastRefreshedAt
    h.presets[0].lastSuccessAt = '2026-10-23T15:50:00Z' // 10 分钟前
    const info = badgeInfo(h, NOW)
    expect(info.tone).toBe('fresh')
    expect(info.text).toContain('规则快照 · 10 分钟前更新')
  })

  it('stale → 「规则快照已 X 未刷新」', () => {
    const h = health()
    h.presets[0].status = 'stale'
    h.presets[0].lastRefreshedAt = '2026-10-20T10:00:00Z' // 3 天多前
    h.presets[0].lastSuccessAt = '2026-10-20T10:00:00Z'
    const info = badgeInfo(h, NOW)
    expect(info.tone).toBe('stale')
    expect(info.text).toContain('规则快照已 3 天未刷新')
  })

  it('backendVersion 为 unknown 时版本部分显示 unknown', () => {
    const info = badgeInfo(health({ backendVersion: 'unknown' }), NOW)
    expect(info.text).toBe('规则快照 · 1 小时前更新 · 引擎 unknown')
  })
})

describe('badgeInfo（豁免档 remote 文案，沿用 v1.0）', () => {
  it('fresh → 「规则内容 X 前更新」（X 取 lastModifiedAt）', () => {
    const info = badgeInfo({ backendVersion: 'v1.10.5', presets: [remotePreset()] }, NOW)
    expect(info.tone).toBe('fresh')
    expect(info.text).toContain('规则内容 1 小时前更新')
    expect(info.text).toContain('引擎 v1.10.5')
  })

  it('stale 且有 lastModifiedAt → 「规则库可能过期（X 未更新）」', () => {
    const h = { backendVersion: 'v1.10.5', presets: [remotePreset({ status: 'stale', lastModifiedAt: '2026-10-20T10:00:00Z' })] }
    const info = badgeInfo(h, NOW)
    expect(info.tone).toBe('stale')
    expect(info.text).toContain('规则库可能过期（3 天未更新）')
  })

  it('stale 且 lastModifiedAt 为 null（最近抓取失败）→ 「规则源暂时够不着」', () => {
    const h = { backendVersion: 'v1.10.5', presets: [remotePreset({ status: 'stale', lastModifiedAt: null })] }
    const info = badgeInfo(h, NOW)
    expect(info.tone).toBe('stale')
    expect(info.text).toContain('规则源暂时够不着')
  })
})

describe('badgeInfo（source 字段缺席 = 旧 sidecar，按 remote 回退）', () => {
  it('fresh → 旧文案「规则内容 X 前更新」', () => {
    const p = remotePreset()
    delete p.source
    const info = badgeInfo({ backendVersion: 'v1.10.5', presets: [p] }, NOW)
    expect(info.text).toContain('规则内容 1 小时前更新')
  })
})

describe('badgeInfo（unknown 态与档位无关）', () => {
  it('unknown → 「规则库状态未知」', () => {
    const h = health()
    h.presets[0].status = 'unknown'
    const info = badgeInfo(h, NOW)
    expect(info.tone).toBe('unknown')
    expect(info.text).toContain('规则库状态未知')
  })
})

describe('relativeDuration', () => {
  it('分钟/小时/天分档', () => {
    expect(relativeDuration(new Date(NOW - 5 * 60_000).toISOString(), NOW)).toBe('5 分钟')
    expect(relativeDuration(new Date(NOW - 3 * 3_600_000).toISOString(), NOW)).toBe('3 小时')
    expect(relativeDuration(new Date(NOW - 2 * 86_400_000).toISOString(), NOW)).toBe('2 天')
    expect(relativeDuration(new Date(NOW - 30_000).toISOString(), NOW)).toBe('刚刚')
  })
})

describe('StatusBadge 组件', () => {
  it('三态 tone class 与文案', () => {
    const fresh = mount(StatusBadge, { props: { badge: { tone: 'fresh', text: '规则快照 · 1 小时前更新 · 引擎 v1.10.5' } } })
    expect(fresh.find('.badge.fresh').exists()).toBe(true)
    expect(fresh.text()).toContain('规则快照 · 1 小时前更新')

    const stale = mount(StatusBadge, { props: { badge: { tone: 'stale', text: '规则源暂时够不着' } } })
    expect(stale.find('.badge.stale').exists()).toBe(true)

    const unknown = mount(StatusBadge, { props: { badge: null } })
    expect(unknown.find('.badge.unknown').exists()).toBe(true)
  })
})
