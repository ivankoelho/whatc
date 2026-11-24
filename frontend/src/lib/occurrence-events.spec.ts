import { describe, expect, it } from 'vitest'
import { eventLabelKey } from './occurrence-events'

describe('eventLabelKey', () => {
  it('uses the same label for every case by default', () => {
    expect(eventLabelKey('opened')).toBe('occurrences.eventOpened')
    expect(eventLabelKey('closed', false)).toBe('occurrences.eventClosed')
  })

  it('speaks of "chamado" for internal tickets, only where the wording differs', () => {
    expect(eventLabelKey('opened', true)).toBe('occurrences.eventOpenedInternal')
    expect(eventLabelKey('closed', true)).toBe('occurrences.eventClosedInternal')
    expect(eventLabelKey('returned', true)).toBe('occurrences.eventReturnedInternal')
    expect(eventLabelKey('note', true)).toBe('occurrences.eventNote')
  })

  it('falls back for unknown event types', () => {
    expect(eventLabelKey('nope', true)).toBe('occurrences.eventUnknown')
  })
})
