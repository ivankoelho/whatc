import { test, expect, request as playwrightRequest } from '@playwright/test'
import { Client } from 'pg'
import { ApiHelper } from '../../helpers'
import {
  createTestScope,
  createUserWithPermissions,
  loginAs,
  SUPER_ADMIN,
  type TestUserHandle,
} from '../../framework'
import { ChatPage } from '../../pages'

/**
 * Strict conversation visibility (Cycle 2).
 *
 * With `strict_conversation_visibility` on, a conversation governed by an
 * active AgentTransfer is visible/actionable only by that transfer's agent
 * plus users holding `conversations:view_all` — see
 * internal/handlers/conversation_visibility.go (authorizeConversation /
 * scopeVisibleConversations). The list is already filtered server-side, so
 * there is no client-side hide logic to test here: we assert what actually
 * renders (or fails to render) after the API has done the filtering, plus
 * the direct-API 403 on the action path.
 *
 * Two distinct non-privileged agent identities are needed (an "assigned"
 * agent and an unrelated "other" agent with no view_all), which the fixed
 * global-setup roles (admin/manager/agent — one of each) can't express.
 * createUserWithPermissions gives each test its own custom-permission users,
 * matching the precedent in chatbot/queue-pickup.spec.ts's "Admin reassign
 * and unassign flows" describe (agentA / agentB).
 */

const DB_URL = process.env.TEST_DATABASE_URL || 'postgres://whatomate:whatomate@127.0.0.1:5432/whatomate'

async function execSQL(sql: string): Promise<Record<string, unknown>[]> {
  const client = new Client({ connectionString: DB_URL })
  await client.connect()
  try {
    const result = await client.query(sql)
    return result.rows as Record<string, unknown>[]
  } finally {
    await client.end()
  }
}

// Flips strict_conversation_visibility through the settings API rather than
// a raw SQL UPDATE. getChatbotSettingsCached (internal/handlers/cache.go)
// caches chatbot settings for 6h and only the PUT handler invalidates that
// cache (a.InvalidateChatbotSettingsCache) — a direct SQL write would be
// invisible to authorizeConversation/scopeVisibleConversations until the
// cache happens to expire. Mirrors updateChatbotSetting in
// chatbot/queue-pickup.spec.ts.
async function setStrictVisibility(value: boolean): Promise<void> {
  const ctx = await playwrightRequest.newContext()
  const api = new ApiHelper(ctx)
  try {
    await api.login(SUPER_ADMIN.email, SUPER_ADMIN.password)
    const resp = await api.put('/api/chatbot/settings', { strict_conversation_visibility: value })
    if (!resp.ok()) {
      throw new Error(`Failed to set strict_conversation_visibility=${value}: ${resp.status()} ${await resp.text()}`)
    }
  } finally {
    await ctx.dispose()
  }
}

const scope = createTestScope('conversation-visibility')

// Serial: every test in this file flips the same org-wide
// strict_conversation_visibility flag, so they can't run interleaved with
// each other (or with other specs' assumption that the flag is off) —
// same rationale as queue-pickup.spec.ts's shared-org serial group.
test.describe.serial('Strict conversation visibility', () => {
  test.setTimeout(60_000)

  let api: ApiHelper
  let orgId: string
  let accountName: string
  let assignedAgent: TestUserHandle
  let otherAgent: TestUserHandle
  let manager: TestUserHandle

  // Basic agent permission set: enough to use the chat UI (chat:read),
  // read contact data (contacts:read) and see transfers (transfers:read) —
  // deliberately WITHOUT conversations:view_all, the one permission the
  // strict-mode bypass checks.
  const agentPermissions = [
    { resource: 'chat', action: 'read' },
    { resource: 'contacts', action: 'read' },
    { resource: 'transfers', action: 'read' },
  ]

  test.beforeAll(async ({ request }) => {
    api = new ApiHelper(request)
    await api.login(SUPER_ADMIN.email, SUPER_ADMIN.password)

    assignedAgent = await createUserWithPermissions(api, scope, {
      userSlug: 'assigned-agent',
      permissions: agentPermissions,
    })
    otherAgent = await createUserWithPermissions(api, scope, {
      userSlug: 'other-agent',
      permissions: agentPermissions,
    })
    manager = await createUserWithPermissions(api, scope, {
      userSlug: 'manager',
      permissions: [...agentPermissions, { resource: 'conversations', action: 'view_all' }],
    })

    const orgRows = await execSQL(
      `SELECT uo.organization_id::text AS org FROM users u
       JOIN user_organizations uo ON uo.user_id = u.id AND uo.is_default = true
       WHERE u.email = '${assignedAgent.email}' LIMIT 1`,
    )
    orgId = orgRows[0]!.org as string

    // A real WhatsApp account must exist for an authorized send to return 200:
    // resolveWhatsAppAccount rejects an unknown account name with 400 before
    // the (async, fake-token) delivery. Create one if the environment has none,
    // mirroring template-sending.spec.ts — otherwise the manager-can-message
    // assertion is coupled to test-ordering.
    let accounts = await api.getWhatsAppAccounts().catch(() => [] as { name: string }[])
    if (accounts.length === 0) {
      const uid = Date.now().toString().slice(-8)
      await api.createWhatsAppAccount({
        name: `e2e-visibility-${uid}`,
        phone_id: `phone-${uid}`,
        business_id: `biz-${uid}`,
        access_token: `token-${uid}`,
      }).catch(() => {})
      accounts = await api.getWhatsAppAccounts().catch(() => [] as { name: string }[])
    }
    accountName = accounts[0]?.name ?? 'test-account'

    await setStrictVisibility(true)
  })

  test.afterAll(async () => {
    // Restore the default so other specs (which assume the flag is off)
    // aren't affected by this file.
    await setStrictVisibility(false).catch(() => {})
    for (const handle of [assignedAgent, otherAgent, manager]) {
      if (handle) {
        await api.deleteUser(handle.user.id).catch(() => {})
        await api.deleteRole(handle.role.id).catch(() => {})
      }
    }
  })

  // Creates a contact with an active AgentTransfer assigned to assignedAgent
  // — the "atendimento ativo com agente" branch of the authorization tree.
  async function seedAssignedConversation(slug: string): Promise<{ contactId: string; contactName: string; phone: string }> {
    const ctx = await playwrightRequest.newContext()
    const localApi = new ApiHelper(ctx)
    try {
      await localApi.login(SUPER_ADMIN.email, SUPER_ADMIN.password)
      const phone = scope.phone()
      const contactName = scope.name(slug)
      const contact = await localApi.createContact(phone, contactName)
      await execSQL(`UPDATE contacts SET whats_app_account = '${accountName}' WHERE id = '${contact.id}'`)
      await execSQL(`
        INSERT INTO agent_transfers (id, organization_id, contact_id, whats_app_account, phone_number, status, source, agent_id, transferred_at, created_at, updated_at)
        VALUES (gen_random_uuid(), '${orgId}', '${contact.id}', '${accountName}', '${phone}', 'active', 'manual', '${assignedAgent.user.id}', NOW(), NOW(), NOW())
      `)
      return { contactId: contact.id, contactName, phone }
    } finally {
      await ctx.dispose()
    }
  }

  test('assigned agent sees the conversation in the list and can open it', async ({ page }) => {
    const { contactId, contactName } = await seedAssignedConversation('assigned-view')

    await loginAs(page, assignedAgent)
    const chatPage = new ChatPage(page)
    await chatPage.goto()

    await expect(
      page.locator('[data-testid="conversation-item"]').filter({ hasText: contactName }),
    ).toBeVisible({ timeout: 10_000 })

    await chatPage.goto(contactId)
    await expect(chatPage.messageInput).toBeVisible({ timeout: 10_000 })
  })

  test('a different agent without view_all does not see the conversation in the list', async ({ page }) => {
    const { contactName } = await seedAssignedConversation('hidden-from-other')

    await loginAs(page, otherAgent)
    const chatPage = new ChatPage(page)
    await chatPage.goto()

    // The list is already filtered server-side (Task 5) — the row simply
    // never arrives in the response, so there is nothing client-side to
    // assert beyond "it's not there".
    await expect(
      page.locator('[data-testid="conversation-item"]').filter({ hasText: contactName }),
    ).toHaveCount(0)
  })

  test('a different agent gets 403 messaging the conversation directly via the API', async () => {
    const { contactId } = await seedAssignedConversation('api-403')

    const ctx = await playwrightRequest.newContext()
    const otherApi = new ApiHelper(ctx)
    try {
      await otherApi.login(otherAgent.email, otherAgent.password)
      const resp = await otherApi.post(`/api/contacts/${contactId}/messages`, {
        type: 'text',
        content: { body: 'should be blocked' },
      })
      expect(resp.status()).toBe(403)
    } finally {
      await ctx.dispose()
    }
  })

  test('a manager with view_all sees and can message the conversation', async ({ page }) => {
    const { contactId, contactName } = await seedAssignedConversation('manager-view')

    await loginAs(page, manager)
    const chatPage = new ChatPage(page)
    await chatPage.goto()

    await expect(
      page.locator('[data-testid="conversation-item"]').filter({ hasText: contactName }),
    ).toBeVisible({ timeout: 10_000 })

    const ctx = await playwrightRequest.newContext()
    const managerApi = new ApiHelper(ctx)
    try {
      await managerApi.login(manager.email, manager.password)
      const resp = await managerApi.post(`/api/contacts/${contactId}/messages`, {
        type: 'text',
        content: { body: 'manager can reply' },
      })
      // The test WhatsApp account has a fake access_token, so delivery itself
      // may later be marked 'failed' async — but the HTTP response for an
      // authorized sender is 200, same assumption as chat-composer.spec.ts.
      expect(resp.ok()).toBe(true)
    } finally {
      await ctx.dispose()
    }
  })

  test('after transferring to a third agent, the original agent immediately loses access', async ({ page }) => {
    const { contactId, contactName } = await seedAssignedConversation('post-transfer')

    // Move the active transfer's agent from assignedAgent to otherAgent —
    // the DB-level equivalent of the reassign dialog covered by
    // chatbot/queue-pickup.spec.ts's "Admin reassign and unassign flows".
    // Authorization reads the CURRENT active transfer row, so this alone
    // must flip visibility for both agents on their next request.
    await execSQL(
      `UPDATE agent_transfers SET agent_id = '${otherAgent.user.id}' WHERE contact_id = '${contactId}' AND status = 'active'`,
    )

    await loginAs(page, assignedAgent)
    const chatPage = new ChatPage(page)
    await chatPage.goto()
    await expect(
      page.locator('[data-testid="conversation-item"]').filter({ hasText: contactName }),
    ).toHaveCount(0)

    await loginAs(page, otherAgent)
    await chatPage.goto()
    await expect(
      page.locator('[data-testid="conversation-item"]').filter({ hasText: contactName }),
    ).toBeVisible({ timeout: 10_000 })
  })
})
