<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Badge } from '@/components/ui/badge'
import { DataTable, CrudFormDialog, DeleteConfirmDialog, IconButton, ErrorState, type Column } from '@/components/shared'
import { whatsappRatesService, whatsappUsageService, type WhatsAppRate } from '@/services/api'
import { useCrudState } from '@/composables/useCrudState'
import { useAuthStore } from '@/stores/auth'
import { getErrorMessage } from '@/lib/api-utils'
import { dayOf, formatMoney } from '@/lib/whatsapp-usage'
import { Plus, Pencil, Trash2, RefreshCw, Receipt } from 'lucide-vue-next'

const { t, locale } = useI18n()
const authStore = useAuthStore()

// The buttons only mirror whatsapp_usage:write; the API is the authority and answers 403 otherwise.
const canWrite = computed(() => authStore.hasPermission('whatsapp_usage', 'write'))

interface RateForm {
  country: string
  category: string
  price: string
  currency: string
  valid_from: string
  valid_to: string
  notes: string
}
const defaultForm: RateForm = { country: '55', category: 'utility', price: '', currency: 'BRL', valid_from: '', valid_to: '', notes: '' }

const {
  items: rates, isLoading, isSubmitting, isDialogOpen, editingItem: editingRate, deleteDialogOpen, itemToDelete: rateToDelete,
  formData, openCreateDialog, openEditDialog: baseOpenEditDialog, openDeleteDialog, closeDialog, closeDeleteDialog,
} = useCrudState<WhatsAppRate, RateForm>(defaultForm)

const loadError = ref<'forbidden' | 'failed' | null>(null)
const isRepricing = ref(false)
const currencyLocale = computed(() => (locale.value === 'en' ? 'en-US' : 'pt-BR'))

const columns = computed<Column<WhatsAppRate>[]>(() => [
  { key: 'category', label: t('whatsappUsage.category') },
  { key: 'country', label: t('whatsappUsage.country') },
  { key: 'price', label: t('whatsappUsage.price'), align: 'right' },
  { key: 'valid_from', label: t('whatsappUsage.validFrom') },
  { key: 'valid_to', label: t('whatsappUsage.validTo') },
  { key: 'in_use', label: t('whatsappUsage.inUse'), align: 'center' },
  ...(canWrite.value ? [{ key: 'actions', label: t('common.actions'), align: 'right' } as Column<WhatsAppRate>] : []),
])

async function fetchRates() {
  isLoading.value = true
  loadError.value = null
  try {
    rates.value = (await whatsappRatesService.list()).data.data.rates
  } catch (e: any) {
    loadError.value = e?.response?.status === 403 ? 'forbidden' : 'failed'
    if (loadError.value === 'failed') toast.error(getErrorMessage(e, t('common.failedLoad', { resource: t('whatsappUsage.ratesTitle') })))
  } finally {
    isLoading.value = false
  }
}
onMounted(fetchRates)

function openEditDialog(rate: WhatsAppRate) {
  baseOpenEditDialog(rate, (r) => ({
    country: r.country, category: r.category, price: String(r.price), currency: r.currency,
    valid_from: dayOf(r.valid_from), valid_to: dayOf(r.valid_to), notes: r.notes ?? '',
  }))
}

// A price already used by messages only accepts closing its period (and notes).
const lockedCore = computed(() => !!editingRate.value?.in_use)

async function saveRate() {
  const f = formData.value
  const price = Number(f.price)
  if (!f.country.trim() || !f.category.trim() || f.price === '' || Number.isNaN(price) || !f.currency.trim() || !f.valid_from) {
    toast.error(t('whatsappUsage.priceRequired'))
    return
  }
  isSubmitting.value = true
  try {
    const payload = {
      country: f.country.trim(), category: f.category.trim().toLowerCase(), price, currency: f.currency.trim().toUpperCase(),
      valid_from: f.valid_from, valid_to: f.valid_to || undefined, notes: f.notes.trim() || undefined,
    }
    if (editingRate.value) await whatsappRatesService.update(editingRate.value.id, payload)
    else await whatsappRatesService.create(payload)
    toast.success(t(editingRate.value ? 'common.updatedSuccess' : 'common.createdSuccess', { resource: t('whatsappUsage.ratesTitle') }))
    closeDialog()
    await fetchRates()
  } catch (e) {
    // Overlaps, a used price and the like come back as 400/409 with their reason, shown as is.
    toast.error(getErrorMessage(e, t('common.failedSave', { resource: t('whatsappUsage.ratesTitle') })))
  } finally {
    isSubmitting.value = false
  }
}

async function confirmDelete() {
  if (!rateToDelete.value) return
  isSubmitting.value = true
  try {
    await whatsappRatesService.delete(rateToDelete.value.id)
    toast.success(t('common.deletedSuccess', { resource: t('whatsappUsage.ratesTitle') }))
    closeDeleteDialog()
    await fetchRates()
  } catch (e) {
    toast.error(getErrorMessage(e, t('common.failedDelete', { resource: t('whatsappUsage.ratesTitle') })))
  } finally {
    isSubmitting.value = false
  }
}

async function reprice() {
  isRepricing.value = true
  try {
    const res = (await whatsappUsageService.reprice()).data.data
    toast.success(t('whatsappUsage.repriced', { changed: res.changed, processed: res.processed }))
  } catch (e: any) {
    toast.error(e?.response?.status === 409 ? t('whatsappUsage.repriceOff') : getErrorMessage(e, t('whatsappUsage.reprice')))
  } finally {
    isRepricing.value = false
  }
}
</script>

<template>
  <div class="flex flex-col h-full bg-[#0a0a0b] light:bg-gray-50">
    <ErrorState
      v-if="loadError && !isLoading"
      :title="loadError === 'forbidden' ? $t('whatsappUsage.loadForbidden') : $t('common.loadErrorTitle')"
      :description="loadError === 'forbidden' ? '' : $t('common.loadErrorDescription')"
      :retry-label="$t('common.retryLoad')"
      class="flex-1"
      @retry="fetchRates"
    />

    <ScrollArea v-else orientation="vertical" class="flex-1">
      <div class="p-6">
        <div class="max-w-5xl mx-auto">
          <Card>
            <CardHeader>
              <div class="flex flex-wrap items-start justify-between gap-3">
                <div>
                  <CardTitle>{{ $t('whatsappUsage.ratesTitle') }}</CardTitle>
                  <CardDescription>{{ $t('whatsappUsage.ratesDesc') }}</CardDescription>
                </div>
                <div v-if="canWrite" class="flex gap-2">
                  <Button variant="outline" size="sm" :disabled="isRepricing" :title="$t('whatsappUsage.repriceHint')" data-testid="rate-reprice" @click="reprice">
                    <RefreshCw class="h-4 w-4 mr-2" :class="isRepricing ? 'animate-spin' : ''" />{{ $t('whatsappUsage.reprice') }}
                  </Button>
                  <Button variant="outline" size="sm" data-testid="rate-add" @click="openCreateDialog"><Plus class="h-4 w-4 mr-2" />{{ $t('whatsappUsage.addRate') }}</Button>
                </div>
              </div>
            </CardHeader>
            <CardContent>
              <DataTable
                :items="rates" :columns="columns" :is-loading="isLoading" :empty-icon="Receipt"
                :empty-title="$t('whatsappUsage.noRates')" :empty-description="$t('whatsappUsage.noRatesDesc')" item-name="rates"
              >
                <template #cell-category="{ item }"><span class="font-medium">{{ item.category }}</span></template>
                <template #cell-country="{ item }">{{ item.country === '*' ? $t('whatsappUsage.anyCountry') : item.country }}</template>
                <template #cell-price="{ item }">{{ formatMoney(item.price, item.currency, currencyLocale) }}</template>
                <template #cell-valid_from="{ item }">{{ dayOf(item.valid_from) }}</template>
                <template #cell-valid_to="{ item }">{{ item.valid_to ? dayOf(item.valid_to) : $t('whatsappUsage.open') }}</template>
                <template #cell-in_use="{ item }"><Badge v-if="item.in_use" variant="secondary">{{ $t('whatsappUsage.inUse') }}</Badge></template>
                <template #cell-actions="{ item }">
                  <div class="flex items-center justify-end gap-1">
                    <IconButton :icon="Pencil" :label="$t('whatsappUsage.editRate')" class="h-8 w-8" @click="openEditDialog(item)" />
                    <IconButton v-if="!item.in_use" :label="$t('whatsappUsage.deleteTitle')" class="h-8 w-8" @click="openDeleteDialog(item)">
                      <Trash2 class="h-4 w-4 text-destructive" />
                    </IconButton>
                  </div>
                </template>
                <template v-if="canWrite" #empty-action>
                  <Button variant="outline" size="sm" @click="openCreateDialog"><Plus class="h-4 w-4 mr-2" />{{ $t('whatsappUsage.addRate') }}</Button>
                </template>
              </DataTable>
            </CardContent>
          </Card>
        </div>
      </div>
    </ScrollArea>

    <CrudFormDialog
      v-model:open="isDialogOpen"
      :is-editing="!!editingRate"
      :is-submitting="isSubmitting"
      :edit-title="$t('whatsappUsage.editTitle')"
      :create-title="$t('whatsappUsage.createTitle')"
      :edit-description="$t('whatsappUsage.editDesc')"
      :create-description="$t('whatsappUsage.createDesc')"
      max-width="max-w-lg"
      @submit="saveRate"
    >
      <div class="grid gap-4 sm:grid-cols-2">
        <div class="space-y-1.5">
          <Label>{{ $t('whatsappUsage.country') }}</Label>
          <Input v-model="formData.country" :disabled="lockedCore" maxlength="4" data-testid="rate-country" />
          <p class="text-xs text-muted-foreground">{{ $t('whatsappUsage.countryHint') }}</p>
        </div>
        <div class="space-y-1.5">
          <Label>{{ $t('whatsappUsage.category') }}</Label>
          <Input v-model="formData.category" :disabled="lockedCore" maxlength="40" data-testid="rate-category" />
          <p class="text-xs text-muted-foreground">{{ $t('whatsappUsage.categoryHint') }}</p>
        </div>
        <div class="space-y-1.5">
          <Label>{{ $t('whatsappUsage.price') }}</Label>
          <Input v-model="formData.price" :disabled="lockedCore" type="number" step="0.000001" min="0" data-testid="rate-price" />
        </div>
        <div class="space-y-1.5">
          <Label>{{ $t('whatsappUsage.currency') }}</Label>
          <Input v-model="formData.currency" :disabled="lockedCore" maxlength="3" data-testid="rate-currency" />
        </div>
        <div class="space-y-1.5">
          <Label>{{ $t('whatsappUsage.validFrom') }}</Label>
          <Input v-model="formData.valid_from" :disabled="lockedCore" type="date" data-testid="rate-valid-from" />
        </div>
        <div class="space-y-1.5">
          <Label>{{ $t('whatsappUsage.validTo') }}</Label>
          <Input v-model="formData.valid_to" type="date" data-testid="rate-valid-to" />
        </div>
        <div class="space-y-1.5 sm:col-span-2">
          <Label>{{ $t('whatsappUsage.notes') }}</Label>
          <Input v-model="formData.notes" data-testid="rate-notes" />
        </div>
      </div>
    </CrudFormDialog>

    <DeleteConfirmDialog v-model:open="deleteDialogOpen" :title="$t('whatsappUsage.deleteTitle')" :item-name="rateToDelete ? `${rateToDelete.category} · ${rateToDelete.country}` : ''" :is-submitting="isSubmitting" @confirm="confirmDelete">
      <p class="text-sm text-muted-foreground">{{ $t('whatsappUsage.deleteWarning') }}</p>
    </DeleteConfirmDialog>
  </div>
</template>
