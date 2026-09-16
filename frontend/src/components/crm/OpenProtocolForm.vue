<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { Button } from '@/components/ui/button'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Loader2, Search, CheckCircle2, UserRound, XCircle } from 'lucide-vue-next'
import {
  contactsService,
  unitsService,
  occurrenceCategoriesService,
  occurrenceWhatHappenedService,
  type Unit,
  type OccurrenceCategory,
  type OccurrenceWhatHappened,
} from '@/services/api'
import type { Contact } from '@/stores/contacts'
import { useOccurrencesStore } from '@/stores/occurrences'
import { debounce } from '@/lib/utils'
import { getErrorMessage } from '@/lib/api-utils'
import { toast } from 'vue-sonner'

const props = defineProps<{
  // Quando aberto a partir de uma conversa, o cliente e a origem já são
  // conhecidos — pula a etapa de busca por telefone (§7 da spec: preserva a
  // relação Contato ↔ Conversa ↔ Ocorrência, sem pedir de novo o que o chat
  // já sabe).
  contactId?: string
  contactPhone?: string
  contactName?: string
  sourceTransferId?: string
}>()

const emit = defineEmits<{ created: [occurrenceId: string]; cancel: [] }>()

const { t } = useI18n()
const router = useRouter()
const store = useOccurrencesStore()

// --- Cliente ---
const phone = ref(props.contactPhone || '')
const searching = ref(false)
const foundContact = ref<Contact | null>(props.contactId
  ? ({ id: props.contactId, phone_number: props.contactPhone || '', profile_name: props.contactName || '' } as Contact)
  : null)
const searchedOnce = ref(!!props.contactId)
const newContactName = ref('')
const cpfCnpj = ref('')

const contactResolved = computed(() => !!foundContact.value || (searchedOnce.value && newContactName.value.trim() !== ''))

async function searchContact(value: string) {
  foundContact.value = null
  searchedOnce.value = false
  if (value.trim().length < 8) return
  searching.value = true
  try {
    const res = await contactsService.list({ search: value.trim(), limit: 1 })
    const data = (res.data as any).data || res.data
    const contacts: Contact[] = data.contacts || []
    foundContact.value = contacts[0] || null
  } catch {
    // Busca é uma conveniência; falha nela não deve travar o formulário.
    foundContact.value = null
  } finally {
    searching.value = false
    searchedOnce.value = true
  }
}

const debouncedSearch = debounce(searchContact, 400)

function onPhoneInput() {
  debouncedSearch(phone.value)
}

function changeContact() {
  foundContact.value = null
  searchedOnce.value = false
  newContactName.value = ''
}

// --- Venda ---
const saleChannel = ref('')
const invoiceNumber = ref('')
const purchaseDate = ref('')
const unitId = ref('')
const productDescription = ref('')
const units = ref<Unit[]>([])

// --- Ocorrência ---
const categoryId = ref('')
const whatHappenedId = ref('')
const priority = ref<'low' | 'normal' | 'high' | 'urgent'>('normal')
const description = ref('')
const internalNote = ref('')
const categories = ref<OccurrenceCategory[]>([])
const whatHappenedOptions = ref<OccurrenceWhatHappened[]>([])

const saleChannels = ['loja_fisica', 'whatsapp', 'telefone', 'site'] as const

onMounted(async () => {
  try {
    const [unitsRes, categoriesRes, whatHappenedRes] = await Promise.all([
      unitsService.list(),
      occurrenceCategoriesService.list(),
      occurrenceWhatHappenedService.list(),
    ])
    units.value = unitsRes.data.data.units
    categories.value = categoriesRes.data.data.categories
    whatHappenedOptions.value = whatHappenedRes.data.data.reasons
  } catch {
    // Unidade/categoria/motivo são opcionais no MVP — form segue funcional sem eles.
  }
})

// --- Submissão ---
const submitting = ref(false)
const result = ref<{ protocolId: string; protocolNumber: string; title: string; sent: boolean } | null>(null)

// Standalone tab (OccurrencesView) has no dialog to close, so Cancel just
// clears the form there; the dialog wrapper (ContactOccurrencesPanel) closes
// itself on this event instead of guessing from context.
function cancelForm() {
  resetForm()
  emit('cancel')
}

function resetForm() {
  phone.value = props.contactPhone || ''
  foundContact.value = props.contactId
    ? ({ id: props.contactId, phone_number: props.contactPhone || '', profile_name: props.contactName || '' } as Contact)
    : null
  searchedOnce.value = !!props.contactId
  newContactName.value = ''
  cpfCnpj.value = ''
  saleChannel.value = ''
  invoiceNumber.value = ''
  purchaseDate.value = ''
  unitId.value = ''
  productDescription.value = ''
  categoryId.value = ''
  whatHappenedId.value = ''
  priority.value = 'normal'
  description.value = ''
  internalNote.value = ''
  result.value = null
}

async function submit() {
  if (submitting.value) return // trava contra duplo clique / duplo protocolo

  if (!phone.value.trim()) {
    toast.error(t('occurrences.validationPhoneRequired'))
    return
  }
  if (!foundContact.value && !newContactName.value.trim()) {
    toast.error(t('occurrences.validationNameRequired'))
    return
  }
  if (!description.value.trim()) {
    toast.error(t('occurrences.validationDescriptionRequired'))
    return
  }

  submitting.value = true
  try {
    let contactId: string
    if (foundContact.value) {
      contactId = foundContact.value.id
      // Só regrava CPF/CNPJ se o agente digitou algo novo além do que já
      // está cadastrado — evita um PUT vazio a cada protocolo.
      if (cpfCnpj.value.trim() && cpfCnpj.value.trim() !== (foundContact.value.cpf_cnpj || '')) {
        await contactsService.update(contactId, { cpf_cnpj: cpfCnpj.value.trim() })
      }
    } else {
      const created = await contactsService.create({
        phone_number: phone.value.trim(),
        profile_name: newContactName.value.trim(),
        cpf_cnpj: cpfCnpj.value.trim() || undefined,
      })
      const data = (created.data as any).data || created.data
      contactId = data.id
    }

    const occurrence = await store.createOccurrence({
      contact_id: contactId,
      description: description.value.trim(),
      priority: priority.value,
      category_id: categoryId.value || undefined,
      what_happened_id: whatHappenedId.value || undefined,
      unit_id: unitId.value || undefined,
      sale_channel: saleChannel.value || undefined,
      invoice_number: invoiceNumber.value.trim() || undefined,
      purchase_date: purchaseDate.value || undefined,
      product_description: productDescription.value.trim() || undefined,
      internal_note: internalNote.value.trim() || undefined,
      source_transfer_id: props.sourceTransferId,
    })

    const sent = await store.trySendProtocol(occurrence.id)

    result.value = {
      protocolId: occurrence.id,
      protocolNumber: occurrence.protocol_number,
      title: occurrence.title,
      sent,
    }
    emit('created', occurrence.id)
  } catch (e) {
    toast.error(getErrorMessage(e, t('chat.occurrenceCreateFailed')))
  } finally {
    submitting.value = false
  }
}

function goToProtocol() {
  if (!result.value) return
  router.push({ name: 'occurrence-detail', params: { id: result.value.protocolId } })
}
</script>

<template>
  <Card class="max-w-3xl">
    <template v-if="result">
      <CardContent class="py-10 text-center space-y-4">
        <CheckCircle2 class="h-12 w-12 text-emerald-500 mx-auto" />
        <div>
          <h3 class="text-lg font-semibold">{{ t('occurrences.protocolCreatedTitle') }}</h3>
          <p class="font-mono text-sm text-muted-foreground mt-1">{{ result.protocolNumber }}</p>
          <p class="text-sm mt-1">{{ result.title }}</p>
        </div>
        <p :class="['text-sm', result.sent ? 'text-emerald-600' : 'text-amber-600']">
          {{ result.sent ? t('occurrences.protocolSentToCustomer') : t('occurrences.protocolNotSentToCustomer') }}
        </p>
        <div class="flex justify-center gap-2 pt-2">
          <Button variant="outline" @click="resetForm">{{ t('occurrences.openAnotherProtocol') }}</Button>
          <Button @click="goToProtocol">{{ t('occurrences.viewProtocol') }}</Button>
        </div>
      </CardContent>
    </template>

    <template v-else>
      <CardHeader>
        <CardTitle>{{ t('occurrences.sectionClient') }}</CardTitle>
      </CardHeader>
      <CardContent class="space-y-4">
        <div class="grid grid-cols-2 gap-4">
          <div class="space-y-2">
            <Label>{{ t('contacts.phoneNumber') }} *</Label>
            <div class="relative">
              <Input v-model="phone" :placeholder="t('contacts.phonePlaceholder')" @input="onPhoneInput" :disabled="!!foundContact" />
              <Loader2 v-if="searching" class="absolute right-2 top-2.5 h-4 w-4 animate-spin text-muted-foreground" />
              <Search v-else class="absolute right-2 top-2.5 h-4 w-4 text-muted-foreground" />
            </div>
          </div>
          <div class="space-y-2">
            <Label>{{ t('occurrences.cpfCnpjLabel') }}</Label>
            <Input v-model="cpfCnpj" :placeholder="t('occurrences.cpfCnpjPlaceholder')" />
          </div>
        </div>

        <div v-if="foundContact" class="flex items-center justify-between rounded-md border border-emerald-500/30 bg-emerald-500/5 p-3">
          <div class="flex items-center gap-2">
            <UserRound class="h-4 w-4 text-emerald-600" />
            <div>
              <p class="text-sm font-medium">{{ t('occurrences.contactFound') }}: {{ foundContact.profile_name || foundContact.phone_number }}</p>
              <p class="text-xs text-muted-foreground">{{ foundContact.phone_number }}</p>
            </div>
          </div>
          <Button v-if="!props.contactId" variant="ghost" size="sm" @click="changeContact">{{ t('occurrences.changeContact') }}</Button>
        </div>

        <div v-else-if="searchedOnce" class="rounded-md border border-dashed p-3 space-y-2">
          <div class="flex items-center gap-2 text-muted-foreground text-sm">
            <XCircle class="h-4 w-4" />
            {{ t('occurrences.contactNotFound') }}
          </div>
          <div class="space-y-2">
            <Label>{{ t('contacts.profileName') }} *</Label>
            <Input v-model="newContactName" :placeholder="t('occurrences.contactNotFoundDesc')" />
          </div>
        </div>
      </CardContent>

      <template v-if="contactResolved">
        <CardHeader>
          <CardTitle>{{ t('occurrences.sectionSale') }}</CardTitle>
        </CardHeader>
        <CardContent class="grid grid-cols-2 gap-4">
          <div class="space-y-2">
            <Label>{{ t('occurrences.saleChannelLabel') }}</Label>
            <Select v-model="saleChannel">
              <SelectTrigger><SelectValue :placeholder="t('occurrences.saleChannelPlaceholder')" /></SelectTrigger>
              <SelectContent>
                <SelectItem v-for="c in saleChannels" :key="c" :value="c">
                  {{ t(`occurrences.saleChannel${c.split('_').map(w => w[0].toUpperCase() + w.slice(1)).join('')}`) }}
                </SelectItem>
              </SelectContent>
            </Select>
          </div>
          <div class="space-y-2">
            <Label>{{ t('occurrences.invoiceNumberLabel') }}</Label>
            <Input v-model="invoiceNumber" />
          </div>
          <div class="space-y-2">
            <Label>{{ t('occurrences.purchaseDateLabel') }}</Label>
            <Input v-model="purchaseDate" type="date" />
          </div>
          <div class="space-y-2">
            <Label>{{ t('occurrences.unitLabel') }}</Label>
            <Select v-model="unitId">
              <SelectTrigger><SelectValue :placeholder="t('occurrences.unitPlaceholder')" /></SelectTrigger>
              <SelectContent>
                <SelectItem v-for="u in units" :key="u.id" :value="u.id">{{ u.name }}</SelectItem>
              </SelectContent>
            </Select>
          </div>
          <div class="space-y-2 col-span-2">
            <Label>{{ t('occurrences.productLabel') }}</Label>
            <Input v-model="productDescription" :placeholder="t('occurrences.productPlaceholder')" />
          </div>
        </CardContent>

        <CardHeader>
          <CardTitle>{{ t('occurrences.sectionCase') }}</CardTitle>
        </CardHeader>
        <CardContent class="space-y-4">
          <div class="grid grid-cols-2 gap-4">
            <div class="space-y-2">
              <Label>{{ t('occurrences.categoryLabel') }}</Label>
              <Select v-model="categoryId">
                <SelectTrigger><SelectValue :placeholder="t('occurrences.categoryPlaceholder')" /></SelectTrigger>
                <SelectContent>
                  <SelectItem v-for="c in categories" :key="c.id" :value="c.id">{{ c.name }}</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div class="space-y-2">
              <Label>{{ t('occurrences.whatHappenedLabel') }}</Label>
              <Select v-model="whatHappenedId">
                <SelectTrigger><SelectValue :placeholder="t('occurrences.whatHappenedPlaceholder')" /></SelectTrigger>
                <SelectContent>
                  <SelectItem v-for="w in whatHappenedOptions" :key="w.id" :value="w.id">{{ w.name }}</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div class="space-y-2">
              <Label>{{ t('occurrences.priorityLabel') }}</Label>
              <Select v-model="priority">
                <SelectTrigger><SelectValue /></SelectTrigger>
                <SelectContent>
                  <SelectItem value="low">{{ t('occurrences.priorityLow') }}</SelectItem>
                  <SelectItem value="normal">{{ t('occurrences.priorityNormal') }}</SelectItem>
                  <SelectItem value="high">{{ t('occurrences.priorityHigh') }}</SelectItem>
                  <SelectItem value="urgent">{{ t('occurrences.priorityUrgent') }}</SelectItem>
                </SelectContent>
              </Select>
            </div>
          </div>
          <div class="space-y-2">
            <Label>{{ t('occurrences.descriptionRequired') }}</Label>
            <Textarea v-model="description" :placeholder="t('occurrences.descriptionPlaceholder')" :rows="4" />
          </div>
        </CardContent>

        <CardHeader>
          <CardTitle class="text-sm text-muted-foreground font-medium">{{ t('occurrences.sectionInternal') }}</CardTitle>
        </CardHeader>
        <CardContent>
          <Textarea v-model="internalNote" :placeholder="t('occurrences.internalNotePlaceholder')" :rows="2" />
        </CardContent>

        <CardContent class="flex justify-end gap-2 pt-0">
          <Button variant="outline" @click="cancelForm" :disabled="submitting">{{ t('common.cancel') }}</Button>
          <Button @click="submit" :disabled="submitting">
            <Loader2 v-if="submitting" class="h-4 w-4 mr-2 animate-spin" />
            {{ submitting ? t('occurrences.registeringProtocol') : t('occurrences.registerProtocol') }}
          </Button>
        </CardContent>
      </template>
    </template>
  </Card>
</template>
