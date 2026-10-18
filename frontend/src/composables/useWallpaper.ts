import { ref, computed } from 'vue'

// Bitrix24-style workspace wallpaper. Per-browser on purpose: each agent picks
// their own backdrop, like Bitrix's theme picker.
// ponytail: localStorage only (~5MB quota, not synced across devices); move to
// a per-user upload endpoint if people ask for it to follow them.

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
    id: 'lagoon',
    name: 'Lagoa',
    css: 'radial-gradient(at 15% 10%, #1fb89a 0, transparent 50%), radial-gradient(at 85% 15%, #1a8fc9 0, transparent 48%), radial-gradient(at 60% 100%, #0f6f7a 0, transparent 55%), linear-gradient(140deg, #0c4a5a, #0f6b6e 55%, #0b3d63)'
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
  },
  {
    id: 'sky',
    name: 'Céu',
    css: 'radial-gradient(at 15% 10%, #7ab8f5 0, transparent 50%), radial-gradient(at 85% 20%, #5b7cf0 0, transparent 50%), radial-gradient(at 50% 100%, #3a56c4 0, transparent 60%), linear-gradient(160deg, #3f6fd1, #2c4aa8)'
  }
]

// Brand green: what every user sees until they pick something else.
export const DEFAULT_WALLPAPER = 'forest'
const SELECTED_KEY = 'whatc-wallpaper'
const CUSTOM_KEY = 'whatc-wallpaper-custom'

function read(key: string): string | null {
  try { return localStorage.getItem(key) } catch { return null }
}

const selected = ref<string>(read(SELECTED_KEY) || DEFAULT_WALLPAPER)
const customUrl = ref<string | null>(read(CUSTOM_KEY))

const background = computed(() => {
  if (selected.value === 'custom' && customUrl.value) {
    return `center / cover no-repeat url("${customUrl.value}")`
  }
  return (wallpaperPresets.find(p => p.id === selected.value) ?? wallpaperPresets[0]).css
})

function select(id: string) {
  selected.value = id
  try { localStorage.setItem(SELECTED_KEY, id) } catch { /* cosmetic */ }
}

// Downscale before storing: a raw phone photo would blow the localStorage quota.
async function upload(file: File): Promise<void> {
  if (!file.type.startsWith('image/')) throw new Error('not-image')
  const bitmap = await createImageBitmap(file)
  const scale = Math.min(1, 2560 / Math.max(bitmap.width, bitmap.height))
  const canvas = document.createElement('canvas')
  canvas.width = Math.round(bitmap.width * scale)
  canvas.height = Math.round(bitmap.height * scale)
  canvas.getContext('2d')!.drawImage(bitmap, 0, 0, canvas.width, canvas.height)
  bitmap.close()
  const dataUrl = canvas.toDataURL('image/jpeg', 0.85)
  localStorage.setItem(CUSTOM_KEY, dataUrl) // throws QuotaExceededError → caller toasts
  customUrl.value = dataUrl
  select('custom')
}

function removeCustom() {
  try { localStorage.removeItem(CUSTOM_KEY) } catch { /* cosmetic */ }
  customUrl.value = null
  if (selected.value === 'custom') select(DEFAULT_WALLPAPER)
}

export function useWallpaper() {
  return { selected, customUrl, background, select, upload, removeCustom }
}
