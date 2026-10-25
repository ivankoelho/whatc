<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Textarea } from '@/components/ui/textarea'
import { caseWhatsAppService, type CaseWhatsApp } from '@/services/api'
import { getErrorMessage } from '@/lib/api-utils'
import { formatDateTime } from '@/lib/utils'
import { MessageCircle, Send } from 'lucide-vue-next'

// The conversation with the CUSTOMER on this case's channel account. Everything
// that leaves from here reaches the customer's WhatsApp: it is styled and labeled
// apart from the internal discussion, and uses a different endpoint.
const props = defineProps<{ occurrenceId: string }>()
const emit = defineEmits<{ configured: [boolean] }>()

const { t, te } = useI18n()
const data = ref<CaseWhatsApp | null>(null)
const text = ref('')
const tplName = ref('')
const tplParams = ref<string[]>([])
const busy = ref(false)

const tpl = computed(() => data.value?.templates?.find(x => x.name === tplName.value))
watch(tpl, v => { tplParams.value = (v?.params ?? []).map(() => '') })

async function load() {
  try {
    const res = await caseWhatsAppService.get(props.occurrenceId)
    const b = res.data as any
    data.value = b.data ?? b
  } catch {
    data.value = null
  }
  emit('configured', !!data.value?.configured)
}

function statusLabel(s: string) { return te(`occurrences.notice.${s}`) ? t(`occurrences.notice.${s}`) : s }

async function sendText() {
  if (!text.value.trim()) return
  busy.value = true
  try {
    await caseWhatsAppService.sendText(props.occurrenceId, text.value.trim())
    text.value = ''
  } catch (e) {
    toast.error(getErrorMessage(e, t('occurrences.waSendFailed')))
  } finally { busy.value = false; await load() }
}

async function sendTemplate() {
  if (!tpl.value || tplParams.value.some(p => !p.trim())) return
  busy.value = true
  try {
    await caseWhatsAppService.sendTemplate(props.occurrenceId, tpl.value.name, tpl.value.language, tplParams.value.map(p => p.trim()))
    tplName.value = ''
  } catch (e) {
    toast.error(getErrorMessage(e, t('occurrences.waSendFailed')))
  } finally { busy.value = false; await load() }
}

watch(() => props.occurrenceId, load, { immediate: true })
</script>

<template>
  <Card v-if="data" data-testid="case-whatsapp" class="border-emerald-500/40">
    <CardHeader class="pb-3">
      <CardTitle class="text-sm font-medium flex items-center gap-2">
        <MessageCircle class="h-4 w-4" aria-hidden="true" />
        {{ $t('occurrences.waTitle') }}
        <Badge variant="outline" class="border-emerald-500/50 text-emerald-600">{{ $t('occurrences.waToCustomer') }}</Badge>
      </CardTitle>
      <p v-if="data.account" class="text-xs text-muted-foreground">{{ $t('occurrences.noticeAccount') }}: {{ data.account }}</p>
    </CardHeader>
    <CardContent class="space-y-3 text-sm">
      <p v-if="!data.configured" class="text-muted-foreground">{{ $t('occurrences.waNotConfigured') }}</p>
      <p v-else-if="data.restricted" class="text-muted-foreground" data-testid="wa-restricted">{{ $t('occurrences.waRestricted') }}</p>
      <template v-else>
        <p v-if="!data.messages?.length" class="text-muted-foreground">{{ $t('occurrences.waEmpty') }}</p>
        <ul v-else class="max-h-96 space-y-2 overflow-y-auto pr-1" data-testid="wa-messages">
          <li v-for="m in data.messages" :key="m.id" class="flex" :class="m.direction === 'outgoing' ? 'justify-end' : 'justify-start'">
            <div class="max-w-[85%] sm:max-w-2xl rounded-lg px-3 py-2" :class="m.direction === 'outgoing' ? 'bg-emerald-500/15' : 'bg-muted'">
              <p class="whitespace-pre-wrap [overflow-wrap:anywhere]">{{ m.content }}</p>
              <div class="mt-1 flex flex-wrap gap-2 text-xs text-muted-foreground">
                <span>{{ formatDateTime(m.created_at) }}</span>
                <span v-if="m.sent_by">{{ m.sent_by }}</span>
                <span v-if="m.direction === 'outgoing'" :class="m.status === 'failed' ? 'text-destructive' : ''">{{ statusLabel(m.status) }}</span>
                <span v-if="m.error" class="text-destructive">{{ m.error }}</span>
              </div>
            </div>
          </li>
        </ul>

        <div v-if="data.can_reply" class="space-y-3 border-t pt-3">
          <div v-if="data.window_open" class="space-y-2">
            <Textarea v-model="text" :rows="2" :placeholder="$t('occurrences.waReplyPlaceholder')" data-testid="wa-text" />
            <Button size="sm" :disabled="busy || !text.trim()" data-testid="wa-send-text" @click="sendText">
              <Send class="mr-2 h-4 w-4" />{{ $t('occurrences.waSendToCustomer') }}
            </Button>
          </div>
          <p v-else class="text-xs text-muted-foreground">{{ $t('occurrences.waWindowClosed') }}</p>

          <div v-if="data.templates?.length" class="space-y-2">
            <label class="text-xs text-muted-foreground">{{ $t('occurrences.waTemplate') }}</label>
            <select v-model="tplName" class="flex h-9 w-full rounded-md border border-input bg-background px-3 text-sm" data-testid="wa-template">
              <option value="">—</option>
              <option v-for="x in data.templates" :key="x.name + x.language" :value="x.name">{{ x.name }}</option>
            </select>
            <template v-if="tpl">
              <p class="rounded bg-muted px-2 py-1 text-xs">{{ tpl.body }}</p>
              <input v-for="(p, i) in tpl.params" :key="p" v-model="tplParams[i]" :placeholder="`{{${p}}}`"
                class="flex h-9 w-full rounded-md border border-input bg-background px-3 text-sm" />
              <Button size="sm" variant="outline" :disabled="busy || tplParams.some(p => !p.trim())" data-testid="wa-send-template" @click="sendTemplate">
                <Send class="mr-2 h-4 w-4" />{{ $t('occurrences.waSendTemplate') }}
              </Button>
            </template>
          </div>
        </div>
      </template>
    </CardContent>
  </Card>
</template>
