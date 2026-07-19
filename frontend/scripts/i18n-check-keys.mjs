import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'

const here = dirname(fileURLToPath(import.meta.url))
const localesDir = join(here, '..', 'src', 'i18n', 'locales')

function flatten(obj, prefix = '', out = new Set()) {
  for (const [k, v] of Object.entries(obj)) {
    const key = prefix ? `${prefix}.${k}` : k
    if (v && typeof v === 'object' && !Array.isArray(v)) flatten(v, key, out)
    else out.add(key)
  }
  return out
}

const en = flatten(JSON.parse(readFileSync(join(localesDir, 'en.json'), 'utf8')))
const pt = flatten(JSON.parse(readFileSync(join(localesDir, 'pt-BR.json'), 'utf8')))

const missingInPt = [...en].filter(k => !pt.has(k)).sort()
const extraInPt = [...pt].filter(k => !en.has(k)).sort()

if (missingInPt.length || extraInPt.length) {
  if (missingInPt.length) {
    console.error(`\n❌ ${missingInPt.length} key(s) missing in pt-BR.json:`)
    missingInPt.forEach(k => console.error('  - ' + k))
  }
  if (extraInPt.length) {
    console.error(`\n❌ ${extraInPt.length} extra key(s) in pt-BR.json (not in en.json):`)
    extraInPt.forEach(k => console.error('  + ' + k))
  }
  process.exit(1)
}
console.log(`✅ en.json and pt-BR.json key sets match (${en.size} keys).`)
