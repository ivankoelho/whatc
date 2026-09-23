import { test, expect } from '@playwright/test'
import { ApiHelper, loginAsAdmin } from '../../helpers'
import { createTestScope, createUserWithPermissions, loginAs, SUPER_ADMIN, type TestUserHandle } from '../../framework'

test.describe('Settings hubs — tab persistence', () => {
  test('refresh keeps the current tab, and a direct ?tab= link opens straight to it', async ({ page }) => {
    await loginAsAdmin(page)
    await page.goto('/settings/occurrences?tab=categories')
    await expect(page.getByRole('tab', { name: 'Occurrence Categories' })).toHaveAttribute('data-state', 'active')

    await page.reload()
    await expect(page).toHaveURL(/\/settings\/occurrences\?tab=categories/)
    await expect(page.getByRole('tab', { name: 'Occurrence Categories' })).toHaveAttribute('data-state', 'active')
  })

  test('an old direct URL redirects to the hub with the right tab', async ({ page }) => {
    await loginAsAdmin(page)
    await page.goto('/settings/occurrence-processes')
    await expect(page).toHaveURL(/\/settings\/occurrences\?tab=processes/)
  })
})

test.describe('Settings hubs — per-tab authorization', () => {
  const scope = createTestScope('settings-hubs-authz')
  let api: ApiHelper
  let singleTabUser: TestUserHandle
  let noGroupAccessUser: TestUserHandle

  test.beforeAll(async ({ request }) => {
    api = new ApiHelper(request)
    await api.login(SUPER_ADMIN.email, SUPER_ADMIN.password)
    singleTabUser = await createUserWithPermissions(api, scope, {
      userSlug: 'single-tab',
      permissions: [
        { resource: 'chat', action: 'read' },
        { resource: 'occurrences.categories', action: 'read' },
      ],
    })
    noGroupAccessUser = await createUserWithPermissions(api, scope, {
      userSlug: 'no-group-access',
      permissions: [{ resource: 'chat', action: 'read' }],
    })
  })

  test.afterAll(async () => {
    await api.deleteUser(singleTabUser.user.id).catch(() => {})
    await api.deleteRole(singleTabUser.role.id).catch(() => {})
    await api.deleteUser(noGroupAccessUser.user.id).catch(() => {})
    await api.deleteRole(noGroupAccessUser.role.id).catch(() => {})
  })

  test('a user with permission on only one tab lands on it automatically, with only that tab visible', async ({ page }) => {
    await loginAs(page, singleTabUser)
    await page.goto('/settings/occurrences')
    await expect(page.getByRole('tab')).toHaveCount(1)
    await expect(page.getByRole('tab', { name: 'Occurrence Categories' })).toHaveAttribute('data-state', 'active')
  })

  test('that user cannot reach a different tab via ?tab= — falls back to the one they have', async ({ page }) => {
    await loginAs(page, singleTabUser)
    await page.goto('/settings/occurrences?tab=processes')
    await expect(page.getByRole('tab')).toHaveCount(1)
    await expect(page.getByRole('tab', { name: 'Occurrence Categories' })).toHaveAttribute('data-state', 'active')
  })

  test('a user with no permission in the group is redirected away from the hub entirely', async ({ page }) => {
    await loginAs(page, noGroupAccessUser)
    await page.goto('/settings/occurrences')
    await expect(page).not.toHaveURL(/\/settings\/occurrences/)
  })
})
