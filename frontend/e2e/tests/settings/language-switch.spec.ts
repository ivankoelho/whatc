import { test, expect } from '@playwright/test'
import { loginAsAdmin } from '../../helpers'

test.describe('Language Switching', () => {
  test.beforeEach(async ({ page }) => {
    await loginAsAdmin(page)
    // Seed English as the starting locale. pt-BR is the app default, so
    // without this the UI would load in Portuguese and the "switch from
    // English" flows below would have no English baseline.
    await page.evaluate(() => localStorage.setItem('locale', 'en'))
  })

  test.afterEach(async ({ page }) => {
    // Reset to English after each test
    await page.evaluate(() => {
      localStorage.setItem('locale', 'en')
    })
  })

  test.describe('Settings Page Language Selector', () => {
    test('should display language selector on settings page', async ({ page }) => {
      await page.goto('/settings')
      await page.waitForLoadState('networkidle')
      await expect(page.getByText('Language', { exact: true })).toBeVisible()
    })

    test('should show available languages in dropdown', async ({ page }) => {
      await page.goto('/settings')
      await page.waitForLoadState('networkidle')

      // Find the language switcher select (the one with Globe icon nearby)
      const languageSelect = page.locator('button[role="combobox"]').filter({ hasText: /English/ })
      await languageSelect.click()

      // Only English and Portuguese (Brazil) should be selectable.
      await expect(page.locator('[role="option"]').filter({ hasText: 'Português (Brasil)' })).toBeVisible()
      await expect(page.locator('[role="option"]')).toHaveCount(2)
      await page.keyboard.press('Escape')
    })

    test('should switch to Portuguese and update UI text', async ({ page }) => {
      await page.goto('/settings')
      await page.waitForLoadState('networkidle')

      // Open language dropdown and select Portuguese
      const languageSelect = page.locator('button[role="combobox"]').filter({ hasText: /English/ })
      await languageSelect.click()
      await page.locator('[role="option"]').filter({ hasText: 'Português (Brasil)' }).click()

      // Settings page headings should update to Portuguese
      await expect(page.getByText('Configurações gerais')).toBeVisible()
    })

    test('should switch back to English', async ({ page }) => {
      await page.goto('/settings')
      await page.waitForLoadState('networkidle')

      // Switch to Portuguese first
      const languageSelect = page.locator('button[role="combobox"]').filter({ hasText: /English/ })
      await languageSelect.click()
      await page.locator('[role="option"]').filter({ hasText: 'Português (Brasil)' }).click()
      await expect(page.getByText('Configurações gerais')).toBeVisible()

      // Switch back to English
      const portugueseSelect = page.locator('button[role="combobox"]').filter({ hasText: /Português/ })
      await portugueseSelect.click()
      await page.locator('[role="option"]').filter({ hasText: 'English' }).click()
      await expect(page.getByText('General Settings')).toBeVisible()
    })
  })

  test.describe('Persistence', () => {
    test('should persist language preference across page reload', async ({ page }) => {
      await page.goto('/settings')
      await page.waitForLoadState('networkidle')

      // Switch to Portuguese
      const languageSelect = page.locator('button[role="combobox"]').filter({ hasText: /English/ })
      await languageSelect.click()
      await page.locator('[role="option"]').filter({ hasText: 'Português (Brasil)' }).click()
      await expect(page.getByText('Configurações gerais')).toBeVisible()

      // Verify localStorage was set
      const savedLocale = await page.evaluate(() => localStorage.getItem('locale'))
      expect(savedLocale).toBe('pt-BR')

      // Reload page
      await page.reload()
      await page.waitForLoadState('networkidle')

      // Should still be in Portuguese
      await expect(page.getByText('Configurações gerais')).toBeVisible()
    })
  })

  test.describe('User Menu Language Switcher', () => {
    test('should display language switcher in user menu', async ({ page }) => {
      await page.goto('/settings')
      await page.waitForLoadState('networkidle')

      // Open user menu popover (in sidebar)
      const userMenuButton = page.locator('aside').getByRole('button').filter({ hasText: /@/ }).first()
      await userMenuButton.click()

      // The popover is portaled by Radix outside aside
      const popoverContent = page.locator('[data-state="open"][data-side]')
      await expect(popoverContent.getByText('Language', { exact: true })).toBeVisible()
    })

    test('should switch language from user menu', async ({ page }) => {
      await page.goto('/settings')
      await page.waitForLoadState('networkidle')

      // Open user menu
      const userMenuButton = page.locator('aside').getByRole('button').filter({ hasText: /@/ }).first()
      await userMenuButton.click()

      // The language switcher is in the popover that appears in the sidebar area
      // Find the combobox within the popover content
      const popoverContent = page.locator('[data-state="open"][data-side]')
      const languageSelect = popoverContent.locator('button[role="combobox"]')
      await languageSelect.click()

      // Select Portuguese
      await page.locator('[role="option"]').filter({ hasText: 'Português (Brasil)' }).click()

      // Close user menu by pressing Escape
      await page.keyboard.press('Escape')

      // Navigate to settings to verify the switch persisted
      await page.goto('/settings')
      await page.waitForLoadState('networkidle')
      await expect(page.getByText('Configurações gerais')).toBeVisible()
    })
  })

  test.describe('Navigation Labels', () => {
    test('should update sidebar navigation when language changes', async ({ page }) => {
      await page.goto('/settings')
      await page.waitForLoadState('networkidle')

      // Switch to Portuguese via settings page
      const languageSelect = page.locator('button[role="combobox"]').filter({ hasText: /English/ })
      await languageSelect.click()
      await page.locator('[role="option"]').filter({ hasText: 'Português (Brasil)' }).click()

      // Sidebar nav items should be in Portuguese
      const sidebar = page.locator('aside')
      await expect(sidebar.getByText('Painel')).toBeVisible() // Dashboard -> Painel
      await expect(sidebar.getByText('Configurações')).toBeVisible() // Settings -> Configurações
    })
  })
})
