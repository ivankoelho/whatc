import { test, expect, request as playwrightRequest, type Page } from '@playwright/test'
import { Client } from 'pg'
import { loginAsAdmin, TEST_USERS } from '../../helpers'
import { ApiHelper } from '../../helpers/api'
import { createTestScope } from '../../framework'

/**
 * Layout of the chat's columns: conversation list | chat | Notes | Contact Info.
 *
 * These specs assert GEOMETRY and HIT-TESTING, not appearance: where every column sits, whether the chat
 * header is contained in its column, and whether the element that would receive a click at the centre of each
 * control is that control. They exist because a layout can look fine in a screenshot while one column's
 * controls sit on top of another column's (the chat header's Notes button used to cover the Notes panel's
 * close button when the chat column collapsed to zero width).
 *
 * Contract (desktop): the chat column never gets narrower than CHAT_MIN_WIDTH; the Contact Info is the area
 * that yields (it shrinks, then becomes a sheet inside the chat column, under the header); nothing is clipped
 * or covered; the Notes panel is never closed by the adaptation.
 */

const scope = createTestScope('chat-layout')
const DB_URL = process.env.TEST_DATABASE_URL || 'postgres://whatomate:whatomate@127.0.0.1:5432/whatomate'

// ChatView.vue's CHAT_MIN_WIDTH. If a header action is ever added and this spec fails on `headerContained`,
// that constant has to grow with it.
const CHAT_MIN_WIDTH = 480

// Which controls the chat header shows depends on the contact and the user: "assign" needs contacts:write,
// "resume chatbot" needs an active agent transfer, "resolve" needs the status permission, and call buttons and
// custom actions come from the organisation. The geometry rules only mean something at the header's widest, so
// this spec never trusts "the first contact in the database": it seeds its own contact with an active transfer
// and asserts the exact inventory below before measuring anything.
const EXPECTED_HEADER_KINDS = { pin: 1, resolve: 1, notes: 1, info: 1, menu: 1 }
const MIN_HEADER_CONTROLS = 7 // pin, resolve, notes, info, menu + assign + resume chatbot

type ControlKind = 'pin' | 'resolve' | 'notes' | 'info' | 'menu' | 'icon'

interface Rect { x: number; y: number; w: number; right: number; bottom: number }

interface Snapshot {
  viewportW: number
  root: Rect | null
  rootOverflow: number
  list: Rect | null
  chat: Rect | null
  header: (Rect & { clientW: number; scrollW: number }) | null
  headerControls: { label: string; kind: ControlKind; rect: Rect; reachable: boolean; topmost: string }[]
  notes: Rect | null
  notesClose: { rect: Rect; reachable: boolean; topmost: string } | null
  info: Rect | null
  infoClose: { rect: Rect; reachable: boolean; topmost: string } | null
}

// Reads the live DOM only. Nothing is clicked or changed.
async function snapshot(page: Page): Promise<Snapshot> {
  return page.evaluate(() => {
    const rect = (el: Element | null) => {
      if (!el) return null
      const b = el.getBoundingClientRect()
      return { x: b.x, y: b.y, w: b.width, right: b.right, bottom: b.bottom }
    }
    // Is the element on top at the centre of `el` the control itself (or something inside it)?
    const hit = (el: Element) => {
      const b = el.getBoundingClientRect()
      const top = document.elementFromPoint(b.x + b.width / 2, b.y + b.height / 2)
      const onScreen = b.width > 0 && b.right > 0 && b.x < window.innerWidth
      const reachable = onScreen && !!top && (top === el || el.contains(top))
      const owner = top?.closest('#notes-panel') ? 'notes-panel' : top?.closest('[class*="bg-muted/60"]') ? 'contact-info' : 'other'
      return { reachable, topmost: `${top?.tagName.toLowerCase()}${top?.id ? '#' + top.id : ''} in ${owner}` }
    }
    const notesButton = document.querySelector('#notes-button')
    const actions = notesButton?.parentElement ?? null
    const header = (notesButton?.closest('.h-14') as HTMLElement | null) ?? null
    const chat = header?.parentElement ?? null
    const root = chat?.parentElement ?? null
    const list = root?.querySelector(':scope > div.border-r') ?? null
    const notes = document.querySelector('#notes-panel')
    const infoRoot = document.querySelector('#contact-info-name')?.closest('[class*="bg-muted/60"]') ?? null
    // the close button is the last control of the panel's own header row (h-14), wherever the panel sits
    const infoCloseBtn = infoRoot ? Array.from(infoRoot.querySelectorAll('.h-14 button')).pop() ?? null : null
    const notesCloseBtn = notes?.querySelector('button') ?? null

    return {
      viewportW: window.innerWidth,
      root: rect(root),
      rootOverflow: root ? root.scrollWidth - root.clientWidth : 0,
      list: rect(list),
      chat: rect(chat),
      header: header ? { ...rect(header)!, clientW: header.clientWidth, scrollW: header.scrollWidth } : null,
      headerControls: actions
        ? Array.from(actions.children).map((c, i) => {
            const kind: ControlKind =
              c.getAttribute('data-testid') === 'header-pin-toggle' ? 'pin'
              : c.id === 'notes-button' ? 'notes'
              : c.id === 'info-button' ? 'info'
              : c.getAttribute('aria-haspopup') === 'menu' ? 'menu'
              : /resolve|reopen/i.test(c.getAttribute('aria-label') || '') ? 'resolve'
              : 'icon'
            return { label: c.id || c.getAttribute('aria-label') || `#${i}`, kind, rect: rect(c)!, ...hit(c) }
          })
        : [],
      notes: rect(notes),
      notesClose: notesCloseBtn ? { rect: rect(notesCloseBtn)!, ...hit(notesCloseBtn) } : null,
      info: rect(infoRoot),
      infoClose: infoCloseBtn ? { rect: rect(infoCloseBtn)!, ...hit(infoCloseBtn) } : null,
    }
  })
}

const intersects = (a: Rect, b: Rect) => a.x < b.right - 0.5 && a.right > b.x + 0.5 && a.y < b.bottom - 0.5 && a.bottom > b.y + 0.5

/**
 * The header must be at its widest, otherwise the geometry rules below prove less than they claim: all of pin,
 * resolve, notes, info and the options menu exactly once, plus the two icon actions that need an assignable
 * contact with an active transfer (assign and resume chatbot) - at least MIN_HEADER_CONTROLS controls in all.
 * A header that silently lost a control would make every layout assertion easier to pass.
 */
function expectWidestHeader(s: Snapshot, where: string) {
  const kinds = s.headerControls.map((c) => c.kind)
  const count = (k: ControlKind) => kinds.filter((x) => x === k).length
  for (const [k, n] of Object.entries(EXPECTED_HEADER_KINDS)) {
    expect(count(k as ControlKind), `${where}: header must have exactly ${n} "${k}" control (found: ${kinds.join(', ')})`).toBe(n)
  }
  expect(count('icon'), `${where}: header must have the assign and resume-chatbot icon actions (found: ${kinds.join(', ')})`).toBeGreaterThanOrEqual(2)
  expect(s.headerControls.length, `${where}: header controls`).toBeGreaterThanOrEqual(MIN_HEADER_CONTROLS)
}

/** Every rule of the desktop contract, with a message that says which one broke and where. */
function expectSoundDesktopLayout(s: Snapshot, opts: { notes: boolean; info: boolean }, where: string) {
  expect(s.chat, `${where}: chat column present`).not.toBeNull()
  expect(s.header, `${where}: chat header present`).not.toBeNull()
  expectWidestHeader(s, where)
  // 1. the chat keeps a functional width
  expect(s.chat!.w, `${where}: chat column width`).toBeGreaterThanOrEqual(CHAT_MIN_WIDTH - 0.5)
  // 2. the header is contained in its column: nothing spills out of it
  expect(s.header!.scrollW, `${where}: header scrollWidth ${s.header!.scrollW} vs clientWidth ${s.header!.clientW}`).toBeLessThanOrEqual(s.header!.clientW + 1)
  // 3. every header control is the element that receives the click at its centre
  const covered = s.headerControls.filter((c) => !c.reachable)
  expect(covered, `${where}: header controls that are covered or off screen: ${JSON.stringify(covered.map((c) => [c.label, c.topmost]))}`).toHaveLength(0)
  // 4. the row of columns does not overflow its container (nothing is clipped)
  expect(s.rootOverflow, `${where}: layout row overflow`).toBeLessThanOrEqual(1)

  if (opts.notes) {
    expect(s.notes, `${where}: notes panel present`).not.toBeNull()
    expect(s.notesClose?.reachable, `${where}: Notes close button reachable (topmost: ${s.notesClose?.topmost})`).toBe(true)
    expect(s.notes!.right, `${where}: Notes panel inside the layout`).toBeLessThanOrEqual(s.root!.right + 1)
    // the header's controls must not sit over the Notes panel
    for (const c of s.headerControls) expect(intersects(c.rect, s.notes!), `${where}: header control ${c.label} overlaps the Notes panel`).toBe(false)
  }
  if (opts.info) {
    expect(s.info, `${where}: contact info present`).not.toBeNull()
    expect(s.infoClose?.reachable, `${where}: Contact Info close button reachable (topmost: ${s.infoClose?.topmost})`).toBe(true)
    expect(s.info!.right, `${where}: Contact Info inside the layout`).toBeLessThanOrEqual(s.root!.right + 1)
    expect(s.info!.x, `${where}: Contact Info inside the layout`).toBeGreaterThanOrEqual(s.root!.x - 1)
    // the Contact Info never covers the header's controls (as a sheet it starts below the header)
    for (const c of s.headerControls) expect(intersects(c.rect, s.info!), `${where}: header control ${c.label} overlaps the Contact Info`).toBe(false)
    if (opts.notes) expect(intersects(s.info!, s.notes!), `${where}: Contact Info overlaps the Notes panel`).toBe(false)
  }
}

test.describe('Chat layout - columns, geometry and hit-testing', () => {
  test.setTimeout(90000)
  let contactId: string

  // A contact made for this spec (E2E- prefixed, so the next run's global cleanup removes it) with an active
  // agent transfer: that is what makes the header show "assign" and "resume chatbot". The transfer is ASSIGNED
  // to the test admin on purpose: an unassigned one is a row of the queue tab, and other specs
  // (agent-transfers "Queue Tab") count the queue's "General" rows, so a leftover queue row breaks them. It is
  // also deleted when this spec is done.
  test.beforeAll(async () => {
    const ctx = await playwrightRequest.newContext()
    const api = new ApiHelper(ctx)
    await api.loginAsAdmin()
    const phone = scope.phone()
    const contact = await api.createContact(phone, scope.name('layout-contact'))
    contactId = contact.id
    await ctx.dispose()

    const db = new Client({ connectionString: DB_URL })
    await db.connect()
    try {
      const res = await db.query(
        `INSERT INTO agent_transfers (id, organization_id, contact_id, whats_app_account, phone_number, status, source, agent_id, transferred_at, created_at, updated_at)
         SELECT gen_random_uuid(), c.organization_id, c.id, 'e2e-chat-layout', c.phone_number, 'active', 'manual',
                (SELECT u.id FROM users u WHERE u.email = $2 AND u.organization_id = c.organization_id LIMIT 1), NOW(), NOW(), NOW()
         FROM contacts c WHERE c.id = $1
         RETURNING agent_id`,
        [contactId, TEST_USERS.admin.email],
      )
      expect(res.rows[0]?.agent_id, 'the seeded transfer must be assigned to the test admin, not left in the queue').toBeTruthy()
    } finally {
      await db.end()
    }
  })

  test.afterAll(async () => {
    const db = new Client({ connectionString: DB_URL })
    await db.connect()
    try {
      await db.query('DELETE FROM agent_transfers WHERE contact_id = $1', [contactId])
    } finally {
      await db.end()
    }
  })

  async function open(page: Page, width: number, height = 720) {
    await page.setViewportSize({ width, height })
    await loginAsAdmin(page)
    await page.goto(`/chat/${contactId}`)
    await page.locator('#notes-button').waitFor({ state: 'visible' })
    // the Contact Info opens by default on desktop
    await expect(page.locator('#contact-info-name')).toBeVisible()
  }

  const settle = (page: Page) => page.waitForTimeout(350)

  async function setPanels(page: Page, want: { notes: boolean; info: boolean }) {
    const infoOpen = () => page.locator('#contact-info-name').isVisible()
    const notesOpen = () => page.locator('#notes-panel').isVisible()
    if ((await infoOpen()) !== want.info) { await page.locator('#info-button').click(); await settle(page) }
    if ((await notesOpen()) !== want.notes) { await page.locator('#notes-button').click(); await settle(page) }
    await settle(page)
  }

  const STATES = [
    { name: 'Contact Info only', notes: false, info: true },
    { name: 'Notes only', notes: true, info: false },
    { name: 'Notes and Contact Info', notes: true, info: true },
  ]

  for (const st of STATES) {
    test(`1280px, ${st.name}: three columns stay usable`, async ({ page }) => {
      await open(page, 1280)
      await setPanels(page, st)
      expectSoundDesktopLayout(await snapshot(page), st, `1280px ${st.name}`)
    })
  }

  test('1280px, Notes and Contact Info: the Notes close button is the element under the pointer and closes only Notes', async ({ page }) => {
    await open(page, 1280)
    await setPanels(page, { notes: true, info: true })
    const s = await snapshot(page)
    expect(s.notesClose?.reachable, `Notes close button reachable (topmost: ${s.notesClose?.topmost})`).toBe(true)
    await page.locator('#notes-panel').locator('button').first().click({ timeout: 5000 })
    await expect(page.locator('#notes-panel')).toBeHidden()
    // closing Notes does not close the Contact Info
    await expect(page.locator('#contact-info-name')).toBeVisible()
  })

  test('adapting the layout never closes the Notes panel nor the Contact Info', async ({ page }) => {
    await open(page, 1600)
    await setPanels(page, { notes: true, info: true })
    for (const w of [1440, 1366, 1280, 1366, 1600]) {
      await page.setViewportSize({ width: w, height: 720 })
      await settle(page)
      await expect(page.locator('#notes-panel'), `Notes open at ${w}px`).toBeVisible()
      await expect(page.locator('#contact-info-name'), `Contact Info open at ${w}px`).toBeVisible()
    }
  })

  test('progressive resizing with Notes and Contact Info open: no collapse and no spill at any step', async ({ page }) => {
    await open(page, 1600)
    await setPanels(page, { notes: true, info: true })
    // shrink and grow the window live: the layout has to follow without a reload
    for (const w of [1600, 1500, 1440, 1400, 1366, 1320, 1280, 1320, 1366, 1440, 1600]) {
      await page.setViewportSize({ width: w, height: 720 })
      await settle(page)
      expectSoundDesktopLayout(await snapshot(page), { notes: true, info: true }, `resized to ${w}px`)
    }
  })

  test('wide window: the Contact Info stays a column of the row (it only leaves it when the row does not fit)', async ({ page }) => {
    await open(page, 1920)
    await setPanels(page, { notes: true, info: true })
    const s = await snapshot(page)
    expectSoundDesktopLayout(s, { notes: true, info: true }, '1920px')
    // in the row: to the right of the chat column, not on top of it
    expect(s.info!.x).toBeGreaterThanOrEqual(s.chat!.right - 1)
  })

  test('375px: header actions stay contained and reachable, and pinning keeps them so', async ({ page }) => {
    await open375(page, contactId)
    const before = await snapshot(page)
    expect(before.header, 'mobile header').not.toBeNull()
    expectWidestHeader(before, 'mobile header')
    expect(before.header!.scrollW, `mobile header scrollWidth ${before.header!.scrollW} vs clientWidth ${before.header!.clientW}`).toBeLessThanOrEqual(before.header!.clientW + 1)
    const covered = before.headerControls.filter((c) => !c.reachable || c.rect.right > before.viewportW + 0.5)
    expect(covered, `mobile header controls covered or off screen: ${JSON.stringify(covered.map((c) => [c.label, c.topmost]))}`).toHaveLength(0)

    const pin = page.locator('[data-testid="header-pin-toggle"]')
    await pin.click()
    await expect(pin).toHaveAttribute('aria-pressed', 'true')
    const pinned = await snapshot(page)
    expect(pinned.header!.scrollW, 'mobile header with a pinned conversation').toBeLessThanOrEqual(pinned.header!.clientW + 1)
    expect(pinned.headerControls.filter((c) => !c.reachable || c.rect.right > pinned.viewportW + 0.5)).toHaveLength(0)
    // leave the contact as it was found
    await pin.click()
    await expect(pin).toHaveAttribute('aria-pressed', 'false')
  })
})

async function open375(page: Page, contactId: string) {
  await page.setViewportSize({ width: 375, height: 812 })
  await loginAsAdmin(page)
  await page.goto(`/chat/${contactId}`)
  await page.locator('#notes-button').waitFor({ state: 'visible' })
  await page.waitForTimeout(350)
}
