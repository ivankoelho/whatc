// Turns a knowledge document's plain-text body into readable blocks plus a
// table of contents, for the article reader. Bodies arrive as text: imported
// manuals are HTML flattened to paragraphs (blank-line separated, tables as
// "a | b |" lines), hand-written ones may be Markdown. Nothing here emits
// HTML — the reader renders the blocks with normal elements, so text stays
// escaped.

export type ReaderBlock =
  | { kind: 'heading'; level: 2 | 3; text: string; id: string }
  | { kind: 'paragraph'; lines: string[] }
  | { kind: 'list'; ordered: boolean; items: string[] }
  | { kind: 'table'; header: string[]; rows: string[][] }

export interface TocEntry {
  id: string
  text: string
  level: 2 | 3
}

const LIST_ITEM = /^\s*(?:[-*•]|\d+[.)])\s+/
const ORDERED_ITEM = /^\s*\d+[.)]\s+/
const MD_HEADING = /^(#{1,6})\s+(.+?)\s*#*\s*$/

export function slugify(text: string): string {
  return text
    .normalize('NFD')
    .replace(/[̀-ͯ]/g, '')
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 60) || 'secao'
}

function isTableLine(line: string): boolean {
  return line.includes('|') && line.trim().endsWith('|')
}

// A value that itself contained "|" shows up as extra cells; fold them back
// into the last column so the table keeps its header's shape.
function fitRow(row: string[], width: number): string[] {
  if (width < 1 || row.length <= width) return row
  return [...row.slice(0, width - 1), row.slice(width - 1).join(' | ')]
}

function cells(line: string): string[] {
  const parts = line.split('|').map(c => c.trim())
  if (parts.at(-1) === '') parts.pop()
  if (parts[0] === '' && line.trim().startsWith('|')) parts.shift()
  return parts
}

// A flattened manual's subtitle: one short line that doesn't read like a
// sentence (no closing punctuation) and isn't a list item or table row.
function looksLikeHeading(block: string): boolean {
  if (block.includes('\n')) return false
  const line = block.trim()
  return line.length > 0 && line.length <= 80 &&
    !/[.:;,!?…]$/.test(line) && !LIST_ITEM.test(line) && !isTableLine(line)
}

export function parseKnowledgeBody(body: string): { blocks: ReaderBlock[]; toc: TocEntry[] } {
  const raw = body.replace(/\r\n/g, '\n').split(/\n\s*\n/).map(b => b.trim()).filter(Boolean)
  const blocks: ReaderBlock[] = []
  const used = new Map<string, number>()
  const uniqueId = (text: string) => {
    const base = slugify(text)
    const n = used.get(base) ?? 0
    used.set(base, n + 1)
    return n === 0 ? base : `${base}-${n + 1}`
  }

  for (let i = 0; i < raw.length; i++) {
    const block = raw[i]
    const lines = block.split('\n').map(l => l.trimEnd())

    const md = lines.length === 1 ? MD_HEADING.exec(lines[0].trim()) : null
    if (md) {
      const text = md[2].trim()
      blocks.push({ kind: 'heading', level: md[1].length <= 2 ? 2 : 3, text, id: uniqueId(text) })
      continue
    }

    if (lines.every(isTableLine)) {
      // Flattened tables put one row per block; merge consecutive ones.
      const prev = blocks.at(-1)
      const rows = lines.filter(l => !/^\s*\|?\s*:?-{2,}/.test(l)).map(cells) // drop Markdown "---" rules
      if (prev?.kind === 'table' && raw[i - 1] && raw[i - 1].split('\n').every(isTableLine)) {
        prev.rows.push(...rows.map(r => fitRow(r, prev.header.length)))
      } else {
        const [header, ...rest] = rows
        blocks.push({ kind: 'table', header, rows: rest.map(r => fitRow(r, header.length)) })
      }
      continue
    }

    if (lines.every(l => LIST_ITEM.test(l))) {
      blocks.push({
        kind: 'list',
        ordered: lines.every(l => ORDERED_ITEM.test(l)),
        items: lines.map(l => l.replace(LIST_ITEM, '')),
      })
      continue
    }

    // A subtitle only counts when something follows it (not a dangling last line).
    if (i < raw.length - 1 && i > 0 && looksLikeHeading(block)) {
      const text = block.trim()
      blocks.push({ kind: 'heading', level: 2, text, id: uniqueId(text) })
      continue
    }

    blocks.push({ kind: 'paragraph', lines })
  }

  const toc = blocks
    .filter((b): b is Extract<ReaderBlock, { kind: 'heading' }> => b.kind === 'heading')
    .map(({ id, text, level }) => ({ id, text, level }))
  return { blocks, toc }
}

/** Minutes to read at ~200 words per minute, at least 1. */
export function readingMinutes(body: string): number {
  return Math.max(1, Math.round(body.split(/\s+/).filter(Boolean).length / 200))
}
