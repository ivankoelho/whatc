// @vitest-environment happy-dom
import { describe, expect, it, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'

const getPublic = vi.hoisted(() => vi.fn())
vi.mock('@/services/api', () => ({ brandingService: { getPublic } }))

describe('EnvironmentRibbon', () => {
  it('shows the label the server sends, and nothing on the real system', async () => {
    getPublic.mockResolvedValue({ data: { data: { environment_label: 'Ambiente de treinamento' } } })
    const { default: Ribbon } = await import('./EnvironmentRibbon.vue')
    const w = mount(Ribbon)
    await flushPromises()
    expect(w.find('[data-testid="environment-ribbon"]').text()).toBe('Ambiente de treinamento')

    vi.resetModules()
    getPublic.mockResolvedValue({ data: { data: { environment_label: null } } })
    const { default: Ribbon2 } = await import('./EnvironmentRibbon.vue')
    const w2 = mount(Ribbon2)
    await flushPromises()
    expect(w2.find('[data-testid="environment-ribbon"]').exists()).toBe(false)
  })
})
