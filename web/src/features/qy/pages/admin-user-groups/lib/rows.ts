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
 * 主从式「用户分组」页的纯逻辑层。
 *
 * 矩阵形态里没有「当前选中哪一行」这个状态，主从式有 —— 而这一个多出来的状态
 * 恰好带来两条会静默出错的规则，所以它们抽在这里并各自有测试：
 *
 *   1. **选中项必须跟着服务端清单走。** 分组不是实体表，只是 `options.GroupRatio`
 *      的键；另一个管理员在「模型分组定价」里删掉一行，这一页下一次回读就会少
 *      一个分组。选中项若还停在那个名字上，右侧会渲染一个不存在的分组，运营在
 *      上面配的每一格都会在保存时被后端拒绝，而界面上看不出原因。
 *   2. **筛选必须与后端同一套归一口径。** 后端 `qianye/groupname` 的 `Normalize`
 *      是「去两侧空白 + 折叠大小写」，前端搜索框若按原文比对，运营输入 `VIP`
 *      会搜不到 `vip` 这一档，然后得出「这个分组不存在」的结论。
 */

import { qyNormalizeGroupName } from '../../../lib/group-options'
import {
  qyGmCellKey,
  qyGmGrantedOf,
  type QyGmDraft,
} from '../../admin-group-matrix/lib/draft'
import type {
  QyGmCell,
  QyGmMatrixResponse,
  QyGmUserGroup,
} from '../../admin-group-matrix/types'

/**
 * 左列表的筛选。
 *
 * 归一后做子串匹配，口径与 {@link qyNormalizeGroupName}（= 后端 `Normalize`）
 * 一致。空查询返回原数组本身而不是拷贝：这是最常见的一支，没必要每次输入
 * 都造一份新数组让整张列表重渲染。
 */
export function qyUgFilterGroups(
  groups: readonly QyGmUserGroup[],
  query: string
): readonly QyGmUserGroup[] {
  const needle = qyNormalizeGroupName(query)
  if (needle === '') return groups
  return groups.filter((group) =>
    qyNormalizeGroupName(group.name).includes(needle)
  )
}

/**
 * 当前应当选中哪一个用户分组。
 *
 * **筛选结果不参与判定**：输入一个搜不到任何东西的词时，右侧详情必须继续显示
 * 上一个分组，而不是跳去别处或整块变空 —— 运营在右侧填了半格倍率再去左边搜
 * 另一个分组名，选中项一跳，那半格草稿就落在了一个他没在看的分组上。
 *
 * `current` 已经不在清单里（被别的管理员删了、或首次进入还没选）时回落到第一项；
 * 一个分组都没有时返回 `null`，由调用方渲染空态。
 */
export function qyUgResolveSelected(
  groups: readonly QyGmUserGroup[],
  current: string | null
): string | null {
  if (current != null && groups.some((group) => group.name === current)) {
    return current
  }
  return groups[0]?.name ?? null
}

/**
 * 某个用户分组此刻（含未保存草稿）在范围内的模型分组数。
 *
 * 左列表的「已设定范围 · N 个」徽章与右侧「范围是空的」警告共用它，两处必须
 * 是同一个数：左边写 3、右边说「一个都没勾」，运营会以为是页面坏了而去重配。
 *
 * 只数**列轴上存在**的模型分组。清单里引用了一个已从分组倍率表删掉的分组时，
 * 那一项不该算进这个数 —— 它已经不可达了，把它算进来会让一个实际为空的范围
 * 显示成「已设定范围 · 1 个」，而那正是后端另发一条「引用了已消失的模型分组」
 * 告警要说的事。
 */
export function qyUgGrantedCount(
  data: QyGmMatrixResponse,
  serverCells: ReadonlyMap<string, QyGmCell>,
  draft: QyGmDraft,
  userGroup: string
): number {
  return qyUgGrantedModelGroups(data, serverCells, draft, userGroup).length
}

/**
 * 某个用户分组此刻（含未保存草稿）在范围内的**模型分组名**，按列轴顺序。
 *
 * ── 为什么「用户分组」表那一列必须是名字而不是个数 ──
 *
 * 项目方原话：「前端这个列：【可用模型分组】直接把模型分组名称显示上去，
 * 如：免费の渠道、浅夜の梦专属号池」。一个数字回答不了运营在那张表上唯一
 * 想问的问题 —— 「这一档人到底能用到哪几个池子」。要知道答案就得点进配置页，
 * 而那正是需求 4 要消掉的那次跳转。
 *
 * 取值与 {@link qyUgGrantedCount} **必须同源**（后者现在就是它的长度），
 * 否则会出现「列表写 3 个、展开只列出 2 个」这种自相矛盾，而运营对着矛盾的
 * 数字最可能的动作是重配一遍 —— 重配的动作恰好是撤销与改价。
 *
 * 只取**列轴上存在**的模型分组：清单里引用了一个已从分组倍率表删掉的分组时，
 * 它已经不可达，列出来只会让人以为那一档还能用它。
 */
export function qyUgGrantedModelGroups(
  data: QyGmMatrixResponse,
  serverCells: ReadonlyMap<string, QyGmCell>,
  draft: QyGmDraft,
  userGroup: string
): string[] {
  const names: string[] = []
  for (const column of data.model_groups) {
    const key = qyGmCellKey(userGroup, column.name)
    if (qyGmGrantedOf(serverCells.get(key), draft.get(key))) {
      names.push(column.name)
    }
  }
  return names
}
