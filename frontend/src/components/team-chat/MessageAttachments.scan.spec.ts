// @vitest-environment happy-dom
import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import MessageAttachments from './MessageAttachments.vue'

const i18n = createI18n({
  legacy: false, locale: 'en', missingWarn: false, fallbackWarn: false,
  messages: { en: { fileScan: { pending: 'Checking the file for malware…', infected: 'Blocked: malware detected' } } }
})

const att = (id: string, kind: 'image' | 'audio' | 'file', scan_status?: string) =>
  ({ id, kind, name: `${id}.bin`, mime: 'x/y', size: 10, scan_status }) as never

describe('MessageAttachments and the antimalware verdict', () => {
  it('a file that is not clean is a card with its state: nothing is loaded or linked', () => {
    const w = mount(MessageAttachments, {
      props: { attachments: [att('a', 'image', 'pending'), att('b', 'audio', 'infected'), att('c', 'file', 'infected')] },
      global: { plugins: [i18n] }
    })
    expect(w.findAll('img, audio, a')).toHaveLength(0)
    expect(w.get('[data-testid="attachment-a"]').text()).toContain('Checking the file for malware')
    expect(w.get('[data-testid="attachment-b"]').attributes('data-scan')).toBe('infected')
    expect(w.get('[data-testid="attachment-c"]').text()).toContain('Blocked: malware detected')
  })

  it('a clean file (or an older answer without a verdict field) renders as before', () => {
    const w = mount(MessageAttachments, {
      props: { attachments: [att('a', 'image', 'clean'), att('b', 'file')] },
      global: { plugins: [i18n] }
    })
    expect(w.find('img').exists()).toBe(true)
    expect(w.find('a[download]').exists()).toBe(true)
  })
})
