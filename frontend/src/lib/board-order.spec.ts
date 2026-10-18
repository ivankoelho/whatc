// @vitest-environment happy-dom
import { describe, it, expect } from 'vitest'
import { byOpenedAtDesc, landingIndex, cardOrder, placePreview } from './board-order'

const card = (id: string, day: number) => ({ id, opened_at: `2026-10-${String(day).padStart(2, '0')}T10:00:00Z` })

describe('board order', () => {
  const column = [card('a', 20), card('b', 15), card('c', 10)] // newest first

  it('the landing slot matches where the sort puts the card after the drop', () => {
    const dragged = card('x', 12)
    const idx = landingIndex(column, dragged)
    expect(idx).toBe(2)
    expect([...column, dragged].sort(byOpenedAtDesc).findIndex(o => o.id === 'x')).toBe(idx)
  })

  it('a newest card lands on top, an oldest at the bottom; the card itself is ignored', () => {
    expect(landingIndex(column, card('x', 25))).toBe(0)
    expect(landingIndex(column, card('x', 1))).toBe(3)
    expect(landingIndex(column, column[1])).toBe(1) // dragging within its own column
  })

  it('the placeholder sits right before the card that will follow it', () => {
    const el = document.createElement('div')
    placePreview(el, column, card('x', 12))
    expect(Number(el.style.order)).toBe(cardOrder(2) - 1)
    expect(Number(el.style.order)).toBeGreaterThan(cardOrder(1))
    expect(Number(el.style.order)).toBeLessThan(cardOrder(2))
  })
})
