// @vitest-environment happy-dom
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createI18n } from 'vue-i18n'
import en from '@/i18n/locales/en.json'

const api = vi.hoisted(() => ({
  channels: vi.fn(), archivedChannels: vi.fn(), messages: vi.fn(), members: vi.fn(), adminChannels: vi.fn(), join: vi.fn()
}))
vi.mock('@/services/api', () => ({ teamChatService: api }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ user: { id: 'me' }, hasPermission: () => true }) }))
const toast = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn() }))
vi.mock('vue-sonner', () => ({ toast }))

import AdminJoinDialog from './AdminJoinDialog.vue'

const i18n = createI18n({ legacy: false, locale: 'en', messages: { en } })
const pass = { template: '<div><slot /></div>' }
const ok = (data: unknown = {}) => Promise.resolve({ data: { data } })
const fail = (status: number, code: string) => Promise.reject({ response: { status, data: { error_type: code } } })

function render() {
  return mount(AdminJoinDialog, {
    props: { open: true, channelId: 'c1', channelName: 'Board' },
    global: {
      plugins: [i18n],
      stubs: { Dialog: pass, DialogContent: pass, DialogHeader: pass, DialogTitle: pass, DialogDescription: pass }
    }
  })
}
const submit = (w: ReturnType<typeof render>) => w.get('button[type="submit"]')

beforeEach(() => {
  setActivePinia(createPinia())
  Object.values(api).forEach(f => f.mockReset())
  api.channels.mockReturnValue(ok({ channels: [] }))
  api.archivedChannels.mockReturnValue(ok({ channels: [] }))
  api.messages.mockReturnValue(ok({ messages: [], has_more: false }))
  api.members.mockReturnValue(ok({ members: [] }))
  api.adminChannels.mockReturnValue(ok({ channels: [] }))
  toast.success.mockReset(); toast.error.mockReset()
})

describe('AdminJoinDialog', () => {
  it('says what entering means: everyone sees it, with the reason, and it is audited', () => {
    const w = render()
    expect(w.text()).toContain('Enter "Board"')
    expect(w.text()).toContain('Everyone in the channel will see that you entered and why')
    expect(w.text()).toContain('audit log')
  })

  it('needs a reason of 5 to 500 characters, trimmed, counting characters and not bytes', async () => {
    const w = render()
    const box = w.get('textarea')
    expect(submit(w).attributes('disabled')).toBeDefined()

    await box.setValue('  abcd  ')
    expect(submit(w).attributes('disabled')).toBeDefined()
    await box.setValue('  abcde  ')
    expect(submit(w).attributes('disabled')).toBeUndefined()
    await box.setValue('😀'.repeat(5)) // 5 characters, 10 UTF-16 units
    expect(submit(w).attributes('disabled')).toBeUndefined()
    await box.setValue('x'.repeat(500))
    expect(submit(w).attributes('disabled')).toBeUndefined()
    await box.setValue('x'.repeat(501))
    expect(submit(w).attributes('disabled')).toBeDefined()
    expect(w.text()).toContain('501 so far')
  })

  it('sends the trimmed reason, then closes and tells the parent', async () => {
    api.join.mockReturnValue(ok())
    const w = render()
    await w.get('textarea').setValue('  Reviewing the complaint #1042  ')
    await w.get('form').trigger('submit')
    await flushPromises()

    expect(api.join).toHaveBeenCalledWith('c1', 'Reviewing the complaint #1042')
    expect(toast.success).toHaveBeenCalledWith(en.teamChat.admin.joined)
    expect(w.emitted('update:open')?.[0]).toEqual([false])
    expect(w.emitted('joined')).toHaveLength(1)
  })

  it('never submits an invalid reason, even by pressing Enter in the form', async () => {
    const w = render()
    await w.get('textarea').setValue('no')
    await w.get('form').trigger('submit')
    await flushPromises()
    expect(api.join).not.toHaveBeenCalled()
  })

  it('explains a refusal and stays open', async () => {
    const w = render()
    for (const [code, text] of [
      ['already_member', en.teamChat.errors.already_member],
      ['archived', en.teamChat.errors.archived],
      ['forbidden', en.teamChat.errors.generic]
    ] as const) {
      toast.error.mockReset()
      api.join.mockReturnValueOnce(fail(409, code))
      await w.get('textarea').setValue('a good enough reason')
      await w.get('form').trigger('submit')
      await flushPromises()
      expect(toast.error).toHaveBeenCalledWith(text)
    }
    expect(w.emitted('joined')).toBeUndefined()
    expect(w.emitted('update:open')).toBeUndefined()
  })
})
