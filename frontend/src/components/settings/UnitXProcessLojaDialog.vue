<script setup lang="ts">
// Administrative link between a local unit and an X2 store (cod_empresa).
// The administrator picks the store explicitly: the "suggestion" badge is only a
// hint from the names and nothing is saved until "Save". The store list comes from
// X2 (and may fail); an already saved link never depends on it.
import { ref, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Checkbox } from '@/components/ui/checkbox'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { unitsService, type Unit, type XProcessLoja } from '@/services/api'
import { getErrorMessage } from '@/lib/api-utils'
import { Loader2 } from 'lucide-vue-next'

const props = defineProps<{ open: boolean; unit: Unit | null }>()
const emit = defineEmits<{ 'update:open': [value: boolean]; saved: [] }>()

const { t } = useI18n()

const lojas = ref<XProcessLoja[]>([])
const loading = ref(false)
const loadFailed = ref(false)
const saving = ref(false)
const selected = ref('')
const search = ref('')
const fillCnpj = ref(false) // off by default: the administrator opts in

const currentCod = computed(() => props.unit?.xprocess_cod_empresa || '')
const unitHasCnpj = computed(() => !!props.unit?.cnpj)

const filtered = computed(() => {
  const q = search.value.trim().toLowerCase()
  if (!q) return lojas.value
  return lojas.value.filter(l =>
    l.cod_empresa.toLowerCase().includes(q) || l.razao_social_empresa.toLowerCase().includes(q) || l.cnpj_empresa.replace(/\D/g, '').includes(q.replace(/\D/g, '') || '\u0000'))
})

function linkedElsewhere(l: XProcessLoja) {
  return !!l.unit_id && l.unit_id !== props.unit?.id
}

async function load() {
  loading.value = true
  loadFailed.value = false
  try {
    const res = await unitsService.listXProcessLojas()
    lojas.value = res.data.data.lojas
  } catch (e) {
    loadFailed.value = true
    lojas.value = []
    toast.error(getErrorMessage(e, t('units.x2LoadFailed')))
  } finally {
    loading.value = false
  }
}

watch(() => props.open, isOpen => {
  if (!isOpen) return
  selected.value = currentCod.value
  search.value = ''
  fillCnpj.value = false
  load()
})

async function save(cod: string) {
  if (!props.unit) return
  saving.value = true
  try {
    const { data } = await unitsService.setXProcessLoja(props.unit.id, { cod_empresa: cod, fill_cnpj: cod !== '' && fillCnpj.value })
    toast.success(cod ? t('units.x2Linked') : t('units.x2Cleared'))
    if (fillCnpj.value && cod) {
      if (data.data.cnpj_filled) toast.success(t('units.x2CnpjFilled'))
      else if (data.data.cnpj_note) toast.warning(t('units.x2CnpjNote.' + data.data.cnpj_note))
    }
    emit('saved')
    emit('update:open', false)
  } catch (e) {
    toast.error(getErrorMessage(e, t('units.x2SaveFailed')))
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <Dialog :open="open" @update:open="emit('update:open', $event)">
    <DialogContent class="sm:max-w-2xl">
      <DialogHeader>
        <DialogTitle>{{ $t('units.x2DialogTitle') }} · {{ unit?.name }}</DialogTitle>
        <DialogDescription>{{ $t('units.x2DialogDesc') }}</DialogDescription>
      </DialogHeader>

      <div class="space-y-3 py-2">
        <Input v-model="search" :placeholder="$t('units.x2Search')" :disabled="loading || loadFailed" />

        <div v-if="loading" class="flex items-center gap-2 text-sm text-muted-foreground py-6 justify-center">
          <Loader2 class="h-4 w-4 animate-spin" /> {{ $t('common.loading') }}
        </div>
        <div v-else-if="loadFailed" class="text-sm text-muted-foreground py-6 text-center space-y-2">
          <p>{{ $t('units.x2LoadFailedHint') }}</p>
          <Button variant="outline" size="sm" @click="load">{{ $t('common.retryLoad') }}</Button>
        </div>
        <ul v-else class="max-h-72 overflow-y-auto divide-y rounded-md border" data-testid="x2-lojas-list">
          <li v-for="l in filtered" :key="l.cod_empresa">
            <label
              class="flex items-start gap-3 p-2.5 text-sm cursor-pointer hover:bg-muted/50"
              :class="{ 'opacity-50 cursor-not-allowed': linkedElsewhere(l) }"
            >
              <input
                v-model="selected"
                type="radio"
                name="x2-loja"
                class="mt-1"
                :value="l.cod_empresa"
                :disabled="linkedElsewhere(l)"
                :data-testid="'x2-loja-' + l.cod_empresa"
              />
              <span class="flex-1 min-w-0">
                <span class="font-medium">X2 {{ l.cod_empresa }}</span>
                <span class="text-muted-foreground"> · {{ l.razao_social_empresa }}</span>
                <span class="block text-xs text-muted-foreground tabular-nums">{{ l.cnpj_empresa }}</span>
              </span>
              <Badge v-if="linkedElsewhere(l)" variant="secondary" class="shrink-0">{{ $t('units.x2LinkedTo', { unit: l.unit_name }) }}</Badge>
              <Badge v-else-if="l.suggested_unit_id === unit?.id" variant="outline" class="shrink-0" :title="$t('units.x2SuggestionHint')">{{ $t('units.x2Suggestion') }}</Badge>
            </label>
          </li>
          <li v-if="filtered.length === 0" class="p-4 text-sm text-center text-muted-foreground">{{ $t('units.x2NoStores') }}</li>
        </ul>

        <div class="flex items-start gap-2">
          <Checkbox id="x2-fill-cnpj" :checked="fillCnpj" :disabled="unitHasCnpj" @update:checked="fillCnpj = $event === true" />
          <div>
            <Label for="x2-fill-cnpj" class="text-sm font-normal">{{ $t('units.x2FillCnpj') }}</Label>
            <p class="text-xs text-muted-foreground">{{ unitHasCnpj ? $t('units.x2FillCnpjHasCnpj') : $t('units.x2FillCnpjHint') }}</p>
          </div>
        </div>
      </div>

      <div class="flex items-center justify-between gap-2">
        <Button v-if="currentCod" variant="ghost" class="text-destructive" :disabled="saving" data-testid="x2-loja-clear" @click="save('')">
          {{ $t('units.x2Clear') }}
        </Button>
        <span v-else />
        <div class="flex gap-2">
          <Button variant="outline" @click="emit('update:open', false)">{{ $t('common.cancel') }}</Button>
          <Button :disabled="saving || !selected || selected === currentCod" data-testid="x2-loja-save" @click="save(selected)">
            <Loader2 v-if="saving" class="h-4 w-4 mr-2 animate-spin" />
            {{ $t('common.save') }}
          </Button>
        </div>
      </div>
    </DialogContent>
  </Dialog>
</template>
