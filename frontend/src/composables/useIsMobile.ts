import { useMediaQuery } from '@vueuse/core'

// Matches Tailwind's own `md` breakpoint (768px) so JS-driven layout
// decisions (e.g. an inline `width` style that a `md:` class can't
// override — inline styles always win over classes) stay in sync with the
// `md:` utility classes used everywhere else for the same breakpoint.
export function useIsMobile() {
  return useMediaQuery('(max-width: 767px)')
}
