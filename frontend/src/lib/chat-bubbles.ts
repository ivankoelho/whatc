// Layout rules of a conversation drawn like a messenger: who is on which side, which messages are
// visually grouped, and where a date separator goes. One place, used by the Chat Interno and by the
// discussion tab of a case, so both look and behave the same.

export interface BubbleMessage {
  id: string
  user_id: string
  created_at: string
  kind?: string
}

export interface BubbleMeta {
  /** First message of a run by the same sender: shows the name (and avatar). */
  first: boolean
  /** A date separator goes above this message. */
  newDay: boolean
}

const GROUP_WINDOW_MS = 5 * 60 * 1000

const dayKey = (iso: string) => {
  const d = new Date(iso)
  return `${d.getFullYear()}-${d.getMonth()}-${d.getDate()}`
}

/** Own messages are the ones whose author id IS the signed-in user's id; nothing else decides the side. */
export const isOwnMessage = (m: { user_id: string }, myId: string | undefined | null) => !!myId && m.user_id === myId

export function bubbleMeta(messages: BubbleMessage[], index: number): BubbleMeta {
  const m = messages[index]
  const prev = index > 0 ? messages[index - 1] : undefined
  const newDay = !prev || dayKey(prev.created_at) !== dayKey(m.created_at)
  const first = newDay || !prev || prev.kind === 'system' || prev.user_id !== m.user_id ||
    new Date(m.created_at).getTime() - new Date(prev.created_at).getTime() > GROUP_WINDOW_MS
  return { first, newDay }
}

/** "Today", "Yesterday" or the date, in the user's language. */
export function dayLabel(iso: string, locale: string, labels: { today: string; yesterday: string }, now = new Date()): string {
  const d = new Date(iso)
  const y = new Date(now)
  y.setDate(now.getDate() - 1)
  if (dayKey(iso) === dayKey(now.toISOString())) return labels.today
  if (dayKey(iso) === dayKey(y.toISOString())) return labels.yesterday
  return d.toLocaleDateString(locale, { weekday: 'long', day: '2-digit', month: 'long', year: d.getFullYear() === now.getFullYear() ? undefined : 'numeric' })
}

/** Bubble colours, from the design system's tokens (the green is the brand's primary). */
export const bubbleClass = (own: boolean) =>
  own ? 'bg-primary/15 rounded-tr-sm' : 'bg-muted rounded-tl-sm'
