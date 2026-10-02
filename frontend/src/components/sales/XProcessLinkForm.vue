<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button } from '@/components/ui/button'
import { salesOpportunityXProcessLinkService, type SalesOpportunityXProcessLink } from '@/services/api'
import { formatCurrency } from '@/lib/currency'
import XProcessLinkDialog from '@/components/sales/XProcessLinkDialog.vue'

const props = defineProps<{ opportunityId: string; editable: boolean }>()

const { t } = useI18n()
const link = ref<SalesOpportunityXProcessLink | null>(null)

// Collapsed by default: a board can render up to 100 cards at once, and this
// section's GET is only useful once the user actually looks at it. `loaded`
// makes sure toggling open/closed repeatedly doesn't refetch every time.
const expanded = ref(false)
const loading = ref(false)
const loaded = ref(false)
const dialogOpen = ref(false)

async function load() {
  loading.value = true
  try {
    const res = await salesOpportunityXProcessLinkService.get(props.opportunityId)
    link.value = res.data.data
  } catch {
    link.value = null
  } finally {
    loading.value = false
  }
}

function toggle() {
  expanded.value = !expanded.value
  if (expanded.value && !loaded.value) {
    loaded.value = true
    load()
  }
}

// The Kanban card is ~250px wide — nowhere near enough for two labeled
// inputs side by side (Tailwind's sm: breakpoint reacts to the page
// viewport, not this card's own width, so it used to force a cramped
// two-column layout even on a wide desktop screen). The registration form
// now lives in its own Dialog, which has real width to work with; this
// component only ever shows a compact, always-legible summary line.
async function onRegistered() {
  await load()
}
</script>

<template>
  <div class="space-y-2 rounded-lg border p-3">
    <button
      type="button"
      class="flex w-full items-center justify-between text-sm font-medium"
      :aria-expanded="expanded"
      @click="toggle"
    >
      <span>{{ t('xprocessLink.title') }}</span>
      <span class="text-xs text-muted-foreground">{{ expanded ? '▾' : '▸' }}</span>
    </button>

    <template v-if="expanded">
      <p v-if="loading" class="text-sm text-muted-foreground">{{ t('common.loading') }}</p>
      <template v-else>
        <dl v-if="link" class="grid grid-cols-[auto_1fr] gap-x-2 gap-y-0.5 text-sm">
          <dt class="text-muted-foreground">{{ t('xprocessLink.numPedido') }}</dt>
          <dd>{{ link.num_pedido }}</dd>
          <template v-if="link.status_xprocess">
            <dt class="text-muted-foreground">{{ t('xprocessLink.status') }}</dt>
            <dd>{{ link.status_xprocess }}</dd>
          </template>
          <template v-if="link.valor_vendido != null">
            <dt class="text-muted-foreground">{{ t('xprocessLink.valorConfirmado') }}</dt>
            <dd>{{ formatCurrency(link.valor_vendido) }}</dd>
          </template>
          <!-- Freight is the order's own figure; the confirmed value above never includes it. -->
          <template v-if="link.valor_frete != null">
            <dt class="text-muted-foreground">{{ t('xprocessLink.valorFrete') }}</dt>
            <dd data-testid="xprocess-link-freight">{{ formatCurrency(link.valor_frete) }}</dd>
          </template>
        </dl>
        <p v-if="link?.link_source === 'auto'" class="text-xs text-muted-foreground" :title="link.match_reason">
          {{ t('xprocessLink.autoLinked') }}
        </p>
        <p v-if="link?.pending_review" class="text-sm text-amber-600">{{ t('xprocessLink.pendingReview') }}</p>
        <!-- Bug: expanding used to render nothing at all here for a closed
             opportunity that never had a pedido registered (no dl, since no
             link; no button, since editable is status==='aberta' only) —
             the ▾ just flipped and nothing else happened. -->
        <p v-if="!link" class="text-sm text-muted-foreground">{{ t('xprocessLink.notRegistered') }}</p>

        <Button v-if="editable" size="sm" variant="outline" class="w-full" @click="dialogOpen = true">
          {{ link && !link.resolved_at ? t('xprocessLink.editButton') : t('xprocessLink.register') }}
        </Button>

        <XProcessLinkDialog
          v-model:open="dialogOpen"
          :opportunity-id="opportunityId"
          :link="link"
          @registered="onRegistered"
        />
      </template>
    </template>
  </div>
</template>
