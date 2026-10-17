import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join, relative } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

// Guard: nothing in the frontend renders a string as HTML. Messages, names and reasons
// that people type reach other people's screens, so the only safe rendering is text
// interpolation. If a real need for raw HTML ever appears, it must come with its own
// sanitizer and its own review, and this test is where that conversation starts.

const SRC = fileURLToPath(new URL('..', import.meta.url))

function files(dir: string): string[] {
  return readdirSync(dir).flatMap(name => {
    const p = join(dir, name)
    if (statSync(p).isDirectory()) return name === 'node_modules' ? [] : files(p)
    return /\.(vue|ts)$/.test(name) && !/\.spec\.ts$/.test(name) ? [p] : []
  })
}

const FORBIDDEN: Array<[string, RegExp]> = [
  ['v-html', /v-html\s*=/],
  ['innerHTML assignment', /\.innerHTML\s*=(?!=)/],
  ['outerHTML assignment', /\.outerHTML\s*=(?!=)/],
  ['insertAdjacentHTML', /insertAdjacentHTML\s*\(/],
  ['document.write', /document\.write\s*\(/],
  ['h() with innerHTML', /innerHTML\s*:/]
]

describe('raw HTML rendering', () => {
  it('appears nowhere in the frontend sources', () => {
    const offenders: string[] = []
    for (const file of files(SRC)) {
      const text = readFileSync(file, 'utf8')
      for (const [name, re] of FORBIDDEN) {
        if (re.test(text)) offenders.push(`${relative(SRC, file)}: ${name}`)
      }
    }
    expect(offenders).toEqual([])
  })

  it('actually scans the chat files (so a green result means something)', () => {
    const all = files(SRC).map(f => relative(SRC, f).replace(/\\/g, '/'))
    expect(all).toContain('components/team-chat/MessagePanel.vue')
    expect(all).toContain('stores/teamChat.ts')
    expect(all.length).toBeGreaterThan(100)
  })
})
