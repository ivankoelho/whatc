import { computed, nextTick, onBeforeUnmount, watch, type Ref } from 'vue'
import { useIsMobile } from './useIsMobile'

const FOCUSABLE =
  'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])'

/**
 * Keyboard behaviour for the phone navigation drawer (the md breakpoint and up
 * shows the sidebar permanently, so none of this applies there):
 *  - opening moves focus into the drawer;
 *  - while open, `locked` is true — the layout binds it to `inert` on everything
 *    outside the drawer, which traps Tab and hides the page from assistive tech;
 *  - Escape closes it;
 *  - closing returns focus to the control that opened it, unless a drawer link was
 *    activated (the route change owns focus then).
 */
export function useMobileDrawerA11y(isOpen: Ref<boolean>, drawer: Ref<HTMLElement | null>) {
  const isPhone = useIsMobile()
  const locked = computed(() => isOpen.value && isPhone.value)
  let opener: HTMLElement | null = null
  let linkActivated = false
  let listeningOn: HTMLElement | null = null

  function onKeydown(e: KeyboardEvent) {
    if (e.key === 'Escape') isOpen.value = false
  }
  // Mouse, touch and keyboard (Enter) activation all dispatch `click`. Listened in the capture phase because the link handler
  // closes the drawer (and so stops listening) before a bubbling listener on the drawer would run.
  function onDrawerClick(e: MouseEvent) {
    if ((e.target as HTMLElement | null)?.closest('a[href]')) linkActivated = true
  }

  // At the first frame of the slide-in the drawer still computes `visibility: hidden`
  // (start of the transition), and a hidden element cannot take focus: retry per frame.
  const nextFrame = () => new Promise<void>(resolve => requestAnimationFrame(() => resolve()))

  function stopListening() {
    window.removeEventListener('keydown', onKeydown)
    listeningOn?.removeEventListener('click', onDrawerClick, true)
    listeningOn = null
  }

  watch(locked, async (open, wasOpen) => {
    if (open) {
      opener = document.activeElement instanceof HTMLElement ? document.activeElement : null
      linkActivated = false
      window.addEventListener('keydown', onKeydown)
      listeningOn = drawer.value
      listeningOn?.addEventListener('click', onDrawerClick, true) // capture: runs before the link's own handler closes the drawer
      await nextTick()
      const first = Array.from(drawer.value?.querySelectorAll<HTMLElement>(FOCUSABLE) ?? []).find(
        el => el.getClientRects().length > 0
      )
      for (let frame = 0; first && frame < 8; frame++) {
        first.focus()
        if (document.activeElement === first) break
        await nextFrame()
      }
    } else if (wasOpen) {
      stopListening()
      await nextTick() // `inert` is gone from the page by now, so the opener is focusable again
      const active = document.activeElement
      const focusIsLost = active === document.body || !!drawer.value?.contains(active)
      if (!linkActivated && opener?.isConnected && focusIsLost) opener.focus()
      opener = null
      linkActivated = false
    }
  })

  onBeforeUnmount(stopListening)

  return { locked }
}
