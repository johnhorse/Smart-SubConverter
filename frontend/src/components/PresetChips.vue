<script setup lang="ts">
import { PRESET_DESCRIPTIONS } from '../constants'
import type { PresetInfo } from '../types'

defineProps<{ presets: PresetInfo[]; modelValue: string }>()
const emit = defineEmits<{ (e: 'update:modelValue', id: string): void }>()
</script>

<template>
  <div>
    <div class="chips" role="radiogroup" aria-label="规则模板">
      <button
        v-for="p in presets"
        :key="p.id"
        type="button"
        class="chip"
        role="radio"
        :aria-checked="modelValue === p.id"
        :class="{ active: modelValue === p.id }"
        @click="emit('update:modelValue', p.id)"
      >
        {{ p.name }}{{ modelValue === p.id ? ' ✓' : '' }}
      </button>
    </div>
    <p v-if="PRESET_DESCRIPTIONS[modelValue]" class="preset-desc">
      {{ PRESET_DESCRIPTIONS[modelValue] }}
    </p>
  </div>
</template>
