// @vitest-environment happy-dom
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'

// The guard decides before any screen renders, so the screens are stubbed: this
// keeps the test fast and proves the decision does not depend on them.
const stub = { default: { render: () => null } }
vi.mock('@/components/layout/AppLayout.vue', () => stub)
vi.mock('@/views/auth/LoginView.vue', () => stub)
vi.mock('@/views/chat/ChatView.vue', () => stub)
vi.mock('@/views/team-chat/TeamChatView.vue', () => stub)
vi.mock('@/views/profile/ProfileView.vue', () => stub)
vi.mock('@/views/dashboard/DashboardView.vue', () => stub)

import { useAuthStore } from '@/stores/auth'
import router from './index'

function login(permissions: Array<[string, string]>) {
  const auth = useAuthStore()
  auth.user = {
    id: 'u1',
    email: 'u1@example.com',
    full_name: 'U One',
    is_super_admin: false,
    role: { permissions: permissions.map(([resource, action]) => ({ resource, action })) }
  } as never
}

beforeEach(() => {
  setActivePinia(createPinia())
  localStorage.clear()
})

describe('/team-chat route', () => {
  it('has its own permission, independent of the WhatsApp /chat route', () => {
    expect(router.resolve('/team-chat').meta.permission).toBe('team_chat')
    expect(router.resolve('/team-chat/abc').meta.permission).toBe('team_chat')
    expect(router.resolve('/chat').meta.permission).toBe('chat')
    expect(router.resolve('/chat/abc').meta.permission).toBe('chat')
  })

  it('sends someone without team_chat away, whatever channel id they typed', async () => {
    login([['chat', 'read'], ['chat', 'write']]) // full WhatsApp access, no internal chat
    await router.push('/team-chat/00000000-0000-0000-0000-000000000001')
    expect(router.currentRoute.value.path).not.toContain('team-chat')
    expect(router.currentRoute.value.path).toBe('/chat') // first place they may open
  })

  it('lets someone with team_chat:read through', async () => {
    login([['team_chat', 'read']])
    await router.push('/team-chat/abc')
    expect(router.currentRoute.value.name).toBe('team-chat')
    expect(router.currentRoute.value.params.channelId).toBe('abc')
  })

  it('does not open the WhatsApp /chat for someone who only has team_chat', async () => {
    login([['team_chat', 'read'], ['team_chat', 'write']])
    await router.push('/chat/some-contact')
    expect(router.currentRoute.value.name).not.toBe('chat-conversation')
    expect(router.currentRoute.value.path).toBe('/team-chat') // their first accessible page
  })

  it('sends an anonymous visitor to the login page', async () => {
    await router.push('/team-chat/abc')
    expect(router.currentRoute.value.name).toBe('login')
  })
})
