/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { useCallback, useRef } from 'react'

import { useUpdateOption } from '../../hooks/use-update-option'

export type GroupOptionValue = boolean | number | string

/**
 * 把一个 option 值归一成可比较的形态。
 *
 * ── 不归一的话，逐键差分就是一句永真的空话 ──
 *
 * 基线是服务端返回的**紧凑** JSON（Go `json.Marshal`，无缩进），而三页序列化出来
 * 的是 `JSON.stringify(x, null, 2)`（2 空格缩进）。裸字符串比较因此恒为真，于是
 * 「只改了『每令牌自定义分组上限』」的一次保存会把这一页读到那一刻的 `GroupRatio`
 * 旧快照一起 PUT 回去 —— 服务端 `types.LoadFromJsonString` 是**整表替换**，
 * 另一个管理员在这期间新增的分组倍率被静默抹掉，该分组随即落进 `GetGroupRatio`
 * 的 fail-open 分支按凭空的 1.0 计费。这正是本轮要消灭的那一档。
 *
 * 解析不出来（非 JSON 的标量 option，例如 `MaxTokenAutoGroups`）时原样返回：
 * 那些值本来就逐位可比。
 */
export function normalizeGroupOptionValue(value: GroupOptionValue): string {
  if (typeof value !== 'string') return String(value)
  const trimmed = value.trim()
  if (trimmed === '') return ''
  if (!/^[[{]/.test(trimmed)) return trimmed
  try {
    return stableStringify(JSON.parse(trimmed))
  } catch {
    return trimmed
  }
}

/**
 * 紧凑序列化，**对象键排序、数组顺序原样保留**。
 *
 * 排序对象键：服务端的 Go `json.Marshal` 对 map 是字典序，而前端 `JSON.stringify`
 * 走的是对象的插入顺序（新增一行、删一行都会改变它）。不排序的话「加一行又删掉」
 * 会被算成一次改动，把整张表重新 PUT 一遍。
 *
 * **绝不排序数组**：`AutoGroups` 的顺序就是它的语义（auto 从上往下试到第一个可用的
 * 分组为止），排序会让一次真实的顺序调整被判成「没变」而根本不保存。
 */
function stableStringify(value: unknown): string {
  if (Array.isArray(value)) {
    return `[${value.map(stableStringify).join(',')}]`
  }
  if (value !== null && typeof value === 'object') {
    const entries = Object.entries(value as Record<string, unknown>).sort(
      ([a], [b]) => a.localeCompare(b)
    )
    return `{${entries
      .map(([key, val]) => `${JSON.stringify(key)}:${stableStringify(val)}`)
      .join(',')}}`
  }
  return JSON.stringify(value) ?? 'null'
}

/**
 * 本次保存里**真的**动过的键。
 *
 * 抽成导出函数是为了让它能被直接测：这条差分是「三页互不覆盖」的全部依据，
 * 而它此前是一句永真的裸字符串比较，从界面上完全看不出来。
 */
export function changedGroupOptionKeys<K extends string>(
  next: Readonly<Record<K, GroupOptionValue>>,
  baseline: Readonly<Partial<Record<K, GroupOptionValue>>>
): K[] {
  return (Object.keys(next) as K[]).filter((key) => {
    const before = baseline[key]
    if (before === undefined) return true
    return (
      normalizeGroupOptionValue(next[key]) !== normalizeGroupOptionValue(before)
    )
  })
}

/**
 * 「把本页改动过的分组配置项写回 `options`」。
 *
 * ── 为什么必须是逐键差分，而不是整页覆写 ──
 *
 * 8 个配置项拆到三页之后，三页会在不同时刻各自保存。整页覆写意味着 A 页
 * 保存时也把它打开那一刻读到的 `GroupRatio` 一起写回去 —— 而 C 页可能在这
 * 期间刚改过它。表现是「在 C 页改完倍率、去 A 页改个充值折扣、倍率就悄悄
 * 回到旧值了」，而且没有任何冲突提示。差分之后每一页只写自己真的动过的键，
 * 三页之间在数据上互不可见。
 *
 * 基线（`baselineRef`）在保存成功后原地推进，不等服务端回读：`updateOption`
 * 已经会 invalidate `system-options`，回读到来时上层会用新的 `defaultValues`
 * 重建页面状态。基线不推进的话，连按两次保存会把同一批键再写一遍。
 */
export function useGroupOptionSave<K extends string>(
  initialBaseline: Readonly<Record<K, GroupOptionValue>>,
  /** 表单字段名 → 后端 option 键名。1:1 的不必列。 */
  apiKeyMap?: Readonly<Partial<Record<K, string>>>
) {
  const updateOption = useUpdateOption()
  const baselineRef = useRef(initialBaseline)

  const save = useCallback(
    async (next: Readonly<Record<K, GroupOptionValue>>) => {
      const changed = changedGroupOptionKeys(next, baselineRef.current)
      for (const key of changed) {
        await updateOption.mutateAsync({
          key: apiKeyMap?.[key] ?? key,
          value: next[key],
        })
      }
      baselineRef.current = next
      return changed
    },
    [apiKeyMap, updateOption]
  )

  /**
   * 服务端回读到达时重设基线。
   *
   * 由调用方在「用新的 `defaultValues` 重建页面状态」的同一处调用，两件事
   * 必须同时发生：只重建状态不重设基线，下一次保存会把刚回读到的值当成改动
   * 再写一遍；只重设基线不重建状态，界面上还是旧值却被当成已保存。
   */
  const resetBaseline = useCallback(
    (baseline: Readonly<Record<K, GroupOptionValue>>) => {
      baselineRef.current = baseline
    },
    []
  )

  return { save, resetBaseline, isSaving: updateOption.isPending }
}
