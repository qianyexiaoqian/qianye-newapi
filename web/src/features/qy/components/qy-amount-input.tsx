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
import { useId, useState, type ChangeEvent } from 'react'
import { useTranslation } from 'react-i18next'

import { Input } from '@/components/ui/input'
import { getCurrencyLabel } from '@/lib/currency'
import { cn } from '@/lib/utils'

import {
  formatQyQuotaLedger,
  parseQyQuota,
  qyQuotaToDisplayAmount,
} from '../lib/format'

type QyAmountInputProps = {
  /** 当前值，单位是 quota 整数（不是展示币种金额）。 */
  value: number
  /** 用户输入后回调，参数同样是换算后的 quota 整数。 */
  onChange: (quota: number) => void
  /** 允许的最小 / 最大 quota，仅用于提示；真正的校验必须在表单 schema 与后端。 */
  minQuota?: number
  maxQuota?: number
  disabled?: boolean
  placeholder?: string
  className?: string
  id?: string
}

/**
 * 额度输入框。
 *
 * 用户按**当前展示币种**输入（USD / CNY / Tokens 由站点配置决定），组件负责
 * 折算成 quota 整数回调给表单。
 *
 * 输入框下方必须实时回显换算结果，这不是锦上添花：CNY 模式下
 * `parseQuotaFromDollars` 内部会 `Math.round(usd * quotaPerUnit)`，
 * 用户输 ¥1.005 与 ¥1.006 可能得到同一个 quota。不回显的话，
 * 用户会在事后发现"我明明填的是另一个数"。
 */
export function QyAmountInput(props: QyAmountInputProps) {
  const { t } = useTranslation()
  const hintId = useId()

  // 受控的是文本而不是数字：用户输到一半的 "1." / "0.0" 都不是合法数字，
  // 直接把 value 反格式化回输入框会让光标乱跳、小数点被吃掉。
  const [text, setText] = useState(() =>
    props.value > 0 ? String(qyQuotaToDisplayAmount(props.value)) : ''
  )

  const handleChange = (event: ChangeEvent<HTMLInputElement>) => {
    const next = event.target.value
    setText(next)
    const parsed = Number(next)
    // 空串、非数字、Infinity（`Number('1e999')`）一律归零，交给表单校验报错。
    props.onChange(next.trim() === '' ? 0 : parseQyQuota(parsed))
  }

  const showHint = props.value > 0

  return (
    <div className={cn('space-y-1.5', props.className)}>
      <div className='relative'>
        <Input
          id={props.id}
          type='text'
          inputMode='decimal'
          autoComplete='off'
          value={text}
          onChange={handleChange}
          disabled={props.disabled}
          placeholder={props.placeholder ?? t('qy_common_amount_placeholder')}
          aria-describedby={hintId}
          className='pr-16'
        />
        <span
          className='text-muted-foreground pointer-events-none absolute inset-y-0 right-3 flex items-center text-xs'
          aria-hidden='true'
        >
          {getCurrencyLabel()}
        </span>
      </div>
      <p id={hintId} className='text-muted-foreground text-xs'>
        {showHint
          ? t('qy_common_amount_converted', {
              quota: props.value,
              amount: formatQyQuotaLedger(props.value),
            })
          : t('qy_common_amount_hint')}
      </p>
      {(props.minQuota != null || props.maxQuota != null) && (
        <p className='text-muted-foreground text-xs'>
          {t('qy_common_amount_range', {
            min: formatQyQuotaLedger(props.minQuota ?? 0),
            max:
              props.maxQuota != null && props.maxQuota > 0
                ? formatQyQuotaLedger(props.maxQuota)
                : t('qy_common_unlimited'),
          })}
        </p>
      )}
    </div>
  )
}
