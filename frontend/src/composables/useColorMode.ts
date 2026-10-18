import { ref, watch, onMounted } from 'vue'

export type ColorMode = 'light' | 'dark' | 'system'

// Light (with the green wallpaper) is everyone's default. The storage key moved
// to color-mode:v2 so the old saved 'dark' choices reset once to light; anything
// picked from now on is kept.
const STORAGE_KEY = 'color-mode:v2'
const colorMode = ref<ColorMode>('light')
const isDark = ref(false)

function getSystemTheme(): boolean {
  return window.matchMedia('(prefers-color-scheme: dark)').matches
}

function updateTheme() {
  if (colorMode.value === 'system') {
    isDark.value = getSystemTheme()
  } else {
    isDark.value = colorMode.value === 'dark'
  }

  // Dark-first: we use .light class for light mode, .dark class for dark mode
  if (isDark.value) {
    document.documentElement.classList.remove('light')
    document.documentElement.classList.add('dark')
  } else {
    document.documentElement.classList.remove('dark')
    document.documentElement.classList.add('light')
  }
}

export function useColorMode() {
  onMounted(() => {
    // Load saved preference
    const saved = localStorage.getItem(STORAGE_KEY) as ColorMode | null
    if (saved) {
      colorMode.value = saved
    }
    updateTheme()

    // Listen for system theme changes
    window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', () => {
      if (colorMode.value === 'system') {
        updateTheme()
      }
    })
  })

  watch(colorMode, (newMode) => {
    localStorage.setItem(STORAGE_KEY, newMode)
    updateTheme()
  })

  function setColorMode(mode: ColorMode) {
    colorMode.value = mode
  }

  return {
    colorMode,
    isDark,
    setColorMode
  }
}
