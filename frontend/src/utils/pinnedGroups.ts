import type { Contact } from '@/stores/contacts'

export const MAX_PINNED = 3 // mirrors the backend limit; the server is the enforcer

// Splits an already-sorted conversation list into the pinned group and the
// rest. While searching there is no group (a search is one flat result list).
export function splitPinned(sorted: Contact[], searching: boolean): { pinned: Contact[]; rest: Contact[] } {
  if (searching) return { pinned: [], rest: sorted }
  return { pinned: sorted.filter(c => c.is_pinned), rest: sorted.filter(c => !c.is_pinned) }
}

// "n/3" is only truthful on the unfiltered list: under a filter the group shows
// just the pins that match, so the counter would undercount.
export function pinnedCounterVisible(statusFilter: string, tagCount: number): boolean {
  return statusFilter === 'all' && tagCount === 0
}
