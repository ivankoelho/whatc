// Who may conclude (or reopen) a conversation, and what to tell them when the server says no.
//
// The backend (canSetContactStatus) accepts contacts:write, or chat:write for concluding/reopening. The
// button follows the same rule so that people who cannot use it do not see a button that always fails.
// The server still decides: this only avoids offering the action.

type HasPermission = (resource: string, action: string) => boolean

export function canChangeConversationStatus(has: HasPermission): boolean {
  return has('contacts', 'write') || has('chat', 'write')
}

// A 403 means "you may not", which is different from "it failed, try again": the second message would
// invite a retry that can never work.
export function statusChangeErrorKey(error: unknown): 'chat.statusChangeForbidden' | 'chat.statusChangeFailed' {
  const status = (error as { response?: { status?: number } } | null)?.response?.status
  return status === 403 ? 'chat.statusChangeForbidden' : 'chat.statusChangeFailed'
}
