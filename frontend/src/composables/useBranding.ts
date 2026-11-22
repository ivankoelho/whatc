import { ref } from 'vue'
import { brandingService } from '@/services/api'

// Module-level (not per-component) so LoginView and AppLayout share one
// fetch instead of hitting the public branding endpoint twice per page load.
const loginBackgroundUrl = ref<string | null>(null)
const logoUrl = ref<string | null>(null)
// Workspace wallpaper image, set by a super admin for every user (null = each user keeps their preset).
const wallpaperUrl = ref<string | null>(null)
const systemName = ref<string | null>(null)
const footerText = ref<string | null>(null)
const footerVersion = ref<string | null>(null)
// System theme (app shell), the same for every user; set by a super admin.
export type UiTheme = 'bitrix' | 'classic'
const uiTheme = ref<UiTheme>('bitrix')
const loaded = ref(false)

function withBasePath(url: string): string {
  const basePath = ((window as any).__BASE_PATH__ ?? '').replace(/\/$/, '')
  return `${basePath}${url}`
}

async function loadBranding() {
  if (loaded.value) return
  try {
    const res = await brandingService.getPublic()
    const data = res.data?.data ?? res.data
    loginBackgroundUrl.value = data?.login_background_url ? withBasePath(data.login_background_url) : null
    logoUrl.value = data?.logo_url ? withBasePath(data.logo_url) : null
    wallpaperUrl.value = data?.wallpaper_url ? withBasePath(data.wallpaper_url) : null
    systemName.value = data?.system_name ?? null
    footerText.value = data?.footer_text ?? null
    footerVersion.value = data?.footer_version ?? null
    uiTheme.value = data?.ui_theme === 'classic' ? 'classic' : 'bitrix'
  } catch {
    // Branding is purely cosmetic and optional -- a failure here (network,
    // 500, malformed JSON) must never block the page it's decorating.
    loginBackgroundUrl.value = null
    logoUrl.value = null
    wallpaperUrl.value = null
    systemName.value = null
    footerText.value = null
    footerVersion.value = null
  } finally {
    loaded.value = true
  }
}

export function useBranding() {
  // setUiTheme: after a super admin saves the theme, switch this session too.
  const setUiTheme = (theme: UiTheme) => { uiTheme.value = theme }
  // setWallpaperUrl: after a super admin uploads or removes it, this session follows (url from the API, or null).
  const setWallpaperUrl = (url: string | null) => { wallpaperUrl.value = url ? withBasePath(url) : null }
  return { loginBackgroundUrl, logoUrl, wallpaperUrl, setWallpaperUrl, systemName, footerText, footerVersion, uiTheme, setUiTheme, loadBranding, brandingLoaded: loaded }
}
