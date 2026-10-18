// Kanban columns are always ordered by opened_at, newest first (Protocolos
// and CRM alike). While a card is dragged, its placeholder must show where it
// will actually land — not wherever the pointer happens to be — or it jumps
// on drop. The column is a flex column, so the placeholder is positioned with
// CSS `order`, independently of where SortableJS put it in the DOM: cards get
// even orders (index * 2), the placeholder the odd one just before its slot.

export interface Opened {
  id: string
  opened_at: string
}

export function byOpenedAtDesc(a: Opened, b: Opened): number {
  return Date.parse(b.opened_at) - Date.parse(a.opened_at)
}

/** Where `item` lands in `list` (ordered newest first), ignoring itself. */
export function landingIndex(list: Opened[], item: Opened): number {
  const t = Date.parse(item.opened_at)
  return list.filter(o => o.id !== item.id && Date.parse(o.opened_at) > t).length
}

/** CSS order for the card rendered at `index`. */
export function cardOrder(index: number): number {
  return index * 2
}

/** Puts the drag placeholder visually at the card's landing slot in `list`. */
export function placePreview(el: HTMLElement | undefined, list: Opened[], item: Opened): void {
  if (el) el.style.order = String(cardOrder(landingIndex(list, item)) - 1)
}
