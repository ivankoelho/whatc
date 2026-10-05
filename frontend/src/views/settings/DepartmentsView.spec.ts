// @vitest-environment happy-dom
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { h } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { TooltipProvider } from 'reka-ui'
import { createI18n } from 'vue-i18n'
import ptBR from '@/i18n/locales/pt-BR.json'
import en from '@/i18n/locales/en.json'

const api = vi.hoisted(() => ({ list: vi.fn(), create: vi.fn(), update: vi.fn(), delete: vi.fn() }))
vi.mock('@/services/api', () => ({ departmentsService: api }))
const toast = vi.hoisted(() => ({ error: vi.fn(), success: vi.fn(), warning: vi.fn() }))
vi.mock('vue-sonner', () => ({ toast }))
const perms = vi.hoisted(() => ({ granted: new Set<string>() }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ hasPermission: (r: string, a: string) => perms.granted.has(`${r}:${a}`) }) }))

import DepartmentsView from './DepartmentsView.vue'

const departments = [
  { id: 'd1', name: 'Logística', active: true },
  { id: 'd2', name: 'TI', active: false },
]

const $ = (id: string) => document.body.querySelector<HTMLElement>(`[data-testid="${id}"]`)

async function render() {
  const i18n = createI18n({ legacy: false, locale: 'pt-BR', messages: { 'pt-BR': ptBR as any, en: en as any } })
  const w = mount({ render: () => h(TooltipProvider, () => h(DepartmentsView)) }, {
    global: { plugins: [i18n], stubs: { RouterLink: true } },
    attachTo: document.body,
  })
  await flushPromises()
  return w
}

beforeEach(() => {
  document.body.innerHTML = ''
  vi.clearAllMocks()
  perms.granted = new Set(['departments:read', 'departments:write', 'departments:delete'])
  api.list.mockResolvedValue({ data: { data: { departments } } })
})

describe('DepartmentsView', () => {
  it('lists the departments with their status', async () => {
    await render()
    const text = document.body.textContent!
    expect(text).toContain('Logística')
    expect(text).toContain('TI')
    expect(api.list).toHaveBeenCalledOnce()
  })

  it('creates a department with a trimmed name and reloads the list', async () => {
    api.create.mockResolvedValue({ data: { data: { id: 'd3', name: 'ADM', active: true } } })
    await render()
    $('department-add')!.click()
    await flushPromises()
    const input = $('department-name') as HTMLInputElement
    input.value = '  ADM  '
    input.dispatchEvent(new Event('input'))
    await flushPromises()
    // CrudFormDialog footer: [Cancel, Create]
    const buttons = document.body.querySelectorAll<HTMLButtonElement>('[role="dialog"] button')
    Array.from(buttons).find(b => b.textContent!.trim() === 'Create')!.click()
    await flushPromises()
    expect(api.create).toHaveBeenCalledWith({ name: 'ADM', active: true })
    expect(api.list).toHaveBeenCalledTimes(2)
  })

  it('hides create/edit/delete when the user only has departments:read', async () => {
    perms.granted = new Set(['departments:read'])
    await render()
    expect($('department-add')).toBeNull()
    expect(document.body.textContent).toContain('Logística')
    expect(document.body.querySelectorAll('button[aria-label="Editar departamento"], button[aria-label="Excluir departamento"]').length).toBe(0)
  })

  it('shows a clear permission message when the API answers 403 to the list', async () => {
    api.list.mockRejectedValue({ response: { status: 403 } })
    await render()
    expect(document.body.textContent).toContain('Você não tem permissão para listar os departamentos.')
    expect(toast.error).not.toHaveBeenCalled()
  })
})
