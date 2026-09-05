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
import { cn } from '@/lib/utils'

import { useStardustName } from '../hooks/use-stardust-name'
import { formatSd } from '../lib/format-sd'

type QySdInputProps = {
  /** 当前值，星屑整数。 */
  value: number
  /** 用户输入后回调，参数同样是星屑整数（空串归 0）。 */
  onChange: (amount: number) => void
  /** 允许的最小 / 最大值，仅用于提示与 `aria-invalid`；真正的校验在表单与后端。 */
  min?: number
  max?: number
  disabled?: boolean
  placeholder?: string
  className?: string
  id?: string
}

/** 输入文本 → 星屑整数。只留数字，所以小数点与负号在这里就被吃掉。 */
function parseSdText(text: string): number {
  const digits = text.replaceAll(/\D/g, '')
  if (digits === '') return 0
  const parsed = Number(digits)
  return Number.isSafeInteger(parsed) ? parsed : 0
}

/**
 * 星屑输入框：整数、无小数、无负数，右侧印单位名。
 *
 * 接口形状照 `QyAmountInput`（value / onChange 都是 number），但**不做任何换算**
 * —— 运营给星屑活动填「1」存的就是 1。旧的 `QyAmountInput` 按 USD 录入，
 * 填「1」会存成 500000，那正是本轮改造要消灭的事故。
 */
export function QySdInput(props: QySdInputProps) {
  const { t } = useTranslation()
  const unit = useStardustName()
  const hintId = useId()

  // 受控的是文本而不是数字：千分位与用户敲到一半的空串都不是一个 number。
  const [text, setText] = useState(() =>
    props.value > 0 ? String(props.value) : ''
  )

  // 外面改了 value（自动填按钮、表单 reset）时回灌，判据与 QyAmountInput 相同：
  // 只有本地文本解析回去不等于新值时才重置，用户正在敲的内容不会被打断。
  const [lastValue, setLastValue] = useState(props.value)
  if (props.value !== lastValue) {
    setLastValue(props.value)
    if (parseSdText(text) !== props.value) {
      setText(props.value > 0 ? String(props.value) : '')
    }
  }

  const handleChange = (event: ChangeEvent<HTMLInputElement>) => {
    const digits = event.target.value.replaceAll(/\D/g, '')
    setText(digits)
    props.onChange(parseSdText(digits))
  }

  const belowMin = props.min != null && props.value < props.min
  const aboveMax = props.max != null && props.max > 0 && props.value > props.max
  const hasRange = props.min != null || (props.max != null && props.max > 0)

  return (
    <div className={cn('space-y-1.5', props.className)}>
      <div className='relative'>
        <Input
          id={props.id}
          type='text'
          inputMode='numeric'
          autoComplete='off'
          value={text}
          onChange={handleChange}
          disabled={props.disabled}
          placeholder={props.placeholder ?? t('qy_sd_input_placeholder')}
          aria-invalid={belowMin || aboveMax ? true : undefined}
          aria-describedby={hintId}
          className='pr-16'
        />
        <span
          className='text-muted-foreground pointer-events-none absolute inset-y-0 right-3 flex items-center text-xs'
          aria-hidden='true'
        >
          {unit}
        </span>
      </div>
      <p id={hintId} className='text-muted-foreground text-xs'>
        {t('qy_sd_input_hint', { unit })}
      </p>
      {hasRange && (
        <p
          className={cn(
            'text-muted-foreground text-xs',
            (belowMin || aboveMax) && 'text-destructive'
          )}
        >
          {props.max != null && props.max > 0
            ? t('qy_sd_input_range', {
                min: formatSd(props.min ?? 0),
                max: formatSd(props.max),
                unit,
              })
            : t('qy_sd_input_range_min_only', {
                min: formatSd(props.min ?? 0),
                unit,
              })}
        </p>
      )}
    </div>
  )
}
