import { test, expect, type Page } from '@playwright/test'
import { ApiHelper, loginAsAdmin } from '../../helpers'
import { createTestScope, createUserWithPermissions, loginAs, SUPER_ADMIN, type TestUserHandle } from '../../framework'

const scope = createTestScope('units-departments')

test.describe('Settings → General: Units and Departments', () => {
  test('tabs read General | Notifications | Calling | Units | Departments', async ({ page }) => {
    await loginAsAdmin(page)
    await page.goto('/settings')
    await expect(page.getByRole('tab')).toHaveText(['General', 'Notifications', 'Calling', 'Units', 'Departments'])
  })

  test('Occurrences no longer offers Units', async ({ page }) => {
    await loginAsAdmin(page)
    await page.goto('/settings/occurrences')
    await expect(page.getByRole('tab', { name: /Stages/ })).toBeVisible()
    await expect(page.getByRole('tab', { name: 'Units' })).toHaveCount(0)
  })

  test('/settings/units and /settings/departments open their tab', async ({ page }) => {
    await loginAsAdmin(page)
    await page.goto('/settings/units')
    await expect(page).toHaveURL(/\/settings\?tab=units/)
    await expect(page.getByRole('tab', { name: 'Units' })).toHaveAttribute('data-state', 'active')
    await page.goto('/settings/departments')
    await expect(page).toHaveURL(/\/settings\?tab=departments/)
    await expect(page.getByRole('tab', { name: 'Departments' })).toHaveAttribute('data-state', 'active')
  })
})

test.describe('Departments tab: create, edit, deactivate, delete', () => {
  test('full lifecycle through the UI', async ({ page }) => {
    const name = scope.name('crud')
    const renamed = `${name}-renamed`
    await loginAsAdmin(page)
    await page.goto('/settings?tab=departments')

    await page.getByTestId('department-add').click()
    await page.getByTestId('department-name').fill(name)
    await page.getByRole('dialog').getByRole('button', { name: 'Create' }).click()
    const row = (n: string) => page.getByRole('row', { name: new RegExp(n) })
    await expect(row(name)).toBeVisible()
    await expect(row(name)).toContainText('Yes')

    await row(name).getByRole('button', { name: 'Edit department' }).click()
    await page.getByTestId('department-name').fill(renamed)
    await page.getByRole('dialog').getByRole('switch').click() // active -> inactive
    await page.getByRole('dialog').getByRole('button', { name: /Save|Update/ }).click()
    await expect(row(renamed)).toBeVisible()
    await expect(row(renamed)).toContainText('No')

    await row(renamed).getByRole('button', { name: 'Delete department' }).click()
    await page.getByRole('alertdialog').getByRole('button', { name: /Delete/ }).click()
    await expect(row(renamed)).toHaveCount(0)
  })
})

test.describe('Colaborador: unit and department pickers', () => {
  let api: ApiHelper
  const created: { units: string[]; departments: string[]; contacts: string[] } = { units: [], departments: [], contacts: [] }
  const names = {
    unitActive: scope.name('unit-active'),
    unitInactive: scope.name('unit-inactive'),
    unitLinkedInactive: scope.name('unit-linked-inactive'),
    deptActive: scope.name('dept-active'),
    deptInactive: scope.name('dept-inactive'),
  }
  let contactId = ''
  let linkedContactId = ''
  let deptActiveId = ''
  let unitActiveId = ''
  let unitLinkedId = ''

  test.beforeAll(async ({ request }) => {
    api = new ApiHelper(request)
    await api.loginAsAdmin()
    const mk = async (path: string, body: any) => (await (await api.post(path, body)).json()).data
    unitActiveId = (await mk('/api/units', { name: names.unitActive, active: true })).id
    const unitInactive = await mk('/api/units', { name: names.unitInactive, active: false })
    unitLinkedId = (await mk('/api/units', { name: names.unitLinkedInactive, active: true })).id
    deptActiveId = (await mk('/api/departments', { name: names.deptActive, active: true })).id
    const deptInactive = await mk('/api/departments', { name: names.deptInactive, active: false })
    created.units.push(unitActiveId, unitInactive.id, unitLinkedId)
    created.departments.push(deptActiveId, deptInactive.id)

    const c1 = await mk('/api/contacts', { phone_number: scope.phone(), profile_name: scope.name('colab'), contact_type: 'colaborador' })
    contactId = c1.id
    const c2 = await mk('/api/contacts', { phone_number: scope.phone(), profile_name: scope.name('colab-linked'), contact_type: 'colaborador', unit_id: unitLinkedId })
    linkedContactId = c2.id
    created.contacts.push(contactId, linkedContactId)
    // The unit becomes inactive AFTER being linked to c2.
    await api.put(`/api/units/${unitLinkedId}`, { name: names.unitLinkedInactive, active: false })
  })

  test.afterAll(async ({ playwright }) => {
    const api = new ApiHelper(await playwright.request.newContext())
    await api.loginAsAdmin()
    for (const id of created.contacts) await api.del(`/api/contacts/${id}`).catch(() => {})
    for (const id of created.departments) await api.del(`/api/departments/${id}`).catch(() => {})
    for (const id of created.units) await api.del(`/api/units/${id}`).catch(() => {})
  })

  const picker = (page: Page, label: 'Unit' | 'Department') =>
    page.locator('div[class~="space-y-1.5"]', { has: page.getByText(label, { exact: true }) }).getByRole('combobox')

  test('offers active units/departments, hides inactive ones, and persists the department id', async ({ page, playwright }) => {
    const admin = new ApiHelper(await playwright.request.newContext())
    await admin.loginAsAdmin()
    await loginAsAdmin(page)
    await page.goto(`/settings/contacts/${contactId}`)

    await picker(page, 'Unit').click()
    await expect(page.getByRole('option', { name: names.unitActive })).toBeVisible()
    await expect(page.getByRole('option', { name: names.unitInactive })).toHaveCount(0)
    await page.keyboard.press('Escape')

    await picker(page, 'Department').click()
    await expect(page.getByRole('option', { name: names.deptInactive })).toHaveCount(0)
    await page.getByRole('option', { name: names.deptActive }).click()
    await page.getByRole('button', { name: /^Save/ }).first().click()

    await expect.poll(async () => {
      const res = await admin.get(`/api/contacts/${contactId}`)
      return (await res.json()).data.department_id
    }).toBe(deptActiveId)
  })

  test('an inactive unit that is already linked stays listed and selected', async ({ page }) => {
    await loginAsAdmin(page)
    await page.goto(`/settings/contacts/${linkedContactId}`)
    await expect(picker(page, 'Unit')).toContainText(names.unitLinkedInactive)
    await picker(page, 'Unit').click()
    await expect(page.getByRole('option', { name: names.unitLinkedInactive })).toBeVisible()
    await expect(page.getByRole('option', { name: names.unitInactive })).toHaveCount(0)
  })
})

test.describe('Departments tab: permissions', () => {
  let api: ApiHelper
  let readerUser: TestUserHandle
  let writerUser: TestUserHandle

  test.beforeAll(async ({ request }) => {
    api = new ApiHelper(request)
    await api.login(SUPER_ADMIN.email, SUPER_ADMIN.password)
    // occurrences:read is what the list endpoint checks today (see ListDepartments).
    readerUser = await createUserWithPermissions(api, scope, {
      userSlug: 'dept-reader',
      permissions: [
        { resource: 'chat', action: 'read' },
        { resource: 'occurrences', action: 'read' },
        { resource: 'departments', action: 'read' },
      ],
    })
    writerUser = await createUserWithPermissions(api, scope, {
      userSlug: 'dept-writer',
      permissions: [
        { resource: 'chat', action: 'read' },
        { resource: 'occurrences', action: 'read' },
        { resource: 'departments', action: 'read' },
        { resource: 'departments', action: 'write' },
      ],
    })
  })

  test.afterAll(async () => {
    for (const u of [readerUser, writerUser]) {
      await api.deleteUser(u.user.id).catch(() => {})
      await api.deleteRole(u.role.id).catch(() => {})
    }
  })

  test('read-only user sees the list but no create/edit/delete controls, and only the Departments tab', async ({ page }) => {
    await loginAs(page, readerUser)
    await page.goto('/settings')
    await expect(page).toHaveURL(/\/settings\?tab=departments/)
    await expect(page.getByRole('tab')).toHaveCount(1)
    await expect(page.getByTestId('department-add')).toHaveCount(0)
    await expect(page.getByRole('button', { name: 'Edit department' })).toHaveCount(0)
    await expect(page.getByRole('button', { name: 'Delete department' })).toHaveCount(0)
  })

  test('the API refuses write/delete without the permission', async ({ request, playwright }) => {
    const reader = new ApiHelper(request)
    const admin = new ApiHelper(await playwright.request.newContext())
    await admin.login(SUPER_ADMIN.email, SUPER_ADMIN.password)
    await reader.login(readerUser.user.email, readerUser.password)
    expect((await reader.get('/api/departments')).status()).toBe(200)
    expect((await reader.post('/api/departments', { name: scope.name('nope'), active: true })).status()).toBe(403)
    const created = await (await admin.post('/api/departments', { name: scope.name('target'), active: true })).json()
    const id = created.data.id
    expect((await reader.put(`/api/departments/${id}`, { name: 'x', active: true })).status()).toBe(403)
    expect((await reader.del(`/api/departments/${id}`)).status()).toBe(403)
    await admin.del(`/api/departments/${id}`)
  })

  test('writer can create but has no delete control', async ({ page }) => {
    await loginAs(page, writerUser)
    await page.goto('/settings?tab=departments')
    await expect(page.getByTestId('department-add')).toBeVisible()
    await expect(page.getByRole('button', { name: 'Delete department' })).toHaveCount(0)
  })
})
