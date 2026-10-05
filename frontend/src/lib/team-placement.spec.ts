import { describe, expect, it } from 'vitest'
import type { Team } from '@/services/api'
import { teamsSharingPlacement } from './team-placement'

const team = (id: string, unit?: string, dept?: string, active = true) =>
  ({ id, name: id, unit_id: unit, department_id: dept, is_active: active }) as Team

describe('teamsSharingPlacement', () => {
  const teams = [team('a', 'u1', 'd1'), team('b', 'u1', 'd1'), team('c', 'u1', 'd2'), team('d', 'u2', 'd1'), team('e', 'u1', 'd1', false), team('f')]

  it('lists the other active teams with the same unit and department', () => {
    expect(teamsSharingPlacement(teams, 'u1', 'd1', 'a').map(t => t.id)).toEqual(['b'])
    expect(teamsSharingPlacement(teams, 'u1', 'd1').map(t => t.id)).toEqual(['a', 'b'])
  })

  it('ignores inactive teams, other pairs and the team itself', () => {
    expect(teamsSharingPlacement(teams, 'u1', 'd2', 'c')).toEqual([])
    expect(teamsSharingPlacement(teams, 'u2', 'd1', 'd')).toEqual([])
  })

  it('needs both fields to form a pair', () => {
    expect(teamsSharingPlacement(teams, 'u1', undefined)).toEqual([])
    expect(teamsSharingPlacement(teams, undefined, 'd1')).toEqual([])
    expect(teamsSharingPlacement(teams, '', '')).toEqual([])
  })
})
