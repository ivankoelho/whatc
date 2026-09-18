<script setup lang="ts">
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import type { SalesOpportunity } from '@/services/api'

defineProps<{ opportunity: SalesOpportunity; disabled?: boolean }>()

defineEmits<{ convert: [opportunity: SalesOpportunity]; lose: [opportunity: SalesOpportunity] }>()

// pt-BR/BRL: this is a Brazilian sales funnel (XProcess integration, spec in
// Portuguese) — same one-off Intl.NumberFormat call MetaInsightsView.vue uses
// for its own currency, just with the locale/currency this domain needs.
function formatCurrency(value?: number): string {
  if (value == null) return '—'
  return new Intl.NumberFormat('pt-BR', { style: 'currency', currency: 'BRL' }).format(value)
}
</script>

<template>
  <div
    data-board-card
    class="rounded-md border border-white/[0.08] light:border-gray-200 bg-white/[0.02] light:bg-white p-3"
    :class="disabled ? 'opacity-50 cursor-wait' : 'cursor-grab'"
  >
    <div class="flex items-center justify-between gap-2">
      <span class="font-mono text-xs text-white/50 light:text-muted-foreground">
        {{ opportunity.opportunity_number }}
      </span>
      <Badge v-if="opportunity.sla_breached" variant="destructive" class="shrink-0 text-xs">
        {{ $t('sales.slaBreached') }}
      </Badge>
    </div>
    <p class="text-xs mt-1 truncate text-white/50 light:text-muted-foreground">
      <span class="text-white/30 light:text-gray-400">{{ $t('sales.contactLabel') }}:</span>
      {{ opportunity.contact?.profile_name || opportunity.contact_id }}
    </p>
    <p class="text-sm mt-1 truncate text-white light:text-gray-900">
      {{ formatCurrency(opportunity.estimated_value) }}
    </p>
    <div v-if="opportunity.stage === 'direcionada'" class="mt-2 flex gap-2">
      <Button size="sm" variant="outline" class="flex-1 text-xs" @click.stop="$emit('convert', opportunity)">
        {{ $t('sales.markConverted') }}
      </Button>
      <Button size="sm" variant="outline" class="flex-1 text-xs" @click.stop="$emit('lose', opportunity)">
        {{ $t('sales.markLost') }}
      </Button>
    </div>
  </div>
</template>
