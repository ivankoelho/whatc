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
