import { ref, computed } from 'vue'
import { brandingService } from '@/services/api'
import { useBranding } from '@/composables/useBranding'

// Workspace wallpaper. Two layers:
//  - presets: gradients each user may pick for themselves (per browser, like Bitrix's theme picker);
//  - an image: uploaded or removed ONLY by a super admin. It is the system default: everyone sees it
//    until they pick a preset of their own, and can come back to it from the picker.

export interface WallpaperPreset {
  id: string
  name: string
  css: string
}

export const wallpaperPresets: WallpaperPreset[] = [
  {
    id: 'forest',
    name: 'Floresta',
    css: 'radial-gradient(at 20% 15%, #4f9d5a 0, transparent 50%), radial-gradient(at 85% 80%, #1f6b4a 0, transparent 55%), linear-gradient(160deg, #1d3b2a, #2c5a3c 50%, #173326)'
  },
  {
    id: 'aurora',
    name: 'Aurora',
    css: 'radial-gradient(at 12% 8%, #7c6cf0 0, transparent 48%), radial-gradient(at 88% 4%, #3b6fe8 0, transparent 46%), radial-gradient(at 70% 95%, #b05cf0 0, transparent 52%), radial-gradient(at 5% 90%, #2f3fb8 0, transparent 50%), linear-gradient(135deg, #2b2a7a, #4a3aa8 50%, #23338f)'
  },
  {
    id: 'dusk',
    name: 'Entardecer',
    css: 'radial-gradient(at 10% 0%, #f08a5d 0, transparent 45%), radial-gradient(at 90% 10%, #d94f8a 0, transparent 50%), radial-gradient(at 50% 100%, #6b3fa0 0, transparent 60%), linear-gradient(150deg, #7a2f5c, #4b2c7a)'
  },
  {
    id: 'graphite',
    name: 'Grafite',
    css: 'radial-gradient(at 20% 0%, #3d4452 0, transparent 50%), radial-gradient(at 90% 100%, #2a2f3a 0, transparent 55%), linear-gradient(160deg, #1b1e25, #2a2e38)'
  }
]

// Brand green: what every user sees until they pick something else.
export const DEFAULT_WALLPAPER = 'forest'
const SELECTED_KEY = 'whatc-wallpaper'
// Per-browser custom images are gone (only a super admin sets an image now): drop what they left behind.
const LEGACY_CUSTOM_KEY = 'whatc-wallpaper-custom'

function read(key: string): string | null {
  try { return localStorage.getItem(key) } catch { return null }
}

try { localStorage.removeItem(LEGACY_CUSTOM_KEY) } catch { /* cosmetic */ }

// A stored choice that no longer exists (Lagoa, Céu, a custom image) falls back to the default.
function validPreset(id: string | null): string {
  return wallpaperPresets.some(p => p.id === id) ? (id as string) : DEFAULT_WALLPAPER
}

// The user's own preset, or null when they never picked one (or went back to the system image).
const stored = read(SELECTED_KEY)
const selected = ref<string | null>(wallpaperPresets.some(p => p.id === stored) ? stored : null)

function select(id: string) {
  selected.value = validPreset(id)
  try { localStorage.setItem(SELECTED_KEY, selected.value) } catch { /* cosmetic */ }
}

// Back to the system image (forgets the user's own preset).
function useSystemImage() {
  selected.value = null
  try { localStorage.removeItem(SELECTED_KEY) } catch { /* cosmetic */ }
}

// Downscale before sending: a raw phone photo easily passes the server's 5 MB limit.
async function toJpeg(file: File): Promise<Blob> {
  if (!file.type.startsWith('image/')) throw new Error('not-image')
  const bitmap = await createImageBitmap(file)
  const scale = Math.min(1, 2560 / Math.max(bitmap.width, bitmap.height))
  const canvas = document.createElement('canvas')
  canvas.width = Math.round(bitmap.width * scale)
  canvas.height = Math.round(bitmap.height * scale)
  canvas.getContext('2d')!.drawImage(bitmap, 0, 0, canvas.width, canvas.height)
  bitmap.close()
  const blob = await new Promise<Blob | null>(resolve => canvas.toBlob(resolve, 'image/jpeg', 0.85))
  if (!blob) throw new Error('encode-failed')
  return blob
}

export function useWallpaper() {
  const { wallpaperUrl, setWallpaperUrl } = useBranding()

  // What is in effect: the user's preset, else the system image if there is one, else the default preset.
  const active = computed(() => selected.value ?? (wallpaperUrl.value ? 'system' : DEFAULT_WALLPAPER))
  const background = computed(() => {
    if (active.value === 'system' && wallpaperUrl.value) return `center / cover no-repeat url("${wallpaperUrl.value}")`
    return (wallpaperPresets.find(p => p.id === active.value) ?? wallpaperPresets[0]).css
  })

  // Super admin only (the server enforces it; the picker only shows these to them).
  async function uploadOrgWallpaper(file: File): Promise<void> {
    const res = await brandingService.uploadWallpaper(await toJpeg(file))
    setWallpaperUrl((res.data?.data ?? (res.data as unknown as { wallpaper_url: string })).wallpaper_url)
  }

  async function removeOrgWallpaper(): Promise<void> {
    await brandingService.deleteWallpaper()
    setWallpaperUrl(null)
  }

  return { active, orgWallpaperUrl: wallpaperUrl, background, select, useSystemImage, uploadOrgWallpaper, removeOrgWallpaper }
}
