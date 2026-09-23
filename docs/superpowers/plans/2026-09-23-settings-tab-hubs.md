# Settings Tab Hubs Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Collapse 16 flat settings routes (Ocorrências: 6, Atendimento: 4, Acesso: 3, Integrações: 3) into 4 tabbed "hub" pages, cutting the settings sidebar from 20 links to 8, with zero change to any existing screen's internal logic.

**Architecture:** A single generic `SettingsTabHub.vue` component (Tabs from `@/components/ui/tabs`, the same primitive the General settings page already uses) renders whichever existing view component matches the active tab. Four thin wrapper views (one per hub) each pass their own fixed list of `{ value, labelKey, permission, component }` to that generic hub. The active tab is derived from `?tab=` in the URL by a pure, unit-tested function (`resolveActiveTab`) that also enforces "no permission → don't show, don't allow via URL, fall back to the first accessible tab." The 16 old routes become `redirect` entries (same `path`/`name`, now pointing at `hub + ?tab=`); their 8 detail sub-routes (`:id`) are untouched.

**Tech Stack:** Vue 3 `<script setup>`, vue-router 4, Pinia (`useAuthStore`), vue-i18n, `reka-ui`-backed `Tabs`/`TabsList`/`TabsTrigger`/`TabsContent`, Vitest.

## Global Constraints

- No functional/logic change to any of the 16 existing view components — only how the user navigates to them changes.
- Only 4 hubs: Ocorrências (`/settings/occurrences`), Atendimento (`/settings/service`), Acesso (`/settings/access`), Integrações (`/settings/integrations`).
- Reuse the existing `Tabs`/`TabsList`/`TabsTrigger`/`TabsContent` components — no new tab primitive.
- Active tab persists in `?tab=`; survives refresh and a directly-pasted/shared URL. Tab switches use `router.replace` (intentional — no new history entry per click; browser back/forward is not a way to move between tabs, and that's expected, not a bug).
- All 16 old top-level routes keep their exact `path` and `name`, converted to `redirect` — never deleted outright. Their 8 `:id` detail routes are not touched, not converted, not renamed.
- Permissions used per tab are exactly the existing `meta.permission` of each old route — no new permission is introduced. A disallowed tab is not rendered and cannot be reached via `?tab=` (falls back to the first accessible tab). Each hub has one explicit default tab.
- The hub itself renders no title/`PageHeader` — the title comes from the embedded existing component, exactly as today.
- The General settings screen (`/settings`, `SettingsView.vue`) is not touched.
- Route name prefix for the 4 hubs is `settings-<hub>` (`settings-occurrences`, `settings-service`, `settings-access`, `settings-integrations`) — plain `occurrences` is already taken by `/crm/occurrences`.

Full per-tab mapping (component / old route / `?tab=` value / permission) is in `docs/superpowers/specs/2026-09-23-settings-tab-hubs-design.md` — copied into each task below as needed.

---

## Task 1: `resolveActiveTab` — pure tab-resolution logic

**Files:**
- Create: `frontend/src/lib/settings-tab-hub.ts`
- Create: `frontend/src/lib/settings-tab-hub.spec.ts`

**Interfaces:**
- Produces: `export interface SettingsTabConfig { value: string; permission: string }` and `export function resolveActiveTab(tabs: SettingsTabConfig[], requestedTab: string | undefined, defaultTab: string, hasPermission: (permission: string) => boolean): string | null` — used by Task 2's `SettingsTabHub.vue`.

- [ ] **Step 1: Write the failing tests**

Create `frontend/src/lib/settings-tab-hub.spec.ts`:

```ts
import { describe, it, expect } from 'vitest'
import { resolveActiveTab, type SettingsTabConfig } from './settings-tab-hub'

const tabs: SettingsTabConfig[] = [
  { value: 'stages', permission: 'occurrences.stages' },
  { value: 'categories', permission: 'occurrences.categories' },
  { value: 'processes', permission: 'occurrences.processes' },
]

describe('resolveActiveTab', () => {
  it('honours the requested tab when it is accessible', () => {
    const result = resolveActiveTab(tabs, 'processes', 'stages', () => true)
    expect(result).toBe('processes')
  })

  it('falls back to the default tab when no tab is requested', () => {
    const result = resolveActiveTab(tabs, undefined, 'stages', () => true)
    expect(result).toBe('stages')
  })

  it('falls back to the first accessible tab when the requested tab is not permitted', () => {
    const hasPermission = (p: string) => p === 'occurrences.categories'
    const result = resolveActiveTab(tabs, 'processes', 'stages', hasPermission)
    expect(result).toBe('categories')
  })

  it('falls back to the first accessible tab when the default tab is not permitted', () => {
    const hasPermission = (p: string) => p === 'occurrences.processes'
    const result = resolveActiveTab(tabs, undefined, 'stages', hasPermission)
    expect(result).toBe('processes')
  })

  it('lands a single-permission user on their only tab even when a different tab is requested', () => {
    const hasPermission = (p: string) => p === 'occurrences.categories'
    const result = resolveActiveTab(tabs, 'processes', 'stages', hasPermission)
    expect(result).toBe('categories')
  })

  it('treats an unknown requested value as not accessible', () => {
    const result = resolveActiveTab(tabs, 'not-a-real-tab', 'stages', () => true)
    expect(result).toBe('stages')
  })

  it('returns null when nothing is accessible', () => {
    const result = resolveActiveTab(tabs, undefined, 'stages', () => false)
    expect(result).toBeNull()
  })
})
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd frontend && npx vitest run src/lib/settings-tab-hub.spec.ts`
Expected: FAIL — `Cannot find module './settings-tab-hub'`

- [ ] **Step 3: Write the implementation**

Create `frontend/src/lib/settings-tab-hub.ts`:

```ts
export interface SettingsTabConfig {
  value: string
  permission: string
}

export function resolveActiveTab(
  tabs: SettingsTabConfig[],
  requestedTab: string | undefined,
  defaultTab: string,
  hasPermission: (permission: string) => boolean,
): string | null {
  const accessible = tabs.filter(tab => hasPermission(tab.permission))
  if (accessible.length === 0) return null
  if (requestedTab && accessible.some(tab => tab.value === requestedTab)) return requestedTab
  if (accessible.some(tab => tab.value === defaultTab)) return defaultTab
  return accessible[0].value
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd frontend && npx vitest run src/lib/settings-tab-hub.spec.ts`
Expected: PASS — 7 tests

- [ ] **Step 5: Commit**

```bash
git add frontend/src/lib/settings-tab-hub.ts frontend/src/lib/settings-tab-hub.spec.ts
git commit -m "feat(settings): add pure tab-resolution logic for settings hubs

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>"
```

---

## Task 2: `SettingsTabHub.vue` — generic hub component

**Files:**
- Create: `frontend/src/components/shared/SettingsTabHub.vue`
- Modify: `frontend/src/components/shared/index.ts`
- Modify: `frontend/src/i18n/locales/en.json`
- Modify: `frontend/src/i18n/locales/pt-BR.json`

**Interfaces:**
- Consumes: `resolveActiveTab`, `SettingsTabConfig` from `@/lib/settings-tab-hub` (Task 1); `useAuthStore().hasPermission(resource, action)` from `@/stores/auth`.
- Produces: `SettingsTabHub` component, exported from `@/components/shared`, with props `tabs: HubTab[]` and `defaultTab: string`, where `export interface HubTab extends SettingsTabConfig { labelKey: string; component: Component }` (also exported from this file) — Tasks 3–6 (the 4 hub views) import `HubTab` and pass these props.

- [ ] **Step 1: Add the empty-state i18n key**

In `frontend/src/i18n/locales/en.json`, find `"common": {` and add inside it (after `"add"`, matching the existing flat list):

```json
    "add": "Add",
    "noAccessToSection": "You don't have permission to access any tab in this section.",
```

In `frontend/src/i18n/locales/pt-BR.json`, find `"common": {` and add the matching entry:

```json
    "add": "Adicionar",
    "noAccessToSection": "Você não tem permissão para acessar nenhuma aba desta seção.",
```

- [ ] **Step 2: Verify the key was added to both locales**

Run: `cd frontend && npm run i18n:keys`
Expected: no new missing/extra key reported for `common.noAccessToSection` (any pre-existing unrelated gaps in the output are expected and not caused by this change).

- [ ] **Step 3: Create the component**

Create `frontend/src/components/shared/SettingsTabHub.vue`:

```vue
<script setup lang="ts">
import { computed } from 'vue'
import type { Component } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { useAuthStore } from '@/stores/auth'
import { resolveActiveTab, type SettingsTabConfig } from '@/lib/settings-tab-hub'

export interface HubTab extends SettingsTabConfig {
  labelKey: string
  component: Component
}

const props = defineProps<{
  tabs: HubTab[]
  defaultTab: string
}>()

const route = useRoute()
const router = useRouter()
const authStore = useAuthStore()
const { t } = useI18n()

// Tailwind's scanner needs literal class names — a template-literal
// `grid-cols-${n}` would compile to nothing since it never appears as a
// whole string in the source.
const GRID_COLS_CLASS: Record<number, string> = {
  1: 'grid-cols-1',
  2: 'grid-cols-2',
  3: 'grid-cols-3',
  4: 'grid-cols-4',
  5: 'grid-cols-5',
  6: 'grid-cols-6',
}

const visibleTabs = computed(() =>
  props.tabs.filter(tab => authStore.hasPermission(tab.permission, 'read')),
)

const gridColsClass = computed(() => GRID_COLS_CLASS[visibleTabs.value.length] ?? 'grid-cols-1')

const activeTab = computed(() =>
  resolveActiveTab(
    props.tabs,
    typeof route.query.tab === 'string' ? route.query.tab : undefined,
    props.defaultTab,
    (permission) => authStore.hasPermission(permission, 'read'),
  ),
)

function onTabChange(value: string | number) {
  router.replace({ query: { ...route.query, tab: String(value) } })
}
</script>

<template>
  <div v-if="visibleTabs.length === 0" class="p-6 text-sm text-white/50 light:text-gray-500">
    {{ t('common.noAccessToSection') }}
  </div>
  <Tabs v-else :model-value="activeTab ?? undefined" class="w-full" @update:model-value="onTabChange">
    <TabsList :class="['grid w-full mb-6 lg:w-auto lg:inline-flex', gridColsClass]">
      <TabsTrigger v-for="tab in visibleTabs" :key="tab.value" :value="tab.value">
        {{ t(tab.labelKey) }}
      </TabsTrigger>
    </TabsList>
    <TabsContent v-for="tab in visibleTabs" :key="tab.value" :value="tab.value">
      <component :is="tab.component" />
    </TabsContent>
  </Tabs>
</template>
```

- [ ] **Step 4: Export it from the shared barrel**

In `frontend/src/components/shared/index.ts`, add near the other component exports:

```ts
export { default as SettingsTabHub } from './SettingsTabHub.vue'
export type { HubTab } from './SettingsTabHub.vue'
```

- [ ] **Step 5: Typecheck**

Run: `cd frontend && npm run typecheck`
Expected: same single pre-existing error as before this change (`AccountDetailView.vue(172,45)`), nothing new.

- [ ] **Step 6: Commit**

```bash
git add frontend/src/components/shared/SettingsTabHub.vue frontend/src/components/shared/index.ts frontend/src/i18n/locales/en.json frontend/src/i18n/locales/pt-BR.json
git commit -m "feat(settings): add generic SettingsTabHub component

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>"
```

---

## Task 3: Router plumbing + Ocorrências hub (first end-to-end hub)

This task wires the shared router mechanism (new `anyPermission` meta + guard branch) and stands up the first hub end-to-end, proving the whole pattern works before repeating it for the other 3 groups.

**Files:**
- Modify: `frontend/src/router/index.ts`
- Create: `frontend/src/views/settings/OccurrenceSettingsHubView.vue`

**Interfaces:**
- Consumes: `SettingsTabHub`, `HubTab` from `@/components/shared` (Task 2).
- Produces: route `settings-occurrences` at `/settings/occurrences`; the pattern (`meta.anyPermission`, hub view shape) that Tasks 4–6 copy.

- [ ] **Step 1: Add `anyPermission` to the route meta type**

In `frontend/src/router/index.ts`, find:

```ts
declare module 'vue-router' {
  interface RouteMeta {
    requiresAuth?: boolean
    permission?: string // Resource permission required (e.g., 'analytics', 'chat')
  }
}
```

Replace with:

```ts
declare module 'vue-router' {
  interface RouteMeta {
    requiresAuth?: boolean
    permission?: string // Resource permission required (e.g., 'analytics', 'chat')
    anyPermission?: string[] // Route is allowed if the user has read on ANY of these (settings hubs)
  }
}
```

- [ ] **Step 2: Add the `anyPermission` branch to the navigation guard**

Find, inside `router.beforeEach`:

```ts
    // Check permission-based access
    const requiredPermission = to.meta.permission
    if (requiredPermission) {
      if (!authStore.hasPermission(requiredPermission, 'read')) {
        // Redirect to first accessible page
        return next({ path: getFirstAccessibleRoute(authStore) })
      }
    }
```

Replace with:

```ts
    // Check permission-based access
    const requiredPermission = to.meta.permission
    if (requiredPermission) {
      if (!authStore.hasPermission(requiredPermission, 'read')) {
        // Redirect to first accessible page
        return next({ path: getFirstAccessibleRoute(authStore) })
      }
    }

    // Settings hubs: allowed if the user has read on at least one of the
    // group's permissions. Which specific tab they land on is decided
    // inside the hub component (resolveActiveTab), not here.
    const anyPermission = to.meta.anyPermission
    if (anyPermission) {
      if (!anyPermission.some(p => authStore.hasPermission(p, 'read'))) {
        return next({ path: getFirstAccessibleRoute(authStore) })
      }
    }
```

- [ ] **Step 3: Register the Ocorrências hub route and convert its 6 old routes to redirects**

Find these 6 route objects (they are not contiguous in the file — locate each by its `path`):

```ts
        {
          path: 'settings/occurrence-stages',
          name: 'occurrence-stages',
          component: () => import('@/views/settings/OccurrenceStagesView.vue'),
          meta: { permission: 'occurrences.stages' }
        },
```
```ts
        {
          path: 'settings/units',
          name: 'units',
          component: () => import('@/views/settings/UnitsView.vue'),
          meta: { permission: 'units' }
        },
```
```ts
        {
          path: 'settings/occurrence-sla-policies',
          name: 'occurrence-sla-policies',
          component: () => import('@/views/settings/OccurrenceSLAPoliciesView.vue'),
          meta: { permission: 'occurrences.sla_policies' }
        },
```
```ts
        {
          path: 'settings/occurrence-categories',
          name: 'occurrence-categories',
          component: () => import('@/views/settings/OccurrenceCategoriesView.vue'),
          meta: { permission: 'occurrences.categories' }
        },
```
```ts
        {
          path: 'settings/occurrence-what-happened',
          name: 'occurrence-what-happened',
          component: () => import('@/views/settings/OccurrenceWhatHappenedView.vue'),
          meta: { permission: 'occurrences.what_happened' }
        },
```
```ts
        {
          path: 'settings/occurrence-processes',
          name: 'occurrence-processes',
          component: () => import('@/views/settings/OccurrenceProcessesView.vue'),
          meta: { permission: 'occurrences.processes' }
        },
```

Replace **all six** with (same order, same `path`/`name`, now redirects — no `component`/`meta.permission`):

```ts
        {
          path: 'settings/occurrence-stages',
          name: 'occurrence-stages',
          redirect: () => ({ path: '/settings/occurrences', query: { tab: 'stages' } })
        },
```
```ts
        {
          path: 'settings/units',
          name: 'units',
          redirect: () => ({ path: '/settings/occurrences', query: { tab: 'units' } })
        },
```
```ts
        {
          path: 'settings/occurrence-sla-policies',
          name: 'occurrence-sla-policies',
          redirect: () => ({ path: '/settings/occurrences', query: { tab: 'sla' } })
        },
```
```ts
        {
          path: 'settings/occurrence-categories',
          name: 'occurrence-categories',
          redirect: () => ({ path: '/settings/occurrences', query: { tab: 'categories' } })
        },
```
```ts
        {
          path: 'settings/occurrence-what-happened',
          name: 'occurrence-what-happened',
          redirect: () => ({ path: '/settings/occurrences', query: { tab: 'what-happened' } })
        },
```
```ts
        {
          path: 'settings/occurrence-processes',
          name: 'occurrence-processes',
          redirect: () => ({ path: '/settings/occurrences', query: { tab: 'processes' } })
        },
```

Then add the new hub route. Insert it directly above the `settings/occurrence-stages` redirect entry (same location):

```ts
        {
          path: 'settings/occurrences',
          name: 'settings-occurrences',
          component: () => import('@/views/settings/OccurrenceSettingsHubView.vue'),
          meta: { anyPermission: ['occurrences.stages', 'occurrences.categories', 'occurrences.what_happened', 'occurrences.processes', 'occurrences.sla_policies', 'units'] }
        },
```

- [ ] **Step 4: Create the Ocorrências hub view**

Create `frontend/src/views/settings/OccurrenceSettingsHubView.vue`:

```vue
<script setup lang="ts">
import { SettingsTabHub, type HubTab } from '@/components/shared'
import OccurrenceStagesView from './OccurrenceStagesView.vue'
import OccurrenceCategoriesView from './OccurrenceCategoriesView.vue'
import OccurrenceWhatHappenedView from './OccurrenceWhatHappenedView.vue'
import OccurrenceProcessesView from './OccurrenceProcessesView.vue'
import OccurrenceSLAPoliciesView from './OccurrenceSLAPoliciesView.vue'
import UnitsView from './UnitsView.vue'

const tabs: HubTab[] = [
  { value: 'stages', labelKey: 'nav.occurrenceStages', permission: 'occurrences.stages', component: OccurrenceStagesView },
  { value: 'categories', labelKey: 'nav.occurrenceCategories', permission: 'occurrences.categories', component: OccurrenceCategoriesView },
  { value: 'what-happened', labelKey: 'nav.whatHappened', permission: 'occurrences.what_happened', component: OccurrenceWhatHappenedView },
  { value: 'processes', labelKey: 'nav.occurrenceProcesses', permission: 'occurrences.processes', component: OccurrenceProcessesView },
  { value: 'sla', labelKey: 'nav.slaPolicies', permission: 'occurrences.sla_policies', component: OccurrenceSLAPoliciesView },
  { value: 'units', labelKey: 'nav.units', permission: 'units', component: UnitsView },
]
</script>

<template>
  <SettingsTabHub :tabs="tabs" default-tab="stages" />
</template>
```

- [ ] **Step 5: Typecheck**

Run: `cd frontend && npm run typecheck`
Expected: same single pre-existing error as before (`AccountDetailView.vue`), nothing new.

- [ ] **Step 6: Manual browser verification**

Start the dev server (`preview_start` with `backend` and `frontend`, or however you already have them running) and, logged in as `admin@admin.com` / `admin`:

1. Navigate to `/settings/occurrences` directly. Expect: tab bar with 6 tabs, "Etapas de ocorrência" active by default, its table visible below.
2. Click "Processos de ocorrência". Expect: URL becomes `/settings/occurrences?tab=processes`, that screen's own title and content render.
3. Refresh the page. Expect: still on the "Processos de ocorrência" tab.
4. Navigate to `/settings/occurrence-what-happened` directly (the old URL). Expect: redirected to `/settings/occurrences?tab=what-happened`, that tab active.
5. Click the browser back button. Expect: goes to whatever page was open before step 4 (not to a different tab of the hub) — confirms `replace` is not adding history entries per tab click.

- [ ] **Step 7: Commit**

```bash
git add frontend/src/router/index.ts frontend/src/views/settings/OccurrenceSettingsHubView.vue
git commit -m "feat(settings): add Ocorrências settings hub with tabbed navigation

Introduces meta.anyPermission for hub routes (a route is allowed if the
user has read on any of the group's permissions; which tab they land on
is decided by resolveActiveTab inside the hub, not by the route guard).
The 6 old occurrence-settings routes become redirects to the hub + ?tab=,
keeping their exact path and name.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>"
```

---

## Task 4: Atendimento hub

**Files:**
- Modify: `frontend/src/router/index.ts`
- Create: `frontend/src/views/settings/ServiceSettingsHubView.vue`

**Interfaces:**
- Consumes: same as Task 3 (`SettingsTabHub`, `HubTab`, `anyPermission` guard already in place).
- Produces: route `settings-service` at `/settings/service`.

- [ ] **Step 1: Register the hub route and convert its 4 old routes to redirects**

Find:

```ts
        {
          path: 'settings/chatbot',
          name: 'chatbot-settings',
          component: () => import('@/views/settings/ChatbotSettingsView.vue'),
          meta: { permission: 'settings.chatbot' }
        },
```

Replace with:

```ts
        {
          path: 'settings/service',
          name: 'settings-service',
          component: () => import('@/views/settings/ServiceSettingsHubView.vue'),
          meta: { anyPermission: ['settings.chatbot', 'canned_responses', 'tags', 'contacts'] }
        },
        {
          path: 'settings/chatbot',
          name: 'chatbot-settings',
          redirect: () => ({ path: '/settings/service', query: { tab: 'chatbot' } })
        },
```

Find:

```ts
        {
          path: 'settings/canned-responses',
          name: 'canned-responses',
          component: () => import('@/views/settings/CannedResponsesView.vue'),
          meta: { permission: 'canned_responses' }
        },
```

Replace with:

```ts
        {
          path: 'settings/canned-responses',
          name: 'canned-responses',
          redirect: () => ({ path: '/settings/service', query: { tab: 'canned-responses' } })
        },
```

(Leave the very next route, `settings/canned-responses/:id`, untouched — it's a detail route, out of scope.)

Find:

```ts
        {
          path: 'settings/tags',
          name: 'tags',
          component: () => import('@/views/settings/TagsView.vue'),
          meta: { permission: 'tags' }
        },
```

Replace with:

```ts
        {
          path: 'settings/tags',
          name: 'tags',
          redirect: () => ({ path: '/settings/service', query: { tab: 'tags' } })
        },
```

Find:

```ts
        {
          path: 'settings/contacts',
          name: 'contacts',
          component: () => import('@/views/settings/ContactsView.vue'),
          meta: { permission: 'contacts' }
        },
```

Replace with:

```ts
        {
          path: 'settings/contacts',
          name: 'contacts',
          redirect: () => ({ path: '/settings/service', query: { tab: 'contacts' } })
        },
```

(Leave `settings/contacts/:id` untouched.)

- [ ] **Step 2: Create the Atendimento hub view**

Create `frontend/src/views/settings/ServiceSettingsHubView.vue`:

```vue
<script setup lang="ts">
import { SettingsTabHub, type HubTab } from '@/components/shared'
import ChatbotSettingsView from './ChatbotSettingsView.vue'
import CannedResponsesView from './CannedResponsesView.vue'
import TagsView from './TagsView.vue'
import ContactsView from './ContactsView.vue'

const tabs: HubTab[] = [
  { value: 'chatbot', labelKey: 'nav.chatbot', permission: 'settings.chatbot', component: ChatbotSettingsView },
  { value: 'canned-responses', labelKey: 'nav.cannedResponses', permission: 'canned_responses', component: CannedResponsesView },
  { value: 'tags', labelKey: 'nav.tags', permission: 'tags', component: TagsView },
  { value: 'contacts', labelKey: 'nav.contacts', permission: 'contacts', component: ContactsView },
]
</script>

<template>
  <SettingsTabHub :tabs="tabs" default-tab="chatbot" />
</template>
```

- [ ] **Step 3: Typecheck**

Run: `cd frontend && npm run typecheck`
Expected: same single pre-existing error, nothing new.

- [ ] **Step 4: Manual browser verification**

1. Navigate to `/settings/service`. Expect: 4 tabs, "Chatbot" active by default (and its own internal 5-tab bar — Mensagens/Agentes/Horários/SLA/IA — renders below; tabs-within-tabs is expected here, not a bug).
2. Navigate to `/settings/tags` (old URL). Expect: redirected to `/settings/service?tab=tags`.
3. Refresh on the "Contatos" tab. Expect: stays on "Contatos".

- [ ] **Step 5: Commit**

```bash
git add frontend/src/router/index.ts frontend/src/views/settings/ServiceSettingsHubView.vue
git commit -m "feat(settings): add Atendimento settings hub with tabbed navigation

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>"
```

---

## Task 5: Acesso hub

**Files:**
- Modify: `frontend/src/router/index.ts`
- Create: `frontend/src/views/settings/AccessSettingsHubView.vue`

**Interfaces:**
- Consumes: same as Task 3.
- Produces: route `settings-access` at `/settings/access`.

- [ ] **Step 1: Register the hub route and convert its 3 old routes to redirects**

Find:

```ts
        {
          path: 'settings/users',
          name: 'users',
          component: () => import('@/views/settings/UsersView.vue'),
          meta: { permission: 'users' }
        },
```

Replace with:

```ts
        {
          path: 'settings/access',
          name: 'settings-access',
          component: () => import('@/views/settings/AccessSettingsHubView.vue'),
          meta: { anyPermission: ['teams', 'users', 'roles'] }
        },
        {
          path: 'settings/users',
          name: 'users',
          redirect: () => ({ path: '/settings/access', query: { tab: 'users' } })
        },
```

(Leave `settings/users/:id` untouched.)

Find:

```ts
        {
          path: 'settings/roles',
          name: 'roles',
          component: () => import('@/views/settings/RolesView.vue'),
          meta: { permission: 'roles' }
        },
```

Replace with:

```ts
        {
          path: 'settings/roles',
          name: 'roles',
          redirect: () => ({ path: '/settings/access', query: { tab: 'roles' } })
        },
```

(Leave `settings/roles/:id` untouched.)

Find:

```ts
        {
          path: 'settings/teams',
          name: 'teams',
          component: () => import('@/views/settings/TeamsView.vue'),
          meta: { permission: 'teams' }
        },
```

Replace with:

```ts
        {
          path: 'settings/teams',
          name: 'teams',
          redirect: () => ({ path: '/settings/access', query: { tab: 'teams' } })
        },
```

(Leave `settings/teams/:id` untouched.)

- [ ] **Step 2: Create the Acesso hub view**

Create `frontend/src/views/settings/AccessSettingsHubView.vue`:

```vue
<script setup lang="ts">
import { SettingsTabHub, type HubTab } from '@/components/shared'
import TeamsView from './TeamsView.vue'
import UsersView from './UsersView.vue'
import RolesView from './RolesView.vue'

const tabs: HubTab[] = [
  { value: 'teams', labelKey: 'nav.teams', permission: 'teams', component: TeamsView },
  { value: 'users', labelKey: 'nav.users', permission: 'users', component: UsersView },
  { value: 'roles', labelKey: 'nav.roles', permission: 'roles', component: RolesView },
]
</script>

<template>
  <SettingsTabHub :tabs="tabs" default-tab="teams" />
</template>
```

- [ ] **Step 3: Typecheck**

Run: `cd frontend && npm run typecheck`
Expected: same single pre-existing error, nothing new.

- [ ] **Step 4: Manual browser verification**

1. Navigate to `/settings/access`. Expect: 3 tabs, "Equipes" active by default.
2. Navigate to `/settings/users` (old URL). Expect: redirected to `/settings/access?tab=users`.
3. From the "Usuários" tab, open a user's detail page (click a row). Expect: URL becomes `/settings/users/<id>` (unchanged detail route, not `/settings/access/...`), page renders normally. Navigate back. Expect: returns to `/settings/access?tab=users`.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/router/index.ts frontend/src/views/settings/AccessSettingsHubView.vue
git commit -m "feat(settings): add Acesso settings hub with tabbed navigation

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>"
```

---

## Task 6: Integrações hub

**Files:**
- Modify: `frontend/src/router/index.ts`
- Create: `frontend/src/views/settings/IntegrationsSettingsHubView.vue`

**Interfaces:**
- Consumes: same as Task 3.
- Produces: route `settings-integrations` at `/settings/integrations`.

- [ ] **Step 1: Register the hub route and convert its 3 old routes to redirects**

Find:

```ts
        {
          path: 'settings/api-keys',
          name: 'api-keys',
          component: () => import('@/views/settings/APIKeysView.vue'),
          meta: { permission: 'api_keys' }
        },
```

Replace with:

```ts
        {
          path: 'settings/integrations',
          name: 'settings-integrations',
          component: () => import('@/views/settings/IntegrationsSettingsHubView.vue'),
          meta: { anyPermission: ['api_keys', 'webhooks', 'custom_actions'] }
        },
        {
          path: 'settings/api-keys',
          name: 'api-keys',
          redirect: () => ({ path: '/settings/integrations', query: { tab: 'api-keys' } })
        },
```

(Leave `settings/api-keys/:id` untouched.)

Find:

```ts
        {
          path: 'settings/webhooks',
          name: 'webhooks',
          component: () => import('@/views/settings/WebhooksView.vue'),
          meta: { permission: 'webhooks' }
        },
```

Replace with:

```ts
        {
          path: 'settings/webhooks',
          name: 'webhooks',
          redirect: () => ({ path: '/settings/integrations', query: { tab: 'webhooks' } })
        },
```

(Leave `settings/webhooks/:id` untouched.)

Find:

```ts
        {
          path: 'settings/custom-actions',
          name: 'custom-actions',
          component: () => import('@/views/settings/CustomActionsView.vue'),
          meta: { permission: 'custom_actions' }
        },
```

Replace with:

```ts
        {
          path: 'settings/custom-actions',
          name: 'custom-actions',
          redirect: () => ({ path: '/settings/integrations', query: { tab: 'custom-actions' } })
        },
```

- [ ] **Step 2: Create the Integrações hub view**

Create `frontend/src/views/settings/IntegrationsSettingsHubView.vue`:

```vue
<script setup lang="ts">
import { SettingsTabHub, type HubTab } from '@/components/shared'
import APIKeysView from './APIKeysView.vue'
import WebhooksView from './WebhooksView.vue'
import CustomActionsView from './CustomActionsView.vue'

const tabs: HubTab[] = [
  { value: 'api-keys', labelKey: 'nav.apiKeys', permission: 'api_keys', component: APIKeysView },
  { value: 'webhooks', labelKey: 'nav.webhooks', permission: 'webhooks', component: WebhooksView },
  { value: 'custom-actions', labelKey: 'nav.customActions', permission: 'custom_actions', component: CustomActionsView },
]
</script>

<template>
  <SettingsTabHub :tabs="tabs" default-tab="api-keys" />
</template>
```

- [ ] **Step 3: Typecheck**

Run: `cd frontend && npm run typecheck`
Expected: same single pre-existing error, nothing new.

- [ ] **Step 4: Manual browser verification**

1. Navigate to `/settings/integrations`. Expect: 3 tabs, "Chaves de API" active by default.
2. Navigate to `/settings/webhooks` (old URL). Expect: redirected to `/settings/integrations?tab=webhooks`.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/router/index.ts frontend/src/views/settings/IntegrationsSettingsHubView.vue
git commit -m "feat(settings): add Integrações settings hub with tabbed navigation

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>"
```

---

## Task 7: Fix `getFirstAccessibleRoute`'s stale path list

**Files:**
- Modify: `frontend/src/router/index.ts`

**Interfaces:**
- Consumes: none new.
- Produces: none new — `getFirstAccessibleRoute` keeps its existing signature and behavior, just returns hub paths instead of the now-redirected old paths for these 16 permissions.

- [ ] **Step 1: Repoint the settings childPaths to the 4 hub paths**

Find, inside the `navigationOrder` array:

```ts
  { path: '/settings', permission: 'settings.general', childPaths: [
    { path: '/settings', permission: 'settings.general' },
    { path: '/settings/chatbot', permission: 'settings.chatbot' },
    { path: '/settings/accounts', permission: 'accounts' },
    { path: '/settings/canned-responses', permission: 'canned_responses' },
    { path: '/settings/contacts', permission: 'contacts' },
    { path: '/settings/tags', permission: 'tags' },
    { path: '/settings/teams', permission: 'teams' },
    { path: '/settings/users', permission: 'users' },
    { path: '/settings/roles', permission: 'roles' },
    { path: '/settings/api-keys', permission: 'api_keys' },
    { path: '/settings/webhooks', permission: 'webhooks' },
    { path: '/settings/custom-actions', permission: 'custom_actions' },
    { path: '/settings/occurrence-stages', permission: 'occurrences.stages' },
    { path: '/settings/units', permission: 'units' },
    { path: '/settings/occurrence-sla-policies', permission: 'occurrences.sla_policies' },
    { path: '/settings/occurrence-categories', permission: 'occurrences.categories' },
    { path: '/settings/occurrence-what-happened', permission: 'occurrences.what_happened' },
    { path: '/settings/occurrence-processes', permission: 'occurrences.processes' },
    { path: '/settings/sso', permission: 'settings.sso' }
  ]}
```

Replace with (same permission strings, same order — only the `path` values for the 16 hubbed permissions change; `/settings`, `/settings/accounts` and `/settings/sso` are untouched since those groups aren't part of this change):

```ts
  { path: '/settings', permission: 'settings.general', childPaths: [
    { path: '/settings', permission: 'settings.general' },
    { path: '/settings/service', permission: 'settings.chatbot' },
    { path: '/settings/accounts', permission: 'accounts' },
    { path: '/settings/service', permission: 'canned_responses' },
    { path: '/settings/service', permission: 'contacts' },
    { path: '/settings/service', permission: 'tags' },
    { path: '/settings/access', permission: 'teams' },
    { path: '/settings/access', permission: 'users' },
    { path: '/settings/access', permission: 'roles' },
    { path: '/settings/integrations', permission: 'api_keys' },
    { path: '/settings/integrations', permission: 'webhooks' },
    { path: '/settings/integrations', permission: 'custom_actions' },
    { path: '/settings/occurrences', permission: 'occurrences.stages' },
    { path: '/settings/occurrences', permission: 'units' },
    { path: '/settings/occurrences', permission: 'occurrences.sla_policies' },
    { path: '/settings/occurrences', permission: 'occurrences.categories' },
    { path: '/settings/occurrences', permission: 'occurrences.what_happened' },
    { path: '/settings/occurrences', permission: 'occurrences.processes' },
    { path: '/settings/sso', permission: 'settings.sso' }
  ]}
```

- [ ] **Step 2: Manual verification**

Using the Roles screen (now at `/settings/access?tab=roles`), create a temporary custom role with **only** `occurrences.processes:read` (no other permission at all, not even `chat` or `analytics`). Log in as a user with that role (or temporarily assign it to a test user).
Expect: after login, the app lands on `/settings/occurrences?tab=processes` (via `getFirstAccessibleRoute` → hub → `resolveActiveTab` picking the only accessible tab). Delete the temporary role/user afterward.

- [ ] **Step 3: Commit**

```bash
git add frontend/src/router/index.ts
git commit -m "fix(settings): point getFirstAccessibleRoute at the new hub paths

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>"
```

---

## Task 8: Collapse the 4 groups in `navigation.ts`

**Files:**
- Modify: `frontend/src/components/layout/navigation.ts`

**Interfaces:** none new.

- [ ] **Step 1: Replace the 4 groups' item lists**

In `frontend/src/components/layout/navigation.ts`, find:

```ts
          {
            label: 'nav.groupService',
            items: [
              { name: 'nav.chatbot', path: '/settings/chatbot', icon: Bot, permission: 'settings.chatbot' },
              { name: 'nav.cannedResponses', path: '/settings/canned-responses', icon: MessageSquareText, permission: 'canned_responses' },
              { name: 'nav.tags', path: '/settings/tags', icon: Tags, permission: 'tags' },
              { name: 'nav.contacts', path: '/settings/contacts', icon: Contact, permission: 'contacts' }
            ]
          },
          {
            label: 'nav.groupOccurrences',
            items: [
              { name: 'nav.occurrenceStages', path: '/settings/occurrence-stages', icon: ClipboardList, permission: 'occurrences.stages' },
              { name: 'nav.occurrenceCategories', path: '/settings/occurrence-categories', icon: Tag, permission: 'occurrences.categories' },
              { name: 'nav.whatHappened', path: '/settings/occurrence-what-happened', icon: HelpCircle, permission: 'occurrences.what_happened' },
              { name: 'nav.occurrenceProcesses', path: '/settings/occurrence-processes', icon: Workflow, permission: 'occurrences.processes' },
              { name: 'nav.slaPolicies', path: '/settings/occurrence-sla-policies', icon: Timer, permission: 'occurrences.sla_policies' },
              { name: 'nav.units', path: '/settings/units', icon: Building2, permission: 'units' }
            ]
          },
          {
            label: 'nav.groupAccess',
            items: [
              { name: 'nav.teams', path: '/settings/teams', icon: Users, permission: 'teams' },
              { name: 'nav.users', path: '/settings/users', icon: Users, permission: 'users' },
              { name: 'nav.roles', path: '/settings/roles', icon: Shield, permission: 'roles' }
            ]
          },
          {
            label: 'nav.groupIntegrations',
            items: [
              { name: 'nav.apiKeys', path: '/settings/api-keys', icon: Key, permission: 'api_keys' },
              { name: 'nav.webhooks', path: '/settings/webhooks', icon: Webhook, permission: 'webhooks' },
              { name: 'nav.customActions', path: '/settings/custom-actions', icon: Zap, permission: 'custom_actions' }
            ]
          }
```

Replace with (each group now has a single item pointing at its hub; `childPermissions` on the item lets `AppLayout.vue`'s existing `filterItems` show the group if the user has *any* of the group's permissions, exactly as it already does for every other multi-permission item like `nav.chatbot`):

```ts
          {
            label: 'nav.groupService',
            items: [
              { name: 'nav.groupService', path: '/settings/service', icon: Bot, childPermissions: ['settings.chatbot', 'canned_responses', 'tags', 'contacts'] }
            ]
          },
          {
            label: 'nav.groupOccurrences',
            items: [
              { name: 'nav.groupOccurrences', path: '/settings/occurrences', icon: ClipboardList, childPermissions: ['occurrences.stages', 'occurrences.categories', 'occurrences.what_happened', 'occurrences.processes', 'occurrences.sla_policies', 'units'] }
            ]
          },
          {
            label: 'nav.groupAccess',
            items: [
              { name: 'nav.groupAccess', path: '/settings/access', icon: Users, childPermissions: ['teams', 'users', 'roles'] }
            ]
          },
          {
            label: 'nav.groupIntegrations',
            items: [
              { name: 'nav.groupIntegrations', path: '/settings/integrations', icon: Key, childPermissions: ['api_keys', 'webhooks', 'custom_actions'] }
            ]
          }
```

- [ ] **Step 2: Check for now-unused icon imports**

Run: `cd frontend && npm run typecheck`

If `MessageSquareText`, `Tag`, `HelpCircle`, `Timer`, `Building2`, `Shield`, `Webhook`, `Zap` are now unused imports at the top of `navigation.ts` (Vue-TSC doesn't flag unused imports by default, so also grep):

```bash
cd frontend && for i in MessageSquareText Tag HelpCircle Timer Building2 Shield Webhook Zap; do echo "$i: $(grep -c "\b$i\b" src/components/layout/navigation.ts)"; done
```

Any icon with a count of `1` appears only in its own `import` line and must be removed from the `import { ... } from 'lucide-vue-next'` block at the top of the file. Keep `Bot`, `ClipboardList`, `Users`, `Key` (still used by the 4 new single items) and any icon still used elsewhere in the file (e.g. `Tags`, `Contact`, `Workflow` are used by other, untouched nav items — check each individually with the same grep before removing).

- [ ] **Step 3: Typecheck**

Run: `cd frontend && npm run typecheck`
Expected: same single pre-existing error, nothing new.

- [ ] **Step 4: Manual browser verification**

Open the sidebar (expanded, not collapsed), click "Configurações". Expect: the submenu now lists — under their existing group headers — Geral/SSO/Registros de auditoria (Organização, unchanged), Contas (Canais, unchanged), **one** "Atendimento" link, **one** "Ocorrências" link, **one** "Acesso" link, **one** "Integrações" link. Total 8 links instead of 20. Click each of the 4 new links; each lands on its hub with its default tab active.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/components/layout/navigation.ts
git commit -m "feat(settings): collapse 4 settings groups to one sidebar link each

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>"
```

---

## Task 9: Fix the known-breaking e2e URL assertions

**Files:**
- Modify: `frontend/e2e/tests/settings/organization-switch.spec.ts`
- Modify: `frontend/e2e/tests/settings/permissions.spec.ts`
- Modify: `frontend/e2e/tests/settings/users.spec.ts`
- Modify: `frontend/e2e/tests/crm/occurrence-permissions.spec.ts`

**Interfaces:** none.

- [ ] **Step 1: Fix `organization-switch.spec.ts:83`**

Find:

```ts
    expect(page.url()).toContain('/settings/users')
```

Replace with:

```ts
    expect(page.url()).toContain('/settings/access')
```

- [ ] **Step 2: Fix `permissions.spec.ts:110` and `:141`**

At both locations, find:

```ts
    expect(page.url()).toContain('/settings/users')
```

Replace with:

```ts
    expect(page.url()).toContain('/settings/access')
```

- [ ] **Step 3: Fix `permissions.spec.ts:145`**

Find:

```ts
    expect(page.url()).toContain('/settings/roles')
```

Replace with:

```ts
    expect(page.url()).toContain('/settings/access')
```

- [ ] **Step 4: Fix `users.spec.ts:168`**

Find:

```ts
    expect(page.url()).not.toContain('/settings/users')
```

Replace with:

```ts
    expect(page.url()).not.toContain('/settings/access')
```

- [ ] **Step 5: Fix `occurrence-permissions.spec.ts:108` and `:136`**

Find:

```ts
    expect(page.url()).not.toContain('/settings/occurrence-stages')
```

Replace with:

```ts
    expect(page.url()).not.toContain('/settings/occurrences')
```

Find:

```ts
    expect(page.url()).toContain('/settings/occurrence-stages')
```

Replace with:

```ts
    expect(page.url()).toContain('/settings/occurrences')
```

- [ ] **Step 6: Read each modified test's surrounding context before running**

For each of the 4 files, read ~15 lines around the change to confirm the assertion still matches the test's intent (e.g. that it's checking "did the user land in the settings area they're allowed in", not something more specific that also needs the `?tab=` value asserted). If a test also clicks a specific old-named sidebar link (e.g. `page.getByText('Usuários')`) that link text still exists inside the hub, so no further change should be needed — but confirm this by reading, don't assume.

- [ ] **Step 7: Run the affected suites**

Run: `cd frontend && BASE_URL=http://localhost:3000 npx playwright test e2e/tests/settings/organization-switch.spec.ts e2e/tests/settings/permissions.spec.ts e2e/tests/settings/users.spec.ts e2e/tests/crm/occurrence-permissions.spec.ts --reporter=line`
Expected: all pass. (Requires the dev server running at `localhost:3000` per this repo's existing e2e convention.)

- [ ] **Step 8: Commit**

```bash
git add frontend/e2e/tests/settings/organization-switch.spec.ts frontend/e2e/tests/settings/permissions.spec.ts frontend/e2e/tests/settings/users.spec.ts frontend/e2e/tests/crm/occurrence-permissions.spec.ts
git commit -m "test(e2e): update settings URL assertions for the new tab hubs

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>"
```

---

## Task 10: New e2e coverage for the authorization scenarios + full verification pass

**Files:**
- Create: `frontend/e2e/tests/settings/settings-hubs.spec.ts`

**Interfaces:** none new — uses the existing e2e framework helpers from `frontend/e2e/framework/` (`createTestScope`, `createUserWithPermissions`, `loginAs`, `SUPER_ADMIN`, `TestUserHandle`) and `ApiHelper`/`loginAsAdmin` from `frontend/e2e/helpers`, the exact same ones `e2e/tests/settings/permissions.spec.ts` already uses. Confirmed by reading that file: `createUserWithPermissions(api, scope, { permissions: [{ resource, action }] })` returns a `TestUserHandle` (has `.email`/`.password`), `loginAs(page, handle)` logs in as it, and `TabsTrigger` (checked in `frontend/src/components/ui/tabs/TabsTrigger.vue`) is a `reka-ui` primitive that renders `role="tab"` with `data-state="active"|"inactive"`.

- [ ] **Step 1: Write the spec**

Create `frontend/e2e/tests/settings/settings-hubs.spec.ts`:

```ts
import { test, expect } from '@playwright/test'
import { ApiHelper, loginAsAdmin } from '../../helpers'
import { createTestScope, createUserWithPermissions, loginAs, SUPER_ADMIN, type TestUserHandle } from '../../framework'

test.describe('Settings hubs — tab persistence', () => {
  test('refresh keeps the current tab, and a direct ?tab= link opens straight to it', async ({ page }) => {
    await loginAsAdmin(page)
    await page.goto('/settings/occurrences?tab=categories')
    await expect(page.getByRole('tab', { name: 'Categorias de ocorrência' })).toHaveAttribute('data-state', 'active')

    await page.reload()
    await expect(page).toHaveURL(/\/settings\/occurrences\?tab=categories/)
    await expect(page.getByRole('tab', { name: 'Categorias de ocorrência' })).toHaveAttribute('data-state', 'active')
  })

  test('an old direct URL redirects to the hub with the right tab', async ({ page }) => {
    await loginAsAdmin(page)
    await page.goto('/settings/occurrence-processes')
    await expect(page).toHaveURL(/\/settings\/occurrences\?tab=processes/)
  })
})

test.describe('Settings hubs — per-tab authorization', () => {
  const scope = createTestScope('settings-hubs-authz')
  let api: ApiHelper
  let singleTabUser: TestUserHandle
  let noGroupAccessUser: TestUserHandle

  test.beforeAll(async ({ request }) => {
    api = new ApiHelper(request)
    await api.login(SUPER_ADMIN.email, SUPER_ADMIN.password)
    singleTabUser = await createUserWithPermissions(api, scope, {
      userSlug: 'single-tab',
      permissions: [
        { resource: 'chat', action: 'read' },
        { resource: 'occurrences.categories', action: 'read' },
      ],
    })
    noGroupAccessUser = await createUserWithPermissions(api, scope, {
      userSlug: 'no-group-access',
      permissions: [{ resource: 'chat', action: 'read' }],
    })
  })

  test.afterAll(async () => {
    await api.deleteUser(singleTabUser.user.id).catch(() => {})
    await api.deleteRole(singleTabUser.role.id).catch(() => {})
    await api.deleteUser(noGroupAccessUser.user.id).catch(() => {})
    await api.deleteRole(noGroupAccessUser.role.id).catch(() => {})
  })

  test('a user with permission on only one tab lands on it automatically, with only that tab visible', async ({ page }) => {
    await loginAs(page, singleTabUser)
    await page.goto('/settings/occurrences')
    await expect(page).toHaveURL(/\/settings\/occurrences\?tab=categories/)
    await expect(page.getByRole('tab')).toHaveCount(1)
  })

  test('that user cannot reach a different tab via ?tab= — falls back to the one they have', async ({ page }) => {
    await loginAs(page, singleTabUser)
    await page.goto('/settings/occurrences?tab=processes')
    await expect(page).toHaveURL(/\/settings\/occurrences\?tab=categories/)
  })

  test('a user with no permission in the group is redirected away from the hub entirely', async ({ page }) => {
    await loginAs(page, noGroupAccessUser)
    await page.goto('/settings/occurrences')
    await expect(page).not.toHaveURL(/\/settings\/occurrences/)
  })
})
```

- [ ] **Step 2: Run the new spec**

Run: `cd frontend && BASE_URL=http://localhost:3000 npx playwright test e2e/tests/settings/settings-hubs.spec.ts --reporter=line`
Expected: all 5 tests pass. If `getByRole('tab', { name: 'Categorias de ocorrência' })` doesn't match (e.g. because of whitespace in the rendered label), inspect the rendered DOM (`npx playwright test --debug` or a screenshot) and adjust to the real accessible name rather than guessing again.

- [ ] **Step 3: Full verification pass**

Run, in order, and confirm each matches "Expected":

```bash
cd frontend && npm run typecheck
```
Expected: only the pre-existing `AccountDetailView.vue(172,45)` error.

```bash
cd frontend && npm run test:unit
```
Expected: all pass, including the new `settings-tab-hub.spec.ts` (7 tests).

```bash
cd frontend && npm run i18n:keys
```
Expected: no new missing/extra keys beyond whatever pre-existing unrelated gaps already existed before this work.

```bash
cd frontend && BASE_URL=http://localhost:3000 npx playwright test e2e/tests/settings e2e/tests/crm/occurrence-permissions.spec.ts --reporter=line
```
Expected: all pass.

```bash
cd frontend && npm run build
```
Expected: build succeeds.

- [ ] **Step 4: Commit**

```bash
git add frontend/e2e/tests/settings/settings-hubs.spec.ts
git commit -m "test(e2e): cover tab persistence and per-tab authorization for settings hubs

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>"
```
