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
  // v1.6.0 未确认关键词 chips：必传受控 prop（持久层要存；改动一律 emit update:draftKeywords）
  draftKeywords: string[]
  // v1.6.1 匹配模式与未成组正则草稿：必传受控 prop（同上，改动一律 emit）
  draftMode: 'keyword' | 'regex'
  draftPattern: string
  // v1.6.0 清除本订阅记忆入口可用（粘贴了订阅链接才有记忆可清）
  canClear?: boolean
}>()
const emit = defineEmits<{
  (e: 'update:open', open: boolean): void
  (e: 'update:modelValue', groups: NodeGroup[]): void
  (e: 'update:excluded', names: string[]): void
  (e: 'update:draftKeywords', words: string[]): void
  (e: 'update:draftMode', mode: 'keyword' | 'regex'): void
  (e: 'update:draftPattern', pattern: string): void
  (e: 'clearMemory'): void
  (e: 'retry'): void
}>()

// 防抖 250ms：高亮/计数按防抖后草稿词刷新（打字不逐字重渲染列表）；已加 chips 即时参与
const KEYWORD_DEBOUNCE_MS = 250
const TOAST_MS = 2500

// 打词步骤：已加词 chips + 输入草稿；确认成组后进命名步骤（默认名 = 全部词拼接，可改）
// chips 受控（v1.6.0 状态提升：draftKeywords 由父层持久化）；修改一律 emit 新数组
const words = computed(() => props.draftKeywords)
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
  clearTimeout(clearConfirmTimer)
})

// v1.6.0 清除本订阅记忆：轻确认防误触——首次点击切换为确认文案，3s 未再点复原
const confirmClear = ref(false)
let clearConfirmTimer: ReturnType<typeof setTimeout> | undefined
function onClearMemory() {
  if (!props.canClear) return
  if (!confirmClear.value) {
    confirmClear.value = true
    clearTimeout(clearConfirmTimer)
    clearConfirmTimer = setTimeout(() => {
      confirmClear.value = false
    }, 3000)
    return
  }
  clearTimeout(clearConfirmTimer)
  confirmClear.value = false
  emit('clearMemory')
}

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
  emit('update:draftKeywords', [...words.value, kwLive.value])
  kwInput.value = ''
  kwDebounced.value = ''
}

function removeWord(i: number) {
  emit(
    'update:draftKeywords',
    words.value.filter((_, j) => j !== i),
  )
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

// ---- v1.6.1 正则匹配模式 ----
// 常用语法模板 chip：点击填入改词即用
const REGEX_TEMPLATES = [
  { label: '包含A但排除B', value: '(?=.*A)(?!.*B)' },
  { label: '包含A或B', value: '(A|B)' },
  { label: '纯排除A', value: '^(?!.*A)' },
]

// 正则草稿实时编译（'i' 忽略大小写，与 sidecar 派生 (?i) 前缀口径一致）：语法错误返回中文提示
const regexError = computed(() => {
  const p = props.draftPattern.trim()
  if (!p) return ''
  if (p.length > 256) return '正则表达式不超过 256 字符'
  if (p.includes('`') || /[\r\n\x00-\x1f\x7f]/.test(p)) return '不能包含反引号或换行符'
  // 若用户手动输入了 (?i) 前缀，剥离后再交由 JS 校验（JS RegExp 不支持内联 (?i) 前缀语法）
  const stripped = p.replace(/^\(\?i\)/i, '')
  try {
    new RegExp(stripped, 'i')
    return ''
  } catch {
    return '正则语法有误，检查括号与转义'
  }
})
const regexLive = computed(() => props.draftPattern.trim())
const regexOk = computed(() => regexLive.value !== '' && regexError.value === '')
// 语法正确且非空才参与高亮/计数；坏正则不高亮不计数（剥除 (?i) 前缀）
const regexRe = computed<RegExp | null>(() => {
  if (!regexOk.value) return null
  const stripped = regexLive.value.replace(/^\(\?i\)/i, '')
  try {
    return new RegExp(stripped, 'i')
  } catch {
    return null
  }
})
const regexHitCount = computed(() => {
  const re = regexRe.value
  return re ? props.nodes.filter((n) => re.test(n)).length : 0
})

function nodeHits(n: string): boolean {
  return props.draftMode === 'regex' ? !!regexRe.value?.test(n) : activeWords.value.every((w) => n.includes(w))
}
// 节点列表高亮：有关键词/有效正则才区分 hit/dim，否则原样
const hasActiveFilter = computed(() =>
  props.draftMode === 'regex' ? regexOk.value : activeWords.value.length > 0,
)

const matchedCount = computed(() =>
  props.draftMode === 'regex' ? regexHitCount.value : hitCountWords(activeWords.value),
)

const regexHint = computed(() => {
  if (regexError.value) return '' // 语法错误走红色轻提示
  if (props.probeState === 'ready' && regexLive.value && regexHitCount.value === 0) {
    return '没命中任何节点，换个写法试试'
  }
  return ''
})

function onPatternInput(e: Event) {
  emit('update:draftPattern', (e.target as HTMLInputElement).value)
}

// 成组判定用即时词（未防抖）：chips + 有效草稿；草稿非空但非法时整组不给确认
const confirmWords = computed(() => {
  if (kwLive.value !== '') {
    if (groupFieldInvalid(kwInput.value)) return []
    return [...words.value, kwLive.value]
  }
  return [...words.value]
})
const fullGroups = computed(() => props.modelValue.length >= GROUPS_MAX)

// 创建期零命中不确认成组（UI 挡；契约明确服务端不管）；正则模式：语法正确且非空且命中>0
const canConfirm = computed(() => {
  if (props.probeState !== 'ready' || fullGroups.value) return false
  if (props.draftMode === 'regex') return regexOk.value && regexHitCount.value > 0
  return confirmWords.value.length > 0 && hitCountWords(confirmWords.value) > 0
})

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
  // 正则模式：默认组名为空并聚焦命名框，引导输入自然组名（如「香港纯净」）
  if (props.draftMode === 'regex') {
    nameInput.value = ''
    naming.value = true
    void nextTick(() => nameEl.value?.focus())
    return
  }
  // 受控模式：emit 后 props 要等重渲染才刷新，默认名按目标数组算（不读旧 props 丢草稿词）
  const nextWords = kwLive.value !== '' ? [...words.value, kwLive.value] : [...words.value]
  if (kwLive.value !== '') {
    emit('update:draftKeywords', nextWords)
    kwInput.value = ''
    kwDebounced.value = ''
  }
  nameInput.value = nextWords.join('')
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
  if (props.draftMode === 'regex') {
    // 正则组：keywords 空、pattern 落位（trim）；成组后清空正则草稿
    emit('update:modelValue', [
      ...props.modelValue,
      { name: nameInput.value.trim(), keywords: [], pattern: props.draftPattern.trim(), urlTest: false },
    ])
    emit('update:draftPattern', '')
    naming.value = false
    nameInput.value = ''
    return
  }
  emit('update:modelValue', [
    ...props.modelValue,
    { name: nameInput.value.trim(), keywords: [...words.value], urlTest: false },
  ])
  emit('update:draftKeywords', []) // 成组后清 chips
  naming.value = false
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

// 已建分组命中计数：正则组按 'i' 正则匹配（坏 pattern 按 0 容错，剥除 (?i)），关键词组按 AND 字面量
function groupHitCount(g: NodeGroup): number {
  if (g.pattern) {
    const stripped = g.pattern.replace(/^\(\?i\)/i, '')
    try {
      const re = new RegExp(stripped, 'i')
      return props.nodes.filter((n) => re.test(n)).length
    } catch {
      return 0
    }
  }
  return hitCountWords(g.keywords)
}

// 节点或模式变化时重置命名步与输入草稿（防换订阅跨用例残留与命名污染）
watch(
  () => props.nodes,
  () => {
    naming.value = false
    nameInput.value = ''
    kwInput.value = ''
    kwDebounced.value = ''
  },
)

const kwEl = ref<HTMLInputElement>()
const reEl = ref<HTMLInputElement>()
function focusInput() {
  ;(props.draftMode === 'regex' ? reEl.value : kwEl.value)?.focus()
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

      <!-- v1.6.1 匹配模式切换：默认关键词（傻瓜体验），正则供进阶筛选（排除/或条件）；
           命名步期间锁定（确认分支按模式取数，中途切换会产出无效组） -->
      <div class="mode-tabs" role="tablist" aria-label="匹配模式">
        <button
          type="button"
          role="tab"
          class="mode-tab"
          :class="{ active: draftMode === 'keyword' }"
          :aria-selected="draftMode === 'keyword'"
          :disabled="naming"
          @click="emit('update:draftMode', 'keyword')"
        >
          关键词匹配
        </button>
        <button
          type="button"
          role="tab"
          class="mode-tab"
          :class="{ active: draftMode === 'regex' }"
          :aria-selected="draftMode === 'regex'"
          :disabled="naming"
          @click="emit('update:draftMode', 'regex')"
        >
          正则匹配
        </button>
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

        <!-- 打词步骤：逐词加 chip（AND 筛选） / 正则步骤：单行正则实时校验 -->
        <div v-else class="group-kw-row">
          <div v-if="draftMode === 'keyword'" class="group-kw-fields">
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
          <template v-else>
            <input
              ref="reEl"
              :value="draftPattern"
              class="group-kw-input regex-input"
              type="text"
              autocomplete="off"
              spellcheck="false"
              placeholder="如 (?=.*香港)(?!.*维护)，默认忽略大小写"
              aria-label="分组正则"
              @input="onPatternInput"
            />
          </template>
          <span v-if="hasActiveFilter" class="match-count">已命中 {{ matchedCount }}</span>
          <button
            type="button"
            class="secondary-btn group-confirm"
            :disabled="!canConfirm"
            @click="confirmGroup"
          >
            成组
          </button>
        </div>
        <!-- 正则模式：快捷语法模板（点击填入改词即用） -->
        <div v-if="!naming && draftMode === 'regex'" class="regex-tpl-chips">
          <button
            v-for="t in REGEX_TEMPLATES"
            :key="t.label"
            type="button"
            class="regex-tpl-chip"
            :title="`填入 ${t.value}`"
            @click="emit('update:draftPattern', t.value)"
          >
            {{ t.label }}
          </button>
        </div>
        <p v-if="!naming && draftMode === 'keyword' && kwHint" class="group-kw-hint">{{ kwHint }}</p>
        <p v-if="!naming && draftMode === 'regex' && regexError" class="regex-error" role="alert">
          {{ regexError }}
        </p>
        <p v-if="!naming && draftMode === 'regex' && regexHint" class="group-kw-hint">{{ regexHint }}</p>

        <ul class="node-list">
          <li v-for="(n, i) in nodes" :key="i" :class="hasActiveFilter ? (nodeHits(n) ? 'hit' : 'dim') : ''">
            {{ n }}
          </li>
        </ul>
      </template>

      <div v-if="modelValue.length" class="group-rows">
        <div v-for="g in modelValue" :key="g.name" class="group-row">
          <!-- 正则组：[正则] 微标（title 悬浮显示完整 pattern），命中计数 pattern 优先 -->
          <span class="group-chip">{{ g.name }}<span v-if="g.pattern" class="group-chip-regex" :title="g.pattern">[正则]</span> ×{{ groupHitCount(g) }}</span>
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

      <!-- v1.6.0 清除本订阅记忆：删本地草稿 + 服务端档案（该订阅全部），轻确认防误触 -->
      <div class="mem-clear-row">
        <button
          type="button"
          class="mem-clear"
          :class="{ confirming: confirmClear }"
          :disabled="!canClear"
          @click="onClearMemory"
        >
          {{ confirmClear ? '再点一次确认清除' : '清除本订阅记忆' }}
        </button>
      </div>
    </div>
  </div>
</template>
