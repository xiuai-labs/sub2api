<!-- xiu fork：用量列表的降智标记（见仓库根 PATCHES.md）。点击标记弹出判定依据。 -->
<template>
  <span class="inline-block">
    <button
      type="button"
      class="inline-flex items-center rounded px-1 py-px text-[10px] font-medium ring-1 ring-inset"
      :class="confirmed
        ? 'bg-red-50 text-red-700 ring-red-200 dark:bg-red-500/10 dark:text-red-300 dark:ring-red-500/30'
        : 'bg-orange-50 text-orange-700 ring-orange-200 dark:bg-orange-500/10 dark:text-orange-300 dark:ring-orange-500/30'"
      @click.stop="toggle"
    >
      {{ label }}
    </button>
    <Teleport to="body">
      <div v-if="open" class="fixed inset-0 z-[60]" @click="open = false">
        <div
          class="fixed z-[61] w-80 max-w-[90vw] rounded-lg border border-gray-200 bg-white p-3 text-xs shadow-lg dark:border-dark-600 dark:bg-dark-800"
          :style="{ top: `${pos.top}px`, left: `${pos.left}px` }"
          @click.stop
        >
          <div class="mb-2 font-medium" :class="confirmed ? 'text-red-600 dark:text-red-400' : 'text-orange-600 dark:text-orange-400'">
            {{ confirmed ? '已降级：上游确认改路由' : '疑似降智' }}
          </div>
          <div class="mb-2 space-y-0.5 text-gray-600 dark:text-gray-300">
            <div>请求模型：{{ report.requested_model || '-' }}</div>
            <div v-if="report.effective_model">实际模型：{{ report.effective_model }}</div>
          </div>
          <ul class="list-disc space-y-1 pl-4 text-gray-700 dark:text-gray-200">
            <li v-for="(signal, i) in report.signals" :key="i" class="break-words">{{ signal }}</li>
          </ul>
        </div>
      </div>
    </Teleport>
  </span>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import type { XiuUsageDowngradeReport } from '@/composables/xiu_useUsageDowngrades'

const props = defineProps<{ report: XiuUsageDowngradeReport }>()

const open = ref(false)
const pos = ref({ top: 0, left: 0 })
const confirmed = computed(() => props.report.verdict === 'confirmed')
const label = computed(() =>
  confirmed.value
    ? `已降级${props.report.effective_model ? ` → ${props.report.effective_model}` : ''}`
    : '疑似降智'
)

function toggle(event: MouseEvent) {
  const rect = (event.currentTarget as HTMLElement).getBoundingClientRect()
  const width = Math.min(320, window.innerWidth * 0.9)
  pos.value = {
    top: rect.bottom + 4,
    left: Math.max(8, Math.min(rect.left, window.innerWidth - width - 8))
  }
  open.value = !open.value
}
</script>
