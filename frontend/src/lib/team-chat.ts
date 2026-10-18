import type { TeamChatMessage } from '@/services/api'

// Pure helpers of the internal chat, kept out of the store so they can be tested
// without Pinia or the network.

export const TEAM_CHAT_PAGE_SIZE = 50
export const TEAM_CHAT_TYPING_MS = 5000
export const TEAM_CHAT_TYPING_THROTTLE_MS = 3000
export const TEAM_CHAT_MAX_BODY = 4000

/**
 * Folds the latest page (oldest-first) into the history the user already holds.
 * Used after an event or a reconnect: the latest page is authoritative for the
 * messages it covers (so edits and deletions inside it show up), older messages
 * already loaded by scrolling up are kept.
 */
export function mergeLatest(
  existing: TeamChatMessage[],
  latest: TeamChatMessage[],
  latestHasMore: boolean
): TeamChatMessage[] {
  // The page reaches the start of the channel: it is the whole history.
  if (!latestHasMore || latest.length === 0) return latest.slice()

  const oldest = latest[0]
  const at = existing.findIndex(m => m.id === oldest.id)
  if (at >= 0) return [...existing.slice(0, at), ...latest]

  // The page's oldest message is not held yet (new traffic pushed it in):
  // keep what is strictly older than it. A same-millisecond neighbour may be
  // dropped here; scrolling up refetches it by cursor.
  const cut = Date.parse(oldest.created_at)
  return [...existing.filter(m => Date.parse(m.created_at) < cut), ...latest]
}

/** Appends a message unless it is already there (events and REST can both deliver it). */
export function appendUnique(list: TeamChatMessage[], msg: TeamChatMessage): TeamChatMessage[] {
  return list.some(m => m.id === msg.id) ? list : [...list, msg]
}

/** A trimmed body is sendable with 1..4000 characters (the same bound as the server). */
export function isSendableBody(raw: string): boolean {
  const n = Array.from(raw.trim()).length
  return n >= 1 && n <= TEAM_CHAT_MAX_BODY
}

/** Names of the people typing, skipping ids the directory does not know. */
export function pickTypingNames(ids: string[], names: Record<string, string>): string[] {
  return ids.map(id => names[id]).filter((n): n is string => !!n)
}

// ---- Attachments (limits mirror the server; the server decides) ----
export const TEAM_CHAT_MAX_FILE_BYTES = 10 * 1024 * 1024
export const TEAM_CHAT_MAX_ATTACHMENTS = 5
export const TEAM_CHAT_MAX_AUDIO_MS = 5 * 60 * 1000
export const TEAM_CHAT_MIN_AUDIO_MS = 1000
// Must be a subset of what the server accepts as a PLAIN file (internal/handlers/team_chat_attachments_validate.go).
// .m4a and .webm are deliberately absent: those containers may hold video, so the server takes them
// only from the voice recorder (kind=audio), never from the paperclip.
export const TEAM_CHAT_ACCEPT = '.png,.jpg,.jpeg,.jpe,.jfif,.gif,.webp,.pdf,.txt,.csv,.docx,.xlsx,.pptx,.mp3,.wav,.ogg,.oga,.opus'
// Fallback only: the upload answer carries the server's own guarantee (pending_ttl_ms), which is the
// source of truth. A ready file is guaranteed until then; afterwards it may or may not still exist, and
// attachment_gone (which names the missing ids) settles it exactly.
export const TEAM_CHAT_PENDING_TTL_MS = 60 * 60 * 1000
/** Safety margin for the time between the server creating the file and the client learning of it. */
export const TEAM_CHAT_PENDING_MARGIN_MS = 2 * 60 * 1000

/** An error raised by the client that looks like the server's for a given code (so one path handles both). */
export class ChatApiError extends Error {
  response: { status: number; data: { error_type: string } }
  constructor(code: string, status = 400) {
    super(code)
    this.name = 'ChatApiError'
    this.response = { status, data: { error_type: code } }
  }
}

/**
 * True when pasted text is only the names (or file:// paths) of the pasted files. Some file managers put
 * the name of a copied file on the clipboard as text; that is not text the user meant to paste.
 */
export function isOnlyFileNames(text: string, files: ArrayLike<{ name: string }>): boolean {
  const names = new Set(Array.from(files, f => f.name))
  const lines = text.split(/\r?\n/).map(l => l.trim()).filter(Boolean)
  return lines.length > 0 && lines.every(l => names.has(l) || /^file:\/\//i.test(l))
}

/** A local id for a queued file. crypto.randomUUID only exists in secure contexts (HTTPS/localhost). */
export function newUploadKey(): string {
  const c = typeof crypto !== 'undefined' ? crypto : undefined
  if (c?.randomUUID) return c.randomUUID()
  const bytes = new Uint8Array(16)
  if (c?.getRandomValues) c.getRandomValues(bytes)
  else for (let i = 0; i < 16; i++) bytes[i] = Math.floor(Math.random() * 256)
  return Array.from(bytes, b => b.toString(16).padStart(2, '0')).join('')
}
const ALLOWED_EXT = new Set(TEAM_CHAT_ACCEPT.split(',').map(e => e.slice(1)))
const DANGEROUS_EXT = new Set([
  'exe', 'bat', 'cmd', 'com', 'scr', 'pif', 'msi', 'dll', 'js', 'mjs', 'jse', 'vbs', 'vbe', 'wsf', 'ps1', 'sh', 'jar', 'apk',
  'app', 'lnk', 'reg', 'hta', 'cpl', 'html', 'htm', 'xhtml', 'svg', 'svgz', 'xml', 'php', 'asp', 'aspx', 'jsp', 'docm', 'xlsm', 'pptm'
])

/** An early warning before uploading; returns the error code the server would use, or ''. */
export function checkAttachmentFile(file: { name: string; size: number }, alreadyQueued: number): string {
  if (alreadyQueued >= TEAM_CHAT_MAX_ATTACHMENTS) return 'too_many_attachments'
  if (file.size === 0) return 'invalid_file'
  if (file.size > TEAM_CHAT_MAX_FILE_BYTES) return 'file_too_large'
  const parts = file.name.toLowerCase().split('.')
  const ext = parts.length > 1 ? parts[parts.length - 1] : ''
  if (parts.slice(1).some(p => DANGEROUS_EXT.has(p)) || !ALLOWED_EXT.has(ext)) return 'file_type_not_allowed'
  return ''
}

export function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(0)} KB`
  return `${(n / 1024 / 1024).toFixed(1)} MB`
}

export function formatDuration(ms: number): string {
  const s = Math.max(0, Math.round(ms / 1000))
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`
}

/** Voice notes need MediaRecorder, getUserMedia and a secure context (HTTPS or localhost). */
export function recordingSupported(): boolean {
  return typeof window !== 'undefined' && window.isSecureContext !== false &&
    typeof MediaRecorder !== 'undefined' && !!navigator.mediaDevices?.getUserMedia
}

/** Same-origin URL of an attachment; the session cookie authorizes it. */
export function teamChatAttachmentUrl(id: string): string {
  const base = (((window as unknown as { __BASE_PATH__?: string }).__BASE_PATH__) ?? '').replace(/\/$/, '')
  return `${base}/api/team-chat/attachments/${id}`
}

/** The error codes the UI explains (anything else gets the generic text). */
export const TEAM_CHAT_UPLOAD_ERRORS = [
  'file_too_large', 'file_type_not_allowed', 'invalid_file', 'too_many_attachments', 'attachment_gone',
  'rate_limited', 'storage_unavailable', 'archived', 'forbidden', 'not_found', 'reply_target_gone'
]

/** The reaction palette: stable server codes and the glyph drawn for each. */
export const TEAM_CHAT_REACTIONS = [
  { code: 'thumbs_up', glyph: '👍' }, { code: 'heart', glyph: '❤️' }, { code: 'laugh', glyph: '😂' },
  { code: 'wow', glyph: '😮' }, { code: 'sad', glyph: '😢' }, { code: 'pray', glyph: '🙏' },
  { code: 'check', glyph: '✅' }, { code: 'eyes', glyph: '👀' }, { code: 'fire', glyph: '🔥' },
  { code: 'party', glyph: '🎉' }, { code: 'thinking', glyph: '🤔' }, { code: 'clap', glyph: '👏' }
] as const

export const reactionGlyph = (code: string) => TEAM_CHAT_REACTIONS.find(r => r.code === code)?.glyph ?? '❔'

/** The server's machine-readable reason for a refusal ("last_owner", "archived", ...). */
export function chatErrorCode(err: unknown): string {
  return (err as { response?: { data?: { error_type?: string } } })?.response?.data?.error_type ?? ''
}

/** i18n key of a system message's event, or '' for a user message / unknown event. */
export const TEAM_CHAT_SYSTEM_EVENTS = [
  'member_added', 'member_removed', 'member_left', 'owner_changed',
  'renamed', 'archived', 'reopened', 'admin_joined'
] as const

export function systemEventKey(meta?: Record<string, string>): string {
  const ev = meta?.event
  return ev && (TEAM_CHAT_SYSTEM_EVENTS as readonly string[]).includes(ev) ? `teamChat.system.${ev}` : ''
}
