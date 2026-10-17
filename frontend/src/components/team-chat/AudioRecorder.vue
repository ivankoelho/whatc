<script setup lang="ts">
import { computed, onBeforeUnmount, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Loader2, Send, Square, Trash2 } from 'lucide-vue-next'
import { toast } from 'vue-sonner'
import { Button } from '@/components/ui/button'
import { formatDuration, recordingSupported, TEAM_CHAT_MAX_AUDIO_MS, TEAM_CHAT_MIN_AUDIO_MS } from '@/lib/team-chat'

// Voice notes: record -> listen -> send or discard. The microphone is released on
// every way out (stop, cancel, tab hidden, unmount, channel switch). The parent starts
// it with start() and hides its own composer while it is active.
const props = defineProps<{ disabled?: boolean }>()
const emit = defineEmits<{
  send: [payload: { file: File; durationMs: number }]
  'update:active': [value: boolean]
}>()
const { t } = useI18n()

type Phase = 'idle' | 'recording' | 'review'
const phase = ref<Phase>('idle')
const elapsed = ref(0)
const sending = ref(false)
const previewUrl = ref('')

let stream: MediaStream | null = null
let recorder: MediaRecorder | null = null
let chunks: Blob[] = []
let tick: ReturnType<typeof setInterval> | null = null
let startedAt = 0
let cancelled = false
let recorded: { file: File; durationMs: number } | null = null

const supported = computed(() => recordingSupported())

const MIME_ORDER = ['audio/webm;codecs=opus', 'audio/webm', 'audio/mp4', 'audio/ogg;codecs=opus']
const pickMime = () => MIME_ORDER.find(m => MediaRecorder.isTypeSupported?.(m)) ?? ''
const extFor = (mime: string) => (mime.includes('mp4') ? 'm4a' : mime.includes('ogg') ? 'ogg' : 'webm')

function setPhase(p: Phase) {
  phase.value = p
  emit('update:active', p !== 'idle')
}

function releaseMic() {
  stream?.getTracks().forEach(tr => tr.stop())
  stream = null
  if (tick) clearInterval(tick)
  tick = null
}

function resetPreview() {
  if (previewUrl.value) URL.revokeObjectURL(previewUrl.value)
  previewUrl.value = ''
  recorded = null
}

async function start() {
  if (phase.value !== 'idle' || props.disabled) return
  if (!supported.value) {
    toast.error(t('teamChat.recorder.unsupported'))
    return
  }
  try {
    stream = await navigator.mediaDevices.getUserMedia({ audio: true })
  } catch (err) {
    const name = (err as { name?: string })?.name
    toast.error(t(name === 'NotAllowedError' || name === 'SecurityError' ? 'teamChat.recorder.denied'
      : name === 'NotFoundError' || name === 'OverconstrainedError' ? 'teamChat.recorder.noMic'
        : 'teamChat.recorder.failed'))
    return
  }
  try {
    const mime = pickMime()
    recorder = new MediaRecorder(stream, mime ? { mimeType: mime } : undefined)
  } catch {
    releaseMic()
    toast.error(t('teamChat.recorder.failed'))
    return
  }
  chunks = []
  cancelled = false
  recorder.ondataavailable = e => { if (e.data.size) chunks.push(e.data) }
  recorder.onstop = finish
  recorder.start()
  startedAt = Date.now()
  elapsed.value = 0
  setPhase('recording')
  tick = setInterval(() => {
    elapsed.value = Date.now() - startedAt
    if (elapsed.value >= TEAM_CHAT_MAX_AUDIO_MS) stop()
  }, 250)
}

function stop() {
  if (recorder && recorder.state !== 'inactive') recorder.stop()
}

function finish() {
  const durationMs = Math.min(Date.now() - startedAt, TEAM_CHAT_MAX_AUDIO_MS)
  const type = recorder?.mimeType || chunks[0]?.type || 'audio/webm'
  releaseMic()
  recorder = null
  if (cancelled) return setPhase('idle')
  if (durationMs < TEAM_CHAT_MIN_AUDIO_MS || chunks.length === 0) {
    toast.error(t('teamChat.recorder.tooShort'))
    return setPhase('idle')
  }
  const base = type.split(';')[0]
  recorded = { file: new File(chunks, `audio.${extFor(base)}`, { type: base }), durationMs }
  previewUrl.value = URL.createObjectURL(recorded.file)
  setPhase('review')
}

/** Throws the recording away (also called by the parent when the channel changes). */
function cancel() {
  cancelled = true
  if (recorder && recorder.state !== 'inactive') recorder.stop()
  else releaseMic()
  resetPreview()
  sending.value = false
  setPhase('idle')
}

function send() {
  if (!recorded || sending.value) return
  sending.value = true
  emit('send', recorded)
}

/** The parent calls this when the send finished (ok: leave; failed: stay to retry). */
function sent(ok: boolean) {
  sending.value = false
  if (ok) {
    resetPreview()
    setPhase('idle')
  }
}

function onVisibility() {
  if (document.visibilityState === 'hidden' && phase.value === 'recording') stop()
}
document.addEventListener('visibilitychange', onVisibility)
onBeforeUnmount(() => {
  document.removeEventListener('visibilitychange', onVisibility)
  cancel()
})

defineExpose({ start, cancel, sent, supported, phase })
</script>

<template>
  <div v-if="phase !== 'idle'" class="flex items-center gap-2 rounded-md border bg-muted/40 px-2 py-2" data-testid="audio-recorder">
    <template v-if="phase === 'recording'">
      <span class="h-2.5 w-2.5 shrink-0 animate-pulse rounded-full bg-destructive" aria-hidden="true" />
      <span class="min-w-0 flex-1 text-sm tabular-nums" role="timer" aria-live="off" data-testid="recorder-time">
        {{ formatDuration(elapsed) }} / {{ formatDuration(TEAM_CHAT_MAX_AUDIO_MS) }}
      </span>
      <Button type="button" variant="ghost" size="icon" class="h-10 w-10 md:h-9 md:w-9" :aria-label="t('teamChat.recorder.cancel')" @click="cancel">
        <Trash2 class="h-4 w-4" />
      </Button>
      <Button type="button" size="icon" class="h-10 w-10 md:h-9 md:w-9" :aria-label="t('teamChat.recorder.stop')" data-testid="recorder-stop" @click="stop">
        <Square class="h-4 w-4" />
      </Button>
    </template>
    <template v-else>
      <audio :src="previewUrl" controls class="h-10 min-w-0 flex-1" :aria-label="t('teamChat.recorder.preview')" />
      <Button type="button" variant="ghost" size="icon" class="h-10 w-10 md:h-9 md:w-9" :aria-label="t('teamChat.recorder.discard')" :disabled="sending" data-testid="recorder-discard" @click="cancel">
        <Trash2 class="h-4 w-4" />
      </Button>
      <Button type="button" size="icon" class="h-10 w-10 md:h-9 md:w-9" :aria-label="t('teamChat.recorder.send')" :disabled="sending" data-testid="recorder-send" @click="send">
        <Loader2 v-if="sending" class="h-4 w-4 animate-spin" />
        <Send v-else class="h-4 w-4" />
      </Button>
    </template>
  </div>
</template>
