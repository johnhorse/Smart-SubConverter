<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import QRCode from 'qrcode'
import { copyText } from '../copy'
import { errorText } from '../constants'
import type { PreviewResponse } from '../types'

const props = defineProps<{ result: PreviewResponse; showEscape: boolean; clientLabel: string }>()
const emit = defineEmits<{ (e: 'escape'): void; (e: 'restart'): void }>()

const copied = ref(false)
const qrDataUrl = ref('')
let copiedTimer: ReturnType<typeof setTimeout> | undefined

async function doCopy() {
  const ok = await copyText(props.result.link)
  if (ok) {
    copied.value = true
    clearTimeout(copiedTimer)
    copiedTimer = setTimeout(() => {
      copied.value = false
    }, 1500)
  }
}

function openInClient() {
  if (props.result.deepLink) window.location.href = props.result.deepLink
}

async function renderQr(link: string) {
  try {
    qrDataUrl.value = await QRCode.toDataURL(link, { margin: 1, width: 220 })
  } catch {
    qrDataUrl.value = ''
  }
}

onMounted(() => void renderQr(props.result.link))
watch(() => props.result.link, (link) => void renderQr(link))
onBeforeUnmount(() => clearTimeout(copiedTimer))
</script>

<template>
  <section class="result">
    <div v-if="!result.ok" class="warn-banner" role="alert">
      <strong>{{ errorText(result.errorCode) }}</strong>
      <span class="warn-note">未通过校验，仍可试用</span>
    </div>

    <h2>转换好了</h2>
    <p class="result-note">导入你的 App 时会自动适配格式</p>

    <div class="link-box">
      <code class="link-text">{{ result.link }}</code>
    </div>

    <!-- 模板标注用响应回显的 preset.name（bad_preset 时为 null 不标注） -->
    <p v-if="result.preset" class="preset-note">模板：{{ result.preset.name }}</p>

    <div class="copy-row">
      <button type="button" class="primary-btn" @click="doCopy">
        {{ copied ? '已复制 ✓' : '复制' }}
      </button>
      <!-- deepLink 仅五家客户端非空（auto/clash/singbox/v2rayn 为 null），null 时只剩复制按钮 -->
      <button v-if="result.deepLink" type="button" class="secondary-btn" @click="openInClient">
        在 {{ clientLabel }} 中打开
      </button>
      <span class="copy-hint">这条短链就是你的订阅钥匙——转发给谁，谁就能用你的订阅；泄露了找部署者撤销，换一条新的</span>
    </div>

    <div v-if="qrDataUrl" class="qr-box">
      <img :src="qrDataUrl" alt="订阅链接二维码" width="220" height="220" />
    </div>

    <p class="alive-note">链接是活的：导入 App 后点「更新」，会自动拉最新规则</p>

    <div class="result-actions">
      <button v-if="showEscape" type="button" class="escape-btn" @click="emit('escape')">
        ？我是 Clash 用户
      </button>
      <button type="button" class="ghost-btn" @click="emit('restart')">换个链接重新转换</button>
    </div>
  </section>
</template>
