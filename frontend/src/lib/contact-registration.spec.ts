import { describe, expect, it } from 'vitest'
import { CONTACT_TYPES, placementPayload, showsPlacement } from './contact-registration'

describe('showsPlacement', () => {
  it('is true only for a colaborador', () => {
    expect(showsPlacement('colaborador')).toBe(true)
    for (const type of ['cliente', 'fornecedor', '', undefined, null, 'Colaborador']) {
      expect(showsPlacement(type)).toBe(false)
    }
  })

  it('knows exactly one type with a placement among the contact types', () => {
    expect(CONTACT_TYPES.filter((t) => showsPlacement(t))).toEqual(['colaborador'])
  })
})

describe('placementPayload', () => {
  it('keeps the unit and department of a colaborador', () => {
    expect(placementPayload('colaborador', 'u1', 'd1')).toEqual({ unit_id: 'u1', department_id: 'd1' })
    expect(placementPayload('colaborador', '', 'd1')).toEqual({ unit_id: '', department_id: 'd1' })
  })

  it('clears both for any other type, whatever the form still holds', () => {
    for (const type of ['cliente', 'fornecedor']) {
      expect(placementPayload(type, 'u1', 'd1')).toEqual({ unit_id: '', department_id: '' })
    }
  })
})
