// Builds the recipient payloads the campaign import sends to
// POST /api/campaigns/{id}/recipients/import, from the manual-entry lines and
// from CSV text.
//
// valid_until (authentication templates only) is the instant until which the
// system that issued the one-time code says it is valid. The UI never invents
// it: it comes from a column/field the user fills in, or from one explicit
// value the user typed for the whole batch. Anything without an explicit
// offset is rejected, because "10:00" without a zone is ambiguous.

export interface RecipientPayload {
  phone_number: string
  recipient_name?: string
  template_params?: Record<string, any>
  header_params?: Record<string, any>
  valid_until?: string
}

export interface RecipientContext {
  headerName: string | null
  bodyNames: string[]
  /** Template category is AUTHENTICATION. */
  isAuth: boolean
  /** ISO-8601 UTC chosen explicitly by the user for recipients without their own. */
  defaultValidUntil?: string | null
}

export type ValidUntilResult = { iso: string; error?: undefined } | { iso?: undefined; error: string }

// RFC 3339 date-time with a mandatory zone: Z or +hh:mm / -hh:mm (+hhmm tolerated).
const RFC3339 = /^(\d{4})-(\d{2})-(\d{2})[T ](\d{2}):(\d{2})(?::(\d{2})(\.\d+)?)?(Z|[+-]\d{2}:?\d{2})$/i

/** Parses a user-typed validity and returns it as an ISO-8601 UTC string. */
export function parseValidUntil(raw: string, now: Date = new Date()): ValidUntilResult {
  const m = RFC3339.exec(raw.trim())
  if (!m) {
    return { error: 'valid_until must be RFC 3339 with a time zone, e.g. 2030-01-01T12:05:00Z or 2030-01-01T09:05:00-03:00' }
  }
  const [, y, mo, d, h, mi, s = '00', frac = '', zone] = m
  const z = zone.toUpperCase() === 'Z' ? 'Z' : zone.length === 5 ? `${zone.slice(0, 3)}:${zone.slice(3)}` : zone
  const date = new Date(`${y}-${mo}-${d}T${h}:${mi}:${s}${frac}${z}`)
  if (Number.isNaN(date.getTime())) return { error: 'valid_until is not a valid date' }
  if (date.getTime() <= now.getTime()) return { error: 'valid_until is already in the past' }
  return { iso: date.toISOString() }
}

/** Converts a <input type="datetime-local"> value (the user's own zone) to ISO UTC. */
export function localInputToUTC(value: string): string | null {
  if (!value) return null
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? null : date.toISOString()
}

/**
 * Checks the batch validity typed in the datetime-local field. The server stays
 * the authority (it refuses a past valid_until); this only tells the user early.
 * null = fine (or empty, which means "no validity informed").
 */
export function batchValidUntilError(local: string, now: Date = new Date()): 'invalid' | 'past' | null {
  if (!local) return null
  const iso = localInputToUTC(local)
  if (!iso) return 'invalid'
  return new Date(iso).getTime() <= now.getTime() ? 'past' : null
}

/** Column position of valid_until in a manual-entry line (after the template values). */
export function manualValidUntilIndex(ctx: RecipientContext): number {
  return 2 + (ctx.headerName ? 1 : 0) + ctx.bodyNames.length
}

/**
 * Manual layout: phone, name, <header value if any>, <body_1>..<body_n>[, valid_until].
 * Header and body values go to separate keys so a positional header {{1}} never
 * collides with body {{1}}. The trailing valid_until exists only for
 * authentication templates.
 */
export function buildManualRecipients(lines: string[], ctx: RecipientContext): RecipientPayload[] {
  return lines.map((line) => {
    const parts = line.split(',').map((p) => p.trim())
    const recipient: RecipientPayload = { phone_number: parts[0].replace(/[^\d+]/g, '') }
    if (parts[1]?.trim()) recipient.recipient_name = parts[1].trim()

    let cursor = 2
    if (ctx.headerName) {
      const val = parts[cursor]
      if (val && val.length > 0) recipient.header_params = { [ctx.headerName]: val }
      cursor++
    }

    const params: Record<string, any> = {}
    for (let i = 0; i < ctx.bodyNames.length; i++) {
      const val = parts[cursor + i]
      if (val && val.length > 0) params[ctx.bodyNames[i]] = val
    }
    if (Object.keys(params).length > 0) recipient.template_params = params

    if (ctx.isAuth) {
      const raw = parts[manualValidUntilIndex(ctx)]
      const parsed = raw ? parseValidUntil(raw) : null
      const iso = parsed?.iso ?? ctx.defaultValidUntil ?? undefined
      if (iso) recipient.valid_until = iso
    }
    return recipient
  })
}

export interface CsvRowError {
  line: number
  reason: string
}

export interface CsvBuildResult {
  recipients: RecipientPayload[]
  /** 'empty': no data rows; 'missing_phone': no phone column; 'no_valid_rows': no row had a phone; 'invalid_rows': see rowErrors. */
  error?: 'empty' | 'missing_phone' | 'no_valid_rows' | 'invalid_rows'
  rowErrors: CsvRowError[]
}

/**
 * CSV with a header row. Besides phone/name/header/body columns, an
 * authentication template accepts a valid_until column. That column is
 * reserved before body parameters are matched by position, so it can never be
 * mistaken for a parameter. Templates that are not authentication behave
 * exactly as before: a column called valid_until is just another column.
 */
export function buildCsvRecipients(text: string, ctx: RecipientContext, now: Date = new Date()): CsvBuildResult {
  const lines = text.split('\n').filter((line) => line.trim())
  if (lines.length < 2) return { recipients: [], error: 'empty', rowErrors: [] }

  const headers = lines[0].split(',').map((h) => h.toLowerCase().trim())
  const phoneIndex = headers.findIndex(
    (h) => h === 'phone' || h === 'phone_number' || h === 'phonenumber' || h === 'mobile' || h === 'number',
  )
  if (phoneIndex === -1) return { recipients: [], error: 'missing_phone', rowErrors: [] }

  const nameIndex = headers.findIndex(
    (h) => h === 'name' || h === 'recipient_name' || h === 'recipientname' || h === 'customer_name',
  )

  // Header parameter column — accept the reserved name 'header', or the
  // variable's name itself when unambiguous (named templates only).
  const headerName = ctx.headerName
  let headerColIdx = -1
  if (headerName) {
    headerColIdx = headers.findIndex((h) => h === 'header')
    if (headerColIdx === -1 && !/^\d+$/.test(headerName)) {
      headerColIdx = headers.findIndex((h) => h === headerName.toLowerCase())
    }
  }

  const validUntilIdx = ctx.isAuth ? headers.findIndex((h) => h === 'valid_until') : -1

  const recipientsList: RecipientPayload[] = []
  const rowErrors: CsvRowError[] = []

  for (let i = 1; i < lines.length; i++) {
    const values = lines[i].split(',').map((v) => v.trim())
    const phone = values[phoneIndex]?.replace(/[^\d+]/g, '')
    if (!phone) continue

    const recipient: RecipientPayload = { phone_number: phone }
    if (nameIndex !== -1 && values[nameIndex]?.trim()) recipient.recipient_name = values[nameIndex].trim()

    const usedIndices = new Set<number>([phoneIndex])
    if (nameIndex !== -1) usedIndices.add(nameIndex)
    if (validUntilIdx !== -1) usedIndices.add(validUntilIdx)

    if (headerName && headerColIdx !== -1) {
      const v = values[headerColIdx]?.trim()
      if (v) recipient.header_params = { [headerName]: v }
      usedIndices.add(headerColIdx)
    }

    if (ctx.bodyNames.length > 0) {
      const params: Record<string, any> = {}

      // Try name matching first
      for (const paramName of ctx.bodyNames) {
        const colIdx = headers.findIndex((h, idx) => !usedIndices.has(idx) && h === paramName.toLowerCase())
        if (colIdx !== -1) {
          params[paramName] = values[colIdx]?.trim() || ''
          usedIndices.add(colIdx)
        }
      }

      // Positional fallback for unmapped params
      const unmapped = ctx.bodyNames.filter((p) => !(p in params))
      const remainingCols = headers.map((_, idx) => idx).filter((idx) => !usedIndices.has(idx))
      for (let j = 0; j < unmapped.length && j < remainingCols.length; j++) {
        params[unmapped[j]] = values[remainingCols[j]]?.trim() || ''
      }

      if (Object.keys(params).length > 0) recipient.template_params = params
    }

    if (ctx.isAuth) {
      const raw = validUntilIdx !== -1 ? values[validUntilIdx] : ''
      if (raw) {
        const parsed = parseValidUntil(raw, now)
        if (parsed.error !== undefined) {
          rowErrors.push({ line: i + 1, reason: parsed.error })
          continue
        }
        recipient.valid_until = parsed.iso
      } else if (ctx.defaultValidUntil) {
        recipient.valid_until = ctx.defaultValidUntil
      }
    }

    recipientsList.push(recipient)
  }

  if (rowErrors.length > 0) return { recipients: [], error: 'invalid_rows', rowErrors }
  if (recipientsList.length === 0) return { recipients: [], error: 'no_valid_rows', rowErrors: [] }
  return { recipients: recipientsList, rowErrors: [] }
}
