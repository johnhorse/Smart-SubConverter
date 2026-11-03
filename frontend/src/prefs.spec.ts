import { beforeEach, describe, expect, it } from 'vitest'
import {
  DRAFT_KEY,
  DRAFT_MAX_SUBS,
  type DraftSnapshot,
  getSnapshot,
  removeSnapshot,
  saveSnapshot,
} from './prefs'

function snap(overrides: Partial<DraftSnapshot> = {}): DraftSnapshot {
  return {
    target: 'auto',
    preset: 'default',
    groups: [],
    excluded: [],
    draftKeywords: [],
    updatedAt: '2026-11-03T00:00:00.000Z',
    ...overrides,
  }
}

describe('prefs 本地草稿层（v1.6.0）', () => {
  beforeEach(() => {
    localStorage.clear()
  })

  it('saveSnapshot + getSnapshot 往返：key 按 trim 后 URL 归一（查询带空白也命中）', () => {
    saveSnapshot('https://example.com/sub', snap({ draftKeywords: ['专线'] }))
    expect(getSnapshot('https://example.com/sub')).toEqual(snap({ draftKeywords: ['专线'] }))
    expect(getSnapshot('  https://example.com/sub  ')).toEqual(snap({ draftKeywords: ['专线'] }))
  })

  it('无存档 / 空 URL → null；坏 JSON 按无草稿降级', () => {
    expect(getSnapshot('https://example.com/sub')).toBeNull()
    expect(getSnapshot('   ')).toBeNull()
    localStorage.setItem(DRAFT_KEY, '{not json')
    expect(getSnapshot('https://example.com/sub')).toBeNull()
    // 坏数据不阻塞后续写入
    saveSnapshot('https://example.com/sub', snap())
    expect(getSnapshot('https://example.com/sub')).toEqual(snap())
  })

  it('空 URL 不落盘', () => {
    saveSnapshot('   ', snap())
    expect(localStorage.getItem(DRAFT_KEY)).toBeNull()
  })

  it('容量淘汰：最多保留最近 5 个订阅，超出按 updatedAt 淘汰最旧', () => {
    for (let i = 0; i < DRAFT_MAX_SUBS + 2; i++) {
      saveSnapshot(
        `https://example.com/sub${i}`,
        snap({ updatedAt: `2026-11-03T00:0${i}:00.000Z` }),
      )
    }
    const store = JSON.parse(localStorage.getItem(DRAFT_KEY)!) as {
      subs: Record<string, unknown>
    }
    expect(Object.keys(store.subs)).toHaveLength(DRAFT_MAX_SUBS)
    // 最旧的 sub0 / sub1 被淘汰，保留 sub2..sub6
    expect(getSnapshot('https://example.com/sub0')).toBeNull()
    expect(getSnapshot('https://example.com/sub1')).toBeNull()
    expect(getSnapshot('https://example.com/sub6')).not.toBeNull()
  })

  it('removeSnapshot 不存在的 key 无副作用', () => {
    saveSnapshot('https://a.com/sub', snap())
    removeSnapshot('https://b.com/sub')
    expect(getSnapshot('https://a.com/sub')).toEqual(snap())
  })

  // ---- 自愈（spec 修订 6）：坏形态不得永久卡死恢复，也不得 TypeError ----

  it('subs 为数组（历史脏数据）：当无草稿处理并回写空 store', () => {
    localStorage.setItem(DRAFT_KEY, JSON.stringify({ subs: [] }))
    expect(getSnapshot('https://example.com/sub')).toBeNull()
    expect(JSON.parse(localStorage.getItem(DRAFT_KEY)!)).toEqual({ subs: {} })
  })

  it('store 本身为数组：当无草稿处理并回写空 store', () => {
    localStorage.setItem(DRAFT_KEY, JSON.stringify([]))
    expect(getSnapshot('https://example.com/sub')).toBeNull()
    expect(JSON.parse(localStorage.getItem(DRAFT_KEY)!)).toEqual({ subs: {} })
  })

  it('坏快照（字段类型不对 / 缺字段）→ 该条返回 null 并剔除回写，不产生 TypeError', () => {
    const good = snap()
    localStorage.setItem(
      DRAFT_KEY,
      JSON.stringify({
        subs: {
          bad1: { target: 1, preset: 'default', groups: [], excluded: [], draftKeywords: [], updatedAt: '' },
          bad2: { target: 'auto', preset: 'default', groups: [{ name: 'x', keywords: 'oops', urlTest: false }], excluded: [], draftKeywords: [], updatedAt: '' },
          bad3: { target: 'auto', preset: 'default', groups: [], excluded: ['x'], updatedAt: '' }, // 缺 draftKeywords
          'https://example.com/sub': good,
        },
      }),
    )
    // 坏条目：返回 null（不炸），且只剔除坏的、保留好的
    expect(getSnapshot('bad1')).toBeNull()
    expect(getSnapshot('bad2')).toBeNull()
    expect(getSnapshot('bad3')).toBeNull()
    expect(getSnapshot('https://example.com/sub')).toEqual(good)
    expect(JSON.parse(localStorage.getItem(DRAFT_KEY)!).subs).toEqual({
      'https://example.com/sub': good,
    })
    // 剔除后同 key 可正常重新写入
    saveSnapshot('bad1', snap())
    expect(getSnapshot('bad1')).toEqual(snap())
  })

  it('坏条目剔除不误伤其他订阅的快照（A 坏 B 好）', () => {
    const good = snap({ draftKeywords: ['家宽'] })
    localStorage.setItem(
      DRAFT_KEY,
      JSON.stringify({ subs: { 'https://a.com/sub': { target: null }, 'https://b.com/sub': good } }),
    )
    expect(getSnapshot('https://a.com/sub')).toBeNull()
    expect(getSnapshot('https://b.com/sub')).toEqual(good)
  })
})
