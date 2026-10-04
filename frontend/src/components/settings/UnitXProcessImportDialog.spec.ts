// @vitest-environment happy-dom
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import ptBR from '@/i18n/locales/pt-BR.json'
import en from '@/i18n/locales/en.json'
import type { XProcessLoja, XProcessImportResponse } from '@/services/api'

const api = vi.hoisted(() => ({ listXProcessLojas: vi.fn(), importXProcess: vi.fn() }))
vi.mock('@/services/api', () => ({ unitsService: api }))
const toast = vi.hoisted(() => ({ error: vi.fn(), success: vi.fn(), warning: vi.fn() }))
vi.mock('vue-sonner', () => ({ toast }))

import UnitXProcessImportDialog from './UnitXProcessImportDialog.vue'

const loja = (cod: string, razao: string, cnpj: string, extra: Partial<XProcessLoja> = {}): XProcessLoja =>
  ({ cod_empresa: cod, razao_social_empresa: razao, cnpj_empresa: cnpj, import_status: 'new', import_name: razao.replace(/.*\((.*)\)/, '$1'), ...extra })

const lojas = [
  loja('2', 'ATACADAO (FEIRA)', '11.111.111/0001-11', { import_status: 'already_imported', unit_name: 'FEIRA' }),
  loja('3', 'ATACADAO (SERRINHA)', '22.222.222/0001-22'),
  loja('4', 'ATACADAO (PETROLINA)', '33.333.333/0001-33'),
  loja('5', 'ATACADAO (JUAZEIRO)', '44.444.444/0001-44', { import_status: 'conflict', conflict_reason: 'cnpj_in_use', conflict_unit_name: 'Unidade A' }),
]

const $ = (id: string) => document.body.querySelector<HTMLElement>(`[data-testid="${id}"]`)
const click = async (id: string) => { $(id)!.click(); await flushPromises() }

async function openDialog(): Promise<VueWrapper> {
  const i18n = createI18n({ legacy: false, locale: 'pt-BR', messages: { 'pt-BR': ptBR as any, en: en as any } })
  const w = mount(UnitXProcessImportDialog, { props: { open: false }, global: { plugins: [i18n] }, attachTo: document.body })
  await w.setProps({ open: true })
  await flushPromises()
  return w
}

beforeEach(() => {
  document.body.innerHTML = ''
  vi.clearAllMocks()
  api.listXProcessLojas.mockResolvedValue({ data: { data: { lojas } } })
})

describe('UnitXProcessImportDialog', () => {
  it('opens, loads the stores and shows code, name, CNPJ and status', async () => {
    await openDialog()
    expect(api.listXProcessLojas).toHaveBeenCalledOnce()
    const list = $('x2-import-list')!.textContent!
    expect(list).toContain('ATACADAO (SERRINHA)')
    expect(list).toContain('CNPJ: 22.222.222/0001-22')
    expect($('x2-import-status-3')!.textContent).toContain('Nova')
    expect($('x2-import-status-2')!.textContent).toContain('Já importada')
    expect($('x2-import-status-5')!.textContent).toContain('Conflito')
    expect(list).toContain('Unidade A')
    expect(list).toContain('Será criada como SERRINHA')
  })

  it('searches by name, code and CNPJ', async () => {
    await openDialog()
    const search = $('x2-import-search') as HTMLInputElement
    search.value = 'petrolina'
    search.dispatchEvent(new Event('input'))
    await flushPromises()
    expect($('x2-import-check-4')).not.toBeNull()
    expect($('x2-import-check-3')).toBeNull()
  })

  it('blocks the import until something is selected, counts, and lets the user unselect', async () => {
    await openDialog()
    expect(($('x2-import-submit') as HTMLButtonElement).disabled).toBe(true)
    await click('x2-import-check-3')
    await click('x2-import-check-4')
    expect($('x2-import-count')!.textContent).toContain('2')
    expect(($('x2-import-submit') as HTMLButtonElement).disabled).toBe(false)
    await click('x2-import-check-3')
    expect($('x2-import-count')!.textContent).toContain('1')
    await click('x2-import-check-4')
    expect(($('x2-import-submit') as HTMLButtonElement).disabled).toBe(true)
    expect(api.importXProcess).not.toHaveBeenCalled()
  })

  it('does not let already imported or conflicting stores be selected', async () => {
    await openDialog()
    expect($('x2-import-check-2')!.hasAttribute('disabled') || $('x2-import-check-2')!.getAttribute('data-disabled') !== null).toBe(true)
    await click('x2-import-check-5')
    expect($('x2-import-count')!.textContent).toContain('0')
  })

  it('imports the selected codes, shows the summary and each result, and refreshes the list behind it', async () => {
    const resp: XProcessImportResponse = {
      summary: { selected: 2, created: 1, updated: 0, already_exists: 0, conflicts: 1, failed: 0 },
      results: [
        { cod_empresa: '3', status: 'created', unit: { id: 'u1', name: 'SERRINHA' } as any },
        { cod_empresa: '4', status: 'conflict', reason: 'name_in_use', existing_unit: { id: 'u2', name: 'PETROLINA' } },
      ],
    }
    api.importXProcess.mockResolvedValue({ data: { data: resp } })
    const w = await openDialog()
    await click('x2-import-check-3')
    await click('x2-import-check-4')
    await click('x2-import-submit')

    expect(api.importXProcess).toHaveBeenCalledWith(['3', '4'])
    expect($('x2-import-summary-created')!.textContent).toContain('Criadas: 1')
    expect($('x2-import-summary-conflicts')!.textContent).toContain('Conflitos: 1')
    expect($('x2-import-row-3')!.textContent).toContain('Criada')
    expect($('x2-import-row-4')!.textContent).toContain('Conflito')
    expect($('x2-import-row-4')!.textContent).toContain('PETROLINA')
    expect(w.emitted('imported')).toHaveLength(1)
  })

  it('does not refresh the list when nothing was created', async () => {
    api.importXProcess.mockResolvedValue({ data: { data: {
      summary: { selected: 1, created: 0, updated: 0, already_exists: 0, conflicts: 1, failed: 0 },
      results: [{ cod_empresa: '3', status: 'conflict', reason: 'cnpj_in_use', existing_unit: { id: 'u', name: 'X' } }],
    } } })
    const w = await openDialog()
    await click('x2-import-check-3')
    await click('x2-import-submit')
    expect(w.emitted('imported')).toBeUndefined()
  })

  it('shows an error when loading the stores fails, with a retry', async () => {
    api.listXProcessLojas.mockRejectedValueOnce(new Error('boom'))
    await openDialog()
    expect($('x2-import-load-failed')).not.toBeNull()
    expect(toast.error).toHaveBeenCalled()
    expect($('x2-import-list')).toBeNull()
  })

  it('keeps the selection and reports an error when the import request fails', async () => {
    api.importXProcess.mockRejectedValue(new Error('502'))
    const w = await openDialog()
    await click('x2-import-check-3')
    await click('x2-import-submit')
    expect(toast.error).toHaveBeenCalled()
    expect($('x2-import-result')).toBeNull()
    expect($('x2-import-count')!.textContent).toContain('1')
    expect(w.emitted('imported')).toBeUndefined()
  })
})
