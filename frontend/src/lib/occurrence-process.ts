import type { OccurrenceProcess } from '@/services/api'

export function isProcessFieldRequired(process: OccurrenceProcess | null, key: string): boolean {
  return process?.required_fields.includes(key) ?? false
}

export function missingProcessFields(
  process: OccurrenceProcess | null,
  values: Record<string, string | undefined>,
): string[] {
  if (!process) return []
  return process.required_fields.filter((key) => !(values[key] ?? '').trim())
}

// The six variables the backend substitutes; it leaves one literal when its value is missing.
const PROCESS_VARIABLE = /\[(?:Nome|Atendente|Protocolo|NF|Prazo|Loja)\]/g

export function unresolvedPlaceholders(text: string): string[] {
  return [...new Set(text.match(PROCESS_VARIABLE) ?? [])]
}
