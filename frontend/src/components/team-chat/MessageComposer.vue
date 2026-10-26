<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Cloud, CornerUpLeft, ListChecks, Loader2, Mic, Monitor, Paperclip, Send, Smile, X } from 'lucide-vue-next'
import { toast } from 'vue-sonner'
import { Button } from '@/components/ui/button'
import { Textarea } from '@/components/ui/textarea'
import { useTeamChatStore } from '@/stores/teamChat'
import { getErrorMessage } from '@/lib/api-utils'
import AttachmentQueue from './AttachmentQueue.vue'
import AudioRecorder from './AudioRecorder.vue'
import EmojiPicker from './EmojiPicker.vue'
import NextcloudDialog from './NextcloudDialog.vue'
import PollDialog from './PollDialog.vue'
import {
  TEAM_CHAT_ACCEPT, TEAM_CHAT_MAX_BODY, TEAM_CHAT_UPLOAD_ERRORS, chatErrorCode, isOnlyFileNames, isSendableBody,
  pickTypingNames, recordingSupported
} from '@/lib/team-chat'

// Everything about WRITING in a conversation: the box, the reply bar, files waiting to go, voice notes,
// the emoji panel, paste, and sending. The message list lives in MessagePanel, which only needs to know
// when something was sent (to follow it) and can hand this component files (drag and drop) or focus it.
const props = defineProps<{
  channelId: string
  /** team_chat:write: buttons mirror what the server allows; the server still decides. */
  canWrite: boolean
  archived: boolean
}>()
const emit = defineEmits<{ sent: [] }>()
defineExpose({
  focus: () => boxEl()?.focus(),
  attach: (files: FileList | File[] | null | undefined) => onFiles(files)
})

const { t } = useI18n()
const store = useTeamChatStore()

const draft = ref('')
const sending = ref(false)
const box = ref<InstanceType<typeof Textarea> | null>(null)
const boxEl = () => (box.value?.$el ?? null) as HTMLTextAreaElement | null

// Reacting, replying and sending are writes: the same people and the same unarchived channels.
const canInteract = computed(() => props.canWrite && !props.archived)
const replyTo = computed(() => store.replyTarget(props.channelId))

// ---- Files and voice notes ----
const fileInput = ref<HTMLInputElement | null>(null)
const recorder = ref<InstanceType<typeof AudioRecorder> | null>(null)
const recActive = ref(false)
const micAvailable = recordingSupported()

// ---- The attach menu: file from this computer, file from Nextcloud, poll ----
const attachOpen = ref(false)
const attachWrap = ref<HTMLElement | null>(null)
const pollOpen = ref(false)
const nextcloudOpen = ref(false)
function chooseAttach(what: 'computer' | 'nextcloud' | 'poll') {
  attachOpen.value = false
  if (what === 'computer') fileInput.value?.click()
  else if (what === 'nextcloud') nextcloudOpen.value = true
  else pollOpen.value = true
}

// ---- Emoji while writing ----
const emojiOpen = ref(false)
const emojiWrap = ref<HTMLElement | null>(null)

function insertEmoji(glyph: string) {
  const el = boxEl()
  const start = el?.selectionStart ?? draft.value.length
  const end = el?.selectionEnd ?? start
  draft.value = draft.value.slice(0, start) + glyph + draft.value.slice(end)
  void nextTick(() => {
    el?.focus()
    el?.setSelectionRange(start + glyph.length, start + glyph.length)
  })
}

// The emoji panel closes on a click anywhere else, or Escape, like any popover.
function onDocPointerDown(e: PointerEvent) {
  if (emojiOpen.value && !emojiWrap.value?.contains(e.target as Node)) emojiOpen.value = false
  if (attachOpen.value && !attachWrap.value?.contains(e.target as Node)) attachOpen.value = false
}
function onDocKeyDown(e: KeyboardEvent) {
  if (e.key === 'Escape') {
    emojiOpen.value = false
    attachOpen.value = false
  }
}

const queue = computed(() => store.uploadsFor(props.channelId))
// Text is optional when files go with it; sending waits until every file is ready.
const canSubmit = computed(() => {
  if (queue.value.length === 0) return isSendableBody(draft.value)
  return store.uploadsReady(props.channelId) && (draft.value.trim() === '' || isSendableBody(draft.value))
})

const explain = (code: string) => t(TEAM_CHAT_UPLOAD_ERRORS.includes(code) ? `teamChat.errors.${code}` : 'teamChat.errors.generic')

function onFiles(list: FileList | File[] | null | undefined) {
  if (!canInteract.value || !list || list.length === 0) return
  for (const r of store.addFiles(props.channelId, Array.from(list))) toast.error(`${r.name}: ${explain(r.code)}`)
  if (fileInput.value) fileInput.value.value = ''
}

// Copying from Excel/Word puts text AND a rendered picture on the clipboard: keep the text
// (let the paste happen) and attach the picture. Only a files-only clipboard is taken over.
function onPaste(e: ClipboardEvent) {
  const files = e.clipboardData?.files
  if (!files?.length) return
  // Text that is only the copied files' names (Finder, some Linux managers) is not text to paste.
  const text = e.clipboardData?.getData('text/plain') ?? ''
  if (!text || isOnlyFileNames(text, files)) e.preventDefault()
  onFiles(files)
}

function dropStaleUploads() {
  if (store.expireStaleUploads(props.channelId).length) toast.error(explain('attachment_gone'))
}

async function sendVoice(p: { file: File; durationMs: number }) {
  try {
    await store.sendVoice(props.channelId, p.file, p.durationMs)
    recorder.value?.sent(true)
    emit('sent')
  } catch (err) {
    recorder.value?.sent(false)
    toast.error(explain(chatErrorCode(err)))
  }
}

const attachmentLabel = (kind?: string, name?: string) =>
  kind ? t(`teamChat.attachments.quote.${kind}`, { name: name ?? '' }) : ''
const replyPreview = computed(() => {
  const m = replyTo.value
  return m ? m.body || attachmentLabel(m.attachments?.[0]?.kind, m.attachments?.[0]?.name) : ''
})

const typingNames = computed(() => pickTypingNames(store.typingUserIds(props.channelId), store.userNames))
const typingText = computed(() => {
  const names = typingNames.value
  if (names.length === 0) return ''
  if (names.length === 1) return t('teamChat.typingOne', { name: names[0] })
  return t('teamChat.typingMany')
})
const overLimit = computed(() => Array.from(draft.value.trim()).length > TEAM_CHAT_MAX_BODY)

onMounted(() => {
  document.addEventListener('pointerdown', onDocPointerDown)
  document.addEventListener('keydown', onDocKeyDown)
  dropStaleUploads()
})
onBeforeUnmount(() => {
  document.removeEventListener('pointerdown', onDocPointerDown)
  document.removeEventListener('keydown', onDocKeyDown)
})
watch(() => props.channelId, () => {
  emojiOpen.value = false
  if (recorder.value) {
    // Also while the permission prompt is open (phase still 'idle'): cancel() aborts that start.
    const wasActive = recorder.value.phase !== 'idle'
    recorder.value.cancel()
    if (wasActive) toast.info(t('teamChat.recorder.cancelledOnSwitch'))
  }
  dropStaleUploads()
  draft.value = ''
})

let inFlight = 0

async function submit() {
  if (!canSubmit.value) return
  // Clear the box before the request: whatever is typed while it is in flight is
  // the next message and must survive. A failed send puts the text back.
  const body = draft.value.trim()
  draft.value = ''
  inFlight++
  sending.value = true
  try {
    await store.send(props.channelId, body)
    emit('sent')
  } catch (err) {
    if (!draft.value) draft.value = body
    const code = chatErrorCode(err)
    if (code === 'reply_target_gone' || TEAM_CHAT_UPLOAD_ERRORS.includes(code)) toast.error(explain(code))
    else toast.error(getErrorMessage(err, t('teamChat.sendFailed')))
  } finally {
    inFlight--
    sending.value = inFlight > 0
  }
}

function onComposerKey(e: KeyboardEvent) {
  if (e.key === 'Escape' && emojiOpen.value) {
    emojiOpen.value = false
    return
  }
  if (e.key === 'Escape' && replyTo.value) {
    store.cancelReply(props.channelId)
    return
  }
  if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) {
    e.preventDefault()
    void submit()
  }
}

function onInput() {
  if (draft.value.trim()) store.notifyTyping(props.channelId)
}
</script>

<template>
  <footer class="border-t p-3">
    <p class="h-4 px-1 text-xs text-muted-foreground" aria-live="polite">{{ typingText }}</p>
    <p v-if="archived" class="px-1 text-sm text-muted-foreground">{{ t('teamChat.manage.archivedNotice') }}</p>
    <div v-if="!archived && canWrite && replyTo" class="mb-2 flex items-center gap-2 rounded-md border-l-2 border-primary/50 bg-muted/60 px-2 py-1 text-xs" data-testid="reply-bar">
      <CornerUpLeft class="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
      <span class="min-w-0 flex-1 truncate">{{ t('teamChat.replyingTo', { name: replyTo.user_name }) }}: {{ replyPreview }}</span>
      <button type="button" class="rounded p-1.5 text-muted-foreground hover:bg-muted" :aria-label="t('teamChat.cancelReply')" @click="store.cancelReply(channelId)">
        <X class="h-3.5 w-3.5" />
      </button>
    </div>
    <AttachmentQueue
      v-if="canInteract && !recActive" :items="queue"
      @retry="store.retryUpload(channelId, $event)" @cancel="store.cancelUpload(channelId, $event)"
    />
    <AudioRecorder v-if="canInteract" ref="recorder" @update:active="recActive = $event" @send="sendVoice" />
    <form v-if="canInteract && !recActive" class="flex items-end gap-2" @submit.prevent="submit">
      <input
        ref="fileInput" type="file" multiple class="hidden" :accept="TEAM_CHAT_ACCEPT"
        data-testid="file-input" @change="onFiles(($event.target as HTMLInputElement).files)"
      >
      <div ref="attachWrap" class="relative shrink-0" @keydown.esc="attachOpen = false">
        <Button
          type="button" variant="ghost" size="icon" class="h-10 w-10 md:h-9 md:w-9"
          :aria-label="t('teamChat.attachments.attach')" aria-haspopup="menu" :aria-expanded="attachOpen"
          data-testid="attach-button" @click="attachOpen = !attachOpen"
        ><Paperclip class="h-4 w-4" /></Button>
        <div
          v-if="attachOpen" role="menu" data-testid="attach-menu"
          class="absolute bottom-full left-0 z-30 mb-2 w-64 rounded-lg border bg-popover p-1 text-popover-foreground shadow-lg"
        >
          <button type="button" role="menuitem" class="flex w-full items-center gap-3 rounded-md px-3 py-2.5 text-left text-sm hover:bg-muted" data-testid="attach-computer" @click="chooseAttach('computer')">
            <Monitor class="h-4 w-4 text-muted-foreground" aria-hidden="true" />{{ t('teamChat.attachMenu.computer') }}
          </button>
          <button type="button" role="menuitem" class="flex w-full items-center gap-3 rounded-md px-3 py-2.5 text-left text-sm hover:bg-muted" data-testid="attach-nextcloud" @click="chooseAttach('nextcloud')">
            <Cloud class="h-4 w-4 text-muted-foreground" aria-hidden="true" />{{ t('teamChat.attachMenu.nextcloud') }}
          </button>
          <button type="button" role="menuitem" class="flex w-full items-center gap-3 rounded-md px-3 py-2.5 text-left text-sm hover:bg-muted" data-testid="attach-poll" @click="chooseAttach('poll')">
            <ListChecks class="h-4 w-4 text-muted-foreground" aria-hidden="true" />{{ t('teamChat.attachMenu.poll') }}
          </button>
        </div>
      </div>
      <Textarea
        id="team-chat-composer"
        ref="box"
        v-model="draft"
        class="max-h-40 min-h-[40px] flex-1 resize-none text-sm"
        :rows="1"
        :placeholder="t('teamChat.composerPlaceholder')"
        :aria-label="t('teamChat.composerPlaceholder')"
        :title="t('teamChat.composerHint')"
        @keydown="onComposerKey"
        @input="onInput"
        @paste="onPaste"
      />
      <div ref="emojiWrap" class="relative shrink-0" @keydown.esc="emojiOpen = false">
        <Button
          type="button" variant="ghost" size="icon" class="h-10 w-10 md:h-9 md:w-9"
          :aria-label="t('teamChat.emoji.open')" :aria-expanded="emojiOpen" data-testid="emoji-button"
          @click="emojiOpen = !emojiOpen"
        ><Smile class="h-4 w-4" /></Button>
        <!-- on a phone it is a centered panel above the composer (anchored to the button it would run off the screen) -->
        <EmojiPicker v-if="emojiOpen" class="absolute bottom-full right-0 z-30 mb-2 max-md:fixed max-md:bottom-20 max-md:left-1/2 max-md:right-auto max-md:mb-0 max-md:-translate-x-1/2" @pick="insertEmoji" />
      </div>
      <Button
        v-if="micAvailable" type="button" variant="ghost" size="icon" class="h-10 w-10 shrink-0 md:h-9 md:w-9"
        :aria-label="t('teamChat.recorder.record')" data-testid="record-button" @click="recorder?.start()"
      ><Mic class="h-4 w-4" /></Button>
      <Button type="submit" size="icon" class="h-10 w-10 shrink-0 md:h-9 md:w-9" :disabled="sending || !canSubmit" :aria-label="t('teamChat.send')">
        <Loader2 v-if="sending" class="h-4 w-4 animate-spin" />
        <Send v-else class="h-4 w-4" />
      </Button>
    </form>
    <p v-else-if="!archived && !canWrite" class="px-1 text-sm text-muted-foreground">{{ t('teamChat.readOnly') }}</p>
    <PollDialog v-if="pollOpen" v-model:open="pollOpen" :channel-id="channelId" />
    <NextcloudDialog v-if="nextcloudOpen" v-model:open="nextcloudOpen" :channel-id="channelId" />
    <p v-if="overLimit" class="mt-1 px-1 text-xs text-destructive">{{ t('teamChat.tooLong', { max: TEAM_CHAT_MAX_BODY }) }}</p>
  </footer>
</template>
