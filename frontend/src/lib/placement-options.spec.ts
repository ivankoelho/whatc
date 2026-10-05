import { describe, expect, it } from 'vitest'
import { selectableOptions } from './placement-options'

const items = [
  { id: 'a', name: 'Ativa', active: true },
  { id: 'b', name: 'Inativa vinculada', active: false },
  { id: 'c', name: 'Inativa', active: false },
]

describe('selectableOptions', () => {
  it('offers only active items for a new link', () => {
    expect(selectableOptions(items).map(i => i.id)).toEqual(['a'])
    expect(selectableOptions(items, '').map(i => i.id)).toEqual(['a'])
    expect(selectableOptions(items, null).map(i => i.id)).toEqual(['a'])
  })

  it('keeps the inactive item that is currently linked, and only that one', () => {
    expect(selectableOptions(items, 'b').map(i => i.id)).toEqual(['a', 'b'])
  })
})
