import { test, expect, request as playwrightRequest } from '@playwright/test'
import { Client } from 'pg'
import { loginAsAdmin, ApiHelper } from '../../helpers'
import { ChatPage } from '../../pages'
import { createTestScope } from '../../framework'

const scope = createTestScope('contact-status')

const DB_URL = process.env.TEST_DATABASE_URL || 'postgres://whatomate:whatomate@127.0.0.1:5432/whatomate'

async function execSQL(sql: string): Promise<void> {
  const client = new Client({ connectionString: DB_URL })
  await client.connect()
  try {
    await client.query(sql)
  } finally {
    await client.end()
  }
}

/**
 * Contact Status E2E Tests
 *
 * Covers the sidebar status filter pills and the resolve/reopen action in the
 * chat header. A conversation stays 'new' until an agent replies; resolving it
 * from the header moves it out of the active filter without a reload.
 */
test.describe('Contact Status', () => {
  test.describe.configure({ mode: 'serial' })
  test.setTimeout(60000)

  let contactId: string

  test.beforeAll(async () => {
    const reqContext = await playwrightRequest.newContext()
    const api = new ApiHelper(reqContext)
    await api.loginAsAdmin()

    const contact = await api.createContact(scope.phone(), scope.name('contact'))
    contactId = contact.id

    await reqContext.dispose()
  })

  test('shows the status filter pills in the sidebar', async ({ page }) => {
    await loginAsAdmin(page)
    const chatPage = new ChatPage(page)
    await chatPage.goto()

    const tablist = page.getByRole('tablist')
    await expect(tablist).toBeVisible({ timeout: 5000 })

    await expect(page.getByRole('tab', { name: /^(Todos|All)$/ })).toBeVisible()
    await expect(page.getByRole('tab', { name: /^(Em andamento|In progress)$/ })).toBeVisible()
  })

  test('selecting a pill filters the conversation list', async ({ page }) => {
    await execSQL(`UPDATE contacts SET contact_status = 'in_progress' WHERE id = '${contactId}'`)

    await loginAsAdmin(page)
    const chatPage = new ChatPage(page)
    await chatPage.goto()

    const inProgressTab = page.getByRole('tab', { name: /^(Em andamento|In progress)$/ })
    await inProgressTab.click()
    await expect(inProgressTab).toHaveAttribute('aria-selected', 'true')

    // The contact is in_progress, so it survives the filter
    await expect(page.locator(`[data-testid="conversation-item"]`).first()).toBeVisible()

    // Switching to "Concluído" must drop it
    await page.getByRole('tab', { name: /^(Concluído|Resolved)$/ }).click()
    await expect(page.locator('[data-testid="conversation-item"]')).toHaveCount(0)
  })

  test('resolves and reopens a conversation from the header', async ({ page }) => {
    await execSQL(`UPDATE contacts SET contact_status = 'in_progress' WHERE id = '${contactId}'`)

    await loginAsAdmin(page)
    const chatPage = new ChatPage(page)
    await chatPage.goto(contactId)

    const resolveButton = page.getByRole('button', { name: /Concluir atendimento|Resolve conversation/ })
    await expect(resolveButton).toBeVisible({ timeout: 5000 })
    await resolveButton.click()

    // The button flips to the reopen action once the change lands
    const reopenButton = page.getByRole('button', { name: /Reabrir atendimento|Reopen conversation/ })
    await expect(reopenButton).toBeVisible({ timeout: 5000 })

    await reopenButton.click()
    await expect(resolveButton).toBeVisible({ timeout: 5000 })
  })
})
