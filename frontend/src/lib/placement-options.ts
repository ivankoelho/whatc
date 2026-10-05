// Options of a unit/department picker: an inactive one is not offered for a NEW choice, but the
// one already linked stays in the list so an existing value is preserved (never silently
// dropped on save). Same rule the Knowledge pickers already follow (see lib/knowledge.ts).
export function selectableOptions<T extends { id: string; active: boolean }>(
  items: T[],
  currentId?: string | null,
): T[] {
  return items.filter(i => i.active || i.id === currentId)
}
