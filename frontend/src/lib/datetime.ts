// One place for how the application shows dates and times.
//
// pt-BR (the default): DD/MM/YYYY, HH:mm in 24 hours, seconds only where a log asks for them (audit).
// en (and any other locale): the regional format the app always used (e.g. "Oct 26, 2026 10:05 AM").
//
// What is stored and transported is never touched: the API sends instants as RFC 3339 with an explicit offset
// (UTC, "Z"), and this module only decides how an instant is DISPLAYED, in the organization's time zone.
//
// Time zone: the organization's configured zone (Settings → General → Default time zone) is applied with
// setDisplayTimeZone() once the settings are known. Until then, or if the configured name is not a valid IANA
// zone, the browser's own zone is used. A pure calendar date (a purchase date, a due day: "YYYY-MM-DD", or the
// midnight-UTC instant the API returns for one) is NOT an instant and is never shifted: use formatCivilDate().

let displayTimeZone: string | undefined

function validZone(tz?: string): string | undefined {
  if (!tz) return undefined
  try {
    new Intl.DateTimeFormat('en', { timeZone: tz })
    return tz
  } catch {
    return undefined
  }
}

/** Sets the zone dates are shown in; an empty or invalid name falls back to the browser's zone. */
export function setDisplayTimeZone(tz?: string) {
  displayTimeZone = validZone(tz)
}

export function getDisplayTimeZone(): string {
  return displayTimeZone ?? Intl.DateTimeFormat().resolvedOptions().timeZone
}

/** pt-BR for Portuguese, otherwise English. <html lang> is kept in sync by the i18n module. */
export function displayLocale(): 'pt-BR' | 'en-US' {
  const lang = typeof document !== 'undefined' ? document.documentElement.getAttribute('lang') || '' : ''
  return lang.toLowerCase().startsWith('pt') ? 'pt-BR' : 'en-US'
}

type Input = string | number | Date | null | undefined

function toDate(v: Input): Date | null {
  if (v === null || v === undefined || v === '') return null
  const d = v instanceof Date ? v : new Date(v)
  return Number.isNaN(d.getTime()) ? null : d
}

function ptParts(d: Date, seconds: boolean) {
  const f = new Intl.DateTimeFormat('pt-BR', {
    timeZone: displayTimeZone, day: '2-digit', month: '2-digit', year: 'numeric',
    hour: '2-digit', minute: '2-digit', second: seconds ? '2-digit' : undefined, hourCycle: 'h23',
  })
  const p: Record<string, string> = {}
  for (const x of f.formatToParts(d)) p[x.type] = x.value
  return p
}

/** DD/MM/YYYY (pt-BR) or "Oct 26, 2026" (en). */
export function formatDate(value: Input, options?: Intl.DateTimeFormatOptions): string {
  const d = toDate(value)
  if (!d) return ''
  if (displayLocale() === 'pt-BR' && !options) {
    const p = ptParts(d, false)
    return `${p.day}/${p.month}/${p.year}`
  }
  return d.toLocaleDateString(displayLocale(), { timeZone: displayTimeZone, year: 'numeric', month: 'short', day: 'numeric', ...options })
}

/** HH:mm in 24 hours (pt-BR) or "10:05 AM" (en). */
export function formatTime(value: Input): string {
  const d = toDate(value)
  if (!d) return ''
  if (displayLocale() === 'pt-BR') {
    const p = ptParts(d, false)
    return `${p.hour}:${p.minute}`
  }
  return d.toLocaleTimeString('en-US', { timeZone: displayTimeZone, hour: '2-digit', minute: '2-digit' })
}

/** DD/MM/YYYY HH:mm (pt-BR) or "Oct 26, 2026 10:05 AM" (en). */
export function formatDateTime(value: Input): string {
  const d = toDate(value)
  if (!d) return ''
  if (displayLocale() === 'pt-BR') {
    const p = ptParts(d, false)
    return `${p.day}/${p.month}/${p.year} ${p.hour}:${p.minute}`
  }
  return `${formatDate(d)} ${formatTime(d)}`
}

/** With seconds: only for audit trails, where the exact second matters. */
export function formatDateTimeSeconds(value: Input): string {
  const d = toDate(value)
  if (!d) return ''
  if (displayLocale() === 'pt-BR') {
    const p = ptParts(d, true)
    return `${p.day}/${p.month}/${p.year} ${p.hour}:${p.minute}:${p.second}`
  }
  return d.toLocaleString('en-US', { timeZone: displayTimeZone, year: 'numeric', month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', second: '2-digit' })
}

/**
 * A calendar date with no time of day ("2026-10-20", or the midnight-UTC instant that stands for it). The
 * parts are read as written, in UTC, so the day never slides back in zones west of Greenwich.
 */
export function formatCivilDate(value: Input): string {
  const d = toDate(typeof value === 'string' && /^\d{4}-\d{2}-\d{2}$/.test(value) ? `${value}T00:00:00Z` : value)
  if (!d) return ''
  const dd = String(d.getUTCDate()).padStart(2, '0')
  const mm = String(d.getUTCMonth() + 1).padStart(2, '0')
  if (displayLocale() === 'pt-BR') return `${dd}/${mm}/${d.getUTCFullYear()}`
  return d.toLocaleDateString('en-US', { timeZone: 'UTC', year: 'numeric', month: 'short', day: 'numeric' })
}

/** A day without the year, for chart axes: DD/MM (pt-BR) or "Oct 26" (en). */
export function formatShortDate(value: Input): string {
  const d = toDate(value)
  if (!d) return ''
  if (displayLocale() === 'pt-BR') {
    const p = ptParts(d, false)
    return `${p.day}/${p.month}`
  }
  return d.toLocaleDateString('en-US', { timeZone: displayTimeZone, month: 'short', day: 'numeric' })
}

/** A month and year, for chart axes: MM/YYYY (pt-BR) or "Oct 2026" (en). */
export function formatMonthYear(value: Input): string {
  const d = toDate(value)
  if (!d) return ''
  if (displayLocale() === 'pt-BR') {
    const p = ptParts(d, false)
    return `${p.month}/${p.year}`
  }
  return d.toLocaleDateString('en-US', { timeZone: displayTimeZone, month: 'short', year: 'numeric' })
}
