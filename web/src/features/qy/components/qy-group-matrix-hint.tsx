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
import { Link } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'

import { useQyConfig } from '../hooks/use-qy-config'

/** 这条提示挂在上游哪一个字段上。两个字段说的不是同一件事，不能共用一句话。 */
export type QyGroupMatrixHintField =
  /** `GroupGroupRatio` —— 矩阵页编辑的就是这一份数据。 */
  | 'inter_group_ratio'
  /** `GroupSpecialUsableGroup` —— 对已接管的用户分组整体失效。 */
  | 'special_usable_rules'

/**
 * 上游「系统设置 → 分组倍率」页里的一行指路提示。
 *
 * ── 为什么上游那一页只加这一个组件、不做别的改动 ──
 *
 * 那两个 JSON 编辑框现在有了第二个编辑入口（矩阵页），而其中一个
 * （`GroupSpecialUsableGroup`）对已接管的用户分组**整体失效**。不说的话，
 * 运营会在这里改一条 `+:vip` 然后发现毫无效果 —— 一个配置项被静默架空、
 * 界面上没有任何信号，正是这个仓库反复栽的那个形状。
 *
 * 但也**不锁死这两个框**：扩展关掉之后运营必须还能在原地改倍率，那是整套方案
 * 的回退能力所在。所以这里只提供信息，不改变上游任何行为。
 *
 * ── 零痕迹 ──
 * 扩展未启用时返回 `null`，上游那一页与本功能出现之前逐位一致。判定走
 * {@link useQyConfig} 的三态：`unknown`（取数中 / 503）时**不渲染**，
 * 宁可少说一句，也不要在扩展其实没开的站点上指一条 404 的路。
 */
export function QyGroupMatrixHint(props: { field: QyGroupMatrixHintField }) {
  const { t } = useTranslation()
  const config = useQyConfig()

  // 判据是**本模块的开关**，不是整个扩展的三态。
  //
  // 只看扩展是否启用时，一个启用千夜但按默认 `group_matrix.enabled=false` 的站点
  // 会在上游那一页上读到「对已被接管的用户分组，下面这些 +: / -: 规则整体不再
  // 生效」—— 事实上一条都没被接管、规则全部照常生效。管理员据此以为自己的
  // `-:xxx` 已失效而不去修它，点进链接则是 guard 返回 404 后的中性空态。
  // 这条改动落在上游文件里，误导也就发生在上游页面上。
  if (config.status !== 'enabled') return null
  if (!config.features.group_matrix) return null

  // 两句文案写成字面量而不是查表：`lib/__tests__/i18n-key-coverage.test.ts`
  // 只扫 `t('…')` 这一种形式，查表得到的键它看不见，会被当成「零引用」删掉，
  // 而删掉的直接后果是上游那一页上出现一行裸键名。
  return (
    <p className='text-muted-foreground mt-1 text-xs'>
      {props.field === 'inter_group_ratio'
        ? t('qy_group_matrix_upstream_ratio_note')
        : t('qy_group_matrix_upstream_rules_note')}{' '}
      <Link
        to='/qy/admin/group-matrix'
        className='text-primary underline underline-offset-2'
      >
        {t('qy_group_matrix_title')}
      </Link>
    </p>
  )
}
