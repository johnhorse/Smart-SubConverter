<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { extractTagFromUrl, useConverter } from './useConverter'
import { useHealth } from './useHealth'
import ClientChips from './components/ClientChips.vue'
import PresetChips from './components/PresetChips.vue'
import GroupPanel from './components/GroupPanel.vue'
import ResultPanel from './components/ResultPanel.vue'
import StatusBadge from './components/StatusBadge.vue'

const {
  phase, inputUrl, mainTag, extraSubs, addExtraSub, removeExtraSub, handleUrlBatch, backupTags,
  selectedTarget, selectedPreset, advancedOpen, submitting,
  convertError, result, clients, presets, customGroupsAvailable,
  groupsOpen, groups, nodes, probeState, excluded, groupHits, templateGroups,
  draftKeywords, draftMode, draftPattern, restoreNotice, restoreNoticeText,
  loadClients, probeNodes, submit,
  selectClient, selectPreset, escapeToClash, undoRestore, clearPreferences, restart,
} = useConverter()

function onMainInput(e: Event) {
  const target = e.target as HTMLInputElement
  const val = target.value
  if (handleUrlBatch(val)) {
    return
  }
}

function onExtraUrlInput(index: number, e: Event) {
  const target = e.target as HTMLInputElement
  const val = target.value
  const cur = extraSubs.value[index]
  if (cur) {
    cur.url = val
    if (!cur.tag) {
      cur.tag = extractTagFromUrl(val)
    }
  }
}

const { badge, init: initHealth } = useHealth()

// selectedTarget 存 target 值（与 ClientChips 选中口径一致），label 从注册表反查
const selectedClientLabel = computed(
  () => clients.value.find((c) => c.target === selectedTarget.value)?.label ?? '',
)

// v1.6.0 清除本订阅记忆的结果轻提示（短暂展示自动消失）
const memoryToast = ref('')
let memoryToastTimer: ReturnType<typeof setTimeout> | undefined
async function onClearMemory() {
  const ok = await clearPreferences()
  memoryToast.value = ok ? '已清除本订阅记忆' : '本地记忆已清，服务端档案删除失败，稍后再试'
  clearTimeout(memoryToastTimer)
  memoryToastTimer = setTimeout(() => {
    memoryToast.value = ''
  }, 2500)
}

onMounted(() => {
  void initHealth()
  void loadClients()
})
</script>

<template>
  <div class="page">
    <main class="panel">
      <!-- 状态①/②：输入区 -->
      <section v-if="phase !== 'result'" class="convert-section">
        <header class="hero">
          <h1>订阅转换</h1>
          <p class="hero-sub">粘贴订阅链接，转成你的 App 能用的格式</p>
        </header>

        <!-- novalidate：URL 合法性由 sidecar 校验（错误码 bad_url_format + 人话文案），不用浏览器原生气泡 -->
        <form class="convert-form" :class="{ 'multi-sub-mode': extraSubs.length > 0 }" novalidate @submit.prevent="submit">
          <!-- 单订阅经典模式 -->
          <div v-if="extraSubs.length === 0" class="single-sub-row">
            <input
              v-model="inputUrl"
              class="url-input"
              type="url"
              inputmode="url"
              autocomplete="off"
              spellcheck="false"
              placeholder="粘贴你的订阅链接（支持多行/竖线分隔）"
              aria-label="订阅链接"
              @input="onMainInput"
            />
            <button type="submit" class="primary-btn" :disabled="submitting">
              {{ submitting ? '转换中…' : '转换' }}
            </button>
          </div>

          <!-- 多订阅卡片列表模式 -->
          <div v-else class="multi-sub-container">
            <div class="sub-item-row main-sub-row">
              <span class="sub-role-badge">主力</span>
              <input
                v-model="mainTag"
                class="tag-input"
                type="text"
                placeholder="主力"
                aria-label="主力机场标记"
              />
              <input
                v-model="inputUrl"
                class="url-input"
                type="url"
                inputmode="url"
                autocomplete="off"
                spellcheck="false"
                placeholder="主力机场订阅链接"
                aria-label="主力订阅链接"
                @input="onMainInput"
              />
            </div>

            <div
              v-for="(sub, idx) in extraSubs"
              :key="idx"
              class="sub-item-row extra-sub-row"
            >
              <span class="sub-role-badge backup">备用</span>
              <input
                v-model="sub.tag"
                class="tag-input"
                type="text"
                placeholder="备用"
                aria-label="备用机场标记"
              />
              <input
                :value="sub.url"
                class="url-input"
                type="url"
                inputmode="url"
                autocomplete="off"
                spellcheck="false"
                placeholder="备用机场订阅链接"
                aria-label="备用订阅链接"
                @input="onExtraUrlInput(idx, $event)"
              />
              <button
                type="button"
                class="remove-sub-btn"
                aria-label="删除此备用订阅"
                @click="removeExtraSub(idx)"
              >
                ×
              </button>
            </div>

            <div class="multi-sub-actions">
              <button
                type="button"
                class="add-sub-btn"
                @click="addExtraSub()"
              >
                + 添加备用订阅
              </button>
              <button type="submit" class="primary-btn submit-multi-btn" :disabled="submitting">
                {{ submitting ? '转换中…' : '转换' }}
              </button>
            </div>
            <p class="multi-sub-tip">
              已启用多机场容灾：节点自动打标，客户端首选「🔯 机场容灾」（主力故障时自动切备用）。
            </p>
          </div>
        </form>

        <div v-if="extraSubs.length === 0" class="sub-addon-row">
          <button type="button" class="add-sub-link" @click="addExtraSub()">
            + 添加备用订阅（多机场容灾 Fallback）
          </button>
        </div>

        <div v-if="convertError && !submitting" class="error-note" role="alert">
          <p>{{ convertError }}</p>
        </div>

        <div class="advanced">
          <button
            type="button"
            class="advanced-toggle"
            :aria-expanded="advancedOpen"
            @click="advancedOpen = !advancedOpen"
          >
            高级 · 客户端与规则模板
            <span class="toggle-hint">{{ advancedOpen ? '收起' : '展开' }}</span>
          </button>
          <div v-show="advancedOpen" class="advanced-body">
            <p class="advanced-hint">默认自动识别你的 App，不确定再展开指定</p>
            <ClientChips
              :clients="clients"
              :model-value="selectedTarget"
              @update:model-value="selectClient"
            />
            <p class="preset-hint">用哪套规则模板</p>
            <PresetChips
              :presets="presets"
              :model-value="selectedPreset"
              @update:model-value="selectPreset"
            />
          </div>
        </div>

        <!-- v1.6.0 偏好回填提示条：自动带出 + 一键撤销（撤销只回滚面板状态，不动存档） -->
        <div v-if="restoreNotice" class="restore-notice" role="status">
          <span>{{ restoreNoticeText }}</span>
          <button type="button" class="restore-undo" @click="undoRestore">不用了</button>
        </div>

        <!-- v1.5.0 自定义分组 + 模板组删减：能力位（部署 × 选中档）全真才渲染入口；
             probe 失败只影响面板内可用性 -->
        <GroupPanel
          :available="customGroupsAvailable"
          :open="groupsOpen"
          :model-value="groups"
          :probe-state="probeState"
          :nodes="nodes"
          :template-groups="templateGroups"
          :excluded="excluded"
          :group-hits="groupHits"
          :draft-keywords="draftKeywords"
          :draft-mode="draftMode"
          :draft-pattern="draftPattern"
          :can-clear="inputUrl.trim() !== ''"
          :backup-tags="backupTags"
          @update:open="groupsOpen = $event"
          @update:model-value="groups = $event"
          @update:excluded="excluded = $event"
          @update:draft-keywords="draftKeywords = $event"
          @update:draft-mode="draftMode = $event"
          @update:draft-pattern="draftPattern = $event"
          @clear-memory="onClearMemory"
          @retry="probeNodes"
        />
        <!-- v1.6.0 清除记忆结果轻提示 -->
        <p v-if="memoryToast" class="memory-toast" role="status">{{ memoryToast }}</p>
        <!-- 能力位失效（切到不支持档）但组已建：面板隐藏，残留组以提示行可见——
             已建的组保留，本次转换不含分组，切回支持档即恢复 -->
        <p
          v-if="!customGroupsAvailable && groups.length > 0"
          class="groups-kept-note"
          role="note"
        >
          所选模板不支持自定义分组，本次转换不含分组（分组已保留，切换模板后可用）
        </p>
      </section>

      <!-- 状态③：结果 -->
      <ResultPanel
        v-else-if="result"
        :result="result"
        :show-escape="selectedTarget === 'auto'"
        :client-label="selectedClientLabel"
        @escape="escapeToClash"
        @restart="restart"
      />
    </main>

    <footer class="footer">
      <StatusBadge :badge="badge" />
    </footer>
  </div>
</template>
