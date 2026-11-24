// @vitest-environment happy-dom
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent } from 'vue'
import { createI18n } from 'vue-i18n'
import pt from '@/i18n/locales/pt-BR.json'

const svc = vi.hoisted(() => ({
  getPublic: vi.fn(), uploadWallpaper: vi.fn(), deleteWallpaper: vi.fn()
}))
vi.mock('@/services/api', () => ({ brandingService: svc }))
const auth = vi.hoisted(() => ({ superAdmin: false }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ user: { is_super_admin: auth.superAdmin } }) }))
vi.mock('vue-sonner', () => ({ toast: { error: vi.fn() } }))

import WallpaperPicker from './WallpaperPicker.vue'
import { wallpaperPresets, useWallpaper } from '@/composables/useWallpaper'
import { useBranding } from '@/composables/useBranding'

const passthrough = defineComponent({ template: '<div><slot /></div>' })
const i18n = createI18n({ legacy: false, locale: 'pt-BR', messages: { 'pt-BR': pt } })
const mountIt = () => mount(WallpaperPicker, {
  global: { plugins: [i18n], stubs: { Popover: passthrough, PopoverTrigger: passthrough, PopoverContent: passthrough } }
})

describe('wallpaper', () => {
  beforeEach(() => {
    Object.values(svc).forEach(f => f.mockReset())
    useBranding().setWallpaperUrl(null)
    auth.superAdmin = false
  })

  it('no longer offers the Lagoa and Céu presets', () => {
    const ids = wallpaperPresets.map(p => p.id)
    expect(ids).not.toContain('lagoon')
    expect(ids).not.toContain('sky')
    expect(wallpaperPresets.map(p => p.name)).not.toContain('Lagoa')
    expect(wallpaperPresets.map(p => p.name)).not.toContain('Céu')
  })

  it('regular users can pick a preset but cannot upload or remove the system image', async () => {
    useBranding().setWallpaperUrl('/api/branding/wallpaper?v=1')
    const w = mountIt()
    expect(w.find('[data-testid="wallpaper-upload"]').exists()).toBe(false)
    expect(w.find('[data-testid="wallpaper-remove"]').exists()).toBe(false)
    expect(w.find('input[type="file"]').exists()).toBe(false)
    expect(w.findAll('button[role="radio"]').every(b => b.attributes('disabled') === undefined)).toBe(true)
  })

  it('the system image is the default; a user preset overrides it and the system tile brings it back', async () => {
    useBranding().setWallpaperUrl('/api/branding/wallpaper?v=7')
    const { background, select, useSystemImage } = useWallpaper()
    useSystemImage()
    expect(background.value).toContain('/api/branding/wallpaper?v=7')
    select('aurora')
    expect(background.value).not.toContain('url(')
    const w = mountIt()
    await w.find('[data-testid="wallpaper-system"]').trigger('click')
    expect(background.value).toContain('/api/branding/wallpaper?v=7')
  })

  it('without a system image the default preset is in effect and there is no system tile', () => {
    const { background, useSystemImage } = useWallpaper()
    useSystemImage()
    expect(background.value).not.toContain('url(')
    expect(mountIt().find('[data-testid="wallpaper-system"]').exists()).toBe(false)
  })

  it('a super admin sees upload and can remove the image for everyone', async () => {
    auth.superAdmin = true
    svc.deleteWallpaper.mockResolvedValue({})
    useBranding().setWallpaperUrl('/api/branding/wallpaper?v=1')
    const w = mountIt()
    expect(w.find('[data-testid="wallpaper-upload"]').exists()).toBe(true)
    await w.find('[data-testid="wallpaper-remove"]').trigger('click')
    await flushPromises()
    expect(svc.deleteWallpaper).toHaveBeenCalledOnce()
    expect(useBranding().wallpaperUrl.value).toBeNull()
  })
})
