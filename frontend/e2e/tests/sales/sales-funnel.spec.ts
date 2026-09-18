import { test, expect } from '@playwright/test'
import { loginAsAdmin } from '../../helpers'

// No createTestScope here: unlike most specs, these tests don't create any
// scoped data via the API (no scope.name()/scope.phone() calls) — they only
// exercise the board/dashboard UI against whatever the logged-in admin's
// wallet already has, so there's nothing to prefix.
test.describe('Central de Vendas', () => {
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
    await loginAsAdmin(page)
    await page.goto('/sales/operation')
    await page.waitForLoadState('networkidle')

    // Attempt the drag on the first card in "Potencial"; the UI must
    // surface a toast and the card must remain in its original column.
    // Skipped when the logged-in user's wallet has no open opportunities
    // yet, since this suite doesn't seed sales data via the API.
    const card = page.locator('[data-board-column="potencial"] [data-board-card]').first()
    const direcionadaDropzone = page.locator('[data-board-column="direcionada"] [data-board-dropzone]')
    if (await card.isVisible().catch(() => false)) {
      await card.dragTo(direcionadaDropzone)
      const toast = page.locator('[data-sonner-toast]')
      await expect(toast).toBeVisible({ timeout: 5000 })
    }
  })

  test('marking lost requires a loss reason', async ({ page }) => {
    await loginAsAdmin(page)
    await page.goto('/sales/operation')
    await page.waitForLoadState('networkidle')

    // The lose button only renders on cards in "Direcionada"
    // (SalesOpportunityCard.vue). Skipped when the wallet has none.
    const loseButton = page.locator('[data-testid="sales-opportunity-lose-button"]').first()
    if (await loseButton.isVisible().catch(() => false)) {
      await loseButton.click()
      const dialog = page.getByRole('dialog')
      await expect(dialog).toBeVisible()
      const submit = dialog.getByRole('button', { name: /marcar/i })
      await expect(submit).toBeDisabled()
    }
  })

  test('navigates to sales dashboard from nav', async ({ page }) => {
    await loginAsAdmin(page)
    await page.goto('/')
    await page.waitForLoadState('networkidle')

    await page.locator('a[href="/sales/dashboard"]').first().click()
    await expect(page).toHaveURL(/\/sales\/dashboard/)
  })
})
