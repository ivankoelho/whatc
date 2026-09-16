<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { PageHeader, DataTable, CrudFormDialog, ErrorState, type Column } from '@/components/shared'
import { occurrenceSLAPoliciesService, type OccurrenceSLAPolicy } from '@/services/api'
import { toast } from 'vue-sonner'
import { Timer, Pencil } from 'lucide-vue-next'
import { getErrorMessage } from '@/lib/api-utils'

const { t } = useI18n()

const policies = ref<OccurrenceSLAPolicy[]>([])
const isLoading = ref(true)
const error = ref(false)

const isDialogOpen = ref(false)
const isSubmitting = ref(false)
const editingPriority = ref<OccurrenceSLAPolicy['priority'] | null>(null)
const formData = ref({ response_minutes: 0, resolution_minutes: 0 })

const columns = computed<Column<OccurrenceSLAPolicy>[]>(() => [
  { key: 'priority', label: t('slaPolicies.columnPriority') },
  { key: 'response_minutes', label: t('slaPolicies.columnResponse') },
  { key: 'resolution_minutes', label: t('slaPolicies.columnResolution') },
  { key: 'actions', label: t('common.actions'), align: 'right' },
])

const priorityLabel = (p: string) => t(`occurrences.priority${p.charAt(0).toUpperCase()}${p.slice(1)}`)

// Minutes are what the API stores and edits; the table shows them as
// hours/days so nobody has to do the arithmetic in their head.
function formatMinutes(mins: number): string {
  if (mins % 60 === 0) return t('slaPolicies.durationHours', { count: mins / 60 })
  if (mins < 60) return t('slaPolicies.durationMinutes', { count: mins })
  return t('slaPolicies.durationHoursMinutes', { hours: Math.floor(mins / 60), minutes: mins % 60 })
}

async function fetchPolicies() {
  isLoading.value = true
  error.value = false
  try {
    const res = await occurrenceSLAPoliciesService.list()
    policies.value = res.data.data.policies
  } catch (e) {
    error.value = true
    toast.error(getErrorMessage(e, t('common.failedLoad', { resource: t('slaPolicies.title') })))
  } finally {
    isLoading.value = false
  }
}

onMounted(fetchPolicies)

function openEditDialog(policy: OccurrenceSLAPolicy) {
  editingPriority.value = policy.priority
  formData.value = { response_minutes: policy.response_minutes, resolution_minutes: policy.resolution_minutes }
  isDialogOpen.value = true
}

function closeDialog() {
  isDialogOpen.value = false
  editingPriority.value = null
}

async function savePolicy() {
  if (!editingPriority.value) return
  if (formData.value.response_minutes <= 0 || formData.value.resolution_minutes <= 0) {
    toast.error(t('slaPolicies.minutesRequired'))
    return
  }
  isSubmitting.value = true
  try {
    await occurrenceSLAPoliciesService.upsert(editingPriority.value, formData.value)
    toast.success(t('common.updatedSuccess', { resource: t('slaPolicies.policy') }))
    closeDialog()
    await fetchPolicies()
  } catch (e) {
    toast.error(getErrorMessage(e, t('common.failedSave', { resource: t('slaPolicies.policy') })))
  } finally {
    isSubmitting.value = false
  }
}
</script>

<template>
  <div class="flex flex-col h-full bg-[#0a0a0b] light:bg-gray-50">
    <PageHeader :title="$t('slaPolicies.title')" :description="$t('slaPolicies.subtitle')" :icon="Timer" icon-gradient="bg-gradient-to-br from-amber-500 to-orange-600 shadow-amber-500/20" back-link="/settings" />

    <ErrorState
      v-if="error && !isLoading"
      :title="$t('common.loadErrorTitle')"
      :description="$t('common.loadErrorDescription')"
      :retry-label="$t('common.retryLoad')"
      class="flex-1"
      @retry="fetchPolicies"
    />

    <ScrollArea v-else orientation="vertical" class="flex-1">
      <div class="p-6">
        <div class="max-w-3xl mx-auto">
          <Card>
            <CardHeader>
              <CardTitle>{{ $t('slaPolicies.cardTitle') }}</CardTitle>
              <CardDescription>{{ $t('slaPolicies.cardDesc') }}</CardDescription>
            </CardHeader>
            <CardContent>
              <DataTable :items="policies" :columns="columns" :is-loading="isLoading" item-name="policies">
                <template #cell-priority="{ item: p }">
                  <span class="font-medium">{{ priorityLabel(p.priority) }}</span>
                </template>
                <template #cell-response_minutes="{ item: p }">
                  <span class="text-muted-foreground">{{ formatMinutes(p.response_minutes) }}</span>
                </template>
                <template #cell-resolution_minutes="{ item: p }">
                  <span class="text-muted-foreground">{{ formatMinutes(p.resolution_minutes) }}</span>
                </template>
                <template #cell-actions="{ item: p }">
                  <div class="flex items-center justify-end">
                    <Button variant="ghost" size="sm" @click="openEditDialog(p)">
                      <Pencil class="h-4 w-4 mr-1.5" />
                      {{ $t('common.edit') }}
                    </Button>
                  </div>
                </template>
              </DataTable>
            </CardContent>
          </Card>
        </div>
      </div>
    </ScrollArea>

    <CrudFormDialog
      v-model:open="isDialogOpen"
      :is-editing="true"
      :is-submitting="isSubmitting"
      :edit-title="$t('slaPolicies.editTitle', { priority: editingPriority ? priorityLabel(editingPriority) : '' })"
      :edit-description="$t('slaPolicies.editDesc')"
      max-width="max-w-sm"
      @submit="savePolicy"
    >
      <div class="space-y-4">
        <div class="space-y-2">
          <Label>{{ $t('slaPolicies.responseMinutesLabel') }}</Label>
          <Input type="number" v-model.number="formData.response_minutes" :min="1" />
          <p class="text-xs text-muted-foreground">{{ $t('slaPolicies.responseMinutesHint') }}</p>
        </div>
        <div class="space-y-2">
          <Label>{{ $t('slaPolicies.resolutionMinutesLabel') }}</Label>
          <Input type="number" v-model.number="formData.resolution_minutes" :min="1" />
          <p class="text-xs text-muted-foreground">{{ $t('slaPolicies.resolutionMinutesHint') }}</p>
        </div>
      </div>
    </CrudFormDialog>
  </div>
</template>
