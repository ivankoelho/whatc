// @vitest-environment happy-dom
import { describe, expect, it, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { createPinia, setActivePinia } from 'pinia'

const votePoll = vi.fn()
vi.mock('@/stores/teamChat', () => ({ useTeamChatStore: () => ({ votePoll: (...a: unknown[]) => votePoll(...a) }) }))
vi.mock('vue-sonner', () => ({ toast: { error: vi.fn(), success: vi.fn() } }))

import PollCard from './PollCard.vue'

const i18n = createI18n({ legacy: false, locale: 'en', missingWarn: false, fallbackWarn: false, messages: { en: {} } })

const message = (multiple: boolean, mine: string[] = []) => ({
  id: 'm1', channel_id: 'c1', user_id: 'u1', user_name: 'Ana', body: 'Lunch?', created_at: '2026-10-26T10:00:00Z',
  poll: {
    multiple, voters: 2,
    options: [
      { id: '1', label: 'Pizza', count: 2, mine: mine.includes('1'), voters: ['Ana', 'Bia'] },
      { id: '2', label: 'Sushi', count: 0, mine: mine.includes('2'), voters: [] }
    ]
  }
}) as never

describe('PollCard', () => {
  beforeEach(() => { setActivePinia(createPinia()); votePoll.mockReset().mockResolvedValue(undefined) })

  it('shows the question, options, counts and share', () => {
    const w = mount(PollCard, { props: { message: message(false), canVote: true }, global: { plugins: [i18n] } })
    expect(w.text()).toContain('Lunch?')
    expect(w.get('[data-testid="poll-option-1"]').text()).toContain('2 · 100%')
    expect(w.get('[data-testid="poll-option-2"]').text()).toContain('0 · 0%')
  })

  it('single choice: picks one, and clicking it again takes the vote back', async () => {
    const w = mount(PollCard, { props: { message: message(false), canVote: true }, global: { plugins: [i18n] } })
    await w.get('[data-testid="poll-option-2"]').trigger('click')
    await flushPromises()
    expect(votePoll).toHaveBeenLastCalledWith(expect.anything(), ['2'])
    const mine = mount(PollCard, { props: { message: message(false, ['2']), canVote: true }, global: { plugins: [i18n] } })
    await mine.get('[data-testid="poll-option-2"]').trigger('click')
    await flushPromises()
    expect(votePoll).toHaveBeenLastCalledWith(expect.anything(), [])
  })

  it('multiple choice: adds to what the person already chose', async () => {
    const w = mount(PollCard, { props: { message: message(true, ['1']), canVote: true }, global: { plugins: [i18n] } })
    await w.get('[data-testid="poll-option-2"]').trigger('click')
    await flushPromises()
    expect(votePoll).toHaveBeenLastCalledWith(expect.anything(), ['1', '2'])
  })

  it('cannot vote without write access or in an archived channel', async () => {
    const w = mount(PollCard, { props: { message: message(false), canVote: false }, global: { plugins: [i18n] } })
    expect(w.get('[data-testid="poll-option-1"]').attributes('disabled')).toBeDefined()
    await w.get('[data-testid="poll-option-1"]').trigger('click')
    expect(votePoll).not.toHaveBeenCalled()
  })
})
