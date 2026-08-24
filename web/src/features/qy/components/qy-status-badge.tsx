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
import { useTranslation } from 'react-i18next'

import { StatusBadge } from '@/components/status-badge'

import { getQyStatusStyle } from '../lib/status'
import type { QyStatus } from '../lib/types'

type QyStatusBadgeProps = {
  status: QyStatus | null | undefined
  size?: 'lg' | 'md' | 'sm'
  className?: string
  /**
   * 覆盖徽章上的文字，**颜色仍由 `status` 决定**。
   *
   * 只有一种正当用法：同一枚徽章上，颜色与文字来自同一件事的两个字段。抽奖活动
   * 的结局就是这样 —— 颜色由 `outcome` 决定（开出奖=绿、取消=中性、流局=紫），
   * 而文字也该是那个结局（「人数不足流局(全额退款)」）。此前两者是两枚并排的
   * 徽章，于是一场取消的活动上写着「已取消 已取消(全额退款)」，一场流局的活动
   * 上写着「已冲正 人数不足流局(全额退款)」。
   *
   * **不要**用它把一个状态说成另一个状态。颜色与文字一旦各说各话，全站统一
   * 徽章这件事就没有意义了。
   */
  label?: string
}

/**
 * qy 全站统一的状态徽章。
 *
 * 所有 qy 页面必须用它渲染单据状态，禁止各页自己挑颜色 —— 同一个 `pending`
 * 在划转页是黄色、在提现页是灰色，用户会以为是两种东西。
 *
 * 未知状态回落成中性徽章并原样显示字符串，绝不崩溃：后端新增枚举值时
 * 前端最多"不好看"，不能白屏。
 */
export function QyStatusBadge(props: QyStatusBadgeProps) {
  const { t } = useTranslation()
  const style = getQyStatusStyle(props.status)
  const label =
    props.label ??
    (style.labelKey === '' ? (props.status ?? '-') : t(style.labelKey))

  return (
    <StatusBadge
      label={String(label)}
      variant={style.variant}
      pulse={style.pulse}
      size={props.size ?? 'sm'}
      // 状态文案没有复制价值，开着只会让整行出现 cursor: copy。
      copyable={false}
      className={props.className}
    />
  )
}
