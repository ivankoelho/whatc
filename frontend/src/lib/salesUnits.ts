// Closed list of units an opportunity's quantities can use. Mirrors
// models.SalesUnitsOfMeasure on the backend, which is the source of truth and
// rejects anything else; labels live in i18n under sales.units.<code>.
export const SALES_UNITS = ['UN', 'M', 'M2', 'KG', 'PCT', 'CX'] as const
export type SalesUnitOfMeasure = (typeof SALES_UNITS)[number]

// Quantities are numeric(14,3): at most 3 decimals.
export function formatQuantity(quantity?: number | null, unit?: string | null): string {
  if (quantity == null) return '—'
  const n = new Intl.NumberFormat('pt-BR', { maximumFractionDigits: 3 }).format(quantity)
  return unit ? `${n} ${unit === 'M2' ? 'm²' : unit}` : n
}

// Parses what a user types in a decimal field ("12,5" or "12.5").
// '' -> undefined (not filled in); anything unparseable or negative -> NaN.
export function parseDecimalInput(raw: string): number | undefined {
  const s = raw.trim().replace(',', '.')
  if (s === '') return undefined
  const n = Number(s)
  return Number.isFinite(n) && n >= 0 ? n : NaN
}
