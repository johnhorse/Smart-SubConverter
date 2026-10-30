<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { GROUPS_MAX, KEYWORDS_MAX, groupFieldInvalid } from '../constants'
import type { NodeGroup, PresetGroup } from '../types'
import type { ProbeState } from '../useConverter'

const props = defineProps<{
  available: boolean
  open: boolean
  modelValue: NodeGroup[]
  probeState: ProbeState
  nodes: string[]
  templateGroups: PresetGroup[]
  excluded: string[]
  groupHits: Record<string, number>
}>()
const emit = defineEmits<{
  (e: 'update:open', open: boolean): void
  (e: 'update:modelValue', groups: NodeGroup[]): void
  (e: 'update:excluded', names: string[]): void
  (e: 'retry'): void
}>()

// 防抖 250ms：高亮/计数按防抖后草稿词刷新（打字不逐字重渲染列表）；已加 chips 即时参与
const KEYWORD_DEBOUNCE_MS = 250
const TOAST_MS = 2500

// 打词步骤：已加词 chips + 输入草稿；确认成组后进命名步骤（默认名 = 全部词拼接，可改）
const words = ref<string[]>([])
const kwInput = ref('')
const kwDebounced = ref('')
let debounceTimer: ReturnType<typeof setTimeout> | undefined
watch(kwInput, (v) => {
  clearTimeout(debounceTimer)
  debounceTimer = setTimeout(() => {
    kwDebounced.value = v
  }, KEYWORD_DEBOUNCE_MS)
})

// 轻提示：一键剔除空群组后短暂展示，自动消失；卸载时清定时器
const toast = ref('')
let toastTimer: ReturnType<typeof setTimeout> | undefined
function showToast(msg: string) {
  toast.value = msg
  clearTimeout(toastTimer)
  toastTimer = setTimeout(() => {
    toast.value = ''
  }, TOAST_MS)
}

onBeforeUnmount(() => {
  clearTimeout(debounceTimer)
  clearTimeout(toastTimer)
})

// ---- 模板组删减 ----
const excludedSet = computed(() => new Set(props.excluded))
// 上区按模板原序渲染未剔除组；下区同理渲染已剔除组（模板中已不存在的名字不渲染）
const visibleTemplate = computed(() => props.templateGroups.filter((g) => !excludedSet.value.has(g.name)))
const visibleExcluded = computed(() => props.templateGroups.filter((g) => excludedSet.value.has(g.name)))

// 0 节点标记：仅可删组（锁定组不标）；groupHits 缺席（无正则/未 probe）不算
function isEmptyGroup(g: PresetGroup): boolean {
  return !g.locked && props.groupHits[g.name] === 0
}

const prunableEmpty = computed(() =>
  visibleTemplate.value.filter((g) => isEmptyGroup(g)).map((g) => g.name),
)
const canPruneEmpty = computed(() => props.probeState === 'ready' && prunableEmpty.value.length > 0)

function excludeGroup(name: string) {
  if (excludedSet.value.has(name)) return
  emit('update:excluded', [...props.excluded, name])
}

function restoreGroup(name: string) {
  emit('update:excluded', props.excluded.filter((n) => n !== name))
}

function pruneEmpty() {
  if (!canPruneEmpty.value) return
  const names = prunableEmpty.value
  emit('update:excluded', [...props.excluded, ...names])
  showToast(`已剔除 ${names.length} 个空群组（随时点下方标签加回）`)
}

const naming = ref(false)
const nameInput = ref('')

const kwLive = computed(() => kwInput.value.trim())
const draftInvalid = computed(() => kwLive.value !== '' && groupFieldInvalid(kwInput.value))
const draftDuplicate = computed(() => kwLive.value !== '' && words.value.includes(kwLive.value))
const fullWords = computed(() => words.value.length >= KEYWORDS_MAX)

const canAddWord = computed(
  () => kwLive.value !== '' && !draftInvalid.value && !draftDuplicate.value && !fullWords.value,
)

function addWord() {
  if (!canAddWord.value) return
  words.value.push(kwLive.value)
  kwInput.value = ''
  kwDebounced.value = ''
}

function removeWord(i: number) {
  words.value.splice(i, 1)
}

// 参与高亮/计数的词：已加 chips + 防抖后草稿（非空即并入；单词不回车直接看命中，与 v1 行为一致）
const activeWords = computed(() => {
  const draft = kwDebounced.value.trim()
  return draft ? [...words.value, draft] : [...words.value]
})

// 字面量 substring AND 匹配（非正则）：与引擎侧 QuoteMeta 前瞻正则语义等价，转义在 sidecar 派生时做
function hitCountWords(kws: string[]): number {
  if (kws.length === 0) return 0
  return props.nodes.filter((n) => kws.every((k) => n.includes(k))).length
}

const matchedCount = computed(() => hitCountWords(activeWords.value))

// 成组判定用即时词（未防抖）：chips + 有效草稿；草稿非空但非法时整组不给确认
const confirmWords = computed(() => {
  if (kwLive.value !== '') {
    if (groupFieldInvalid(kwInput.value)) return []
    return [...words.value, kwLive.value]
  }
  return [...words.value]
})
const fullGroups = computed(() => props.modelValue.length >= GROUPS_MAX)

// 创建期零命中不确认成组（UI 挡；契约明确服务端不管）
const canConfirm = computed(
  () =>
    props.probeState === 'ready' &&
    confirmWords.value.length > 0 &&
    !fullGroups.value &&
    hitCountWords(confirmWords.value) > 0,
)

const kwHint = computed(() => {
  if (draftInvalid.value) return '关键词不超过 32 字，不能用反引号'
  if (draftDuplicate.value) return '这个词已经加过了'
  if (fullWords.value) return '最多 5 个关键词'
  if (props.probeState === 'ready' && confirmWords.value.length > 0 && hitCountWords(confirmWords.value) === 0) {
    return '没命中任何节点，换个词试试'
  }
  return ''
})

const nameEl = ref<HTMLInputElement>()

function confirmGroup() {
  if (!canConfirm.value) return
  if (kwLive.value !== '') {
    words.value.push(kwLive.value)
    kwInput.value = ''
    kwDebounced.value = ''
  }
  nameInput.value = words.value.join('')
  naming.value = true
  void nextTick(() => nameEl.value?.focus())
}

// 组名校验独立于词：trim 非空 / ≤32 rune / 禁反引号与控制字符 / 组内不重名
const nameInvalid = computed(() => groupFieldInvalid(nameInput.value))
const nameDuplicate = computed(() => {
  const t = nameInput.value.trim()
  return t !== '' && props.modelValue.some((g) => g.name === t)
})
const canConfirmName = computed(() => !nameInvalid.value && !nameDuplicate.value)

const nameHint = computed(() => {
  if (nameInvalid.value) return '组名不超过 32 字，不能用反引号'
  if (nameDuplicate.value) return '同名分组只留一个'
  return ''
})

function confirmName() {
  if (!naming.value || !canConfirmName.value) return
  emit('update:modelValue', [
    ...props.modelValue,
    { name: nameInput.value.trim(), keywords: [...words.value], urlTest: false },
  ])
  naming.value = false
  words.value = []
  nameInput.value = ''
}

function cancelName() {
  naming.value = false
  nameInput.value = ''
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
    <!-- 收起入口（虚线）：模板组清单不依赖订阅，入口不要求先粘 URL -->
    <button v-if="!open" type="button" class="groups-entry" @click="emit('update:open', true)">
      ⚙️ 分组管理<span class="groups-entry-sub">删减模板自带群组，或打词新建自己的组</span>
    </button>

    <div v-else class="groups-body">
      <div class="groups-head">
        <span>分组管理</span>
        <button type="button" class="groups-collapse" @click="emit('update:open', false)">收起</button>
      </div>

      <!-- 上区：模板自带群组（清单来自 /api/clients，未粘订阅也可删减） -->
      <section v-if="templateGroups.length" class="tpl-zone" aria-label="模板自带群组">
        <div class="zone-head">
          <span class="zone-title">模板自带群组</span>
          <button
            type="button"
            class="prune-empty"
            :disabled="!canPruneEmpty"
            @click="pruneEmpty"
          >
            一键剔除空群组
          </button>
          <button
            type="button"
            class="restore-default"
            :disabled="excluded.length === 0"
            @click="emit('update:excluded', [])"
          >
            ↺ 恢复默认
          </button>
        </div>
        <div class="tpl-chips">
          <span
            v-for="g in visibleTemplate"
            :key="g.name"
            class="tpl-chip"
            :title="g.locked ? '规则入口，必须保留' : undefined"
          >
            <span v-if="g.locked" class="tpl-lock" aria-label="规则入口，必须保留">🔒</span>
            <span class="tpl-chip-name">{{ g.name }}</span>
            <span v-if="isEmptyGroup(g)" class="tpl-zero">(0 节点)</span>
            <button
              v-if="!g.locked"
              type="button"
              class="tpl-x"
              :aria-label="`剔除组 ${g.name}`"
              @click="excludeGroup(g.name)"
            >
              ×
            </button>
          </span>
        </div>
        <p v-if="toast" class="prune-toast" role="status">{{ toast }}</p>
      </section>

      <!-- 下区：已剔除池（点 ＋ 加回；按模板原序，模板中已不存在的名字不渲染） -->
      <section v-if="visibleExcluded.length" class="excluded-zone" aria-label="已剔除">
        <div class="zone-head">
          <span class="zone-title">已剔除</span>
        </div>
        <div class="tpl-chips">
          <button
            v-for="g in visibleExcluded"
            :key="g.name"
            type="button"
            class="excluded-chip"
            :aria-label="`加回组 ${g.name}`"
            @click="restoreGroup(g.name)"
          >
            ＋ {{ g.name }}
          </button>
        </div>
      </section>

      <!-- 打词成组（现有 v1.5.0 流程） -->
      <div class="zone-head">
        <span class="zone-title">自定义分组</span>
      </div>

      <p v-if="probeState === 'loading'" class="groups-status">正在拉取节点名单…</p>
      <div v-else-if="probeState === 'failed'" class="groups-status">
        节点名单拉取失败
        <button type="button" class="groups-retry" @click="emit('retry')">重试</button>
      </div>
      <p v-else-if="probeState === 'idle'" class="groups-status">粘贴订阅链接后可加载节点名单</p>

      <template v-else>
        <!-- 命名步骤：确认成组后出现，默认名 = 关键词拼接 -->
        <div v-if="naming" class="group-kw-row">
          <input
            ref="nameEl"
            v-model="nameInput"
            class="group-kw-input"
            type="text"
            autocomplete="off"
            spellcheck="false"
            placeholder="给这组起个名字，回车确认"
            aria-label="分组名称"
            @keydown.enter.prevent="confirmName"
          />
          <span class="match-count">已命中 {{ matchedCount }}</span>
          <button type="button" class="secondary-btn group-confirm" :disabled="!canConfirmName" @click="confirmName">
            确认
          </button>
          <button type="button" class="group-cancel" @click="cancelName">取消</button>
        </div>
        <p v-if="naming && nameHint" class="group-kw-hint">{{ nameHint }}</p>

        <!-- 打词步骤：逐词加 chip（AND 筛选） -->
        <div v-else class="group-kw-row">
          <div class="group-kw-fields">
            <span v-for="(w, i) in words" :key="w" class="kw-chip">
              {{ w }}
              <button type="button" class="kw-chip-x" :aria-label="`删除关键词 ${w}`" @click="removeWord(i)">
                ×
              </button>
            </span>
            <input
              ref="kwEl"
              v-model="kwInput"
              class="group-kw-input"
              type="text"
              autocomplete="off"
              spellcheck="false"
              placeholder="输入关键词后回车，可加多个"
              aria-label="分组关键词"
              :disabled="fullWords"
              @keydown.enter.prevent="addWord"
            />
            <button
              type="button"
              class="group-add-kw"
              aria-label="添加关键词"
              :disabled="!canAddWord"
              @click="addWord"
            >
              ＋
            </button>
          </div>
          <span v-if="activeWords.length" class="match-count">已命中 {{ matchedCount }}</span>
          <button type="button" class="secondary-btn group-confirm" :disabled="!canConfirm" @click="confirmGroup">
            成组
          </button>
        </div>
        <p v-if="!naming && kwHint" class="group-kw-hint">{{ kwHint }}</p>

        <ul class="node-list">
          <li
            v-for="(n, i) in nodes"
            :key="i"
            :class="activeWords.length ? (activeWords.every((w) => n.includes(w)) ? 'hit' : 'dim') : ''"
          >
            {{ n }}
          </li>
        </ul>
      </template>

      <div v-if="modelValue.length" class="group-rows">
        <div v-for="g in modelValue" :key="g.name" class="group-row">
          <span class="group-chip">{{ g.name }} ×{{ hitCountWords(g.keywords) }}</span>
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
