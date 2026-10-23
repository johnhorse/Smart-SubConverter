<script setup lang="ts">
import type { ClientEntry } from '../types'

defineProps<{ clients: ClientEntry[]; modelValue: string }>()
const emit = defineEmits<{ (e: 'update:modelValue', target: string): void }>()
</script>

<template>
  <div class="chips" role="radiogroup" aria-label="指定客户端">
    <button
      v-for="c in clients"
      :key="c.id"
      type="button"
      class="chip"
      role="radio"
      :aria-checked="modelValue === c.target"
      :class="{ active: modelValue === c.target }"
      @click="emit('update:modelValue', c.target)"
    >
      {{ c.label }}{{ modelValue === c.target ? ' ✓' : '' }}
    </button>
  </div>
</template>
