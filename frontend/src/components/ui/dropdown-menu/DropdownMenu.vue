<script setup lang="ts">
import type { DropdownMenuRootEmits, DropdownMenuRootProps } from 'reka-ui'
import { DropdownMenuRoot, useForwardPropsEmits } from 'reka-ui'

const props = defineProps<DropdownMenuRootProps>()
const emits = defineEmits<DropdownMenuRootEmits>()

// Forward props/emits the same way the Popover and Select wrappers do. The
// previous hand-rolled `:open="open"` bound open even when it was undefined,
// which put DropdownMenuRoot into controlled mode with a value nothing ever
// updated — so the menu could never open. useForwardPropsEmits omits undefined
// props, leaving reka-ui in its uncontrolled (passive) mode.
const forwarded = useForwardPropsEmits(props, emits)
</script>

<template>
  <DropdownMenuRoot v-bind="forwarded">
    <slot />
  </DropdownMenuRoot>
</template>
