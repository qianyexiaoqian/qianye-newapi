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

/**
 * 「解锁模型分组」那一次写入的结果。
 *
 * `blocked` 单独成一档、不与 `unchanged` 合并：两者在界面上要说两句不同的话。
 * `unchanged` 是"这次没什么可写的"（静默），`blocked` 是"你改了，但这份改动
 * 会写出一笔谁都花不掉的死钱，所以没写" —— 后者不说出来，运营会以为已经生效。
 */
export type QyUnlockSaveOutcome = 'blocked' | 'saved' | 'unchanged'

export type UseQyPlanUnlockGroupsResult = {
  state: QyUnlockGroupsState
  /** 当前勾选（含已失效的存量绑定）。 */
  selected: string[]
  /** 模型分组轴的事实清单，由后端下发。 */
  candidates: string[]
  /** 已绑定、但已经不在候选里的名字（运营把它从分组倍率表里删了）。 */
  orphans: string[]
  /**
   * 该套餐余额的使用范围。选择这一项要去行操作里的完整弹窗（那里有影响面与
   * 现算倍率）；抽屉里唯一能动它的是下面那个 `resetScopeToUniversal`。
   */
  balanceScope: QyPlanBalanceScope
  /**
   * 「仅限」+ 零**有效**绑定 = 一份任何请求都用不上的死钱。
   *
   * 它挡住的是**解锁清单那一次写入**，不是整张套餐表单：标题、价格、上下架
   * 与解锁无关，让它们为这一格陪葬只会换来一个谁都改不了的套餐 —— 而这个状态
   * 多半不是当前这次编辑造成的（运营在别处删掉了那个模型分组，回头打开抽屉
   * 就已经是这样了）。
   */
  restrictedWithoutBinding: boolean
  toggle: (group: string) => void
  /**
   * 把余额范围改回「通用」。
   *
   * 这是 `restrictedWithoutBinding` 在**抽屉内部**的出口。少了它，候选清单为空
   * 时（运营刚把那个模型分组从倍率表里删了）勾选框里没有任何可勾的东西，
   * 而抽屉又不提供范围选择 —— 这个状态就再也走不出去了。
   */
  resetScopeToUniversal: () => void
  /**
   * 值真的变了才写（清单或范围任一变化都算）。
   *
   * **失败时抛出**：调用方要把它与套餐本体的保存结果分开报（两次写入跨两个库，
   * 中间没有事务），报成"保存成功"会让运营以为解锁已经生效。
   */
  saveIfChanged: (planId: number) => Promise<QyUnlockSaveOutcome>
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
  // 范围也要记住服务端上一次的值。只比清单的话，运营点了「改回通用」却没动
  // 勾选时这次保存会被判成"没变化"直接短路 —— 界面上范围已经变了、库里还是
  // 「仅限」，而那正是他刚刚为了救那笔死钱做的唯一一件事。
  const [initialScope, setInitialScope] =
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
      setInitialScope('universal')
      setNote('')
      return
    }
    setState('loading')
    setSelected([])
    setInitial([])
    // 范围也要跟着清空。留着上一个套餐的「仅限」，取数在途的这一段里
    // 「仅限 + 空清单」会成立，于是上一个套餐的配置在这个套餐的抽屉里
    // 闪出一条死钱警告。
    setBalanceScope('universal')
    setInitialScope('universal')
    let stale = false
    qyGetPlanEntitlement(planId)
      .then((data) => {
        if (stale) return
        setSelected(data.unlock_groups)
        setInitial(data.unlock_groups)
        setCandidates(data.model_group_candidates)
        setBalanceScope(data.balance_scope)
        setInitialScope(data.balance_scope)
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

  const resetScopeToUniversal = useCallback(
    () => setBalanceScope('universal'),
    []
  )

  const orphans = selected.filter((group) => !candidates.includes(group))
  // 判定用的是**活着的**绑定：已从倍率表消失的名字在快照里已被剔除，绑着等于
  // 没绑，把它算进来会让一个实际上花不掉的余额池通过校验。
  //
  // 只在 ready 时成立：loading / error 下这几个 state 是占位而不是真值，
  // 拿占位去判"这个套餐是不是死钱"会得到一个与库里无关的结论。
  const restrictedWithoutBinding =
    state === 'ready' &&
    balanceScope === 'restricted' &&
    !selected.some((group) => candidates.includes(group))

  const saveIfChanged = useCallback(
    async (targetPlanId: number): Promise<QyUnlockSaveOutcome> => {
      if (state !== 'ready' || targetPlanId <= 0) return 'unchanged'
      if (sameSet(selected, initial) && balanceScope === initialScope) {
        return 'unchanged'
      }
      // 危险判定放在"有没有改动"之后：一个打开时就已经是死钱的套餐（运营在
      // 别处删了那个模型分组），运营只改了标题就保存时不该收到任何指责 ——
      // 那次保存根本不碰这份绑定。只有他**亲手**把状态改成死钱时才拦。
      if (restrictedWithoutBinding) return 'blocked'
      await qySavePlanEntitlement(targetPlanId, {
        unlock_groups: selected,
        balance_scope: balanceScope,
        note,
      })
      setInitial(selected)
      setInitialScope(balanceScope)
      // 矩阵页的格子带「经套餐 P 可达」，它的数据源就是这份绑定：不失效它，
      // 运营切过去会看到一格仍然画着"不可达"，然后在看不见影响面的情况下去调
      // 那一列的兜底倍率。
      await Promise.all([
        queryClient.invalidateQueries({
          queryKey: qyKeys.adminPlanEntitlement(targetPlanId),
        }),
        queryClient.invalidateQueries({ queryKey: qyKeys.adminGroupMatrix() }),
      ])
      return 'saved'
    },
    [
      balanceScope,
      initial,
      initialScope,
      note,
      queryClient,
      restrictedWithoutBinding,
      selected,
      state,
    ]
  )

  return {
    state,
    selected,
    candidates,
    orphans,
    balanceScope,
    restrictedWithoutBinding,
    toggle,
    resetScopeToUniversal,
    saveIfChanged,
  }
}
