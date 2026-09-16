import { ref } from 'vue'
import { brandingService } from '@/services/api'

// Module-level (not per-component) so LoginView and AppLayout share one
// fetch instead of hitting the public branding endpoint twice per page load.
const loginBackgroundUrl = ref<string | null>(null)
const logoUrl = ref<string | null>(null)
const systemName = ref<string | null>(null)
const footerText = ref<string | null>(null)
const footerVersion = ref<string | null>(null)
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
    systemName.value = data?.system_name ?? null
    footerText.value = data?.footer_text ?? null
    footerVersion.value = data?.footer_version ?? null
  } catch {
    // Branding is purely cosmetic and optional -- a failure here (network,
    // 500, malformed JSON) must never block the page it's decorating.
    loginBackgroundUrl.value = null
    logoUrl.value = null
    systemName.value = null
    footerText.value = null
    footerVersion.value = null
  } finally {
    loaded.value = true
  }
}

export function useBranding() {
  return { loginBackgroundUrl, logoUrl, systemName, footerText, footerVersion, loadBranding }
}
