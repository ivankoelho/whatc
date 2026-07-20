import { test, expect, request as playwrightRequest } from '@playwright/test'
import { loginAsAdmin, loginAsManager, ApiHelper } from '../../helpers'
import { ChatPage } from '../../pages'
import { createTestScope } from '../../framework'

const scope = createTestScope('agent-typing')

/**
 * Agent Typing E2E Tests
 *
 * Two browser contexts share one contact: one agent types, the other must see
 * the indicator appear and then expire on its own after ~3s.
 */
test.describe('Agent Typing', () => {
  test.describe.configure({ mode: 'serial' })
  test.setTimeout(90000)

  let contactId: string

  test.beforeAll(async () => {
    const reqContext = await playwrightRequest.newContext()
    const api = new ApiHelper(reqContext)
    await api.loginAsAdmin()
    const contact = await api.createContact(scope.phone(), scope.name('contact'))
    contactId = contact.id
    await reqContext.dispose()
  })

  test('shows and expires the typing indicator for another agent', async ({ browser }) => {
    const watcherCtx = await browser.newContext()
    const typistCtx = await browser.newContext()

    try {
      const watcher = await watcherCtx.newPage()
      const typist = await typistCtx.newPage()

      // Two distinct agent accounts: applyAgentTyping() on the frontend
      // intentionally drops an agent's own typing event, so watcher and
      // typist must be different users or the indicator never renders.
      await loginAsManager(watcher)
      await loginAsAdmin(typist)

      // Land on the conversation list first (not a direct deep link into the
      // contact) so the WebSocket — opened asynchronously on app mount — has
      // settled before the client sends set_contact. A deep link to
      // /chat/:id fires set_contact from onMounted while the socket may
      // still be CONNECTING; that message is silently dropped (no queueing
      // in wsService.send) and never retried, so the server never learns
      // this session is viewing the contact. Clicking in from the list is
      // also how a real agent normally opens a conversation.
      await new ChatPage(watcher).goto()
      await new ChatPage(typist).goto()

      const contactName = scope.name('contact')
      await watcher.locator('[data-testid="conversation-item"]').filter({ hasText: contactName }).click()
      await typist.locator('[data-testid="conversation-item"]').filter({ hasText: contactName }).click()

      // Give both sockets time to send set_contact and the server to register it
      await watcher.waitForTimeout(1000)

      await typist.locator('textarea').first().fill('escrevendo uma resposta')

      const indicator = watcher.getByText(/está digitando|is typing/)
      await expect(indicator).toBeVisible({ timeout: 5000 })

      // No further events: it must disappear on its own
      await expect(indicator).toBeHidden({ timeout: 8000 })
    } finally {
      await watcherCtx.close()
      await typistCtx.close()
    }
  })

  test('shows the agent name on an outgoing bubble', async ({ page }) => {
    await loginAsAdmin(page)
    const chatPage = new ChatPage(page)
    await chatPage.goto(contactId)

    await page.locator('textarea').first().fill('mensagem do agente')
    await page.keyboard.press('Enter')

    // The bubble carries the sending agent's display name
    await expect(page.getByText('Test Admin').first()).toBeVisible({ timeout: 10000 })
  })
})
