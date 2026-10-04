import type { Permission } from '@/services/api'

// Functional grouping of the permission catalog. The backend decides the group
// and the order (GET /api/permissions); this only shapes them for the screen and
// filters them. Permission keys are never created or altered here.

export interface PermissionResourceBlock {
  resource: string
  permissions: Permission[]
}

export interface FunctionalGroup {
  group: string
  order: number
  resources: PermissionResourceBlock[]
  permissions: Permission[]
}

// "settings.general:read" -> "settings_general_read": i18n paths split on dots.
export const i18nKey = (s: string) => s.replace(/[.:]/g, '_')

export function groupPermissions(perms: Permission[]): FunctionalGroup[] {
  const groups = new Map<string, FunctionalGroup>()
  const sorted = [...perms].sort((a, b) => (a.sort_order ?? 0) - (b.sort_order ?? 0) || a.key.localeCompare(b.key))
  for (const p of sorted) {
    const name = p.group || 'other'
    let g = groups.get(name)
    if (!g) {
      g = { group: name, order: p.group_order ?? 999, resources: [], permissions: [] }
      groups.set(name, g)
    }
    g.permissions.push(p)
    let block = g.resources.find(r => r.resource === p.resource)
    if (!block) {
      block = { resource: p.resource, permissions: [] }
      g.resources.push(block)
    }
    block.permissions.push(p)
  }
  return [...groups.values()].sort((a, b) => a.order - b.order)
}

export const activeCount = (g: FunctionalGroup, selected: string[]) =>
  g.permissions.filter(p => selected.includes(p.key)).length

// Keeps the group structure; drops only resources/groups left empty. A resource
// that matches as a whole (its label/name) keeps all of its permissions.
export function filterGroups(
  groups: FunctionalGroup[],
  query: string,
  textOf: (p: Permission) => string,
  resourceTextOf: (resource: string) => string,
): FunctionalGroup[] {
  const q = query.trim().toLowerCase()
  if (!q) return groups
  const out: FunctionalGroup[] = []
  for (const g of groups) {
    const resources: PermissionResourceBlock[] = []
    for (const r of g.resources) {
      const whole = resourceTextOf(r.resource).toLowerCase().includes(q)
      const perms = whole ? r.permissions : r.permissions.filter(p => textOf(p).toLowerCase().includes(q))
      if (perms.length) resources.push({ resource: r.resource, permissions: perms })
    }
    if (resources.length) out.push({ ...g, resources, permissions: resources.flatMap(r => r.permissions) })
  }
  return out
}

// Turn a set of keys on/off on top of the current selection, keeping every other key.
export function setKeys(selected: string[], keys: string[], on: boolean): string[] {
  const set = new Set(selected)
  keys.forEach(k => (on ? set.add(k) : set.delete(k)))
  return [...set]
}
