import { test, expect, request as playwrightRequest } from '@playwright/test'
import { randomInt } from 'node:crypto'
import { Client } from 'pg'
import { loginAsAdmin, ApiHelper } from '../../helpers'
import { createTestScope } from '../../framework'

// Two of these tests (drag-block, loss-reason) need a real SalesOpportunity
// in a specific stage assigned to the logged-in test admin. There is no
// creation endpoint for this scenario — opportunities are only created by
// the chatbot flow (Task 5) — so we seed directly via SQL, mirroring
// queue-pickup.spec.ts's execSQL pattern for the same "no REST endpoint
// exists for this fixture" situation.
const scope = createTestScope('sales-funnel')

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

// Format per internal/models/sales_opportunity.go: OPP-YYYYMMDD-NNNNNN
// (size:20). Doesn't need to go through the real per-org daily counter —
// any unique string in that shape is fine for a test fixture.
function randomOpportunityNumber(): string {
  const now = new Date()
  const day = `${now.getFullYear()}${String(now.getMonth() + 1).padStart(2, '0')}${String(now.getDate()).padStart(2, '0')}`
  const seq = randomInt(0, 999_999).toString().padStart(6, '0')
  return `OPP-${day}-${seq}`
}

async function seedOpportunity(params: {
  orgId: string
  contactId: string
  assignedUserId: string
  stage: 'abrir_orcamento' | 'direcionada'
  direcionamento?: 'visita' | 'whatsapp'
}): Promise<{ id: string; opportunityNumber: string }> {
  const opportunityNumber = randomOpportunityNumber()
  const direcionamentoValue = params.direcionamento ? `'${params.direcionamento}'` : 'NULL'
  const rows = await execSQL(`
    INSERT INTO sales_opportunities (
      id, organization_id, opportunity_number, contact_id, assigned_user_id,
      stage, status, direcionamento, stage_changed_at, opened_at, created_at, updated_at
    )
    VALUES (
      gen_random_uuid(), '${params.orgId}', '${opportunityNumber}', '${params.contactId}', '${params.assignedUserId}',
      '${params.stage}', 'aberta', ${direcionamentoValue}, NOW(), NOW(), NOW(), NOW()
    )
    RETURNING id::text AS id
  `)
  return { id: rows[0]!.id as string, opportunityNumber }
}

// Fresh request context: the beforeAll-bound `api` can't be reused inside
// test bodies (Playwright disposes the `request` fixture as soon as
// beforeAll returns). Mirrors queue-pickup.spec.ts's seedContactAndQueue.
async function createContactFresh(phone: string, name: string): Promise<{ id: string }> {
  const ctx = await playwrightRequest.newContext()
  const localApi = new ApiHelper(ctx)
  try {
    await localApi.loginAsAdmin()
    return await localApi.createContact(phone, name)
  } finally {
    await ctx.dispose()
  }
}

// Navigates to /sales/operation and waits for the opportunities list GET to
// resolve, mirroring queue-pickup.spec.ts's gotoTransfersAndWaitLoad —
// networkidle alone races against the board's lazy-loaded data fetch.
async function gotoOperationAndWaitLoad(page: import('@playwright/test').Page): Promise<void> {
  const listLoaded = page.waitForResponse(
    r => r.url().includes('/sales-opportunities') && r.request().method() === 'GET' && r.ok(),
    { timeout: 15_000 },
  )
  await page.goto('/sales/operation')
  await listLoaded
}

test.describe('Central de Vendas', () => {
  let api: ApiHelper
  let adminUserId: string
  let orgId: string

  test.beforeAll(async ({ request }) => {
    api = new ApiHelper(request)
    await api.loginAsAdmin()

    const rows = await execSQL(
      `SELECT u.id::text AS id, uo.organization_id::text AS org FROM users u
       JOIN user_organizations uo ON uo.user_id = u.id AND uo.is_default = true
       WHERE u.email = 'admin@test.com' LIMIT 1`,
    )
    adminUserId = rows[0]!.id as string
    orgId = rows[0]!.org as string
  })

  test('opportunity board renders the three funnel stage columns', async ({ page }) => {
    await loginAsAdmin(page)

    // Creation is triggered by the chatbot flow (Task 5), which this e2e
    // suite does not drive end-to-end through WhatsApp webhooks — instead
    // it exercises the board UI directly, consistent with how
    // OccurrencesPage.ts already drives its dialog flow directly rather
    // than simulating an inbound WhatsApp message. The board always
    // renders its three stage columns regardless of data (SalesOpportunityBoard.vue
    // maps STAGES to columns unconditionally), so this doesn't depend on
    // any opportunity actually existing in this account's data.
    await page.goto('/sales/operation')
    await page.waitForLoadState('networkidle')

    await expect(page.locator('[data-board-column="potencial"]')).toBeVisible()
    await expect(page.locator('[data-board-column="abrir_orcamento"]')).toBeVisible()
    await expect(page.locator('[data-board-column="direcionada"]')).toBeVisible()
  })

  test('dragging into Direcionada without direcionamento is blocked', async ({ page }) => {
    const contact = await createContactFresh(scope.phone(), scope.name('drag-block'))
    const { opportunityNumber } = await seedOpportunity({
      orgId,
      contactId: contact.id,
      assignedUserId: adminUserId,
      stage: 'abrir_orcamento',
    })

    await loginAsAdmin(page)
    await gotoOperationAndWaitLoad(page)

    // Drag the seeded card from "Abrir orçamento" (a valid source stage)
    // into "Direcionada". The backend's Task 7 guard must reject it since
    // direcionamento is null, surfacing a toast and leaving the card put.
    const sourceColumn = page.locator('[data-board-column="abrir_orcamento"]')
    const card = sourceColumn.locator('[data-board-card]').filter({ hasText: opportunityNumber })
    await expect(card).toBeVisible({ timeout: 10_000 })

    const direcionadaDropzone = page.locator('[data-board-column="direcionada"] [data-board-dropzone]')
    await card.dragTo(direcionadaDropzone)

    const toast = page.locator('[data-sonner-toast]')
    await expect(toast).toBeVisible({ timeout: 5000 })

    // The failed drag must actually revert: the card stays in its original
    // column and never lands in Direcionada.
    await expect(sourceColumn.locator('[data-board-card]').filter({ hasText: opportunityNumber })).toBeVisible()
    await expect(
      page.locator('[data-board-column="direcionada"] [data-board-card]').filter({ hasText: opportunityNumber }),
    ).toHaveCount(0)
  })

  test('marking lost requires a loss reason', async ({ page }) => {
    const contact = await createContactFresh(scope.phone(), scope.name('loss-reason'))
    const { opportunityNumber } = await seedOpportunity({
      orgId,
      contactId: contact.id,
      assignedUserId: adminUserId,
      stage: 'direcionada',
      direcionamento: 'whatsapp',
    })

    await loginAsAdmin(page)
    await gotoOperationAndWaitLoad(page)

    // The lose button only renders on cards in "Direcionada"
    // (SalesOpportunityCard.vue's v-if="opportunity.stage === 'direcionada'").
    const card = page.locator('[data-board-column="direcionada"] [data-board-card]').filter({ hasText: opportunityNumber })
    await expect(card).toBeVisible({ timeout: 10_000 })

    const loseButton = card.locator('[data-testid="sales-opportunity-lose-button"]')
    await expect(loseButton).toBeVisible()
    await loseButton.click()

    const dialog = page.getByRole('dialog')
    await expect(dialog).toBeVisible()
    const submit = dialog.getByRole('button', { name: /marcar/i })
    await expect(submit).toBeDisabled()
  })

  test('navigates to sales dashboard from nav', async ({ page }) => {
    await loginAsAdmin(page)
    await page.goto('/')
    await page.waitForLoadState('networkidle')

    await page.locator('a[href="/sales/dashboard"]').first().click()
    await expect(page).toHaveURL(/\/sales\/dashboard/)
  })
})
