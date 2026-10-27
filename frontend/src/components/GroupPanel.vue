<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { GROUPS_MAX, groupFieldInvalid } from '../constants'
import type { NodeGroup } from '../types'
import type { ProbeState } from '../useConverter'

const props = defineProps<{
  available: boolean
  url: string
  open: boolean
  modelValue: NodeGroup[]
  probeState: ProbeState
  nodes: string[]
}>()
const emit = defineEmits<{
  (e: 'update:open', open: boolean): void
  (e: 'update:modelValue', groups: NodeGroup[]): void
  (e: 'retry'): void
}>()

// 防抖 250ms：高亮/计数按防抖后关键词刷新（打字不逐字重渲染列表）
const KEYWORD_DEBOUNCE_MS = 250

const kwInput = ref('')
const kwDebounced = ref('')
let debounceTimer: ReturnType<typeof setTimeout> | undefined
watch(kwInput, (v) => {
  clearTimeout(debounceTimer)
  debounceTimer = setTimeout(() => {
    kwDebounced.value = v
  }, KEYWORD_DEBOUNCE_MS)
})
onBeforeUnmount(() => clearTimeout(debounceTimer))

const kwLive = computed(() => kwInput.value.trim())
const kwShown = computed(() => kwDebounced.value.trim())

// 字面量 substring 匹配（非正则）：与引擎侧 QuoteMeta 转义语义等价，转义在 sidecar 派生时做
function hitCount(keyword: string): number {
  if (!keyword) return 0
  return props.nodes.filter((n) => n.includes(keyword)).length
}

const matchedCount = computed(() => hitCount(kwShown.value))
const liveMatchedCount = computed(() => hitCount(kwLive.value))

const kwInvalid = computed(() => groupFieldInvalid(kwInput.value))
const kwDuplicate = computed(
  () => kwLive.value !== '' && props.modelValue.some((g) => g.name === kwLive.value),
)
const fullGroups = computed(() => props.modelValue.length >= GROUPS_MAX)

// 创建期零命中不确认成组（UI 挡；契约明确服务端不管）
const canConfirm = computed(
  () =>
    props.probeState === 'ready' &&
    !kwInvalid.value &&
    !kwDuplicate.value &&
    !fullGroups.value &&
    liveMatchedCount.value > 0,
)

const kwHint = computed(() => {
  if (kwInvalid.value) return '关键词不超过 32 字，不能用反引号'
  if (kwDuplicate.value) return '同名分组只留一个'
  if (props.probeState === 'ready' && kwLive.value && liveMatchedCount.value === 0) {
    return '没命中任何节点，换个词试试'
  }
  return ''
})

function confirmGroup() {
  if (!canConfirm.value) return
  emit('update:modelValue', [
    ...props.modelValue,
    { name: kwLive.value, keyword: kwLive.value, urlTest: false },
  ])
  kwInput.value = ''
}

function toggleUrlTest(name: string) {
  emit(
    'update:modelValue',
    props.modelValue.map((g) => (g.name === name ? { ...g, urlTest: !g.urlTest } : g)),
  )
}

function removeGroup(name: string) {
  emit('update:modelValue', props.modelValue.filter((g) => g.name !== name))
}

const kwEl = ref<HTMLInputElement>()
function focusInput() {
  kwEl.value?.focus()
}
</script>

<template>
  <div v-if="available" class="groups">
    <!-- 收起入口（虚线）：未粘贴订阅置灰不可点 -->
    <button
      v-if="!open"
      type="button"
      class="groups-entry"
      :disabled="!url.trim()"
      @click="emit('update:open', true)"
    >
      ＋ 自定义分组<span class="groups-entry-sub">给节点打词，自动归成一组</span>
    </button>

    <div v-else class="groups-body">
      <div class="groups-head">
        <span>自定义分组</span>
        <button type="button" class="groups-collapse" @click="emit('update:open', false)">收起</button>
      </div>

      <p v-if="probeState === 'loading'" class="groups-status">正在拉取节点名单…</p>
      <div v-else-if="probeState === 'failed'" class="groups-status">
        节点名单拉取失败
        <button type="button" class="groups-retry" @click="emit('retry')">重试</button>
      </div>
      <p v-else-if="probeState === 'idle'" class="groups-status">粘贴订阅链接后可加载节点名单</p>

      <template v-else>
        <div class="group-kw-row">
          <input
            ref="kwEl"
            v-model="kwInput"
            class="group-kw-input"
            type="text"
            autocomplete="off"
            spellcheck="false"
            placeholder="输入关键词，如：专线"
            aria-label="分组关键词"
          />
          <span v-if="kwShown" class="match-count">已命中 {{ matchedCount }}</span>
          <button type="button" class="secondary-btn group-confirm" :disabled="!canConfirm" @click="confirmGroup">
            成组
          </button>
        </div>
        <p v-if="kwHint" class="group-kw-hint">{{ kwHint }}</p>

        <ul class="node-list">
          <li v-for="(n, i) in nodes" :key="i" :class="kwShown ? (n.includes(kwShown) ? 'hit' : 'dim') : ''">
            {{ n }}
          </li>
        </ul>
      </template>

      <div v-if="modelValue.length" class="group-rows">
        <div v-for="g in modelValue" :key="g.name" class="group-row">
          <span class="group-chip">{{ g.name }} ×{{ hitCount(g.keyword) }}</span>
          <label class="group-switch">
            <input
              type="checkbox"
              :checked="g.urlTest"
              :aria-label="`${g.name} 自动选最快`"
              @change="toggleUrlTest(g.name)"
            />
            自动选最快
          </label>
          <button
            type="button"
            class="group-remove"
            :aria-label="`删除分组 ${g.name}`"
            @click="removeGroup(g.name)"
          >
            ×
          </button>
        </div>
      </div>

      <div class="groups-foot">
        <button
          v-if="modelValue.length"
          type="button"
          class="add-group-btn"
          :disabled="fullGroups"
          @click="focusInput"
        >
          ＋ 添加分组
        </button>
        <p class="groups-note">机场新增同类节点会自动进组</p>
      </div>
    </div>
  </div>
</template>
