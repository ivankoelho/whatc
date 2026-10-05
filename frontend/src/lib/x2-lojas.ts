import type { XProcessLoja } from '@/services/api'

// Search an X2 store list by code, company name or CNPJ (punctuation ignored).
export function filterLojas(lojas: XProcessLoja[], query: string): XProcessLoja[] {
  const q = query.trim().toLowerCase()
  if (!q) return lojas
  const digits = q.replace(/\D/g, '')
  return lojas.filter(l =>
    l.cod_empresa.toLowerCase().includes(q) ||
    l.razao_social_empresa.toLowerCase().includes(q) ||
    (l.import_name ?? '').toLowerCase().includes(q) ||
    (digits !== '' && l.cnpj_empresa.replace(/\D/g, '').includes(digits))
  )
}

// Only a store with no unit yet and no clash can be imported.
export const isImportable = (l: XProcessLoja) => l.import_status === 'new'
