import { onBeforeUnmount, watch, type WatchSource } from 'vue'

// Controlled polling for things that are "still being checked" (files waiting for the antimalware
// scan). It runs only while `active` is true, backs off (4 s, 6 s, 9 s ... up to 20 s), stops by
// itself after `maxMs` (the next load or event picks the state up anyway) and never overlaps itself.
export function usePendingPoll(active: WatchSource<boolean>, tick: () => Promise<unknown> | unknown, maxMs = 5 * 60 * 1000) {
  let timer: ReturnType<typeof setTimeout> | null = null
  let began = 0
  let delay = 4000
  let stopped = false
  let on = false // a round that finishes after the work was released must not schedule another

  function stop() {
    if (timer) clearTimeout(timer)
    timer = null
  }

  function schedule() {
    stop()
    if (stopped || !on || Date.now() - began > maxMs) return
    timer = setTimeout(async () => {
      timer = null
      try { await tick() } catch { /* the next round retries */ }
      delay = Math.min(Math.round(delay * 1.5), 20000)
      schedule()
    }, delay)
  }

  watch(active, (now) => {
    on = now
    if (now) {
      if (!timer) { began = Date.now(); delay = 4000; stopped = false; schedule() }
    } else {
      stop()
    }
  }, { immediate: true })

  onBeforeUnmount(() => { stopped = true; stop() })
}
