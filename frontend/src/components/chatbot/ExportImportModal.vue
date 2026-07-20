<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
} from '@/components/ui/dialog'
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/tabs'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Download, Upload, Loader2 } from 'lucide-vue-next'
import { chatbotService, accountsService } from '@/services/api'
import { useChatbotExportImportStore } from '@/stores/chatbotExportImport'
import { getErrorMessage } from '@/lib/api-utils'
import { toast } from 'vue-sonner'

const open = defineModel<boolean>('open', { default: false })
const props = withDefaults(defineProps<{ initialTab?: 'export' | 'import' }>(), {
  initialTab: 'export',
})
const emit = defineEmits<{ imported: [] }>()

const { t } = useI18n()
const store = useChatbotExportImportStore()

const activeTab = ref<'export' | 'import'>('export')
const flows = ref<{ id: string; name: string }[]>([])
const accounts = ref<{ name: string }[]>([])
const selectedFlowId = ref('')
const selectedAccount = ref('')
const importFile = ref<File | null>(null)
const loadingLists = ref(false)

watch(open, async (isOpen) => {
  if (!isOpen) return
  activeTab.value = props.initialTab
  selectedFlowId.value = ''
  selectedAccount.value = ''
  importFile.value = null
  await loadLists()
})

async function loadLists() {
  loadingLists.value = true
  try {
    const [flowsRes, accountsRes] = await Promise.all([
      chatbotService.listFlows({ limit: 200 }),
      accountsService.list(),
    ])
    const fData = (flowsRes.data as any).data || flowsRes.data
    flows.value = (fData.flows || []).map((f: any) => ({ id: f.id, name: f.name }))
    const aData = (accountsRes.data as any).data || accountsRes.data
    accounts.value = (aData.accounts || []).map((a: any) => ({ name: a.name }))
  } catch (e) {
    toast.error(getErrorMessage(e))
  } finally {
    loadingLists.value = false
  }
}

async function handleExport() {
  if (!selectedFlowId.value) return
  try {
    await store.exportFlow(selectedFlowId.value)
    toast.success(t('chatbotFlows.exportSuccess'))
  } catch (e) {
    toast.error(getErrorMessage(e, t('chatbotFlows.exportFailed')))
  }
}

function onFileChange(e: Event) {
  const input = e.target as HTMLInputElement
  importFile.value = input.files?.[0] || null
}

async function handleImport() {
  if (!importFile.value) {
    toast.error(t('chatbotFlows.selectFileFirst'))
    return
  }
  if (!selectedAccount.value) {
    toast.error(t('chatbotFlows.selectAccountFirst'))
    return
  }
  try {
    // Read the file as text before sending — the File object is never sent.
    const text = await importFile.value.text()
    const result = await store.importFlow(text, selectedAccount.value)
    toast.success(
      t('chatbotFlows.importSuccess', {
        name: result.imported_flow_name,
        count: result.imported_keywords,
      }),
    )
    emit('imported')
    open.value = false
  } catch (e: any) {
    if (e?.message === 'invalid-json') {
      toast.error(t('chatbotFlows.invalidJsonFile'))
    } else {
      toast.error(getErrorMessage(e, t('chatbotFlows.importFailed')))
    }
  }
}
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent class="sm:max-w-[480px]">
      <DialogHeader>
        <DialogTitle>{{ t('chatbotFlows.exportImport') }}</DialogTitle>
        <DialogDescription>{{ t('chatbotFlows.exportDesc') }}</DialogDescription>
      </DialogHeader>

      <Tabs v-model="activeTab" class="mt-2">
        <TabsList class="grid w-full grid-cols-2">
          <TabsTrigger value="export">{{ t('chatbotFlows.exportTab') }}</TabsTrigger>
          <TabsTrigger value="import">{{ t('chatbotFlows.importTab') }}</TabsTrigger>
        </TabsList>

        <!-- Export -->
        <TabsContent value="export" class="space-y-4 pt-4">
          <div class="space-y-1.5">
            <Label class="text-sm">{{ t('chatbotFlows.selectFlowToExport') }}</Label>
            <Select v-model="selectedFlowId" :disabled="loadingLists || flows.length === 0">
              <SelectTrigger>
                <SelectValue :placeholder="t('chatbotFlows.selectFlowToExport')" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem v-for="f in flows" :key="f.id" :value="f.id">{{ f.name }}</SelectItem>
              </SelectContent>
            </Select>
            <p v-if="!loadingLists && flows.length === 0" class="text-xs text-muted-foreground">
              {{ t('chatbotFlows.noFlowsToExport') }}
            </p>
          </div>
          <Button class="w-full" :disabled="!selectedFlowId || store.exporting" @click="handleExport">
            <Loader2 v-if="store.exporting" class="h-4 w-4 mr-2 animate-spin" />
            <Download v-else class="h-4 w-4 mr-2" />
            {{ store.exporting ? t('chatbotFlows.exporting') : t('chatbotFlows.exportTab') }}
          </Button>
        </TabsContent>

        <!-- Import -->
        <TabsContent value="import" class="space-y-4 pt-4">
          <p class="text-xs text-muted-foreground">{{ t('chatbotFlows.importDesc') }}</p>
          <div class="space-y-1.5">
            <Label class="text-sm">{{ t('chatbotFlows.selectFile') }}</Label>
            <input
              type="file"
              accept="application/json,.json"
              class="block w-full text-sm text-muted-foreground file:mr-3 file:rounded-md file:border file:border-input file:bg-background file:px-3 file:py-1.5 file:text-sm file:font-medium hover:file:bg-accent"
              @change="onFileChange"
            />
          </div>
          <div class="space-y-1.5">
            <Label class="text-sm">{{ t('chatbotFlows.targetAccount') }}</Label>
            <Select v-model="selectedAccount" :disabled="loadingLists || accounts.length === 0">
              <SelectTrigger>
                <SelectValue :placeholder="t('chatbotFlows.selectAccount')" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem v-for="a in accounts" :key="a.name" :value="a.name">{{ a.name }}</SelectItem>
              </SelectContent>
            </Select>
            <p v-if="!loadingLists && accounts.length === 0" class="text-xs text-muted-foreground">
              {{ t('chatbotFlows.noAccounts') }}
            </p>
          </div>
          <Button
            class="w-full"
            :disabled="!importFile || !selectedAccount || store.importing"
            @click="handleImport"
          >
            <Loader2 v-if="store.importing" class="h-4 w-4 mr-2 animate-spin" />
            <Upload v-else class="h-4 w-4 mr-2" />
            {{ store.importing ? t('chatbotFlows.importing') : t('chatbotFlows.importTab') }}
          </Button>
        </TabsContent>
      </Tabs>
    </DialogContent>
  </Dialog>
</template>
