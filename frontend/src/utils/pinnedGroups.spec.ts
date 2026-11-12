import { describe, it, expect } from 'vitest'
import { splitPinned, pinnedCounterVisible } from './pinnedGroups'

const c = (id: string, pinned = false) => ({ id, is_pinned: pinned }) as any
const list = [c('p1', true), c('p2', true), c('a'), c('b')]

describe('splitPinned', () => {
  it('groups pinned first and keeps the rest in order', () => {
    const r = splitPinned(list, false)
    expect(r.pinned.map(x => x.id)).toEqual(['p1', 'p2'])
    expect(r.rest.map(x => x.id)).toEqual(['a', 'b'])
  })
  it('has no pinned group while searching, and keeps every row', () => {
    const r = splitPinned(list, true)
    expect(r.pinned).toEqual([])
    expect(r.rest).toHaveLength(4)
  })
  it('zero pinned means no group', () => {
    expect(splitPinned([c('a'), c('b')], false).pinned).toEqual([])
  })
  it('works for one and three pinned', () => {
    expect(splitPinned([c('p', true), c('a')], false).pinned).toHaveLength(1)
    expect(splitPinned([c('1', true), c('2', true), c('3', true)], false).rest).toEqual([])
  })
})

describe('pinnedCounterVisible', () => {
  it('shows only on the unfiltered list', () => {
    expect(pinnedCounterVisible('all', 0)).toBe(true)
    expect(pinnedCounterVisible('new', 0)).toBe(false)
    expect(pinnedCounterVisible('all', 2)).toBe(false)
  })
})
