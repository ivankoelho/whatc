<script setup lang="ts">
// Import X2 stores as new units. It is NOT the link dialog (UnitXProcessLojaDialog):
// that one points an existing unit at a store, this one creates the unit of stores
// that have none. Only "new" stores can be ticked; a store that is already imported
// or clashes with an existing unit is shown with the reason and cannot be selected.
// The server decides and reports each store; this screen never creates anything itself.
import { ref, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Input } from '@/components/ui/input'
import { Checkbox } from '@/components/ui/checkbox'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { unitsService, type XProcessLoja, type XProcessImportResponse } from '@/services/api'
import { filterLojas, isImportable } from '@/lib/x2-lojas'
import { getErrorMessage } from '@/lib/api-utils'
import { Loader2 } from 'lucide-vue-next'

const props = defineProps<{ open: boolean }>()
const emit = defineEmits<{ 'update:open': [value: boolean]; imported: [] }>()

const { t } = useI18n()

const lojas = ref<XProcessLoja[]>([])
const loading = ref(false)
const loadFailed = ref(false)
const importing = ref(false)
const search = ref('')
const selected = ref<string[]>([])
const result = ref<XProcessImportResponse | null>(null)

const filtered = computed(() => filterLojas(lojas.value, search.value))

async function load() {
  loading.value = true
  loadFailed.value = false
  selected.value = []
  try {
    const res = await unitsService.listXProcessLojas()
    lojas.value = res.data.data.lojas
  } catch (e) {
    loadFailed.value = true
    lojas.value = []
    toast.error(getErrorMessage(e, t('units.import.loadFailed')))
  } finally {
    loading.value = false
  }
}

watch(() => props.open, isOpen => {
  if (!isOpen) return
  search.value = ''
  result.value = null
  load()
})

function toggle(cod: string, on: boolean) {
  selected.value = on ? [...new Set([...selected.value, cod])] : selected.value.filter(c => c !== cod)
}

async function doImport() {
  if (selected.value.length === 0 || importing.value) return
  importing.value = true
  try {
    const { data } = await unitsService.importXProcess(selected.value)
    result.value = data.data
    // Created/updated units are already in the table behind the dialog.
    if (data.data.summary.created + data.data.summary.updated > 0) emit('imported')
  } catch (e) {
    toast.error(getErrorMessage(e, t('units.import.importFailed')))
  } finally {
    importing.value = false
  }
}

function backToList() {
  result.value = null
  load()
}

const summaryKeys = ['created', 'updated', 'already_exists', 'conflicts', 'failed'] as const

function resultStoreName(cod: string): string {
  const l = lojas.value.find(x => x.cod_empresa === cod)
  return l?.import_name || l?.razao_social_empresa || ''
}
</script>

<template>
  <Dialog :open="open" @update:open="emit('update:open', $event)">
    <DialogContent class="sm:max-w-2xl" data-testid="x2-import-dialog">
      <DialogHeader>
        <DialogTitle>{{ result ? $t('units.import.resultTitle') : $t('units.import.title') }}</DialogTitle>
        <DialogDescription>{{ result ? $t('units.import.resultDescription') : $t('units.import.description') }}</DialogDescription>
      </DialogHeader>

      <!-- Result of the import -->
      <div v-if="result" class="space-y-3 py-2" data-testid="x2-import-result">
        <div class="flex flex-wrap gap-2">
          <Badge v-for="k in summaryKeys" :key="k" variant="secondary" :data-testid="'x2-import-summary-' + k">
            {{ $t('units.import.summary.' + k) }}: {{ result.summary[k] }}
          </Badge>
        </div>
        <ul class="max-h-72 overflow-y-auto divide-y rounded-md border">
          <li v-for="r in result.results" :key="r.cod_empresa" class="p-2.5 text-sm space-y-0.5" :data-testid="'x2-import-row-' + r.cod_empresa">
            <div class="flex items-center gap-2">
              <span class="font-medium tabular-nums">X2 {{ r.cod_empresa }}</span>
              <span class="text-muted-foreground truncate">{{ r.unit?.name || resultStoreName(r.cod_empresa) }}</span>
              <Badge :variant="r.status === 'created' || r.status === 'updated' ? 'success' : r.status === 'already_exists' ? 'secondary' : 'destructive'" class="ml-auto shrink-0">
                {{ $t('units.import.result.' + r.status) }}
              </Badge>
            </div>
            <p v-if="r.reason" class="text-xs text-muted-foreground">
              {{ $t('units.import.reason.' + r.reason, { unit: r.existing_unit?.name ?? '' }) }}
            </p>
            <p v-for="n in r.notes || []" :key="n" class="text-xs text-muted-foreground">{{ $t('units.import.note.' + n) }}</p>
          </li>
        </ul>
        <div class="flex justify-end gap-2">
          <Button variant="outline" @click="backToList">{{ $t('units.import.backToList') }}</Button>
          <Button @click="emit('update:open', false)">{{ $t('units.import.close') }}</Button>
        </div>
      </div>

      <!-- Store picker -->
      <div v-else class="space-y-3 py-2">
        <Input v-model="search" :placeholder="$t('units.import.search')" :disabled="loading || loadFailed" data-testid="x2-import-search" />

        <div v-if="loading" class="flex items-center gap-2 text-sm text-muted-foreground py-6 justify-center">
          <Loader2 class="h-4 w-4 animate-spin" /> {{ $t('common.loading') }}
        </div>
        <div v-else-if="loadFailed" class="text-sm text-muted-foreground py-6 text-center space-y-2" data-testid="x2-import-load-failed">
          <p>{{ $t('units.import.loadFailedHint') }}</p>
          <Button variant="outline" size="sm" @click="load">{{ $t('common.retryLoad') }}</Button>
        </div>
        <ul v-else class="max-h-80 overflow-y-auto divide-y rounded-md border" data-testid="x2-import-list">
          <li v-for="l in filtered" :key="l.cod_empresa">
            <label
              class="flex items-start gap-3 p-2.5 text-sm"
              :class="isImportable(l) ? 'cursor-pointer hover:bg-muted/50' : 'cursor-not-allowed opacity-70'"
            >
              <Checkbox
                class="mt-0.5"
                :checked="selected.includes(l.cod_empresa)"
                :disabled="!isImportable(l)"
                :aria-label="'X2 ' + l.cod_empresa + ' ' + (l.import_name || l.razao_social_empresa)"
                :data-testid="'x2-import-check-' + l.cod_empresa"
                @update:checked="toggle(l.cod_empresa, $event === true)"
              />
              <span class="flex-1 min-w-0">
                <span class="font-medium tabular-nums">{{ l.cod_empresa }}</span>
                <span class="text-muted-foreground"> · {{ l.razao_social_empresa }}</span>
                <span class="block text-xs text-muted-foreground tabular-nums">CNPJ: {{ l.cnpj_empresa }}</span>
                <span v-if="l.import_status === 'new'" class="block text-xs text-muted-foreground">
                  {{ $t('units.import.willBeNamed', { name: l.import_name }) }}
                </span>
                <span v-else-if="l.import_status === 'already_imported'" class="block text-xs text-muted-foreground">
                  {{ $t('units.import.linkedUnit', { unit: l.unit_name }) }}
                </span>
                <span v-else-if="l.import_status === 'conflict' && l.conflict_reason" class="block text-xs text-destructive">
                  {{ $t('units.import.conflictReason.' + l.conflict_reason, { unit: l.conflict_unit_name }) }}
                </span>
              </span>
              <Badge
                :variant="l.import_status === 'new' ? 'success' : l.import_status === 'conflict' ? 'destructive' : 'secondary'"
                class="shrink-0"
                :data-testid="'x2-import-status-' + l.cod_empresa"
              >{{ $t('units.import.status.' + (l.import_status || 'new')) }}</Badge>
            </label>
          </li>
          <li v-if="filtered.length === 0" class="p-4 text-sm text-center text-muted-foreground">{{ $t('units.import.noStores') }}</li>
        </ul>

        <div class="flex items-center justify-between gap-2">
          <span class="text-sm text-muted-foreground" data-testid="x2-import-count">{{ $t('units.import.selectedCount', { count: selected.length }) }}</span>
          <div class="flex gap-2">
            <Button variant="outline" @click="emit('update:open', false)">{{ $t('common.cancel') }}</Button>
            <Button :disabled="selected.length === 0 || importing" data-testid="x2-import-submit" @click="doImport">
              <Loader2 v-if="importing" class="h-4 w-4 mr-2 animate-spin" />
              {{ importing ? $t('units.import.importing') : $t('units.import.importSelected') }}
            </Button>
          </div>
        </div>
      </div>
    </DialogContent>
  </Dialog>
</template>
