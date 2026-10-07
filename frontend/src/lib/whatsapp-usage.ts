// Presentation helpers of the WhatsApp consumption screens. Amounts always carry their own
// currency: figures in different currencies are never added together.

/** Formats an amount in its currency. Per-message prices are tiny (0.0068), so up to 4 decimals. */
export function formatMoney(amount: number, currency: string, locale = 'pt-BR'): string {
  if (!currency) return new Intl.NumberFormat(locale, { minimumFractionDigits: 2, maximumFractionDigits: 4 }).format(amount)
  try {
    return new Intl.NumberFormat(locale, { style: 'currency', currency, minimumFractionDigits: 2, maximumFractionDigits: 4 }).format(amount)
  } catch {
    return `${currency} ${amount}`
  }
}

/** YYYY-MM-DD of a date in the browser's local calendar. */
export function isoDay(d: Date): string {
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`
}

/** The last `days` days up to today (inclusive), as the from/to query params. */
export function lastDays(days: number, now = new Date()): { from: string; to: string } {
  const start = new Date(now.getFullYear(), now.getMonth(), now.getDate() - (days - 1))
  return { from: isoDay(start), to: isoDay(now) }
}

/** The calendar day of a stored date/timestamp ("2026-01-01T00:00:00Z" → "2026-01-01"). */
export function dayOf(value?: string | null): string {
  return value ? value.slice(0, 10) : ''
}

export const USAGE_GROUPS = ['day', 'category', 'unit', 'agent', 'account', 'actor'] as const
export type UsageGroupBy = (typeof USAGE_GROUPS)[number]

/** Sentinel of the filter selects: the Select component cannot hold an empty value. */
export const ALL = '__all__'

/** Drops the "all" sentinel and empty values, so only real filters reach the query string. */
export function activeFilters<T extends Record<string, unknown>>(filters: T): Partial<T> {
  const out: Partial<T> = {}
  for (const [k, v] of Object.entries(filters)) {
    if (v !== ALL && v !== '' && v !== undefined && v !== null) (out as Record<string, unknown>)[k] = v
  }
  return out
}
