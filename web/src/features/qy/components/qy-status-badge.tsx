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
    style.labelKey === '' ? (props.status ?? '-') : t(style.labelKey)

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
