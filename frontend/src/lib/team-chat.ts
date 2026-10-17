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
