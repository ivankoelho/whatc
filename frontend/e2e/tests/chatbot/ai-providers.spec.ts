import { test, expect, request as playwrightRequest } from '@playwright/test'
import { ApiHelper } from '../../helpers'
import { loginAsAdmin } from '../../helpers'
import { SUPER_ADMIN } from '../../framework'
import { ChatbotSettingsPage } from '../../pages'

/**
 * Phase 7: AI provider foundation. Groq is a fourth provider whose models are
 * never hardcoded: they come from the provider's own list. The real provider is
 * not reachable from E2E, so the model-list call is answered by a route mock;
 * what is verified here is the UI contract and that the key is never shown.
 */

const KEY = `sk-e2e-${Date.now()}-DO-NOT-LEAK`

test.describe.configure({ mode: 'serial' })

test.afterAll(async () => {
  // Leave AI disabled for the other specs that share this organization.
  const ctx = await playwrightRequest.newContext()
  const api = new ApiHelper(ctx)
  await api.login(SUPER_ADMIN.email, SUPER_ADMIN.password)
  await api.put('/api/chatbot/settings', { ai_enabled: false, ai_provider: '', ai_model: '' })
  await ctx.dispose()
})

test('Groq is offered, has no built-in models, loads them from the provider and never shows the key', async ({ page }) => {
  await loginAsAdmin(page)
  const settingsPage = new ChatbotSettingsPage(page)
  await settingsPage.goto()
  await settingsPage.switchToAITab()

  const toggle = page.locator('button[role="switch"]').first()
  if ((await toggle.getAttribute('data-state')) === 'unchecked') await toggle.click()

  // The provider list has Groq.
  await page.locator('label').filter({ hasText: /^AI Provider$/ }).locator('..').getByRole('combobox').click()
  await page.getByRole('option', { name: 'Groq' }).click()

  // Load models is disabled without a key; no static list exists for Groq.
  const load = page.getByTestId('load-ai-models')
  await expect(load).toBeDisabled()
  await expect(page.getByText(/Load models/i).first()).toBeVisible()

  // Typing a key enables it; the provider list is requested with that key.
  await page.locator('input[type="password"]').fill(KEY)
  await expect(load).toBeEnabled()

  let modelsRequest: Record<string, unknown> | null = null
  await page.route('**/api/chatbot/ai/models', async (route) => {
    modelsRequest = route.request().postDataJSON()
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ status: 'success', data: { provider: 'groq', models: [{ id: 'e2e-model-b' }, { id: 'e2e-model-a' }] } }),
    })
  })
  await load.click()
  await expect.poll(() => modelsRequest).not.toBeNull()
  expect(modelsRequest).toMatchObject({ provider: 'groq', api_key: KEY })

  // The model picker now offers exactly what the provider returned.
  await page.locator('label').filter({ hasText: /^Model$/ }).locator('../..').getByRole('combobox').click()
  await expect(page.getByRole('option', { name: 'e2e-model-a' })).toBeVisible()
  await expect(page.getByRole('option', { name: 'e2e-model-b' })).toBeVisible()
  await page.getByRole('option', { name: 'e2e-model-a' }).click()

  // Save: the payload carries the typed key once; afterwards the UI only says "saved".
  const saved = page.waitForRequest((r) => r.url().includes('/api/chatbot/settings') && r.method() === 'PUT')
  await page.getByRole('button', { name: /save changes/i }).last().click()
  const putBody = (await saved).postDataJSON()
  expect(putBody).toMatchObject({ ai_provider: 'groq', ai_model: 'e2e-model-a', ai_api_key: KEY })
  await expect(page.getByTestId('ai-key-saved')).toBeVisible({ timeout: 10_000 })
  await expect(page.locator('input[type="password"]')).toHaveValue('')

  // Reload: the settings response says a key is configured and never contains it.
  const getResp = page.waitForResponse((r) => r.url().includes('/api/chatbot/settings') && r.request().method() === 'GET')
  await page.reload()
  const body = await (await getResp).text()
  expect(body).not.toContain(KEY)
  expect(body).toContain('"ai_api_key_configured":true')
  await settingsPage.switchToAITab()
  await expect(page.getByTestId('ai-key-saved')).toBeVisible()
  await expect(page.locator('body')).not.toContainText(KEY)
})

test('a server without app.encryption_key says so, keeps the typed key hidden and does not mark it saved', async ({ page }) => {
  const NEW_KEY = `sk-e2e-new-${Date.now()}-DO-NOT-LEAK`
  await loginAsAdmin(page)
  const settingsPage = new ChatbotSettingsPage(page)
  await settingsPage.goto()
  await settingsPage.switchToAITab()

  const toggle = page.locator('button[role="switch"]').first()
  if ((await toggle.getAttribute('data-state')) === 'unchecked') await toggle.click()

  await page.route('**/api/chatbot/settings', async (route) => {
    if (route.request().method() !== 'PUT') return route.fallback()
    await route.fulfill({
      status: 503,
      contentType: 'application/json',
      body: JSON.stringify({ status: 'error', message: 'AI API keys cannot be stored', error_type: 'AIEncryptionKeyUnavailable' }),
    })
  })

  await page.locator('input[type="password"]').fill(NEW_KEY)
  await page.getByRole('button', { name: /save changes/i }).last().click()

  await expect(page.getByText(/app\.encryption_key/i).first()).toBeVisible({ timeout: 10_000 })
  await expect(page.locator('body')).not.toContainText(NEW_KEY)
  await expect(page.locator('input[type="password"]')).toHaveAttribute('type', 'password')
})
