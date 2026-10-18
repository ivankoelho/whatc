import { describe, it, expect } from 'vitest'
import { parseKnowledgeBody, slugify, readingMinutes } from './knowledge-reader'

describe('parseKnowledgeBody', () => {
  it('reads a flattened manual: intro, subtitles, merged table rows', () => {
    const body = [
      'Resposta curta à pergunta: há três variáveis automáticas.',
      'Variáveis de sessão do fluxo ({{nome}})',
      'Variável | De onde vem |',
      '{{phone_number}} | Automática. O telefone da conversa. |',
      '{{contact_name}} | Automática. O nome de perfil. |',
      'Campos do cadastro do contato',
      'O texto final explica o resto.',
    ].join('\n\n')

    const { blocks, toc } = parseKnowledgeBody(body)
    expect(blocks.map(b => b.kind)).toEqual(['paragraph', 'heading', 'table', 'heading', 'paragraph'])
    expect(toc.map(t => t.text)).toEqual(['Variáveis de sessão do fluxo ({{nome}})', 'Campos do cadastro do contato'])
    const table = blocks[2]
    expect(table.kind === 'table' && table.header).toEqual(['Variável', 'De onde vem'])
    expect(table.kind === 'table' && table.rows.length).toBe(2)
  })

  it('takes Markdown headings and lists, with unique ids', () => {
    const { blocks, toc } = parseKnowledgeBody('# Passos\n\n1. Abrir\n2. Salvar\n\n## Passos\n\n- um\n- dois')
    expect(toc.map(t => [t.id, t.level])).toEqual([['passos', 2], ['passos-2', 2]])
    expect(blocks[1]).toEqual({ kind: 'list', ordered: true, items: ['Abrir', 'Salvar'] })
    expect(blocks[3]).toEqual({ kind: 'list', ordered: false, items: ['um', 'dois'] })
  })

  it('folds a cell that contained "|" back into the last column', () => {
    const { blocks } = parseKnowledgeBody('Intro.\n\nOnde | Variáveis |\n\nAções | {{user.id | name}} e mais |')
    const table = blocks[1]
    expect(table.kind === 'table' && table.rows[0]).toEqual(['Ações', '{{user.id | name}} e mais'])
  })

  it('does not promote sentences or a dangling last line to headings', () => {
    const { toc } = parseKnowledgeBody('Primeiro parágrafo.\n\nOutra frase que termina com ponto.\n\nÚltima linha solta')
    expect(toc).toEqual([])
  })
})

describe('helpers', () => {
  it('slugifies accents and punctuation', () => {
    expect(slugify('Variáveis de sessão ({{nome}})')).toBe('variaveis-de-sessao-nome')
  })
  it('estimates at least one minute', () => {
    expect(readingMinutes('curto')).toBe(1)
    expect(readingMinutes(Array(600).fill('palavra').join(' '))).toBe(3)
  })
})
