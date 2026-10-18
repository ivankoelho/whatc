// @vitest-environment happy-dom
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createI18n } from 'vue-i18n'
import en from '@/i18n/locales/en.json'

const api = vi.hoisted(() => ({ adminChannels: vi.fn() }))
vi.mock('@/services/api', () => ({ teamChatService: api }))
const router = vi.hoisted(() => ({ push: vi.fn() }))
vi.mock('vue-router', () => ({ useRouter: () => router }))
const perms = vi.hoisted(() => ({ granted: new Set<string>() }))
vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ user: { id: 'me' }, hasPermission: (r: string, a: string) => perms.granted.has(`${r}:${a}`) })
}))

import TeamChatAdminView from './TeamChatAdminView.vue'

const i18n = createI18n({ legacy: false, locale: 'en', messages: { en } })
const ok = (data: unknown) => Promise.resolve({ data: { data } })

// The composition component is replaced by a probe that exposes the flags the view
// computed for it, so this test is about the view's decisions and not about buttons.
const Probe = {
  props: ['channelId', 'name', 'archivedAt', 'members', 'canCompose', 'canReopen', 'canJoin', 'isMember', 'meId'],
  template: '<div :data-testid="`probe-${channelId}`" :data-compose="canCompose" :data-reopen="canReopen" :data-join="canJoin" :data-member="isMember">{{ name }}</div>'
}

const person = (id: string, role = 'member') => ({ user_id: id, name: id, role, is_online: false })
const admin = (id: string, name: string, members: unknown[], extra: Record<string, unknown> = {}) => ({
  id, name, kind: 'channel', created_at: '2026-10-17T00:00:00Z', created_by_name: 'Ana',
  owners: [person('ana', 'owner')], members, ...extra
})

function render(channels: unknown[]) {
  api.adminChannels.mockReturnValue(ok({ channels }))
  return mount(TeamChatAdminView, {
    global: { plugins: [i18n], stubs: { ChannelComposition: Probe } }
  })
}
const flags = (w: ReturnType<typeof render>, id: string) => w.get(`[data-testid="probe-${id}"]`).attributes()

beforeEach(() => {
  setActivePinia(createPinia())
  api.adminChannels.mockReset()
  router.push.mockReset()
  perms.granted = new Set(['team_chat:read', 'team_chat:write', 'team_chat:manage'])
})

describe('TeamChatAdminView', () => {
  it('lists each channel with who created it and its state, and nothing about what was said', async () => {
    const w = render([
      admin('a', 'Sales', [person('ana', 'owner'), person('bia')]),
      admin('b', 'Old', [person('ana', 'owner')], { archived_at: '2026-10-01T00:00:00Z', created_by_name: 'Caio' })
    ])
    await flushPromises()

    expect(w.text()).toContain('Channel administration')
    expect(w.text()).toContain('Messages are never shown here')
    expect(w.text()).toContain('Sales')
    expect(w.text()).toContain('Created by Caio')
    expect(w.text()).toContain('Archived')
    expect(w.find('[data-testid="admin-channel-a"]').exists()).toBe(true)
    expect(w.find('textarea').exists()).toBe(false) // there is no composer here
  })

  it('a manager composes and reopens but cannot enter: no join_any', async () => {
    const w = render([admin('a', 'Sales', [person('ana', 'owner')])])
    await flushPromises()
    const f = flags(w, 'a')
    expect(f['data-compose']).toBe('true')
    expect(f['data-reopen']).toBe('true')
    expect(f['data-join']).toBe('false')
    expect(f['data-member']).toBe('false')
  })

  it('an administrator with join_any can enter channels they are not in, but not ones they already are', async () => {
    perms.granted.add('team_chat:join_any')
    const w = render([
      admin('out', 'Board', [person('ana', 'owner')]),
      admin('in', 'Mine', [person('me'), person('ana', 'owner')])
    ])
    await flushPromises()
    expect(flags(w, 'out')['data-join']).toBe('true')
    expect(flags(w, 'in')['data-join']).toBe('false')
    expect(flags(w, 'in')['data-member']).toBe('true')
    expect(w.get('[data-testid="admin-channel-in"]').text()).toContain('You are a member')
    expect(w.get('[data-testid="admin-channel-out"]').text()).not.toContain('You are a member')
  })

  it('shows loading, an empty state and a retryable error', async () => {
    api.adminChannels.mockReturnValueOnce(new Promise(() => {})) // never answers
    const loading = mount(TeamChatAdminView, { global: { plugins: [i18n], stubs: { ChannelComposition: Probe } } })
    await flushPromises()
    expect(loading.findAll('[class*="h-40"]').length).toBeGreaterThan(0)

    setActivePinia(createPinia())
    const empty = render([])
    await flushPromises()
    expect(empty.text()).toContain('No channels yet')

    setActivePinia(createPinia())
    api.adminChannels.mockReturnValueOnce(Promise.reject({ response: { status: 500 } }))
    const failed = mount(TeamChatAdminView, { global: { plugins: [i18n], stubs: { ChannelComposition: Probe } } })
    await flushPromises()
    expect(failed.text()).toContain('Could not load your channels')
    api.adminChannels.mockReturnValue(ok({ channels: [admin('a', 'Recovered', [])] }))
    await failed.findAll('button').find(b => b.text().includes('Retry'))!.trigger('click')
    await flushPromises()
    expect(failed.text()).toContain('Recovered')
  })

  it('goes back to the chat', async () => {
    const w = render([])
    await flushPromises()
    await w.get('button[aria-label="Back"]').trigger('click')
    expect(router.push).toHaveBeenCalledWith({ name: 'team-chat' })
  })
})
