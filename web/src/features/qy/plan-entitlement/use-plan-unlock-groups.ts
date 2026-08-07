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
import { useQueryClient } from '@tanstack/react-query'
import { useCallback, useEffect, useState } from 'react'

import { isQyError } from '../lib/api'
import { qyKeys } from '../lib/query-keys'
import { qyGetPlanEntitlement, qySavePlanEntitlement } from './api'
import type { QyPlanBalanceScope } from './types'

/**
 * 「解锁模型分组」那一格的可用状态。
 *
 * 与总名额那一格（`SeatCapState`）刻意同构，理由也逐字相同：
 *
 * - `hidden` 是"这套后端根本没这功能"（扩展关闭 / 接口未部署）—— 零痕迹不显示；
 * - `error`  是"功能在、这次没读到" —— 格子要显示出来但**禁用**，并且保存时
 *   绝对不能写：此刻界面上的空清单是占位而不是真值，允许提交就会把一个已经
 *   卖出去的套餐的解锁分组**悄悄清空**，而买过的人下一次请求就选不到那些分组了。
 */
export type QyUnlockGroupsState = 'error' | 'hidden' | 'loading' | 'ready'

export type UseQyPlanUnlockGroupsResult = {
  state: QyUnlockGroupsState
  /** 当前勾选（含已失效的存量绑定）。 */
  selected: string[]
  /** 模型分组轴的事实清单，由后端下发。 */
  candidates: string[]
  /** 已绑定、但已经不在候选里的名字（运营把它从分组倍率表里删了）。 */
  orphans: string[]
  /** 该套餐余额的使用范围。抽屉里只读，改它要去行操作里的完整弹窗。 */
  balanceScope: QyPlanBalanceScope
  /**
   * 「仅限」+ 零**有效**绑定 = 一份任何请求都用不上的死钱，后端会 400。
   * 抽屉必须在提交前自己挡住，而不是让人按下去再吃一个错误。
   */
  restrictedWithoutBinding: boolean
  toggle: (group: string) => void
  /**
   * 值真的变了才写。返回 `true` 表示这次确实写了一次。
   *
   * **失败时抛出**：调用方要把它与套餐本体的保存结果分开报（两次写入跨两个库，
   * 中间没有事务），报成"保存成功"会让运营以为解锁已经生效。
   */
  saveIfChanged: (planId: number) => Promise<boolean>
}

function sameSet(a: string[], b: string[]): boolean {
  if (a.length !== b.length) return false
  const left = [...a].sort()
  const right = [...b].sort()
  return left.every((item, index) => item === right[index])
}

/**
 * 套餐编辑抽屉里的「解锁模型分组」。
 *
 * ── 为什么这一格值得从零件级别单独拿出来 ──
 *
 * 它落**扩展库**的 `qy_plan_group_grants`，而套餐本体落主库 `subscription_plans`：
 * 一次"保存"横跨两个库、两次写入、两条审计，中间没有事务。抽屉里已经有一格
 * （总名额）是这个形状，两格必须用同一套状态机与同一种失败口径 —— 不然运营会
 * 从两格不同的沉默里推出两个不同的结论。
 *
 * ── 它替换掉的是什么 ──
 *
 * 原来这个位置是上游的「升级分组 / 降级分组」两个下拉，它们写的是
 * `subscription_plans.upgrade_group|downgrade_group`，而那两列在购买时会
 * **直接改写 `users.group`**。用户分组与模型分组分离之后，那正是要消灭的东西：
 * 买一个套餐不该把人从一个用户分组搬到另一个用户分组，只该给他多几个**模型分组**。
 */
export function useQyPlanUnlockGroups(args: {
  open: boolean
  /** 编辑现有套餐时是它的 id；新建时 <= 0。 */
  planId: number
  /** 扩展是否可用（与总名额那一格同一个判据）。 */
  supported: boolean
}): UseQyPlanUnlockGroupsResult {
  const { open, planId, supported } = args
  const queryClient = useQueryClient()

  const [state, setState] = useState<QyUnlockGroupsState>('hidden')
  const [selected, setSelected] = useState<string[]>([])
  // 服务端上一次告诉我们的清单。比对它来决定要不要发第二次写入 ——
  // 少了这一步，管理员每改一次套餐标题都会往审计里塞一条没有内容的解锁变更。
  const [initial, setInitial] = useState<string[]>([])
  const [candidates, setCandidates] = useState<string[]>([])
  const [balanceScope, setBalanceScope] =
    useState<QyPlanBalanceScope>('universal')
  const [note, setNote] = useState('')

  useEffect(() => {
    if (!open) return
    if (!supported) {
      setState('hidden')
      return
    }
    if (planId <= 0) {
      // 新建：没有 id 可查，空清单 + 可编辑，套餐建好之后再写第二次。
      setState('ready')
      setSelected([])
      setInitial([])
      setCandidates([])
      setBalanceScope('universal')
      setNote('')
      return
    }
    setState('loading')
    setSelected([])
    setInitial([])
    let stale = false
    qyGetPlanEntitlement(planId)
      .then((data) => {
        if (stale) return
        setSelected(data.unlock_groups)
        setInitial(data.unlock_groups)
        setCandidates(data.model_group_candidates)
        setBalanceScope(data.balance_scope)
        // 原样带回去。后端在缺 `balance_scope` 时按 universal 处理 ——
        // 也就是**静默把「仅限」改回「通用」**，而那一改会让一笔本来花不出去的
        // 余额突然能花在任何模型分组上。note 同理，不带回去等于清空。
        setNote(data.note)
        setState('ready')
      })
      .catch((error) => {
        if (stale) return
        setState(isQyError(error) && error.isHidden ? 'hidden' : 'error')
      })
    return () => {
      stale = true
    }
  }, [open, planId, supported])

  const toggle = useCallback((group: string) => {
    setSelected((current) =>
      current.includes(group)
        ? current.filter((item) => item !== group)
        : [...current, group]
    )
  }, [])

  const orphans = selected.filter((group) => !candidates.includes(group))
  // 判定用的是**活着的**绑定：已从倍率表消失的名字在快照里已被剔除，绑着等于
  // 没绑，把它算进来会让一个实际上花不掉的余额池通过校验。
  const restrictedWithoutBinding =
    balanceScope === 'restricted' && selected.length === orphans.length

  const saveIfChanged = useCallback(
    async (targetPlanId: number) => {
      if (state !== 'ready' || targetPlanId <= 0) return false
      if (sameSet(selected, initial)) return false
      await qySavePlanEntitlement(targetPlanId, {
        unlock_groups: selected,
        balance_scope: balanceScope,
        note,
      })
      setInitial(selected)
      // 矩阵页的格子带「经套餐 P 可达」，它的数据源就是这份绑定：不失效它，
      // 运营切过去会看到一格仍然画着"不可达"，然后在看不见影响面的情况下去调
      // 那一列的兜底倍率。
      await Promise.all([
        queryClient.invalidateQueries({
          queryKey: qyKeys.adminPlanEntitlement(targetPlanId),
        }),
        queryClient.invalidateQueries({ queryKey: qyKeys.adminGroupMatrix() }),
      ])
      return true
    },
    [balanceScope, initial, note, queryClient, selected, state]
  )

  return {
    state,
    selected,
    candidates,
    orphans,
    balanceScope,
    restrictedWithoutBinding,
    toggle,
    saveIfChanged,
  }
}
