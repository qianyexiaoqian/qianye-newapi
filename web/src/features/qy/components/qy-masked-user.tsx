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

import { CopyButton } from '@/components/copy-button'
import { cn } from '@/lib/utils'

type QyMaskedUserProps = {
  /** 用户 ID。0 / `null` 表示对方不可见（例如系统发起的单据）。 */
  userId?: number | null
  /** **已由后端脱敏**的用户名，如 `zh***ng`。 */
  maskedName?: string | null
  /** 是否显示复制用户 ID 的按钮。列表里逐行都显示会很吵，默认关。 */
  copyable?: boolean
  className?: string
}

/**
 * 脱敏用户展示：`#1024 · zh***ng`。
 *
 * **本组件不做脱敏，只做展示。** 真正的打码必须在后端完成
 * （`qianye/modules/commission/mask.go` 等）—— 前端遮挡在 devtools 里一览无余，
 * 那等于没遮。看到未打码的用户名，是后端的 bug，不要在这里补救。
 *
 * 永远不展示邮箱与手机号：即便接口返回了，也应该先去掉后端的返回。
 */
export function QyMaskedUser(props: QyMaskedUserProps) {
  const { t } = useTranslation()
  const hasId = props.userId != null && props.userId > 0
  const name = props.maskedName?.trim()

  if (!hasId && (name == null || name === '')) {
    return (
      <span className={cn('text-muted-foreground', props.className)}>-</span>
    )
  }

  return (
    <span
      className={cn(
        'inline-flex min-w-0 items-center gap-1.5',
        props.className
      )}
    >
      {hasId && (
        <span className='text-muted-foreground shrink-0 font-mono text-xs'>
          #{props.userId}
        </span>
      )}
      {name != null && name !== '' && (
        <span className='min-w-0 truncate'>{name}</span>
      )}
      {props.copyable === true && hasId && (
        <CopyButton
          value={String(props.userId)}
          className='size-6 shrink-0'
          iconClassName='size-3'
          tooltip={t('qy_common_copy_user_id')}
          aria-label={t('qy_common_copy_user_id')}
        />
      )}
    </span>
  )
}
