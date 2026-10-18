// @vitest-environment happy-dom
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'

// The guard decides before any screen renders, so the screens are stubbed.
const stub = { default: { render: () => null } }
vi.mock('@/components/layout/AppLayout.vue', () => stub)
vi.mock('@/views/auth/LoginView.vue', () => stub)
vi.mock('@/views/chat/ChatView.vue', () => stub)
vi.mock('@/views/team-chat/TeamChatView.vue', () => stub)
vi.mock('@/views/team-chat/TeamChatAdminView.vue', () => stub)
vi.mock('@/views/profile/ProfileView.vue', () => stub)
vi.mock('@/views/dashboard/DashboardView.vue', () => stub)

import { useAuthStore } from '@/stores/auth'
import router from './index'

function login(permissions: Array<[string, string]>) {
  useAuthStore().user = {
    id: 'u1', email: 'u1@example.com', full_name: 'U One', is_super_admin: false,
    role: { permissions: permissions.map(([resource, action]) => ({ resource, action })) }
  } as never
}

beforeEach(async () => {
  setActivePinia(createPinia())
  localStorage.clear()
  // The router is a singleton: start every test from a neutral page, otherwise a
  // navigation to the page the previous test ended on is a no-op and skips the guard.
  await router.replace('/login')
})

describe('/team-chat/admin route', () => {
  it('needs team_chat:manage, not just team_chat:read', () => {
    const meta = router.resolve('/team-chat/admin').meta
    expect(meta.permission).toBe('team_chat')
    expect(meta.permissionAction).toBe('manage')
    // It is a screen of its own, not a channel id for the regular route.
    expect(router.resolve('/team-chat/admin').name).toBe('team-chat-admin')
    expect(router.resolve('/team-chat/some-id').name).toBe('team-chat')
  })

  it('sends someone who only uses the chat away from it', async () => {
    login([['team_chat', 'read'], ['team_chat', 'write']])
    await router.push('/team-chat/admin')
    expect(router.currentRoute.value.name).not.toBe('team-chat-admin')
  })

  it('does not take join_any or delete as a substitute for manage', async () => {
    login([['team_chat', 'read'], ['team_chat', 'write'], ['team_chat', 'delete'], ['team_chat', 'join_any']])
    await router.push('/team-chat/admin')
    expect(router.currentRoute.value.name).not.toBe('team-chat-admin')
  })

  it('lets a manager in', async () => {
    login([['team_chat', 'read'], ['team_chat', 'manage']])
    await router.push('/team-chat/admin')
    expect(router.currentRoute.value.name).toBe('team-chat-admin')
  })

  it('sends an anonymous visitor to the login page', async () => {
    await router.push('/team-chat/admin')
    expect(router.currentRoute.value.name).toBe('login')
  })
})
