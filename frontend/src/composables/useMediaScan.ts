import { computed, reactive, watch, type Ref } from 'vue'
import { mediaScanService, type FileScanStatus } from '@/services/api'
import { usePendingPoll } from '@/composables/usePendingPoll'

// The scan state of the WhatsApp media of the open conversation. The server decides (a media that is
// not "clean" answers 423); this asks for the state of the media on screen so the bubble can say
// "verifying" instead of showing a broken image, and refreshes by itself until the media is released.
// Only the file's own state is shown: nothing about the scanner reaches the screen.
export function useMediaScan(messages: Ref<{ id: string; media_url?: string }[]>) {
  const states = reactive<Record<string, FileScanStatus>>({})

  const mediaIds = computed(() => messages.value.filter(m => !!m.media_url).map(m => m.id))
  // what is not yet known to be clean (a media we have not asked about counts as unknown, not blocked)
  const unsettled = computed(() => Object.keys(states).filter(id => states[id] !== 'clean' && states[id] !== 'infected' && states[id] !== 'skipped'))

  async function ask(ids: string[]) {
    for (let i = 0; i < ids.length; i += 100) {
      const res = await mediaScanService.status(ids.slice(i, i + 100))
      const map = ((res.data as { data?: { statuses?: Record<string, FileScanStatus> } }).data?.statuses) ?? {}
      for (const id of ids.slice(i, i + 100)) {
        if (map[id]) states[id] = map[id]
      }
    }
  }

  // New media on screen: find out its state once (clean ones are then left alone).
  watch(mediaIds, (ids) => {
    const unknown = ids.filter(id => !(id in states))
    if (unknown.length) void ask(unknown).catch(() => { /* the media itself still answers 423 */ })
  }, { immediate: true })

  usePendingPoll(() => unsettled.value.length > 0, () => ask(unsettled.value))

  /** null = show the media; otherwise the state to show in its place. */
  const blockedState = (id: string): FileScanStatus | null => {
    const s = states[id]
    return s && s !== 'clean' ? s : null
  }
  // A changed state makes the media element reload with a fresh URL once it is released.
  const releasedAt = reactive<Record<string, number>>({})
  watch(() => ({ ...states }), (now, before) => {
    for (const id of Object.keys(now)) {
      if (now[id] === 'clean' && before && before[id] && before[id] !== 'clean') releasedAt[id] = Date.now()
    }
  })

  return { blockedState, releasedAt }
}
