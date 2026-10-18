<script setup lang="ts">
// "Telefone" tab next to Conversas: the VoIP dialer's place in the app.
// ponytail: UI shell only — no SIP connection. The browser softphone (JsSIP,
// one extension per agent) is blocked on the phase-0 spike, which waits for
// the provider's name (docs/superpowers/specs/2026-10-11-sip-softphone-*.md);
// no softphone product code before that report. Until then the call button
// stays disabled and the page says why; wiring it is phase 1.
import { ref, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAuthStore } from '@/stores/auth'
import { Button } from '@/components/ui/button'
import { Phone, Delete, Settings2, PhoneOff } from 'lucide-vue-next'

const { t } = useI18n()
const authStore = useAuthStore()
const isAdmin = computed(() => !!authStore.user?.is_super_admin || authStore.hasPermission('settings.general', 'write'))

// Not connected until an administrator configures the provider (phase 1).
const configured = false

const number = ref('')
const KEYS: { digit: string; letters: string }[] = [
  { digit: '1', letters: '' }, { digit: '2', letters: 'ABC' }, { digit: '3', letters: 'DEF' },
  { digit: '4', letters: 'GHI' }, { digit: '5', letters: 'JKL' }, { digit: '6', letters: 'MNO' },
  { digit: '7', letters: 'PQRS' }, { digit: '8', letters: 'TUV' }, { digit: '9', letters: 'WXYZ' },
  { digit: '*', letters: '' }, { digit: '0', letters: '+' }, { digit: '#', letters: '' },
]

function press(d: string) {
  if (number.value.length < 20) number.value += d
}
function backspace() {
  number.value = number.value.slice(0, -1)
}
// Typing in the field keeps only what a dial pad can send.
function onInput(e: Event) {
  const el = e.target as HTMLInputElement
  number.value = el.value.replace(/[^\d*#+]/g, '').slice(0, 20)
  el.value = number.value
}
</script>

<template>
  <div class="flex h-full items-start justify-center overflow-y-auto bg-muted/40 px-4 py-10">
    <div class="w-full max-w-sm">
      <!-- Status: honest about what works today -->
      <div
        v-if="!configured"
        class="mb-6 flex gap-3 rounded-2xl border border-amber-500/30 bg-amber-500/10 p-4 text-sm"
        role="status"
      >
        <PhoneOff class="mt-0.5 h-5 w-5 shrink-0 text-amber-600 dark:text-amber-300" aria-hidden="true" />
        <div>
          <p class="font-medium text-foreground">{{ t('phone.notConfiguredTitle') }}</p>
          <p class="mt-1 text-muted-foreground">{{ isAdmin ? t('phone.notConfiguredAdmin') : t('phone.notConfiguredAgent') }}</p>
          <p v-if="isAdmin" class="mt-2 flex items-center gap-1.5 text-xs text-muted-foreground">
            <Settings2 class="h-3.5 w-3.5" aria-hidden="true" />{{ t('phone.configPath') }}
          </p>
        </div>
      </div>

      <div class="rounded-3xl bg-card p-6 shadow-lg ring-1 ring-black/5 dark:ring-white/10">
        <label class="sr-only" for="dialer-number">{{ t('phone.numberLabel') }}</label>
        <div class="flex items-center gap-2 border-b pb-4">
          <input
            id="dialer-number"
            :value="number"
            inputmode="tel"
            autocomplete="off"
            :placeholder="t('phone.numberPlaceholder')"
            class="h-12 min-w-0 flex-1 bg-transparent text-center text-[26px] font-semibold tracking-wider text-foreground placeholder:text-base placeholder:font-normal placeholder:tracking-normal placeholder:text-muted-foreground focus:outline-none"
            @input="onInput"
          >
          <button
            type="button"
            class="flex h-10 w-10 shrink-0 items-center justify-center rounded-full text-muted-foreground transition hover:bg-muted hover:text-foreground disabled:opacity-0"
            :disabled="!number"
            :aria-label="t('phone.backspace')"
            @click="backspace"
          >
            <Delete class="h-5 w-5" aria-hidden="true" />
          </button>
        </div>

        <div class="mt-5 grid grid-cols-3 gap-3" role="group" :aria-label="t('phone.keypad')">
          <button
            v-for="k in KEYS"
            :key="k.digit"
            type="button"
            class="flex aspect-square flex-col items-center justify-center rounded-full bg-muted/70 transition hover:bg-muted active:scale-95 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            :aria-label="k.digit"
            @click="press(k.digit)"
          >
            <span class="text-[24px] font-medium leading-none text-foreground">{{ k.digit }}</span>
            <span class="mt-1 h-3 text-[10px] font-semibold tracking-[0.18em] text-muted-foreground">{{ k.letters }}</span>
          </button>
        </div>

        <div class="mt-6 flex justify-center">
          <Button
            size="lg"
            class="h-16 w-16 rounded-full bg-emerald-600 p-0 text-white hover:bg-emerald-700"
            :disabled="!configured || !number"
            :aria-label="t('phone.call')"
            :title="configured ? t('phone.call') : t('phone.notConfiguredTitle')"
          >
            <Phone class="h-6 w-6" aria-hidden="true" />
          </Button>
        </div>
      </div>
    </div>
  </div>
</template>
