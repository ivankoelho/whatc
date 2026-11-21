<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { Clock } from 'lucide-vue-next'

// Digital clock: always São Paulo time in pt-BR format (HH:MM, 24h; date dd/mm/aaaa in the tooltip), so every
// screen shows the same hour whatever the browser's own timezone is.
const TZ = 'America/Sao_Paulo'
const now = ref(new Date())
const time = computed(() => new Intl.DateTimeFormat('pt-BR', { timeZone: TZ, hour: '2-digit', minute: '2-digit', hour12: false }).format(now.value))
const date = computed(() => new Intl.DateTimeFormat('pt-BR', { timeZone: TZ, day: '2-digit', month: '2-digit', year: 'numeric' }).format(now.value))

let timer: number
onMounted(() => { timer = window.setInterval(() => { now.value = new Date() }, 1000) })
onUnmounted(() => clearInterval(timer))
</script>

<template>
  <div class="flex items-center gap-1.5 tabular-nums text-white" data-testid="clock" :title="date">
    <Clock class="h-3.5 w-3.5 opacity-80" aria-hidden="true" />
    <span class="text-sm font-medium">{{ time }}</span>
    <span class="sr-only">{{ date }}</span>
  </div>
</template>
