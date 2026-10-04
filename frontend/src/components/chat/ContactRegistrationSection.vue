<script setup lang="ts">
import { ref, computed, watch, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { Loader2 } from 'lucide-vue-next'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { contactsService, unitsService, departmentsService, type Unit, type Department } from '@/services/api'
import { getErrorMessage } from '@/lib/api-utils'
import { CONTACT_TYPES, showsPlacement, placementPayload } from '@/lib/contact-registration'
import type { Contact } from '@/stores/contacts'

// The registration rows of a contact (type and document; unit and department only for a
// colaborador). It sits under the name and phone in the header of the panel, and the pencil that
// opens the form lives in that header: `editing` is owned by the parent.
const props = defineProps<{ contact: Contact; editing: boolean }>()
const emit = defineEmits<{
  'update:editing': [editing: boolean]
  updated: [registration: Pick<Contact, 'contact_type' | 'cpf_cnpj' | 'unit_id' | 'department_id'>]
}>()

const { t } = useI18n()

const NONE = 'none' // the Select cannot hold an empty value
const units = ref<Unit[]>([])
const departments = ref<Department[]>([])
const isSaving = ref(false)
const form = ref({ contact_type: 'cliente' as string, cpf_cnpj: '', unit_id: NONE, department_id: NONE })

const type = computed(() => props.contact.contact_type || 'cliente')
const unitName = computed(() => units.value.find(u => u.id === props.contact.unit_id)?.name)
const departmentName = computed(() => departments.value.find(d => d.id === props.contact.department_id)?.name)

// Both lists are optional context: a user without units/departments access gets empty selects.
async function fetchPlacementOptions() {
  try {
    const res = await unitsService.list()
    units.value = ((res.data as any).data || res.data).units || []
  } catch { /* no access */ }
  try {
    const res = await departmentsService.list()
    departments.value = ((res.data as any).data || res.data).departments || []
  } catch { /* no access */ }
}
onMounted(fetchPlacementOptions)

// The form starts from the contact every time it opens.
watch(() => props.editing, (editing) => {
  if (!editing) return
  form.value = {
    contact_type: type.value,
    cpf_cnpj: props.contact.cpf_cnpj || '',
    unit_id: props.contact.unit_id || NONE,
    department_id: props.contact.department_id || NONE,
  }
})

// Switching contacts with the form open must not post the old draft to the new contact.
watch(() => props.contact.id, () => emit('update:editing', false))

async function save() {
  isSaving.value = true
  try {
    const payload: Record<string, any> = {
      contact_type: form.value.contact_type,
      ...placementPayload(
        form.value.contact_type,
        form.value.unit_id === NONE ? '' : form.value.unit_id,
        form.value.department_id === NONE ? '' : form.value.department_id,
      ),
    }
    // Only send the document when it was edited: with number masking on, the value shown is
    // masked and must not be written back.
    if (form.value.cpf_cnpj !== (props.contact.cpf_cnpj || '')) {
      payload.cpf_cnpj = form.value.cpf_cnpj
    }
    const res = await contactsService.update(props.contact.id, payload)
    const saved = (res.data as any).data || res.data
    emit('updated', {
      contact_type: saved.contact_type,
      cpf_cnpj: saved.cpf_cnpj,
      unit_id: saved.unit_id,
      department_id: saved.department_id,
    })
    emit('update:editing', false)
    toast.success(t('contacts.registrationSaved'))
  } catch (e) {
    toast.error(getErrorMessage(e, t('common.failedSave', { resource: t('resources.contact') })))
  } finally {
    isSaving.value = false
  }
}
</script>

<template>
  <div class="mt-3" data-testid="contact-registration">
    <!-- Read: labels in one column, values aligned in the next -->
    <dl v-if="!editing" class="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-sm">
      <dt class="text-muted-foreground">{{ $t('contacts.type') }}</dt>
      <dd class="min-w-0 break-words" data-testid="contact-registration-type">{{ $t('contacts.types.' + type) }}</dd>

      <dt class="text-muted-foreground">{{ $t('contacts.document') }}</dt>
      <dd class="min-w-0 break-words" data-testid="contact-registration-document">{{ contact.cpf_cnpj || $t('contacts.notInformed') }}</dd>

      <template v-if="showsPlacement(type)">
        <dt class="text-muted-foreground">{{ $t('contacts.unit') }}</dt>
        <dd class="min-w-0 break-words" data-testid="contact-registration-unit">{{ unitName || (contact.unit_id ? $t('contacts.defined') : $t('contacts.notInformed')) }}</dd>

        <dt class="text-muted-foreground">{{ $t('contacts.department') }}</dt>
        <dd class="min-w-0 break-words" data-testid="contact-registration-department">{{ departmentName || (contact.department_id ? $t('contacts.defined') : $t('contacts.notInformed')) }}</dd>
      </template>
    </dl>

    <!-- Edit -->
    <form v-else class="space-y-3" @submit.prevent="save">
      <div class="space-y-1.5">
        <Label class="text-xs">{{ $t('contacts.type') }}</Label>
        <Select v-model="form.contact_type">
          <SelectTrigger data-testid="contact-registration-type-select"><SelectValue /></SelectTrigger>
          <SelectContent>
            <SelectItem v-for="ct in CONTACT_TYPES" :key="ct" :value="ct">{{ $t('contacts.types.' + ct) }}</SelectItem>
          </SelectContent>
        </Select>
      </div>

      <div class="space-y-1.5">
        <Label class="text-xs">{{ $t('contacts.document') }}</Label>
        <Input v-model="form.cpf_cnpj" :placeholder="$t('contacts.documentPlaceholder')" data-testid="contact-registration-document-input" />
      </div>

      <template v-if="showsPlacement(form.contact_type)">
        <div class="space-y-1.5">
          <Label class="text-xs">{{ $t('contacts.unit') }}</Label>
          <Select v-model="form.unit_id">
            <SelectTrigger data-testid="contact-registration-unit-select"><SelectValue /></SelectTrigger>
            <SelectContent>
              <SelectItem :value="NONE">{{ $t('contacts.noneSelected') }}</SelectItem>
              <SelectItem v-for="u in units" :key="u.id" :value="u.id">{{ u.name }}</SelectItem>
            </SelectContent>
          </Select>
        </div>
        <div class="space-y-1.5">
          <Label class="text-xs">{{ $t('contacts.department') }}</Label>
          <Select v-model="form.department_id">
            <SelectTrigger data-testid="contact-registration-department-select"><SelectValue /></SelectTrigger>
            <SelectContent>
              <SelectItem :value="NONE">{{ $t('contacts.noneSelected') }}</SelectItem>
              <SelectItem v-for="d in departments" :key="d.id" :value="d.id">{{ d.name }}</SelectItem>
            </SelectContent>
          </Select>
        </div>
      </template>

      <div class="flex justify-end gap-2">
        <Button type="button" variant="ghost" size="sm" :disabled="isSaving" @click="emit('update:editing', false)">{{ $t('common.cancel') }}</Button>
        <Button type="submit" size="sm" :disabled="isSaving" data-testid="contact-registration-save">
          <Loader2 v-if="isSaving" class="h-3.5 w-3.5 mr-1 animate-spin" />
          {{ $t('common.save') }}
        </Button>
      </div>
    </form>
  </div>
</template>
