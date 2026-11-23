<script setup lang="ts">
import { onMounted } from 'vue'
import { useBranding } from '@/composables/useBranding'

// A small tab at the top centre, on every screen, when the server says this is not the real system
// (config app.environment_label, e.g. "Treinamento"). Floats over the page: it takes no space and no clicks.
const { environmentLabel, loadBranding } = useBranding()
onMounted(loadBranding)
</script>

<template>
  <div
    v-if="environmentLabel"
    data-testid="environment-ribbon"
    role="status"
    class="pointer-events-none fixed left-1/2 top-0 z-[100] -translate-x-1/2 rounded-b-md bg-amber-400 px-3 py-0.5 text-[10px] font-bold uppercase tracking-widest text-amber-950 shadow-sm"
  >
    {{ environmentLabel }}
  </div>
</template>
