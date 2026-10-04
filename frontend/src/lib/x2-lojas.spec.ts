import { describe, expect, it } from 'vitest'
import type { XProcessLoja } from '@/services/api'
import { filterLojas, isImportable } from './x2-lojas'

const l = (cod: string, razao: string, cnpj: string, st: XProcessLoja['import_status'] = 'new', name = ''): XProcessLoja =>
  ({ cod_empresa: cod, razao_social_empresa: razao, cnpj_empresa: cnpj, import_status: st, import_name: name })

const lojas = [
  l('2', 'ATACADAO LTDA (FEIRA)', '58.675.622/0001-08', 'already_imported', 'FEIRA'),
  l('3', 'ATACADAO LTDA (SERRINHA)', '58.675.622/0005-23', 'new', 'SERRINHA'),
  l('4', 'ATACADAO LTDA (PETROLINA)', '58.675.622/0003-61', 'conflict', 'PETROLINA'),
]

describe('filterLojas', () => {
  it('returns all stores for an empty query', () => expect(filterLojas(lojas, ' ')).toHaveLength(3))
  it('matches the code, the name and the CNPJ with or without punctuation', () => {
    expect(filterLojas(lojas, '3').map(x => x.cod_empresa)).toContain('3')
    expect(filterLojas(lojas, 'petrolina').map(x => x.cod_empresa)).toEqual(['4'])
    expect(filterLojas(lojas, '58675622000523').map(x => x.cod_empresa)).toEqual(['3'])
    expect(filterLojas(lojas, '0005-23').map(x => x.cod_empresa)).toEqual(['3'])
  })
  it('does not match everything when the query has no digits', () => {
    expect(filterLojas(lojas, 'zzz')).toEqual([])
  })
})

describe('isImportable', () => {
  it('only new stores', () => expect(lojas.map(isImportable)).toEqual([false, true, false]))
})
