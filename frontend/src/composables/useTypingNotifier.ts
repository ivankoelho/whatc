import { onUnmounted } from 'vue'
import { contactsService } from '@/services/api'

const THROTTLE_MS = 2500

/**
 * Notifies other agents that the current user is typing, at most once every
 * 2.5s per contact. Errors are swallowed on purpose: a presence indicator must
 * never block the composer.
 */
export function useTypingNotifier() {
  let lastSentAt = 0
  let lastContactId = ''

  function notifyTyping(contactId: string) {
    if (!contactId) return

    const now = Date.now()
    // Switching contacts resets the throttle so the first keystroke in a new
    // conversation is announced immediately.
    if (contactId !== lastContactId) {
      lastContactId = contactId
      lastSentAt = 0
    }
    if (now - lastSentAt < THROTTLE_MS) return

    lastSentAt = now
    contactsService.notifyTyping(contactId).catch(() => {})
  }

  onUnmounted(() => {
    lastSentAt = 0
    lastContactId = ''
  })

  return { notifyTyping }
}
