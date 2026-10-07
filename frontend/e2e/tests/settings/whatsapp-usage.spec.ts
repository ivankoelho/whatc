import { test, expect, type Page } from '@playwright/test'
import { Client } from 'pg'
import { ApiHelper, loginAsAdmin } from '../../helpers'
import {
  createTestScope,
  createUserWithPermissions,
  loginAs,
  SUPER_ADMIN,
  type TestUserHandle,
} from '../../framework'

// WhatsApp consumption (whatsapp_usage:read|write). The ledger rows are seeded straight into the
// database the suite runs against (the messages and Meta webhooks that would produce them need a
// real WhatsApp account); everything else goes through the screen.
//
// DB_URL is the same variable global-setup uses. It must point at a THROWAWAY database: never at
// a development or production one.
const DB_URL = process.env.TEST_DATABASE_URL || 'postgres://whatomate:whatomate@127.0.0.1:5432/whatomate'

const scope = createTestScope('wausage')
// A category and a country no other spec or price table uses.
const CATEGORY = 'tmp' + scope.prefix.replace(/[^a-z]/gi, '').toLowerCase().slice(-12)
const COUNTRY = '998'
const WAMID_PREFIX = scope.prefix + '-w'

async function db<T>(fn: (c: Client) => Promise<T>): Promise<T> {
  const c = new Client({ connectionString: DB_URL })
  await c.connect()
  try {
    return await fn(c)
  } finally {
    await c.end()
  }
}

async function orgIdOf(c: Client, email: string): Promise<string> {
  const r = await c.query('SELECT organization_id FROM users WHERE email = $1', [email])
  return r.rows[0].organization_id
}

async function seedUsage(c: Client, org: string, rows: Array<{ wamid: string; state: string; cost?: number; currency?: string; category?: string }>) {
  for (const r of rows) {
    await c.query(
      `INSERT INTO message_usage (id, organization_id, whatsapp_account, wamid, direction, recipient_country, actor_type, unit_source,
         quantity, billing_category, billable, estimated_cost, estimated_currency, billing_state, link_state, sent_at, created_at, updated_at)
       VALUES (gen_random_uuid(), $1, 'e2e', $2, 'outgoing', $3, 'agent', 'none', 1, $4, $5, $6, $7, $8, 'linked', now() - interval '1 hour', now(), now())`,
      [org, r.wamid, COUNTRY, r.category ?? '', r.state === 'priced' ? true : null, r.cost ?? null, r.currency ?? '', r.state],
    )
  }
}

async function cleanup() {
  await db(async (c) => {
    await c.query(`DELETE FROM message_pricing_events WHERE wamid LIKE $1`, [WAMID_PREFIX + '%'])
    await c.query(`DELETE FROM message_usage WHERE wamid LIKE $1`, [WAMID_PREFIX + '%'])
    await c.query(`DELETE FROM whatsapp_rates WHERE country = $1 AND category = $2`, [COUNTRY, CATEGORY])
  })
}

async function openConsumption(page: Page) {
  await page.goto('/settings/whatsapp-usage')
  await page.waitForLoadState('networkidle')
}

test.describe.configure({ mode: 'serial' })

test.describe('WhatsApp consumption', () => {
  let org: string

  test.beforeAll(async () => {
    await cleanup()
    org = await db((c) => orgIdOf(c, 'admin@test.com'))
  })

  test.afterAll(async () => {
    await cleanup()
  })

  test('the admin reaches the screen from Settings and sees both tabs', async ({ page }) => {
    await loginAsAdmin(page)
    await page.goto('/settings')
    await page.waitForLoadState('networkidle')
    await openConsumption(page)
    await expect(page.getByRole('tab').first()).toBeVisible()
    expect(await page.getByRole('tab').count()).toBe(2)
  })

  test('registers a price in the price table, and refuses an overlapping period', async ({ page }) => {
    await loginAsAdmin(page)
    await page.goto('/settings/whatsapp-usage?tab=rates')
    await page.waitForLoadState('networkidle')

    const add = async (from: string, to: string) => {
      await page.getByTestId('rate-add').click()
      await page.getByTestId('rate-country').fill(COUNTRY)
      await page.getByTestId('rate-category').fill(CATEGORY)
      await page.getByTestId('rate-price').fill('0.2')
      await page.getByTestId('rate-currency').fill('BRL')
      await page.getByTestId('rate-valid-from').fill(from)
      if (to) await page.getByTestId('rate-valid-to').fill(to)
      await page.getByRole('button', { name: /^Create$/ }).click()
    }

    await add('2026-01-01', '2026-12-31')
    await expect(page.getByRole('cell', { name: CATEGORY })).toBeVisible()

    await add('2026-06-01', '')
    // the API refuses the overlap; the dialog stays open with the reason as a toast
    await expect(page.getByRole('dialog')).toBeVisible()
    await expect(page.getByText(/overlaps/i).first()).toBeVisible()
    await page.keyboard.press('Escape')
  })

  test('shows the consumption per currency, and reprices a message that was waiting for a price', async ({ page }) => {
    await db(async (c) => {
      await seedUsage(c, org, [
        { wamid: WAMID_PREFIX + '-a', state: 'priced', cost: 0.0068, currency: 'BRL', category: 'utility' },
        { wamid: WAMID_PREFIX + '-b', state: 'priced', cost: 0.0068, currency: 'BRL', category: 'utility' },
        { wamid: WAMID_PREFIX + '-c', state: 'priced', cost: 0.05, currency: 'USD', category: 'marketing' },
        { wamid: WAMID_PREFIX + '-d', state: 'no_rate', category: CATEGORY },
      ])
      await c.query(
        `INSERT INTO message_pricing_events (id, organization_id, whatsapp_account, wamid, status, event_at, received_at, has_pricing, pricing)
         VALUES (gen_random_uuid(), $1, 'e2e', $2, 'delivered', now(), now(), true, $3::jsonb)`,
        [org, WAMID_PREFIX + '-d', JSON.stringify({ billable: true, pricing_model: 'PMP', category: CATEGORY })],
      )
    })

    await loginAsAdmin(page)
    await openConsumption(page)

    // each currency on its own: the two BRL messages add up, the USD one stays apart
    await expect(page.getByTestId('usage-cost-BRL')).toContainText(/0[.,]0136/)
    await expect(page.getByTestId('usage-cost-USD')).toContainText(/0[.,]05/)
    await expect(page.getByTestId('usage-card-no-rate')).toContainText('1')
    await expect(page.getByTestId('usage-recording-since')).toBeVisible()

    // the price registered in the previous test reaches the waiting message when repricing
    await page.goto('/settings/whatsapp-usage?tab=rates')
    await page.waitForLoadState('networkidle')
    await page.getByTestId('rate-reprice').click()
    await expect(page.getByText(/1 of 1 messages recalculated|1 de 1 mensagens recalculadas/)).toBeVisible()

    await openConsumption(page)
    await expect(page.getByTestId('usage-card-no-rate')).toContainText('0')
    await expect(page.getByTestId('usage-cost-BRL')).toContainText(/0[.,]2136/)
  })
})

test.describe('WhatsApp consumption permissions', () => {
  const permScope = createTestScope('wausage-perms')
  let api: ApiHelper
  let reader: TestUserHandle
  let nobody: TestUserHandle

  test.beforeAll(async ({ request }) => {
    api = new ApiHelper(request)
    await api.login(SUPER_ADMIN.email, SUPER_ADMIN.password)
    reader = await createUserWithPermissions(api, permScope, { permissions: [{ resource: 'whatsapp_usage', action: 'read' }] })
    nobody = await createUserWithPermissions(api, permScope, { permissions: [{ resource: 'chat', action: 'read' }] })
  })

  test.afterAll(async () => {
    for (const h of [reader, nobody]) {
      await api.deleteUser(h.user.id).catch(() => {})
      await api.deleteRole(h.role.id).catch(() => {})
    }
  })

  test('a reader sees the consumption and the price table but cannot edit prices', async ({ page }) => {
    await loginAs(page, reader)
    await page.goto('/settings/whatsapp-usage?tab=rates')
    await page.waitForLoadState('networkidle')
    await expect(page.getByRole('tab').first()).toBeVisible()
    await expect(page.getByTestId('rate-add')).toHaveCount(0)
    await expect(page.getByTestId('rate-reprice')).toHaveCount(0)
  })

  test('a user without the permission is sent away from the screen', async ({ page }) => {
    await loginAs(page, nobody)
    await page.goto('/settings/whatsapp-usage')
    await page.waitForLoadState('networkidle')
    expect(page.url()).not.toContain('/settings/whatsapp-usage')
  })
})
