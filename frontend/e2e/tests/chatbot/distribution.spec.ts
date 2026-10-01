import { test, expect, request as playwrightRequest } from '@playwright/test'
import type { Browser, BrowserContext, Page } from '@playwright/test'
import { Client } from 'pg'
import { ApiHelper } from '../../helpers'
import {
  createTestScope,
  createUserWithPermissions,
  SUPER_ADMIN,
  type TestUserHandle,
} from '../../framework'

/**
 * Phase 6: distribution (presence-aware eligibility, atomic claim, ownership
 * gate, 60s disconnect grace, realtime presence/availability).
 * See docs/superpowers/specs/2026-10-01-fase-6-distribuicao-design.md.
 *
 * Presence is "has a live WebSocket", so "an agent is connected" means a real
 * browser session logged in as that agent. Everything else goes through the
 * public API, as the frontend does.
 */

const DB_URL = process.env.TEST_DATABASE_URL || 'postgres://whatomate:whatomate@127.0.0.1:5432/whatomate'
const scope = createTestScope('distribution')

async function execSQL(sql: string): Promise<Record<string, unknown>[]> {
  const client = new Client({ connectionString: DB_URL })
  await client.connect()
  try {
    return (await client.query(sql)).rows as Record<string, unknown>[]
  } finally {
    await client.end()
  }
}

async function connectAgent(browser: Browser, agent: { email: string; password: string }): Promise<{ context: BrowserContext; page: Page }> {
  const context = await browser.newContext()
  const page = await context.newPage()
  await page.goto('/login')
  await page.locator('input[type="email"]').fill(agent.email)
  await page.locator('input[type="password"]').fill(agent.password)
  await page.locator('button[type="submit"]').click()
  await page.waitForURL((url) => !url.pathname.includes('/login'), { timeout: 10_000 })
  await page.waitForTimeout(1_500) // WebSocket authenticates right after load
  return { context, page }
}

async function apiAs(user: { email: string; password: string }): Promise<{ api: ApiHelper; dispose: () => Promise<void> }> {
  const ctx = await playwrightRequest.newContext()
  const api = new ApiHelper(ctx)
  await api.login(user.email, user.password)
  return { api, dispose: () => ctx.dispose() }
}

const agentPermissions = [
  { resource: 'chat', action: 'read' },
  { resource: 'chat', action: 'write' },
  { resource: 'contacts', action: 'read' },
  { resource: 'transfers', action: 'read' },
  { resource: 'transfers', action: 'pickup' },
]

let admin: ApiHelper
let adminCtxDispose: () => Promise<void>
let orgId: string
let accountName: string

async function newContact(slug: string): Promise<string> {
  const c = await admin.createContact(scope.phone(), scope.name(slug))
  await execSQL(`UPDATE contacts SET whats_app_account = '${accountName}' WHERE id = '${c.id}'`)
  return c.id as string
}

async function seedTransfer(contactId: string, agentId: string | null): Promise<string> {
  const rows = await execSQL(`
    INSERT INTO agent_transfers (id, organization_id, contact_id, whats_app_account, phone_number, status, source, agent_id, transferred_at, created_at, updated_at)
    VALUES (gen_random_uuid(), '${orgId}', '${contactId}', '${accountName}', '${scope.phone()}', 'active', 'manual', ${agentId ? `'${agentId}'` : 'NULL'}, NOW(), NOW(), NOW())
    RETURNING id::text AS id`)
  return rows[0]!.id as string
}

async function transferAgent(transferId: string): Promise<string | null> {
  const rows = await execSQL(`SELECT agent_id::text AS agent_id FROM agent_transfers WHERE id = '${transferId}'`)
  return (rows[0]!.agent_id as string | null) ?? null
}

async function makeAgent(slug: string, extra: { resource: string; action: string }[] = []): Promise<TestUserHandle> {
  return createUserWithPermissions(admin, scope, { userSlug: slug, permissions: [...agentPermissions, ...extra] })
}

test.beforeAll(async () => {
  const ctx = await playwrightRequest.newContext()
  adminCtxDispose = () => ctx.dispose()
  admin = new ApiHelper(ctx)
  await admin.login(SUPER_ADMIN.email, SUPER_ADMIN.password)

  const accounts = await admin.getWhatsAppAccounts().catch(() => [] as { name: string }[])
  if (accounts.length > 0) {
    accountName = accounts[0]!.name
  } else {
    accountName = (await admin.createWhatsAppAccount({
      name: scope.name('acc').toLowerCase().replace(/\s/g, '-'),
      phone_id: `phone-${Date.now()}`,
      business_id: `biz-${Date.now()}`,
      access_token: 'test-token-distribution',
    })).name
  }
  const rows = await execSQL(`SELECT organization_id::text AS org FROM whatsapp_accounts WHERE name = '${accountName}' LIMIT 1`)
  orgId = rows[0]!.org as string
})

test.afterAll(async () => {
  await adminCtxDispose?.()
})

test.describe('Eligibility: presence decides who receives distribution', () => {
  test('a connected agent receives the attendance, an offline one never does', async ({ browser }) => {
    const online = await makeAgent('elig-online')
    const offline = await makeAgent('elig-offline')
    const teamResp = await admin.post('/api/teams', {
      name: scope.name('elig-team'), description: 'distribution e2e', assignment_strategy: 'round_robin', is_active: true,
    })
    expect(teamResp.ok(), await teamResp.text()).toBe(true)
    const teamId = (await teamResp.json()).data.team.id
    for (const a of [online, offline]) {
      const r = await admin.post(`/api/teams/${teamId}/members`, { user_id: a.user.id, role: 'agent' })
      expect(r.ok(), await r.text()).toBe(true)
    }

    // Both are is_available=true in the database; only `online` has a live session.
    const session = await connectAgent(browser, online)
    try {
      for (let i = 0; i < 3; i++) {
        const contactId = await newContact(`elig-${i}`)
        const r = await admin.post('/api/chatbot/transfers', { contact_id: contactId, whatsapp_account: accountName, team_id: teamId })
        expect(r.ok(), await r.text()).toBe(true)
        const transfer = (await r.json()).data?.transfer ?? (await r.json()).data
        const agentId = await transferAgent(transfer.id)
        expect(agentId, 'round-robin must only ever pick the connected agent').toBe(online.user.id)
      }

      // Explicitly assigning to the offline agent is refused with a clear reason.
      const contactId = await newContact('elig-explicit')
      const refused = await admin.post('/api/chatbot/transfers', {
        contact_id: contactId, whatsapp_account: accountName, agent_id: offline.user.id,
      })
      expect(refused.status()).toBe(400)
      expect((await refused.json()).message).toMatch(/offline/i)
    } finally {
      await session.context.close()
      await execSQL(`DELETE FROM team_members WHERE team_id = '${teamId}'`)
    }
  })

  test('an agent who set themselves away is not eligible even while connected', async ({ browser }) => {
    const agent = await makeAgent('elig-away')
    const session = await connectAgent(browser, agent)
    const { api, dispose } = await apiAs(agent)
    try {
      const away = await api.put('/api/me/availability', { is_available: false })
      expect(away.ok()).toBe(true)
      const contactId = await newContact('elig-away')
      const refused = await admin.post('/api/chatbot/transfers', {
        contact_id: contactId, whatsapp_account: accountName, agent_id: agent.user.id,
      })
      expect(refused.status()).toBe(400)
      expect((await refused.json()).message).toMatch(/away/i)

      await api.put('/api/me/availability', { is_available: true })
      const ok = await admin.post('/api/chatbot/transfers', {
        contact_id: contactId, whatsapp_account: accountName, agent_id: agent.user.id,
      })
      expect(ok.ok(), await ok.text()).toBe(true)
    } finally {
      await dispose()
      await session.context.close()
    }
  })
})

test.describe('Concurrency and ownership', () => {
  test('two agents claiming the same attendance: exactly one wins, the other gets 409', async () => {
    const a = await makeAgent('race-a')
    const b = await makeAgent('race-b')
    const contactId = await newContact('race')
    const transferId = await seedTransfer(contactId, null)
    const sa = await apiAs(a)
    const sb = await apiAs(b)
    try {
      const [ra, rb] = await Promise.all([
        sa.api.put(`/api/chatbot/transfers/${transferId}/assign`, {}),
        sb.api.put(`/api/chatbot/transfers/${transferId}/assign`, {}),
      ])
      const statuses = [ra.status(), rb.status()].sort()
      expect(statuses).toEqual([200, 409])
      const winner = ra.status() === 200 ? a : b
      expect(await transferAgent(transferId)).toBe(winner.user.id)

      const loser = ra.status() === 409 ? ra : rb
      const body = await loser.json()
      expect(body.data.agent_id, 'the 409 carries the current owner so the UI can refresh').toBe(winner.user.id)
    } finally {
      await sa.dispose()
      await sb.dispose()
    }
  })

  test('replying in another agent\'s conversation returns 409; a supervisor is not blocked', async () => {
    const owner = await makeAgent('own-owner')
    const other = await makeAgent('own-other')
    const supervisor = await makeAgent('own-sup', [{ resource: 'transfers', action: 'write' }])
    const contactId = await newContact('own')
    const transferId = await seedTransfer(contactId, owner.user.id)
    const msg = { type: 'text', content: { body: 'hello from the wrong agent' } }

    const so = await apiAs(other)
    const ss = await apiAs(supervisor)
    try {
      const blocked = await so.api.post(`/api/contacts/${contactId}/messages`, msg)
      expect(blocked.status()).toBe(409)
      expect(await transferAgent(transferId)).toBe(owner.user.id)

      // The supervisor passes the ownership gate (the send itself may fail
      // against Meta in this environment; the point is that it is not a 409
      // and ownership does not change).
      const allowed = await ss.api.post(`/api/contacts/${contactId}/messages`, msg)
      expect(allowed.status()).not.toBe(409)
      expect(await transferAgent(transferId)).toBe(owner.user.id)
    } finally {
      await so.dispose()
      await ss.dispose()
    }
  })
})

test.describe('Disconnect grace period (60s)', () => {
  test.setTimeout(150_000)

  test('reconnecting inside the window keeps the attendance', async ({ browser }) => {
    const agent = await makeAgent('grace-keep')
    const contactId = await newContact('grace-keep')
    const transferId = await seedTransfer(contactId, agent.user.id)

    let session = await connectAgent(browser, agent)
    await session.context.close() // connection lost
    await new Promise((r) => setTimeout(r, 20_000))
    expect(await transferAgent(transferId), 'still assigned during the window').toBe(agent.user.id)

    session = await connectAgent(browser, agent) // back at ~20s
    try {
      // Wait past the point where the ORIGINAL disconnect would have expired.
      await new Promise((r) => setTimeout(r, 55_000))
      expect(await transferAgent(transferId), 'a reconnect keeps the assignment').toBe(agent.user.id)
    } finally {
      await session.context.close()
    }
  })

  test('staying disconnected past 60s returns the attendance to the queue', async ({ browser }) => {
    const agent = await makeAgent('grace-return')
    const contactId = await newContact('grace-return')
    const transferId = await seedTransfer(contactId, agent.user.id)

    const session = await connectAgent(browser, agent)
    await session.context.close()
    const disconnectedAt = Date.now()

    await new Promise((r) => setTimeout(r, 40_000))
    expect(await transferAgent(transferId), 'not returned before the grace period').toBe(agent.user.id)

    await expect.poll(() => transferAgent(transferId), { timeout: 60_000, intervals: [2_000] }).toBeNull()
    const elapsed = (Date.now() - disconnectedAt) / 1000
    expect(elapsed).toBeGreaterThanOrEqual(55)
    expect(elapsed).toBeLessThan(85)

    // The attendance is still open (back in the queue), not closed.
    const rows = await execSQL(`SELECT status FROM agent_transfers WHERE id = '${transferId}'`)
    expect(rows[0]!.status).toBe('active')
  })
})

test.describe('Presence and availability in the UI', () => {
  test('the users list shows presence and availability separately, in realtime', async ({ page, browser }) => {
    const agent = await makeAgent('ui-presence')

    await page.goto('/login')
    await page.locator('input[type="email"]').fill(SUPER_ADMIN.email)
    await page.locator('input[type="password"]').fill(SUPER_ADMIN.password)
    await page.locator('button[type="submit"]').click()
    await page.waitForURL((url) => !url.pathname.includes('/login'), { timeout: 10_000 })
    await page.goto('/settings/users')
    await page.getByPlaceholder(/search/i).first().fill(agent.email).catch(() => {})
    const dot = page.locator('tbody tr').filter({ hasText: agent.email }).locator('[role="img"]').first()
    await expect(dot).toHaveAttribute('aria-label', /Offline/i, { timeout: 15_000 })

    // Connects: online + available, without reloading the admin page.
    const session = await connectAgent(browser, agent)
    try {
      await expect(dot).toHaveAttribute('aria-label', /Online, available/i, { timeout: 10_000 })

      // Goes away: still online, now away.
      const { api, dispose } = await apiAs(agent)
      try {
        await api.put('/api/me/availability', { is_available: false })
      } finally {
        await dispose()
      }
      await expect(dot).toHaveAttribute('aria-label', /Online, away/i, { timeout: 10_000 })
    } finally {
      await session.context.close()
    }
    // Last connection closed: offline again.
    await expect(dot).toHaveAttribute('aria-label', /Offline/i, { timeout: 10_000 })
  })

  test('two tabs of the same agent stay in sync when availability changes', async ({ browser }) => {
    const agent = await makeAgent('ui-tabs')
    const { context, page: tab1 } = await connectAgent(browser, agent)
    const tab2 = await context.newPage()
    await tab2.goto('/')
    await tab2.waitForTimeout(1_500)
    try {
      const switchOf = async (p: Page) => {
        await p.getByTestId('user-menu-trigger').click()
        const sw = p.getByRole('switch', { name: /availability/i })
        await expect(sw).toBeVisible()
        return sw
      }
      // Toggle away in tab 1 through the real UI.
      const sw1 = await switchOf(tab1)
      await expect(sw1).toHaveAttribute('aria-checked', 'true')
      await sw1.click()
      const confirm = tab1.getByRole('button', { name: /go away|away/i }).last()
      if (await confirm.isVisible().catch(() => false)) await confirm.click()
      await expect(sw1).toHaveAttribute('aria-checked', 'false', { timeout: 10_000 })

      // Tab 2 must reflect it without any action of its own.
      const sw2 = await switchOf(tab2)
      await expect(sw2).toHaveAttribute('aria-checked', 'false', { timeout: 10_000 })
    } finally {
      await context.close()
    }
  })
})
