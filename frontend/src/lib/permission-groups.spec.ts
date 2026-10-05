import { describe, expect, it } from 'vitest'
import type { Permission } from '@/services/api'
import { activeCount, filterGroups, groupPermissions, i18nKey, setKeys } from './permission-groups'

const p = (resource: string, action: string, group: string, group_order: number, sort_order: number) =>
  ({ id: resource + action, resource, action, key: `${resource}:${action}`, description: '', group, group_order, sort_order }) as Permission

const perms = [
  p('units', 'write', 'crm', 30, 30101),
  p('users', 'write', 'admin', 10, 10001),
  p('users', 'read', 'admin', 10, 10000),
  p('units', 'read', 'crm', 30, 30100),
]

describe('groupPermissions', () => {
  it('orders groups, resources and actions as the backend says and loses no key', () => {
    const g = groupPermissions(perms)
    expect(g.map(x => x.group)).toEqual(['admin', 'crm'])
    expect(g[0].resources[0].permissions.map(x => x.key)).toEqual(['users:read', 'users:write'])
    expect(g.flatMap(x => x.permissions).length).toBe(perms.length)
  })

  it('puts a permission without group metadata in "other"', () => {
    const g = groupPermissions([{ id: 'x', resource: 'x', action: 'read', key: 'x:read', description: '' } as Permission])
    expect(g[0].group).toBe('other')
  })
})

describe('filterGroups', () => {
  const groups = groupPermissions(perms)
  const text = (x: Permission) => x.key
  const res = (r: string) => r

  it('keeps the group structure and drops only empty ones', () => {
    const f = filterGroups(groups, 'units:write', text, res)
    expect(f.map(x => x.group)).toEqual(['crm'])
    expect(f[0].permissions.map(x => x.key)).toEqual(['units:write'])
  })

  it('a matching resource name keeps all its permissions', () => {
    expect(filterGroups(groups, 'users', text, res)[0].permissions.length).toBe(2)
  })

  it('an empty query returns everything', () => {
    expect(filterGroups(groups, '  ', text, res)).toBe(groups)
  })
})

describe('selection helpers', () => {
  const g = groupPermissions(perms)[0]
  it('counts active permissions of a group', () => {
    expect(activeCount(g, ['users:read', 'units:read'])).toBe(1)
  })
  it('turns keys on/off without touching other keys', () => {
    expect(setKeys(['a:read'], ['users:read'], true).sort()).toEqual(['a:read', 'users:read'])
    expect(setKeys(['a:read', 'users:read'], ['users:read'], false)).toEqual(['a:read'])
  })
  it('builds i18n-safe keys', () => {
    expect(i18nKey('settings.general:read')).toBe('settings_general_read')
  })
})
