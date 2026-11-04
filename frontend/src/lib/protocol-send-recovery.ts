// What the browser remembers about a protocol send whose outcome the page did not see, so that a
// refresh, a lost answer or coming back later can ask the server what happened instead of sending
// again. It holds ids only (never the customer's text) and is scoped by organization, user and case,
// so two people on one machine, or two cases, never read each other's record.

export const RECOVERY_TTL_MS = 24 * 60 * 60 * 1000 // long past the 2 min reconciliation, far under the 90 day retention
// A request the server never answered may still be on its way for as long as the server waits on the
// provider (30 s). Until then "no claim found" is not proof that nothing is being sent.
export const IN_FLIGHT_GRACE_MS = 60 * 1000

export interface SendIntentRecord {
  key: string // the Idempotency-Key of the intention
  textHash: string // which text the key stands for (a different text is a different intention)
  sendId?: string // known once any answer arrived
  savedAt: number
}

export interface RecoveryScope {
  organizationId: string
  userId: string
  occurrenceId: string
}

const PREFIX = 'protocolSend:v1:'

export const recordStorageKey = (s: RecoveryScope) => `${PREFIX}${s.organizationId}:${s.userId}:${s.occurrenceId}`

// Not a security hash: it only tells "same text" from "other text".
export function textHash(text: string | undefined): string {
  let h = 5381
  for (const ch of text ?? '') h = ((h << 5) + h + ch.codePointAt(0)!) | 0
  return String(h >>> 0)
}

function storage(): Storage | null {
  try {
    return window.localStorage
  } catch {
    return null // private window / blocked: recovery is simply unavailable
  }
}

export function loadRecord(s: RecoveryScope, now = Date.now()): SendIntentRecord | null {
  const st = storage()
  if (!st || !s.organizationId || !s.userId) return null
  try {
    const raw = st.getItem(recordStorageKey(s))
    if (!raw) return null
    const rec = JSON.parse(raw) as SendIntentRecord
    if (!rec?.key || typeof rec.savedAt !== 'number' || now - rec.savedAt > RECOVERY_TTL_MS) {
      st.removeItem(recordStorageKey(s))
      return null
    }
    return rec
  } catch {
    return null
  }
}

export function saveRecord(s: RecoveryScope, rec: SendIntentRecord) {
  if (!s.organizationId || !s.userId) return
  try {
    storage()?.setItem(recordStorageKey(s), JSON.stringify(rec))
  } catch {
    /* full or blocked: the in-page key still protects this page */
  }
}

export function clearRecord(s: RecoveryScope) {
  try {
    storage()?.removeItem(recordStorageKey(s))
  } catch {
    /* nothing to do */
  }
}

// The server's word for a send, as the state query returns it.
export type SendState = 'processing' | 'sent' | 'failed' | 'uncertain'

// Did the page get an answer that CLOSES the intention? Only the application's own words do:
// - a body that carries the send_id (sent / failed / uncertain are all answers about THAT send), or
// - an application error envelope in the 4xx range (key missing, not allowed, not found, key reused,
//   closed window): the request was refused before anything was sent under this key.
// Everything else is "no answer" for this purpose: a timeout, a dropped connection, and also a 5xx
// from a proxy or load balancer (502/503/504 while the backend restarts, an upstream reset): such a
// page says nothing about whether the backend claimed the key or reached the provider. The record
// stays, and the page asks the server instead of sending again.
export function answerClosesIntention(e: unknown): boolean {
  const r = (e as { response?: { status?: number; data?: any } } | null | undefined)?.response
  if (!r) return false
  const body = r.data
  if (typeof body?.data?.send_id === 'string' && body.data.send_id) return true
  const status = r.status ?? 0
  return status >= 400 && status < 500 && body && typeof body === 'object' && body.status === 'error'
}

// A different text while an earlier send of this case is still unresolved. Raised instead of minting a
// second key: that would send a second message while the first may be in flight or delivered.
export class ProtocolSendPendingError extends Error {
  constructor() {
    super('A protocol send for this case is still unresolved')
    this.name = 'ProtocolSendPendingError'
  }
}
