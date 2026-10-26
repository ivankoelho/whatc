<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Loader2, Lock } from 'lucide-vue-next'
import { toast } from 'vue-sonner'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { occurrenceExportService } from '@/services/api'

// Pick what to export and in which format. The server decides what each section may contain: a section
// the person may not have is shown disabled (and the server would refuse it anyway).
const props = defineProps<{ open: boolean; occurrenceId: string }>()
const emit = defineEmits<{ 'update:open': [value: boolean] }>()
const { t } = useI18n()

const SECTIONS = ['summary', 'history', 'internal_chat', 'whatsapp', 'documents'] as const
type Section = typeof SECTIONS[number]
const TABULAR: Section[] = ['history', 'internal_chat', 'whatsapp', 'documents']

const available = ref<Record<string, boolean>>({})
const loading = ref(false)
const working = ref(false)
const selected = ref<Section[]>(['summary', 'history'])
const format = ref<'pdf' | 'csv' | 'zip'>('pdf')
const unwrap = <T,>(res: { data: unknown }): T => ((res.data as { data?: unknown }).data ?? res.data) as T

watch(() => props.open, async open => {
  if (!open) return
  selected.value = ['summary', 'history']
  format.value = 'pdf'
  loading.value = true
  try {
    available.value = unwrap<{ sections: Record<string, boolean> }>(await occurrenceExportService.options(props.occurrenceId)).sections
    selected.value = selected.value.filter(s => available.value[s])
  } catch {
    toast.error(t('occurrences.export.failed'))
  } finally {
    loading.value = false
  }
}, { immediate: true })

const toggle = (s: Section) => {
  if (!available.value[s]) return
  selected.value = selected.value.includes(s) ? selected.value.filter(x => x !== s) : [...selected.value, s]
}
const allowed = computed(() => SECTIONS.filter(s => available.value[s]))
const everything = () => { selected.value = [...allowed.value] }

// CSV is one table: it needs exactly one tabular section and no summary.
const csvOk = computed(() => selected.value.length === 1 && TABULAR.includes(selected.value[0]))
const canGo = computed(() => selected.value.length > 0 && !working.value && (format.value !== 'csv' || csvOk.value))

async function run() {
  working.value = true
  try {
    const res = await occurrenceExportService.run(props.occurrenceId, selected.value, format.value)
    const cd = (res.headers['content-disposition'] as string | undefined) ?? ''
    const name = /filename="?([^";]+)"?/.exec(cd)?.[1] ?? `protocolo.${format.value}`
    const url = URL.createObjectURL(res.data as Blob)
    const a = document.createElement('a')
    a.href = url
    a.download = name
    document.body.appendChild(a)
    a.click()
    a.remove()
    setTimeout(() => URL.revokeObjectURL(url), 10_000)
    toast.success(t('occurrences.export.done'))
    emit('update:open', false)
  } catch (err) {
    const status = (err as { response?: { status?: number } }).response?.status
    toast.error(t(status === 403 ? 'occurrences.export.forbidden' : status === 413 ? 'occurrences.export.tooLarge' : 'occurrences.export.failed'))
  } finally {
    working.value = false
  }
}
</script>

<template>
  <Dialog :open="open" @update:open="emit('update:open', $event)">
    <DialogContent class="max-w-md" data-testid="export-dialog">
      <DialogHeader>
        <DialogTitle>{{ t('occurrences.export.title') }}</DialogTitle>
        <DialogDescription>{{ t('occurrences.export.hint') }}</DialogDescription>
      </DialogHeader>
      <div v-if="loading" class="flex justify-center py-6"><Loader2 class="h-5 w-5 animate-spin text-muted-foreground" /></div>
      <form v-else class="space-y-4" @submit.prevent="run">
        <fieldset class="space-y-1.5">
          <legend class="sr-only">{{ t('occurrences.export.sections') }}</legend>
          <div class="mb-1 flex items-center justify-between text-sm">
            <span class="font-medium">{{ t('occurrences.export.sections') }}</span>
            <button type="button" class="text-xs font-normal text-primary hover:underline" data-testid="export-all" @click="everything">{{ t('occurrences.export.all') }}</button>
          </div>
          <label
            v-for="s in SECTIONS" :key="s" class="flex items-center gap-2 rounded-md border px-3 py-2 text-sm"
            :class="available[s] ? 'cursor-pointer hover:bg-muted' : 'cursor-not-allowed opacity-60'"
          >
            <input type="checkbox" class="h-4 w-4" :checked="selected.includes(s)" :disabled="!available[s]" :data-testid="`export-section-${s}`" @change="toggle(s)">
            <span class="flex-1">{{ t(`occurrences.export.section.${s}`) }}</span>
            <span v-if="!available[s]" class="flex items-center gap-1 text-xs text-muted-foreground"><Lock class="h-3 w-3" aria-hidden="true" />{{ t('occurrences.export.noPermission') }}</span>
          </label>
        </fieldset>

        <fieldset class="space-y-1.5">
          <legend class="mb-1 text-sm font-medium">{{ t('occurrences.export.format') }}</legend>
          <div class="flex gap-2" role="radiogroup">
            <button
              v-for="f in (['pdf', 'csv', 'zip'] as const)" :key="f" type="button" role="radio" :aria-checked="format === f"
              class="rounded-md border px-3 py-1.5 text-sm" :class="format === f ? 'border-primary bg-primary/10 font-medium' : 'hover:bg-muted'"
              :data-testid="`export-format-${f}`" @click="format = f"
            >{{ t(`occurrences.export.fmt.${f}`) }}</button>
          </div>
          <p v-if="format === 'csv' && !csvOk" class="text-xs text-destructive" role="alert">{{ t('occurrences.export.csvOne') }}</p>
          <p v-if="format === 'zip'" class="text-xs text-muted-foreground">{{ t('occurrences.export.zipHint') }}</p>
        </fieldset>

        <div class="flex justify-end gap-2">
          <Button type="button" variant="ghost" @click="emit('update:open', false)">{{ t('common.cancel') }}</Button>
          <Button type="submit" :disabled="!canGo" data-testid="export-run">
            <Loader2 v-if="working" class="mr-2 h-4 w-4 animate-spin" />{{ working ? t('occurrences.export.working') : t('occurrences.export.run') }}
          </Button>
        </div>
      </form>
    </DialogContent>
  </Dialog>
</template>
