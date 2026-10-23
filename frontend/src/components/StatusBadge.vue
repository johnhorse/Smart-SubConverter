<script setup lang="ts">
import type { BadgeInfo } from '../constants'

defineProps<{ badge: BadgeInfo | null }>()
</script>

<template>
  <span v-if="badge" class="badge" :class="badge.tone" role="status">
    <span class="dot" aria-hidden="true"></span>{{ badge.text }}
  </span>
  <span v-else class="badge unknown" role="status">
    <span class="dot" aria-hidden="true"></span>状态加载中…
  </span>
</template>
