/**
 * xiu fork：用量列表的「已降级 / 疑似降智」标记（见仓库根 PATCHES.md）。
 *
 * 表格拿到一页数据后，带着这一页每行的 (id, api_key_id, request_id) 去
 * POST /admin/usage/xiu-downgrades 查一次；后端只返回被标记的那几条，其余视为干净。
 * 标记在 Redis 里只存 3 天，更早的记录不会有标记。查询失败不打扰页面，只是不显示标记。
 */
import { ref, watch, type Ref } from 'vue'
import { apiClient } from '@/api/client'

export interface XiuUsageDowngradeReport {
  verdict: 'confirmed' | 'suspected'
  requested_model?: string
  effective_model?: string
  safety_buffering?: boolean
  reasons?: string[]
  use_cases?: string[]
  faster_model?: string
  verifications?: string[]
  turn_state_len?: number
  primary_used_percent?: number
  signals: string[]
}

interface XiuUsageRow {
  id: number
  api_key_id: number
  request_id: string
}

export function useXiuUsageDowngrades(rows: () => XiuUsageRow[]): Ref<Record<number, XiuUsageDowngradeReport>> {
  const reports = ref<Record<number, XiuUsageDowngradeReport>>({})
  let seq = 0
  // UsageTable 也被用户侧的「使用记录」页复用；那里不能去打管理端接口。
  if (typeof window === 'undefined' || !window.location.pathname.startsWith('/admin')) {
    return reports
  }

  watch(
    // 串成字符串再比：同一页数据被重新赋值（自动刷新）时不重复请求。
    () => JSON.stringify(rows()
      .filter((row) => row.request_id)
      .map((row) => ({ id: row.id, api_key_id: row.api_key_id, request_id: row.request_id }))),
    async (key) => {
      const list = JSON.parse(key) as XiuUsageRow[]
      const current = ++seq
      if (list.length === 0) {
        reports.value = {}
        return
      }
      try {
        const { data } = await apiClient.post<Record<string, XiuUsageDowngradeReport>>('/admin/usage/xiu-downgrades', {
          rows: list
        })
        if (current === seq) reports.value = (data ?? {}) as Record<number, XiuUsageDowngradeReport>
      } catch {
        if (current === seq) reports.value = {}
      }
    },
    { immediate: true }
  )

  return reports
}
