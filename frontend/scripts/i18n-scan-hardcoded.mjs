import { readFileSync, readdirSync, statSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join, relative } from 'node:path'

const here = dirname(fileURLToPath(import.meta.url))
const srcDir = join(here, '..', 'src')
const allowlist = JSON.parse(readFileSync(join(here, 'i18n-allowlist.json'), 'utf8'))
const allowSet = new Set(allowlist.strings)
const allowFiles = new Set(allowlist.files)

// Optional path filter: `node i18n-scan-hardcoded.mjs components/chatbot/nodes`
const filters = process.argv.slice(2)

function walk(dir, files = []) {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name)
    if (statSync(p).isDirectory()) {
      if (name === 'ui') continue // shadcn primitives: no copy
      walk(p, files)
    } else if (name.endsWith('.vue')) {
      files.push(p)
    }
  }
  return files
}

// Text between tags: >Some Text<  (letters, not a binding {{ }})
const textRe = />\s*([A-Za-z][A-Za-z0-9 ,.'!?&/()-]{2,})\s*</g
// Literal attributes: placeholder="Foo" title="Foo" label="Foo" aria-label="Foo"
const attrRe = /\s(placeholder|title|label|aria-label)="([A-Za-z][^"]{2,})"/g

let total = 0
for (const file of walk(srcDir)) {
  const rel = relative(join(here, '..'), file).replace(/\\/g, '/')
  if (allowFiles.has(rel)) continue
  if (filters.length && !filters.some(f => rel.includes(f.replace(/\\/g, '/')))) continue
  const src = readFileSync(file, 'utf8')
  const hits = []
  let m
  while ((m = textRe.exec(src))) {
    const s = m[1].trim()
    // Skip bindings and code fragments (e.g. `=> fetchItems()` bleeding into
    // the next tag): real UI copy never contains `{{`, `()`, or `=>`.
    if (s.includes('{{') || s.includes('()') || s.includes('=>') || allowSet.has(s)) continue
    hits.push(s)
  }
  while ((m = attrRe.exec(src))) {
    if (allowSet.has(m[2])) continue
    hits.push(`${m[1]}="${m[2]}"`)
  }
  if (hits.length) {
    total += hits.length
    console.log(`\n${rel} (${hits.length})`)
    hits.forEach(h => console.log('  • ' + h))
  }
}
if (total) {
  console.error(`\n❌ ${total} hardcoded string(s) found.`)
  process.exit(1)
}
console.log('✅ No hardcoded strings found outside allowlist.')
