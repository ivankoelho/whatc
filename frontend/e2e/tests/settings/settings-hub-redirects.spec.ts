import { test, expect } from '@playwright/test'
import { loginAsAdmin } from '../../helpers'

// Every old flat settings route must still redirect to its hub + correct
// tab. Six of these are already covered incidentally by other specs
// (occurrence-stages, occurrence-processes, users, roles, teams,
// canned-responses); this table closes the gap for the rest, and is the
// single place that would catch a typo in either the router's redirect
// target or a hub view's tab `value` — the two are duplicated with
// nothing linking them at compile time.
const redirects: Array<[string, string]> = [
  ['/settings/chatbot', '/settings/service?tab=chatbot'],
  ['/settings/canned-responses', '/settings/service?tab=canned-responses'],
  ['/settings/tags', '/settings/service?tab=tags'],
  ['/settings/contacts', '/settings/service?tab=contacts'],
  ['/settings/teams', '/settings/access?tab=teams'],
  ['/settings/users', '/settings/access?tab=users'],
  ['/settings/roles', '/settings/access?tab=roles'],
  ['/settings/api-keys', '/settings/integrations?tab=api-keys'],
  ['/settings/webhooks', '/settings/integrations?tab=webhooks'],
  ['/settings/custom-actions', '/settings/integrations?tab=custom-actions'],
  ['/settings/occurrence-stages', '/settings/occurrences?tab=stages'],
  ['/settings/units', '/settings/occurrences?tab=units'],
  ['/settings/occurrence-sla-policies', '/settings/occurrences?tab=sla'],
  ['/settings/occurrence-categories', '/settings/occurrences?tab=categories'],
  ['/settings/occurrence-what-happened', '/settings/occurrences?tab=what-happened'],
  ['/settings/occurrence-processes', '/settings/occurrences?tab=processes'],
]

test.describe('Settings hub redirects — every old route lands on its correct tab', () => {
  test.beforeEach(async ({ page }) => {
    await loginAsAdmin(page)
  })

  for (const [oldPath, expected] of redirects) {
    test(`${oldPath} → ${expected}`, async ({ page }) => {
      await page.goto(oldPath)
      await page.waitForLoadState('networkidle')
      expect(page.url()).toContain(expected)
    })
  }
})
