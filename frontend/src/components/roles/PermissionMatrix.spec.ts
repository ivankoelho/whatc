// @vitest-environment happy-dom
import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import ptBR from '@/i18n/locales/pt-BR.json'
import en from '@/i18n/locales/en.json'
import type { Permission } from '@/services/api'
import { groupPermissions } from '@/lib/permission-groups'
import PermissionMatrix from './PermissionMatrix.vue'

const p = (resource: string, action: string, group: string, order: number, sort: number) =>
  ({ id: resource + action, resource, action, key: `${resource}:${action}`, description: 'fallback', group, group_order: order, sort_order: sort }) as Permission

const perms = [
  p('users', 'read', 'admin', 10, 10000),
  p('users', 'write', 'admin', 10, 10001),
  p('units', 'read', 'crm', 30, 30100),
  p('units', 'write', 'crm', 30, 30101),
]

function build(selected: string[], disabled = false, locale: 'pt-BR' | 'en' = 'pt-BR') {
  const i18n = createI18n({ legacy: false, locale, messages: { 'pt-BR': ptBR as any, en: en as any } })
  return mount(PermissionMatrix, {
    props: { permissionGroups: groupPermissions(perms), selectedPermissions: selected, disabled },
    global: { plugins: [i18n] },
    attachTo: document.body,
  })
}

describe('PermissionMatrix', () => {
  it('shows functional groups with active counts in Portuguese', () => {
    const w = build(['users:read'])
    expect(w.text()).toContain('Administração e Acesso')
    expect(w.text()).toContain('SAC / CRM')
    expect(w.text()).toContain('1 de 2 permissões ativas')
  })

  it('renders one labelled switch per permission with the right state', () => {
    const w = build(['users:read'])
    const sw = w.find('[data-testid="permission-users:read"]')
    expect(sw.attributes('role')).toBe('switch')
    expect(sw.attributes('aria-checked')).toBe('true')
    expect(sw.attributes('aria-label')).toBe('Visualizar usuários')
    expect(w.find('[data-testid="permission-users:write"]').attributes('aria-checked')).toBe('false')
    expect(w.text()).toContain('Ligado')
    expect(w.text()).toContain('Desligado')
  })

  it('emits the new selection keeping every other key', async () => {
    const w = build(['users:read', 'legacy:keep'])
    await w.find('[data-testid="permission-users:write"]').trigger('click')
    expect((w.emitted('update:selectedPermissions')![0][0] as string[]).sort()).toEqual(['legacy:keep', 'users:read', 'users:write'])
  })

  it('does not emit when disabled', async () => {
    const w = build(['users:write'], true)
    await w.find('[data-testid="permission-users:read"]').trigger('click')
    expect(w.emitted('update:selectedPermissions')).toBeUndefined()
  })

  it('search keeps the group structure and filters by technical key', async () => {
    const w = build([])
    await w.find('[data-testid="permission-search"]').setValue('units:write')
    expect(w.find('[data-testid="permission-group-crm"]').exists()).toBe(true)
    expect(w.find('[data-testid="permission-group-admin"]').exists()).toBe(false)
    expect(w.find('[data-testid="permission-units:write"]').exists()).toBe(true)
    expect(w.find('[data-testid="permission-units:read"]').exists()).toBe(false)
  })

  it('shows a message when nothing matches', async () => {
    const w = build([])
    await w.find('[data-testid="permission-search"]').setValue('zzzz')
    expect(w.text()).toContain('Nenhuma permissão encontrada')
  })

  it('group button turns the whole group on, then off', async () => {
    const w = build(['users:read'])
    const btn = w.find('[data-testid="permission-group-admin"] button[aria-label^="Ligar ou desligar"]')
    await btn.trigger('click')
    expect((w.emitted('update:selectedPermissions')![0][0] as string[]).sort()).toEqual(['users:read', 'users:write'])
  })

  it('has the English texts too', () => {
    expect(build([], false, 'en').text()).toContain('Administration and Access')
  })
})
