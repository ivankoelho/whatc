<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Search } from 'lucide-vue-next'
import { Switch } from '@/components/ui/switch'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Badge } from '@/components/ui/badge'
import {
  Accordion,
  AccordionContent,
  AccordionItem,
  AccordionTrigger
} from '@/components/ui/accordion'
import type { Permission } from '@/services/api'
import {
  activeCount,
  filterGroups,
  i18nKey,
  setKeys,
  type FunctionalGroup
} from '@/lib/permission-groups'

const props = defineProps<{
  permissionGroups: FunctionalGroup[]
  selectedPermissions: string[]
  disabled?: boolean
}>()

const emit = defineEmits<{
  (e: 'update:selectedPermissions', value: string[]): void
}>()

const { t, te } = useI18n()

// Texts come from the i18n catalog; a key missing there falls back to what the backend sent.
function permLabel(p: Permission): string {
  const k = `permissionCatalog.permissions.${i18nKey(p.key)}.label`
  return te(k) ? t(k) : p.key
}
function permDescription(p: Permission): string {
  const k = `permissionCatalog.permissions.${i18nKey(p.key)}.description`
  return te(k) ? t(k) : p.description
}
function resourceLabel(resource: string): string {
  const k = `permissionCatalog.resources.${i18nKey(resource)}.label`
  return te(k) ? t(k) : resource
}
function resourceDescription(resource: string): string {
  const k = `permissionCatalog.resources.${i18nKey(resource)}.description`
  return te(k) ? t(k) : ''
}
function groupLabel(group: string): string {
  const k = `permissionCatalog.groups.${group}`
  return te(k) ? t(k) : group
}

const query = ref('')

// Search keeps the group structure: name, resource, description and the technical key.
const visibleGroups = computed(() =>
  filterGroups(
    props.permissionGroups,
    query.value,
    p => [permLabel(p), resourceLabel(p.resource), permDescription(p), p.key].join(' '),
    r => resourceLabel(r) + ' ' + r
  )
)

function isSelected(key: string): boolean {
  return props.selectedPermissions.includes(key)
}

function setPermission(key: string, on: boolean) {
  if (props.disabled) return
  emit('update:selectedPermissions', setKeys(props.selectedPermissions, [key], on))
}

// Bulk toggle of a whole group (always the whole group, even while a search is
// filtering it); it only emits the new selection, saving stays with the page's Save.
function setGroup(group: string, on: boolean) {
  if (props.disabled) return
  const full = props.permissionGroups.find(g => g.group === group)
  if (!full) return
  emit('update:selectedPermissions', setKeys(props.selectedPermissions, full.permissions.map(p => p.key), on))
}

function groupActive(group: FunctionalGroup): number {
  const full = props.permissionGroups.find(g => g.group === group.group) ?? group
  return activeCount(full, props.selectedPermissions)
}

function groupTotal(group: FunctionalGroup): number {
  return (props.permissionGroups.find(g => g.group === group.group) ?? group).permissions.length
}

// Open groups: those with something on at first; every match while searching.
const openGroups = ref<string[]>([])
watch(
  () => props.permissionGroups,
  (groups) => {
    if (groups.length > 0 && openGroups.value.length === 0) {
      openGroups.value = groups.filter(g => activeCount(g, props.selectedPermissions) > 0).map(g => g.group)
    }
  },
  { immediate: true }
)
watch(query, (q) => {
  if (q.trim()) openGroups.value = visibleGroups.value.map(g => g.group)
})
</script>

<template>
  <div class="space-y-3">
    <!-- Empty state -->
    <div v-if="permissionGroups.length === 0" class="text-center py-8 text-muted-foreground border rounded-lg">
      <p>{{ $t('roles.noPermissionsSeeded') }}</p>
    </div>

    <template v-else>
      <div class="relative">
        <Search class="absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" aria-hidden="true" />
        <Input
          v-model="query"
          type="search"
          class="pl-9"
          :placeholder="$t('permissionCatalog.searchPlaceholder')"
          :aria-label="$t('permissionCatalog.search')"
          data-testid="permission-search"
        />
      </div>

      <p v-if="visibleGroups.length === 0" class="text-center py-6 text-sm text-muted-foreground border rounded-lg">
        {{ $t('permissionCatalog.noResults') }}
      </p>

      <Accordion v-else type="multiple" v-model="openGroups" class="w-full space-y-2">
        <AccordionItem
          v-for="group in visibleGroups"
          :key="group.group"
          :value="group.group"
          class="border rounded-lg px-4"
          :data-testid="`permission-group-${group.group}`"
        >
          <AccordionTrigger class="hover:no-underline py-3">
            <div class="flex flex-1 items-center gap-3 text-left">
              <span class="font-medium">{{ groupLabel(group.group) }}</span>
              <Badge variant="secondary">
                {{ $t('permissionCatalog.activeCount', { active: groupActive(group), total: groupTotal(group) }) }}
              </Badge>
            </div>
          </AccordionTrigger>
          <AccordionContent>
            <div class="flex justify-end pb-2">
              <Button
                type="button"
                variant="ghost"
                size="sm"
                :disabled="disabled"
                :aria-label="$t('permissionCatalog.groupToggle', { group: groupLabel(group.group) })"
                @click="setGroup(group.group, groupActive(group) < groupTotal(group))"
              >
                {{ groupActive(group) < groupTotal(group) ? $t('permissionCatalog.enableAll') : $t('permissionCatalog.disableAll') }}
              </Button>
            </div>

            <div class="space-y-5 pb-4">
              <section v-for="block in group.resources" :key="block.resource">
                <h4 class="text-sm font-semibold">{{ resourceLabel(block.resource) }}</h4>
                <p v-if="resourceDescription(block.resource)" class="text-xs text-muted-foreground">
                  {{ resourceDescription(block.resource) }}
                </p>

                <ul class="mt-2 divide-y rounded-md border">
                  <li
                    v-for="permission in block.permissions"
                    :key="permission.key"
                    class="flex items-start justify-between gap-4 p-3"
                  >
                    <div class="min-w-0">
                      <label :for="`perm-${permission.key}`" class="block text-sm font-medium cursor-pointer">
                        {{ permLabel(permission) }}
                      </label>
                      <p class="text-xs text-muted-foreground">{{ permDescription(permission) }}</p>
                    </div>
                    <div class="flex shrink-0 items-center gap-2">
                      <span
                        class="w-20 text-right text-xs"
                        :class="isSelected(permission.key) ? 'font-medium text-foreground' : 'text-muted-foreground'"
                      >
                        {{ isSelected(permission.key) ? $t('permissionCatalog.on') : $t('permissionCatalog.off') }}
                      </span>
                      <Switch
                        :id="`perm-${permission.key}`"
                        :checked="isSelected(permission.key)"
                        :disabled="disabled"
                        :aria-label="permLabel(permission)"
                        :data-testid="`permission-${permission.key}`"
                        @update:checked="(on: boolean) => setPermission(permission.key, on)"
                      />
                    </div>
                  </li>
                </ul>
              </section>
            </div>
          </AccordionContent>
        </AccordionItem>
      </Accordion>
    </template>
  </div>
</template>
