// @vitest-environment happy-dom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createI18n } from 'vue-i18n'
import en from '@/i18n/locales/en.json'
import ptBR from '@/i18n/locales/pt-BR.json'

const api = vi.hoisted(() => ({
  channels: vi.fn(), archivedChannels: vi.fn(), users: vi.fn(), messages: vi.fn(), members: vi.fn(),
  adminChannels: vi.fn(), markRead: vi.fn(), typing: vi.fn(), send: vi.fn(), react: vi.fn(), unreact: vi.fn(), message: vi.fn(),
  uploadAttachment: vi.fn(), discardAttachment: vi.fn()
}))
vi.mock('@/services/api', () => ({ teamChatService: api }))
const toast = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn(), info: vi.fn() }))
vi.mock('vue-sonner', () => ({ toast }))
const perms = vi.hoisted(() => ({ granted: new Set<string>() }))
vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ user: { id: 'me' }, hasPermission: (r: string, a: string) => perms.granted.has(`${r}:${a}`) })
}))

import MessagePanel from './MessagePanel.vue'
import { useTeamChatStore } from '@/stores/teamChat'
import { checkAttachmentFile, TEAM_CHAT_ACCEPT, TEAM_CHAT_MAX_FILE_BYTES } from '@/lib/team-chat'

const ScrollArea = { template: '<div><slot /></div>' }
const ManageStub = { props: ['open', 'channelId'], template: '<div />' }
const ok = (data: unknown = {}) => Promise.resolve({ data: { data } })
const fail = (status: number, code: string) => Promise.reject({ response: { status, data: { error_type: code } } })
const att = (id: string, kind: 'image' | 'audio' | 'file', name: string, extra: Record<string, unknown> = {}) =>
  ({ id, kind, name, mime: kind === 'image' ? 'image/png' : kind === 'audio' ? 'audio/webm' : 'application/pdf', size: 2048, ...extra })
const msg = (id: string, from: string, body: string, extra: Record<string, unknown> = {}) => ({
  id, channel_id: 'c1', user_id: from, user_name: from === 'me' ? 'Me' : 'Ana', body, kind: 'user',
  created_at: '2026-10-17T12:00:00Z', reactions: [], attachments: [], ...extra
})
const file = (name: string, size = 100, type = '') => {
  const f = new File(['x'], name, { type })
  Object.defineProperty(f, 'size', { value: size })
  return f
}

function render(opts: { archived?: boolean; messages?: unknown[] } = {}) {
  api.channels.mockReturnValue(ok({ channels: [{
    id: 'c1', kind: 'channel', name: 'ops', unread_count: 0, created_at: '2026-10-17T00:00:00Z',
    archived_at: opts.archived ? '2026-10-17T10:00:00Z' : undefined
  }] }))
  api.archivedChannels.mockReturnValue(ok({ channels: [] }))
  const store = useTeamChatStore()
  const i18n = createI18n({ legacy: false, locale: 'en', messages: { en, 'pt-BR': ptBR } })
  const wrapper = mount(MessagePanel, {
    props: { channelId: 'c1' },
    global: { plugins: [i18n], stubs: { ScrollArea, ChannelManagePanel: ManageStub } },
    attachTo: document.body
  })
  store.panel('c1').messages = (opts.messages ?? []) as never
  return { store, wrapper }
}

async function pick(wrapper: ReturnType<typeof render>['wrapper'], files: File[]) {
  const input = wrapper.get('[data-testid="file-input"]')
  Object.defineProperty(input.element, 'files', { value: files, configurable: true })
  await input.trigger('change')
  await flushPromises()
}

const sendBtn = (w: ReturnType<typeof render>['wrapper']) => w.get('form button[type="submit"]')

beforeEach(() => {
  setActivePinia(createPinia())
  Object.values(api).forEach(f => f.mockReset())
  Object.values(toast).forEach(f => f.mockReset())
  api.users.mockReturnValue(ok({ users: [] }))
  api.messages.mockReturnValue(ok({ messages: [], has_more: false }))
  api.members.mockReturnValue(ok({ members: [] }))
  api.typing.mockResolvedValue({})
  api.markRead.mockResolvedValue({})
  api.discardAttachment.mockResolvedValue({})
  perms.granted = new Set(['team_chat:read', 'team_chat:write'])
  Element.prototype.scrollIntoView = vi.fn()
  document.body.innerHTML = ''
  vi.stubGlobal('crypto', { randomUUID: (() => { let n = 0; return () => `k${++n}` })() })
})
afterEach(() => { vi.unstubAllGlobals(); vi.useRealTimers() })

describe('early validation (the server still decides)', () => {
  it('mirrors the server rules', () => {
    expect(checkAttachmentFile({ name: 'a.png', size: 10 }, 0)).toBe('')
    expect(checkAttachmentFile({ name: 'a.PDF', size: 10 }, 0)).toBe('')
    expect(checkAttachmentFile({ name: 'a.png', size: TEAM_CHAT_MAX_FILE_BYTES + 1 }, 0)).toBe('file_too_large')
    expect(checkAttachmentFile({ name: 'a.png', size: 0 }, 0)).toBe('invalid_file')
    expect(checkAttachmentFile({ name: 'a.svg', size: 10 }, 0)).toBe('file_type_not_allowed')
    expect(checkAttachmentFile({ name: 'a.pdf.exe', size: 10 }, 0)).toBe('file_type_not_allowed')
    expect(checkAttachmentFile({ name: 'noext', size: 10 }, 0)).toBe('file_type_not_allowed')
    expect(checkAttachmentFile({ name: 'a.docm', size: 10 }, 0)).toBe('file_type_not_allowed')
    expect(checkAttachmentFile({ name: 'a.png', size: 10 }, 5)).toBe('too_many_attachments')
  })
})

describe('the upload queue', () => {
  it('shows progress, becomes ready, and only then lets the message go', async () => {
    let resolve!: (v: unknown) => void
    api.uploadAttachment.mockImplementation((_c: string, _f: File, o: { onProgress: (n: number) => void }) => {
      o.onProgress(40)
      return new Promise(r => { resolve = r })
    })
    const { store, wrapper } = render()
    await store.fetchChannels()
    await flushPromises()
    await pick(wrapper, [file('foto.png', 500, 'image/png')])

    const card = wrapper.get('[data-testid="attachment-queue"] li')
    expect(card.attributes('data-status')).toBe('uploading')
    expect(card.text()).toContain('Uploading 40%')
    expect(card.get('[role="progressbar"]').attributes('aria-valuenow')).toBe('40')
    expect(sendBtn(wrapper).attributes('disabled')).toBeDefined()

    resolve({ data: { data: att('a1', 'image', 'foto.png') } })
    await flushPromises()
    expect(wrapper.get('[data-testid="attachment-queue"] li').attributes('data-status')).toBe('ready')
    expect(sendBtn(wrapper).attributes('disabled')).toBeUndefined()
  })

  it('sending with files and no text sends attachment_ids, then clears the queue', async () => {
    api.uploadAttachment.mockReturnValue(ok(att('a1', 'image', 'foto.png')))
    api.send.mockReturnValue(ok(msg('m1', 'me', '', { attachments: [att('a1', 'image', 'foto.png')] })))
    const { store, wrapper } = render()
    await store.fetchChannels()
    await flushPromises()
    await pick(wrapper, [file('foto.png', 500, 'image/png')])
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(api.send).toHaveBeenCalledWith('c1', '', undefined, ['a1'])
    expect(wrapper.find('[data-testid="attachment-queue"]').exists()).toBe(false)
  })

  it('text goes along with the files, and a plain message is unchanged', async () => {
    api.uploadAttachment.mockReturnValue(ok(att('a1', 'file', 'c.pdf')))
    api.send.mockReturnValue(ok(msg('m1', 'me', 'segue')))
    const { store, wrapper } = render()
    await store.fetchChannels()
    await flushPromises()
    await pick(wrapper, [file('c.pdf')])
    await wrapper.get('textarea').setValue('segue')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(api.send).toHaveBeenLastCalledWith('c1', 'segue', undefined, ['a1'])
    await wrapper.get('textarea').setValue('plain')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(api.send).toHaveBeenLastCalledWith('c1', 'plain')
  })

  it('a failed upload shows its reason and can be retried; the others are not blocked', async () => {
    api.uploadAttachment
      .mockImplementationOnce(() => fail(415, 'file_type_not_allowed'))
      .mockReturnValueOnce(ok(att('a2', 'file', 'b.pdf')))
      .mockReturnValueOnce(ok(att('a1', 'file', 'a.pdf')))
    const { store, wrapper } = render()
    await store.fetchChannels()
    await flushPromises()
    await pick(wrapper, [file('a.pdf'), file('b.pdf')])
    const cards = wrapper.findAll('[data-testid="attachment-queue"] li')
    expect(cards[0].attributes('data-status')).toBe('failed')
    expect(cards[0].text()).toContain(en.teamChat.errors.file_type_not_allowed)
    expect(cards[1].attributes('data-status')).toBe('ready')
    expect(sendBtn(wrapper).attributes('disabled')).toBeDefined()

    await cards[0].get('[aria-label="Try again"]').trigger('click')
    await flushPromises()
    expect(wrapper.findAll('[data-testid="attachment-queue"] li')[0].attributes('data-status')).toBe('ready')
    expect(sendBtn(wrapper).attributes('disabled')).toBeUndefined()
  })

  it('removing a ready file discards it on the server; removing an uploading one aborts the request', async () => {
    let signal: AbortSignal | undefined
    api.uploadAttachment
      .mockReturnValueOnce(ok(att('a1', 'file', 'a.pdf')))
      .mockImplementationOnce((_c: string, _f: File, o: { signal: AbortSignal }) => { signal = o.signal; return new Promise(() => undefined) })
    const { store, wrapper } = render()
    await store.fetchChannels()
    await flushPromises()
    await pick(wrapper, [file('a.pdf'), file('b.pdf')])

    await wrapper.get('[aria-label="Remove a.pdf"]').trigger('click')
    expect(api.discardAttachment).toHaveBeenCalledWith('c1', 'a1')
    await wrapper.get('[aria-label="Remove b.pdf"]').trigger('click')
    expect(signal?.aborted).toBe(true)
    expect(wrapper.find('[data-testid="attachment-queue"]').exists()).toBe(false)
  })

  it('a failed send keeps the files and the text for another try', async () => {
    api.uploadAttachment.mockReturnValue(ok(att('a1', 'file', 'a.pdf')))
    api.send.mockImplementationOnce(() => fail(503, 'storage_unavailable')).mockReturnValueOnce(ok(msg('m1', 'me', 'oi')))
    const { store, wrapper } = render()
    await store.fetchChannels()
    await flushPromises()
    await pick(wrapper, [file('a.pdf')])
    await wrapper.get('textarea').setValue('oi')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(toast.error).toHaveBeenCalledWith(en.teamChat.errors.storage_unavailable)
    expect((wrapper.get('textarea').element as HTMLTextAreaElement).value).toBe('oi')
    expect(wrapper.findAll('[data-testid="attachment-queue"] li')).toHaveLength(1)
  })

  it('files refused before upload are explained and never queued', async () => {
    const { store, wrapper } = render()
    await store.fetchChannels()
    await flushPromises()
    await pick(wrapper, [file('virus.exe'), file('big.png', TEAM_CHAT_MAX_FILE_BYTES + 1)])
    expect(api.uploadAttachment).not.toHaveBeenCalled()
    expect(toast.error).toHaveBeenCalledWith(`virus.exe: ${en.teamChat.errors.file_type_not_allowed}`)
    expect(toast.error).toHaveBeenCalledWith(`big.png: ${en.teamChat.errors.file_too_large}`)
    expect(wrapper.find('[data-testid="attachment-queue"]').exists()).toBe(false)
  })

  it('at most five files in the queue', async () => {
    api.uploadAttachment.mockImplementation(() => new Promise(() => undefined))
    const { store, wrapper } = render()
    await store.fetchChannels()
    await flushPromises()
    await pick(wrapper, Array.from({ length: 7 }, (_, i) => file(`f${i}.pdf`)))
    expect(wrapper.findAll('[data-testid="attachment-queue"] li')).toHaveLength(5)
    expect(toast.error).toHaveBeenCalledTimes(2)
  })

  it('pasting an image and dropping files both queue them', async () => {
    api.uploadAttachment.mockReturnValue(ok(att('a1', 'image', 'shot.png')))
    const { store, wrapper } = render()
    await store.fetchChannels()
    await flushPromises()
    const pasted = file('shot.png', 10, 'image/png')
    const ev = new Event('paste', { bubbles: true, cancelable: true }) as Event & { clipboardData: unknown }
    ev.clipboardData = { files: [pasted], getData: () => '' }
    wrapper.get('textarea').element.dispatchEvent(ev)
    await flushPromises()
    expect(ev.defaultPrevented).toBe(true)
    expect(api.uploadAttachment).toHaveBeenCalledTimes(1)

    const dragover = new Event('dragover', { bubbles: true, cancelable: true }) as Event & { dataTransfer: unknown }
    dragover.dataTransfer = { types: ['Files'] }
    wrapper.element.dispatchEvent(dragover)
    await flushPromises()
    expect(wrapper.find('[data-testid="drop-overlay"]').exists()).toBe(true)
    const drop = new Event('drop', { bubbles: true, cancelable: true }) as Event & { dataTransfer: unknown }
    drop.dataTransfer = { files: [file('d.pdf')] }
    wrapper.get('[data-testid="drop-overlay"]').element.dispatchEvent(drop)
    await flushPromises()
    expect(api.uploadAttachment).toHaveBeenCalledTimes(2)
    expect(wrapper.find('[data-testid="drop-overlay"]').exists()).toBe(false)
  })

  it('read-only users and archived channels cannot attach or record', async () => {
    perms.granted = new Set(['team_chat:read'])
    const a = render()
    await a.store.fetchChannels()
    await flushPromises()
    expect(a.wrapper.find('[data-testid="file-input"]').exists()).toBe(false)
    expect(a.wrapper.find('[aria-label="Attach a file"]').exists()).toBe(false)
    a.wrapper.unmount()

    perms.granted = new Set(['team_chat:read', 'team_chat:write'])
    setActivePinia(createPinia())
    const b = render({ archived: true })
    await b.store.fetchChannels()
    await flushPromises()
    expect(b.wrapper.find('[data-testid="file-input"]').exists()).toBe(false)
    expect(b.wrapper.text()).toContain(en.teamChat.manage.archivedNotice)
    expect(b.wrapper.text()).not.toContain(en.teamChat.readOnly)
  })
})

describe('paste', () => {
  const paste = (w: ReturnType<typeof render>['wrapper'], files: File[], text: string) => {
    const ev = new Event('paste', { bubbles: true, cancelable: true }) as Event & { clipboardData: unknown }
    ev.clipboardData = { files, getData: (t: string) => (t === 'text/plain' ? text : '') }
    w.get('textarea').element.dispatchEvent(ev)
    return ev
  }

  it('keeps the text and attaches the picture when the clipboard has both (Excel, Word)', async () => {
    api.uploadAttachment.mockReturnValue(ok(att('a1', 'image', 'image.png')))
    const { store, wrapper } = render()
    await store.fetchChannels()
    await flushPromises()
    const ev = paste(wrapper, [file('image.png', 10, 'image/png')], 'a\tb\n1\t2')
    await flushPromises()
    expect(ev.defaultPrevented).toBe(false) // the browser still pastes the text
    expect(api.uploadAttachment).toHaveBeenCalledTimes(1)
    expect(wrapper.findAll('[data-testid="attachment-queue"] li')).toHaveLength(1)
  })

  it('takes over only a clipboard that is files and nothing else', async () => {
    api.uploadAttachment.mockReturnValue(ok(att('a1', 'image', 'shot.png')))
    const { store, wrapper } = render()
    await store.fetchChannels()
    await flushPromises()
    const ev = paste(wrapper, [file('shot.png', 10, 'image/png')], '')
    await flushPromises()
    expect(ev.defaultPrevented).toBe(true)
    expect(api.uploadAttachment).toHaveBeenCalledTimes(1)
  })

  it('a text-only paste is left alone', async () => {
    const { store, wrapper } = render()
    await store.fetchChannels()
    await flushPromises()
    const ev = paste(wrapper, [], 'just text')
    expect(ev.defaultPrevented).toBe(false)
    expect(api.uploadAttachment).not.toHaveBeenCalled()
  })
})

describe('one format matrix for client and server', () => {
  it('the paperclip does not offer m4a or webm (the server takes them only from the recorder)', async () => {
    expect(TEAM_CHAT_ACCEPT.split(',')).not.toContain('.m4a')
    expect(TEAM_CHAT_ACCEPT.split(',')).not.toContain('.webm')
    expect(checkAttachmentFile({ name: 'nota.m4a', size: 10 }, 0)).toBe('file_type_not_allowed')
    expect(checkAttachmentFile({ name: 'clip.webm', size: 10 }, 0)).toBe('file_type_not_allowed')
    const { store, wrapper } = render()
    await store.fetchChannels()
    await flushPromises()
    await pick(wrapper, [file('nota.m4a', 100)])
    expect(api.uploadAttachment).not.toHaveBeenCalled() // refused before a byte is sent
    expect(toast.error).toHaveBeenCalledWith('nota.m4a: ' + en.teamChat.errors.file_type_not_allowed)
  })

  it('if the server still says 415, the card shows the reason and can be removed', async () => {
    api.uploadAttachment.mockImplementation(() => fail(415, 'file_type_not_allowed'))
    const { store, wrapper } = render()
    await store.fetchChannels()
    await flushPromises()
    await pick(wrapper, [file('odd.csv', 100)])
    expect(wrapper.get('[data-testid="attachment-queue"] li').text()).toContain(en.teamChat.errors.file_type_not_allowed)
    await wrapper.get('[aria-label="Remove odd.csv"]').trigger('click')
    expect(wrapper.find('[data-testid="attachment-queue"]').exists()).toBe(false)
  })
})

describe('expired attachments', () => {
  const readyQueue = async (n = 2) => {
    api.uploadAttachment.mockImplementation((_c: string, f: File) => ok(att('id-' + f.name, 'file', f.name)))
    const r = render()
    await r.store.fetchChannels()
    await flushPromises()
    await pick(r.wrapper, Array.from({ length: n }, (_, i) => file('f' + i + '.pdf')))
    return r
  }

  it('a ready file older than the server grace is dropped before sending, with no request', async () => {
    const { store, wrapper } = await readyQueue(1)
    store.uploadsFor('c1')[0].readyAt = Date.now() - 56 * 60 * 1000
    await wrapper.get('textarea').setValue('oi')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(api.send).not.toHaveBeenCalled()
    expect(toast.error).toHaveBeenCalledWith(en.teamChat.errors.attachment_gone)
    expect(store.uploadsFor('c1')).toHaveLength(0)
    expect(wrapper.find('[data-testid="attachment-queue"]').exists()).toBe(false)
    expect((wrapper.get('textarea').element as HTMLTextAreaElement).value).toBe('oi')
  })

  it('stale files are also dropped when the conversation is opened again', async () => {
    const { store, wrapper } = await readyQueue(1)
    store.uploadsFor('c1')[0].readyAt = Date.now() - 2 * 60 * 60 * 1000
    await wrapper.setProps({ channelId: 'c2' })
    await wrapper.setProps({ channelId: 'c1' })
    await flushPromises()
    expect(store.uploadsFor('c1')).toHaveLength(0)
    expect(toast.error).toHaveBeenCalledWith(en.teamChat.errors.attachment_gone)
  })

  it('when the server names the missing ids, only those leave the queue; the others stay ready', async () => {
    api.send.mockImplementationOnce(() => Promise.reject({
      response: { status: 400, data: { error_type: 'attachment_gone', data: { attachment_ids: ['id-f0.pdf'] } } }
    }))
    const { store, wrapper } = await readyQueue(2)
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    const left = store.uploadsFor('c1')
    expect(left.map(u => u.name)).toEqual(['f1.pdf'])
    expect(left[0].status).toBe('ready')
    expect(wrapper.findAll('[data-testid="attachment-queue"] li')).toHaveLength(1)
    expect(toast.error).toHaveBeenCalledWith(en.teamChat.errors.attachment_gone)
  })

  it('when the server cannot say which, every ready file is dropped (none is shown as ready)', async () => {
    api.send.mockImplementationOnce(() => fail(400, 'attachment_gone'))
    const { store, wrapper } = await readyQueue(2)
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(store.uploadsFor('c1')).toHaveLength(0)
    expect(wrapper.find('[data-testid="attachment-queue"]').exists()).toBe(false)
  })

  it('a file still uploading is not touched by an expiry', async () => {
    api.uploadAttachment.mockImplementation(() => new Promise(() => undefined))
    const { store, wrapper } = render()
    await store.fetchChannels()
    await flushPromises()
    await pick(wrapper, [file('slow.pdf')])
    expect(store.expireStaleUploads('c1', Date.now() + 10 * 60 * 60 * 1000)).toHaveLength(0)
    expect(store.uploadsFor('c1')).toHaveLength(1)
  })
})

describe('plain HTTP (no crypto.randomUUID)', () => {
  it.each([
    ['getRandomValues only', { getRandomValues: (a: Uint8Array) => a.map((_, i) => i * 7) }],
    ['no crypto at all', undefined]
  ])('attaches and sends with %s', async (_n, fake) => {
    vi.stubGlobal('crypto', fake)
    Object.defineProperty(window, 'isSecureContext', { value: false, configurable: true })
    api.uploadAttachment.mockReturnValue(ok(att('a1', 'file', 'c.pdf')))
    api.send.mockReturnValue(ok(msg('m1', 'me', 'oi')))
    const { store, wrapper } = render()
    await store.fetchChannels()
    await flushPromises()
    await pick(wrapper, [file('c.pdf')])
    expect(wrapper.get('[data-testid="attachment-queue"] li').attributes('data-status')).toBe('ready')
    await wrapper.get('textarea').setValue('oi')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(api.send).toHaveBeenCalledWith('c1', 'oi', undefined, ['a1'])
    Object.defineProperty(window, 'isSecureContext', { value: true, configurable: true })
  })

  it('two queued files get different keys', async () => {
    vi.stubGlobal('crypto', undefined)
    api.uploadAttachment.mockImplementation(() => new Promise(() => undefined))
    const { store, wrapper } = render()
    await store.fetchChannels()
    await flushPromises()
    await pick(wrapper, [file('a.pdf'), file('b.pdf')])
    const keys = store.uploadsFor('c1').map(u => u.key)
    expect(new Set(keys).size).toBe(2)
  })
})

describe('attachments in messages', () => {
  const rendered = async () => {
    const { store, wrapper } = render({ messages: [
      msg('m1', 'ana', '', { attachments: [att('i1', 'image', 'foto.png', { width: 800, height: 600 })] }),
      msg('m2', 'ana', 'ouça', { attachments: [att('v1', 'audio', 'audio.webm', { duration_ms: 31000 })] }),
      msg('m3', 'ana', '', { attachments: [att('f1', 'file', 'contrato.pdf', { size: 2048 })] })
    ] })
    await store.fetchChannels()
    await flushPromises()
    return wrapper
  }

  it('images are thumbnails, audio has a player, files are download cards', async () => {
    const w = await rendered()
    const img = w.get('[data-testid="attachment-i1"] img')
    expect(img.attributes('src')).toBe('/api/team-chat/attachments/i1')
    expect(img.attributes('alt')).toBe('foto.png')
    const audio = w.get('[data-testid="attachment-v1"] audio')
    expect(audio.attributes('src')).toBe('/api/team-chat/attachments/v1')
    expect(audio.attributes('preload')).toBe('none')
    expect(w.get('[data-testid="attachment-v1"]').text()).toContain('0:31')
    const card = w.get('[data-testid="attachment-f1"]')
    expect(card.attributes('href')).toBe('/api/team-chat/attachments/f1')
    expect(card.attributes('download')).toBeDefined()
    expect(card.text()).toContain('contrato.pdf')
    expect(card.text()).toContain('2 KB')
  })

  it('a message with files and no text has no empty paragraph', async () => {
    const w = await rendered()
    expect(w.get('[data-testid="message-m1"]').find('p').exists()).toBe(false)
  })

  it('starting one voice note pauses the others', async () => {
    const w = await rendered()
    const audios = Array.from(document.querySelectorAll('audio')) as HTMLAudioElement[]
    const pause = vi.fn()
    audios[0].pause = pause
    w.get('[data-testid="attachment-v1"] audio').element.dispatchEvent(new Event('play'))
    // the only audio is the one playing: nothing else to pause
    expect(pause).not.toHaveBeenCalled()
    const other = document.createElement('audio')
    other.pause = pause
    document.body.appendChild(other)
    w.get('[data-testid="attachment-v1"] audio').element.dispatchEvent(new Event('play'))
    expect(pause).toHaveBeenCalledTimes(1)
  })

  it('names and quotes with HTML stay text', async () => {
    const evil = '<img src=x onerror="window.__xss=1"><b>x</b>.pdf'
    const { store, wrapper } = render({ messages: [
      msg('m1', 'ana', '', { attachments: [att('f1', 'file', evil)] }),
      msg('m2', 'me', 'a', { reply_to: { id: 'm1', user_name: 'Ana', snippet: '', deleted: false, attachment_kind: 'file', attachment_name: evil } })
    ] })
    await store.fetchChannels()
    await flushPromises()
    expect(wrapper.get('[data-testid="attachment-f1"]').find('b, img').exists()).toBe(false)
    expect(wrapper.get('[data-testid="quote-m2"]').find('b, img').exists()).toBe(false)
    expect(wrapper.get('[data-testid="quote-m2"]').text()).toContain(evil)
    expect((window as unknown as Record<string, unknown>).__xss).toBeUndefined()
  })

  it('quoting a message that is only a file or a voice note says what it was', async () => {
    const { store, wrapper } = render({ messages: [
      msg('m2', 'me', 'a', { reply_to: { id: 'm1', user_name: 'Ana', snippet: '', deleted: false, attachment_kind: 'image', attachment_name: 'foto.png' } }),
      msg('m3', 'me', 'b', { reply_to: { id: 'm9', user_name: 'Ana', snippet: '', deleted: false, attachment_kind: 'audio', attachment_name: 'audio.webm' } })
    ] })
    await store.fetchChannels()
    await flushPromises()
    expect(wrapper.get('[data-testid="quote-m2"]').text()).toContain('🖼 foto.png')
    expect(wrapper.get('[data-testid="quote-m3"]').text()).toContain('🎤 Voice message')
  })

  it('the reply bar of a message with only a file shows the file, and reply sends with attachment_ids too', async () => {
    api.uploadAttachment.mockReturnValue(ok(att('a1', 'file', 'x.pdf')))
    api.send.mockReturnValue(ok(msg('m9', 'me', 'ok')))
    const { store, wrapper } = render({ messages: [msg('m1', 'ana', '', { attachments: [att('f1', 'file', 'contrato.pdf')] })] })
    await store.fetchChannels()
    await flushPromises()
    await wrapper.get('[data-testid="message-m1"] [aria-label="Reply"]').trigger('click')
    expect(wrapper.get('[data-testid="reply-bar"]').text()).toContain('📎 contrato.pdf')
    await pick(wrapper, [file('x.pdf')])
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(api.send).toHaveBeenCalledWith('c1', '', 'm1', ['a1'])
  })
})

describe('voice notes', () => {
  let trackStop: ReturnType<typeof vi.fn>
  let getUserMedia: ReturnType<typeof vi.fn>

  class FakeRecorder {
    static isTypeSupported = (m: string) => m.startsWith('audio/webm')
    state = 'inactive'
    mimeType: string
    ondataavailable: ((e: { data: Blob }) => void) | null = null
    onstop: (() => void) | null = null
    constructor(_s: unknown, o?: { mimeType?: string }) { this.mimeType = o?.mimeType ?? '' }
    start() { this.state = 'recording' }
    stop() {
      this.state = 'inactive'
      this.ondataavailable?.({ data: new Blob(['voice'], { type: this.mimeType }) })
      this.onstop?.()
    }
  }

  beforeEach(() => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval', 'Date'] })
    trackStop = vi.fn()
    getUserMedia = vi.fn().mockResolvedValue({ getTracks: () => [{ stop: trackStop }] })
    vi.stubGlobal('MediaRecorder', FakeRecorder)
    Object.defineProperty(navigator, 'mediaDevices', { value: { getUserMedia }, configurable: true })
    URL.createObjectURL = vi.fn(() => 'blob:voice')
    URL.revokeObjectURL = vi.fn()
  })

  const start = async (w: ReturnType<typeof render>['wrapper']) => {
    await w.get('[data-testid="record-button"]').trigger('click')
    await flushPromises()
  }

  it('records, previews, sends as its own message with the duration, and releases the mic', async () => {
    api.uploadAttachment.mockReturnValue(ok(att('v1', 'audio', 'audio.webm')))
    api.send.mockReturnValue(ok(msg('m1', 'me', '', { attachments: [att('v1', 'audio', 'audio.webm')] })))
    const { store, wrapper } = render()
    await store.fetchChannels()
    await flushPromises()
    await start(wrapper)
    expect(wrapper.find('form').exists()).toBe(false)
    expect(wrapper.find('[data-testid="audio-recorder"]').exists()).toBe(true)

    vi.advanceTimersByTime(3000)
    await wrapper.get('[data-testid="recorder-stop"]').trigger('click')
    await flushPromises()
    expect(trackStop).toHaveBeenCalled()
    expect(wrapper.get('[data-testid="audio-recorder"] audio').attributes('src')).toBe('blob:voice')

    await wrapper.get('[data-testid="recorder-send"]').trigger('click')
    await flushPromises()
    const [, sentFile, opts] = api.uploadAttachment.mock.calls[0]
    expect((sentFile as File).name).toBe('audio.webm')
    expect((sentFile as File).type).toBe('audio/webm')
    expect(opts.audioMs).toBe(3000)
    expect(api.send).toHaveBeenCalledWith('c1', '', undefined, ['v1'])
    expect(wrapper.find('[data-testid="audio-recorder"]').exists()).toBe(false)
    expect(wrapper.find('form').exists()).toBe(true)
  })

  it('under one second is discarded with a message; discarding a recording sends nothing', async () => {
    const { store, wrapper } = render()
    await store.fetchChannels()
    await flushPromises()
    await start(wrapper)
    vi.advanceTimersByTime(300)
    await wrapper.get('[data-testid="recorder-stop"]').trigger('click')
    expect(toast.error).toHaveBeenCalledWith(en.teamChat.recorder.tooShort)
    expect(wrapper.find('[data-testid="audio-recorder"]').exists()).toBe(false)

    await start(wrapper)
    vi.advanceTimersByTime(2000)
    await wrapper.get('[data-testid="recorder-stop"]').trigger('click')
    await wrapper.get('[data-testid="recorder-discard"]').trigger('click')
    expect(api.uploadAttachment).not.toHaveBeenCalled()
    expect(URL.revokeObjectURL).toHaveBeenCalled()
  })

  it('stops by itself at five minutes', async () => {
    const { store, wrapper } = render()
    await store.fetchChannels()
    await flushPromises()
    await start(wrapper)
    vi.advanceTimersByTime(300_500)
    await flushPromises()
    expect(wrapper.find('[data-testid="recorder-send"]').exists()).toBe(true)
    expect(trackStop).toHaveBeenCalled()
  })

  it('cancelling while recording releases the microphone', async () => {
    const { store, wrapper } = render()
    await store.fetchChannels()
    await flushPromises()
    await start(wrapper)
    await wrapper.get('[aria-label="Cancel recording"]').trigger('click')
    expect(trackStop).toHaveBeenCalled()
    expect(wrapper.find('[data-testid="audio-recorder"]').exists()).toBe(false)
  })

  it('explains a blocked or missing microphone, and stays out of recording', async () => {
    const { store, wrapper } = render()
    await store.fetchChannels()
    await flushPromises()
    getUserMedia.mockRejectedValueOnce(Object.assign(new Error('x'), { name: 'NotAllowedError' }))
    await start(wrapper)
    expect(toast.error).toHaveBeenCalledWith(en.teamChat.recorder.denied)
    getUserMedia.mockRejectedValueOnce(Object.assign(new Error('x'), { name: 'NotFoundError' }))
    await start(wrapper)
    expect(toast.error).toHaveBeenCalledWith(en.teamChat.recorder.noMic)
    expect(wrapper.find('[data-testid="audio-recorder"]').exists()).toBe(false)
  })

  it('a failed voice send keeps the recording so it can be sent again', async () => {
    api.uploadAttachment.mockImplementationOnce(() => fail(503, 'storage_unavailable')).mockReturnValueOnce(ok(att('v1', 'audio', 'audio.webm')))
    api.send.mockReturnValue(ok(msg('m1', 'me', '')))
    const { store, wrapper } = render()
    await store.fetchChannels()
    await flushPromises()
    await start(wrapper)
    vi.advanceTimersByTime(2000)
    await wrapper.get('[data-testid="recorder-stop"]').trigger('click')
    await wrapper.get('[data-testid="recorder-send"]').trigger('click')
    await flushPromises()
    expect(toast.error).toHaveBeenCalledWith(en.teamChat.errors.storage_unavailable)
    expect(wrapper.find('[data-testid="recorder-send"]').exists()).toBe(true)
    await wrapper.get('[data-testid="recorder-send"]').trigger('click')
    await flushPromises()
    expect(api.send).toHaveBeenCalledTimes(1)
    expect(wrapper.find('[data-testid="audio-recorder"]').exists()).toBe(false)
  })

  it('changing conversation discards a recording in progress', async () => {
    const { store, wrapper } = render()
    await store.fetchChannels()
    await flushPromises()
    await start(wrapper)
    await wrapper.setProps({ channelId: 'c2' })
    await flushPromises()
    expect(trackStop).toHaveBeenCalled()
    expect(toast.info).toHaveBeenCalledWith(en.teamChat.recorder.cancelledOnSwitch)
  })

  it('a second click while the permission prompt is open does not open a second stream', async () => {
    let grant!: (s: unknown) => void
    getUserMedia.mockReturnValue(new Promise(r => { grant = r }))
    const { store, wrapper } = render()
    await store.fetchChannels()
    await flushPromises()
    await wrapper.get('[data-testid="record-button"]').trigger('click')
    await wrapper.get('[data-testid="record-button"]').trigger('click')
    await wrapper.get('[data-testid="record-button"]').trigger('click')
    expect(getUserMedia).toHaveBeenCalledTimes(1)
    grant({ getTracks: () => [{ stop: trackStop }] })
    await flushPromises()
    expect(wrapper.find('[data-testid="audio-recorder"]').exists()).toBe(true)
    await wrapper.get('[aria-label="Cancel recording"]').trigger('click')
    expect(trackStop).toHaveBeenCalledTimes(1)
  })

  it('unmounting while the prompt is open: the late stream is stopped, nothing records', async () => {
    let grant!: (s: unknown) => void
    getUserMedia.mockReturnValue(new Promise(r => { grant = r }))
    const { store, wrapper } = render()
    await store.fetchChannels()
    await flushPromises()
    await wrapper.get('[data-testid="record-button"]').trigger('click')
    wrapper.unmount()
    grant({ getTracks: () => [{ stop: trackStop }] })
    await flushPromises()
    expect(trackStop).toHaveBeenCalledTimes(1)
  })

  it('changing conversation while the prompt is open: the late stream is stopped too', async () => {
    let grant!: (s: unknown) => void
    getUserMedia.mockReturnValue(new Promise(r => { grant = r }))
    const { store, wrapper } = render()
    await store.fetchChannels()
    await flushPromises()
    await wrapper.get('[data-testid="record-button"]').trigger('click')
    await wrapper.setProps({ channelId: 'c2' })
    grant({ getTracks: () => [{ stop: trackStop }] })
    await flushPromises()
    expect(trackStop).toHaveBeenCalledTimes(1)
    expect(wrapper.find('[data-testid="audio-recorder"]').exists()).toBe(false)
    expect(toast.info).not.toHaveBeenCalled() // nothing was recording yet, so nothing to announce
  })

  it('after a refused prompt the button works again', async () => {
    const { store, wrapper } = render()
    await store.fetchChannels()
    await flushPromises()
    getUserMedia.mockRejectedValueOnce(Object.assign(new Error('x'), { name: 'NotAllowedError' }))
    await start(wrapper)
    await start(wrapper)
    expect(getUserMedia).toHaveBeenCalledTimes(2)
    expect(wrapper.find('[data-testid="audio-recorder"]').exists()).toBe(true)
  })

  it('without MediaRecorder or a secure context there is no microphone button', async () => {
    vi.stubGlobal('MediaRecorder', undefined)
    const a = render()
    await a.store.fetchChannels()
    await flushPromises()
    expect(a.wrapper.find('[data-testid="record-button"]').exists()).toBe(false)
    a.wrapper.unmount()
    vi.stubGlobal('MediaRecorder', FakeRecorder)
    Object.defineProperty(window, 'isSecureContext', { value: false, configurable: true })
    setActivePinia(createPinia())
    const b = render()
    await b.store.fetchChannels()
    await flushPromises()
    expect(b.wrapper.find('[data-testid="record-button"]').exists()).toBe(false)
    Object.defineProperty(window, 'isSecureContext', { value: true, configurable: true })
  })
})

describe('texts', () => {
  it('every upload error and recorder text exists in both languages', () => {
    const keys = ['file_too_large', 'file_type_not_allowed', 'invalid_file', 'too_many_attachments', 'attachment_gone', 'rate_limited', 'storage_unavailable']
    for (const k of keys) {
      expect((en.teamChat.errors as Record<string, string>)[k]).toBeTruthy()
      expect((ptBR.teamChat.errors as Record<string, string>)[k]).toBeTruthy()
    }
    expect(Object.keys(en.teamChat.recorder).sort()).toEqual(Object.keys(ptBR.teamChat.recorder).sort())
    expect(Object.keys(en.teamChat.attachments).sort()).toEqual(Object.keys(ptBR.teamChat.attachments).sort())
  })
})
