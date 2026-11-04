// What the interface offers for writing to the customer from a case. The server
// decides (chat:write plus the scope of the case's channel) and says so in the case's
// `can` map; the interface only follows it, so it never shows a button the server
// would refuse. A missing answer means "no".

export type CaseCapabilities = Record<string, boolean> | undefined

// The "Enviar protocolo" action in the case header.
export function canSendProtocol(can: CaseCapabilities): boolean {
  return can?.send_protocol === true
}

// Sending the registration message right after a protocol was opened goes through the
// same endpoint (free text). The case was just created by this person, so the server's
// scope check is not known yet; the permission to write to customers is the part the
// interface can know, and a refusal on the scope still reaches the person as the
// server's message.
export function canSendRegistrationMessage(hasChatWrite: boolean): boolean {
  return hasChatWrite
}

// The outcome of a protocol send, as the server reports it in the body (the HTTP status
// alone cannot tell them apart: 200 sent, 202 not confirmed, 502 proven failure).
export type SendOutcome = 'sent' | 'uncertain'

// 202 with result "uncertain": the provider did not confirm the message and it may have
// been delivered. That is NOT a success and NOT a failure: the interface says it is
// pending confirmation, never offers to send it again, and sends nothing by itself.
export function sendOutcome(responseData: unknown): SendOutcome {
  const result = (responseData as { data?: { result?: unknown } } | null | undefined)?.data?.result
  // 'processing' = the same send is still in flight on the server: also not a success yet
  return result === 'uncertain' || result === 'processing' ? 'uncertain' : 'sent'
}
