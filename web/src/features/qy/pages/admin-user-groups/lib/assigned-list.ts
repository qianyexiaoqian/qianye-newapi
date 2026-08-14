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
/**
 * 「这一档能用哪些模型分组」这份**列表**的纯逻辑层。
 *
 * 界面上只剩两块：已分配的一个列表，加一个从尚未分配里挑的入口。而"这一档
 * 有没有自己的清单"（`qy_group_scopes` 有没有行）不再是一个要运营先理解、
 * 先打开的开关 —— 它由列表内容隐式表达。这一层负责把那个内部状态翻译成
 * 一份列表，三条规则各自都是"错了不会有任何信号"的形状：
 *
 *  1. **没有自己的清单时，列表读的是 `model_groups` 而不是 `granted`。**
 *     撤销接管只删 scope 行、`qy_group_grants` 刻意保留，所以一档没有清单的
 *     分组身上完全可能挂着一批上一次配的 grant 行。照 `granted` 画，列表显示
 *     的是一份**谁也没在用**的旧清单，而运营会照着它判断"这一档现在能用什么"。
 *     后端下发的 `model_groups` 在这一支取的是上游 `GetUserUsableGroups` 的
 *     实际结果 —— 那才是这一档人此刻真的能用的东西。
 *
 *  2. **仅经套餐可达的模型分组必须留在列表里。** 它不在清单内（`granted` 为
 *     假），但买了对应套餐的用户照样能用它，且**那一格的倍率正在扣钱**。
 *     按"清单成员"过滤掉它，运营就再也改不到那个价，而站内没有任何一页显示过
 *     这一档人能到达它。所以它进列表，只是标成另一种来源、并且不给"移除"
 *     （范围里本来就没有它，移无可移）。
 *
 *  3. **列表只走列轴。** 清单里可以引用一个已经从 `options.GroupRatio` 消失
 *     的模型分组；那种项已经不可达，列出来只会让人以为还能用。后端另有一条
 *     「引用了已消失的模型分组」告警负责说这件事。
 */

import {
  qyGmCellKey,
  qyGmGrantedOf,
  type QyGmDraft,
} from '../../admin-group-matrix/lib/draft'
import type {
  QyGmCell,
  QyGmMatrixResponse,
  QyGmModelGroup,
  QyGmUserGroup,
} from '../../admin-group-matrix/types'

/**
 * 一项为什么出现在列表里。
 *
 *  - `scope`    这一档自己的清单里有它（可移除）。
 *  - `fallback` 这一档还没有自己的清单，它来自全局「用户可选分组」（移除它 =
 *               先为这一档建立独立清单）。
 *  - `plan`     清单里没有它，但某个套餐解锁了它。**倍率照样在扣钱**。
 */
export type QyUgListOrigin = 'fallback' | 'plan' | 'scope'

export type QyUgListItem = {
  name: string
  /** 该模型分组的兜底倍率（十进制字符串）。继承格显示它，但不预填。 */
  baseRatio: string
  hasChannels: boolean
  origin: QyUgListOrigin
  /** 与用户分组同名的那一列 —— 删掉它并不会让分组为空的令牌发不出请求。 */
  selfEdge: boolean
  /** 让这一项可达的套餐名（`origin === 'plan'` 时才有值）。 */
  planTitles: string[]
}

/** 一个候选项：模型分组本身，外加「它已经被某个套餐解锁了」这条注记。 */
export type QyUgAddable = {
  column: QyGmModelGroup
  /** 非空 = 已有套餐解锁了它（此时该分组对买了套餐的人本就可用，倍率照样在扣钱）。 */
  planTitles: string[]
}

export type QyUgListView = {
  /** 列表本身，按列轴顺序。**只含这一档真的授权过的**，不含套餐可达。 */
  items: QyUgListItem[]
  /** 「添加模型分组」入口里能挑的那些（这一档清单里还没有的）。 */
  addable: QyUgAddable[]
  /** 这一档有没有自己的清单。 */
  scoped: boolean
  /**
   * 清单里此刻有几项 —— **不含仅经套餐可达的那些**。
   *
   * 「移除这一项之后就一个都不剩了」这个判据只能用它：把套餐可达的项算进来，
   * 清空清单的那一次点击就不会弹出那个二选一的确认，而"回落全局清单"与
   * 「一个都不给用」的两个错误方向分别是整档全放行和整档 403。
   */
  memberCount: number
}

/**
 * 把矩阵响应 + 本地草稿投影成这一档的可用模型分组列表。
 *
 * `draft` 只在**已有自己的清单**时参与判定：没有清单的那一档写不进 grant，
 * 后端会拒绝，让草稿影响显示只会让界面先变成一个保存不了的样子。
 */
export function qyUgListView(
  data: QyGmMatrixResponse,
  serverCells: ReadonlyMap<string, QyGmCell>,
  draft: QyGmDraft,
  userGroup: QyGmUserGroup
): QyUgListView {
  const scoped = userGroup.scope_state !== 'unset'
  const fallback = new Set(userGroup.model_groups)

  const items: QyUgListItem[] = []
  const addable: QyUgAddable[] = []
  let memberCount = 0

  for (const column of data.model_groups) {
    const key = qyGmCellKey(userGroup.name, column.name)
    const cell = serverCells.get(key)
    const member = scoped
      ? qyGmGrantedOf(cell, draft.get(key))
      : fallback.has(column.name)

    if (member) {
      memberCount += 1
      items.push({
        name: column.name,
        baseRatio: column.base_ratio,
        hasChannels: column.has_channels,
        origin: scoped ? 'scope' : 'fallback',
        selfEdge: column.name === userGroup.name,
        planTitles: [],
      })
      continue
    }

    /*
      ── 套餐可达的分组**不进这张清单** ──

      它此前被 push 进 items,于是「这一档能用的模型分组」里混着一批
      **这一档根本没有授权**的行。项目方原话:「为何套餐可达的分组会直接插入在
      用户分组列内,不要这样」。

      混进来是错的,理由不只是观感:这张表回答的是"我给这一档配了什么",而套餐
      解锁挂在**人**身上、与用户分组无关(见 planentitlement 的「不绑定用户组」)。
      两者并排会让运营以为撤销那一行就能收回权限 —— 撤不回,那一行根本不是他配的。

      它仍然可以被添加(那是一次真实的授权),所以照旧落进 addable;
      「已经被某个套餐解锁了」这件事在候选项那一侧标注,不再占用清单的行。
    */
    addable.push({ column, planTitles: cell?.plan_titles ?? [] })
  }

  return { items, addable, scoped, memberCount }
}
