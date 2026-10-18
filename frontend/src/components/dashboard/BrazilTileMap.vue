<script setup lang="ts">
// Heat map of Brazil by state (UF). A tile map: every state is one square placed
// roughly where it sits, so small states (DF, SE, AL) stay readable and it needs
// no map data, token or tiles. Intensity is the share of the busiest state.
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'

interface DataPoint {
  label: string
  value: number
}

const props = defineProps<{ dataPoints: DataPoint[] }>()
const { t } = useI18n()

// [column, row] on a 7 x 8 grid.
const TILES: Record<string, [number, number]> = {
  RR: [2, 0], AP: [4, 0],
  AM: [1, 1], PA: [3, 1], MA: [4, 1], CE: [5, 1], RN: [6, 1],
  AC: [0, 2], RO: [1, 2], MT: [2, 2], TO: [3, 2], PI: [4, 2], PE: [5, 2], PB: [6, 2],
  MS: [2, 3], GO: [3, 3], DF: [4, 3], BA: [5, 3], AL: [6, 3],
  SP: [3, 4], MG: [4, 4], ES: [5, 4], SE: [6, 4],
  PR: [3, 5], RJ: [4, 5],
  SC: [3, 6],
  RS: [3, 7],
}

const OTHER = 'OUTROS'

const byState = computed(() => {
  const m = new Map<string, number>()
  for (const p of props.dataPoints) m.set(p.label, (m.get(p.label) || 0) + p.value)
  return m
})
const other = computed(() => byState.value.get(OTHER) || 0)
const total = computed(() => [...byState.value.values()].reduce((a, b) => a + b, 0))
const max = computed(() => Math.max(0, ...Object.keys(TILES).map(uf => byState.value.get(uf) || 0)))

const tiles = computed(() =>
  Object.entries(TILES).map(([uf, [col, row]]) => {
    const value = byState.value.get(uf) || 0
    const intensity = max.value > 0 ? value / max.value : 0
    return { uf, value, intensity, col, row, share: total.value > 0 ? (value / total.value) * 100 : 0 }
  })
)

const active = ref<string | null>(null)
const activeTile = computed(() => tiles.value.find(x => x.uf === active.value) || null)

// Empty states stay visible but quiet; the rest ramp from a faint to a solid tint.
const tileStyle = (intensity: number, value: number) => ({
  backgroundColor: value > 0 ? `rgba(16, 185, 129, ${0.18 + intensity * 0.77})` : undefined,
})
const fmt = (n: number) => new Intl.NumberFormat().format(n)
</script>

<template>
  <div class="flex h-full w-full flex-col gap-2">
    <div class="relative min-h-0 flex-1" style="container-type: size">
      <div
        class="mx-auto grid h-full max-h-full gap-1"
        style="grid-template-columns: repeat(7, minmax(0, 1fr)); grid-template-rows: repeat(8, minmax(0, 1fr)); aspect-ratio: 7 / 8;"
        role="list"
        :aria-label="t('dashboard.mapAria')"
      >
        <div
          v-for="tile in tiles"
          :key="tile.uf"
          role="listitem"
          tabindex="0"
          :aria-label="`${tile.uf}: ${fmt(tile.value)}`"
          :style="{ gridColumn: tile.col + 1, gridRow: tile.row + 1, fontSize: 'clamp(7px, 4.2cqmin, 15px)', ...tileStyle(tile.intensity, tile.value) }"
          :class="[
            'flex flex-col items-center justify-center rounded-md border font-semibold leading-none transition-colors cursor-default outline-none',
            'border-white/[0.08] bg-white/[0.04] text-white/50 light:border-gray-200 light:bg-gray-100 light:text-gray-400',
            tile.value > 0 ? 'text-white light:text-gray-900' : '',
            active === tile.uf ? 'ring-2 ring-emerald-400' : 'focus-visible:ring-2 focus-visible:ring-emerald-400',
          ]"
          @mouseenter="active = tile.uf"
          @mouseleave="active = null"
          @focus="active = tile.uf"
          @blur="active = null"
        >
          <span>{{ tile.uf }}</span>
          <span v-if="tile.value > 0" class="mt-0.5 text-[0.8em] font-normal opacity-80">{{ fmt(tile.value) }}</span>
        </div>
      </div>
    </div>

    <div class="flex items-center justify-between gap-2 text-xs text-white/50 light:text-gray-500">
      <span v-if="activeTile" class="truncate">
        <strong class="text-white light:text-gray-900">{{ activeTile.uf }}</strong>
        · {{ fmt(activeTile.value) }} · {{ activeTile.share.toFixed(1) }}%
      </span>
      <span v-else class="truncate">{{ t('dashboard.mapTotal', { count: fmt(total) }) }}</span>
      <span v-if="other > 0" class="shrink-0" :title="t('dashboard.mapOtherHint')">
        {{ t('dashboard.mapOther') }}: {{ fmt(other) }}
      </span>
    </div>
  </div>
</template>
