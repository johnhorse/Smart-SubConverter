<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { useConverter } from './useConverter'
import { useHealth } from './useHealth'
import ClientChips from './components/ClientChips.vue'
import PresetChips from './components/PresetChips.vue'
import GroupPanel from './components/GroupPanel.vue'
import ResultPanel from './components/ResultPanel.vue'
import StatusBadge from './components/StatusBadge.vue'

const {
  phase, inputUrl, selectedTarget, selectedPreset, advancedOpen, submitting,
  convertError, result, clients, presets, customGroupsAvailable,
  groupsOpen, groups, nodes, probeState, excluded, groupHits, templateGroups,
  loadClients, probeNodes, submit,
  selectClient, selectPreset, escapeToClash, restart,
} = useConverter()

const { badge, init: initHealth } = useHealth()

// selectedTarget 存 target 值（与 ClientChips 选中口径一致），label 从注册表反查
const selectedClientLabel = computed(
  () => clients.value.find((c) => c.target === selectedTarget.value)?.label ?? '',
)

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
        <form class="convert-form" novalidate @submit.prevent="submit">
          <input
            v-model="inputUrl"
            class="url-input"
            type="url"
            inputmode="url"
            autocomplete="off"
            spellcheck="false"
            placeholder="粘贴你的订阅链接"
            aria-label="订阅链接"
          />
          <button type="submit" class="primary-btn" :disabled="submitting">
            {{ submitting ? '转换中…' : '转换' }}
          </button>
        </form>

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
          @update:open="groupsOpen = $event"
          @update:model-value="groups = $event"
          @update:excluded="excluded = $event"
          @retry="probeNodes"
        />
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
