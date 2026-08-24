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
import { CircleAlert, CircleCheck } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'

import { qyArray } from '../../../lib/array'
import { qyLotMissingKey, qyLotMissingValues } from '../lib/display'
import type { QyLotEligibility, QyLotMissing } from '../types'

/**
 * 「我为什么不能参加」。
 *
 * 一个置灰的按钮只告诉用户"不行"，不告诉他"差在哪、差多少"。后者才是他能
 * 采取行动的信息：差 200 额度可以去充值，差 3 天注册时长只能等 —— 两种情况下
 * 用户该做的事完全不同。
 *
 * ## 它不是放行依据
 *
 * 预检与真正的扣费之间隔着一整段时间，分组、余额、封禁状态都可能变。这一块
 * 只负责"尽早说清楚"，说了算的永远是报名接口在两把行锁里的判定。所以即便这里
 * 显示"符合条件"，提交仍然可能被拒 —— 那不是矛盾，是必然。
 */
export function QyLotEligibilityCard(props: {
  eligibility: QyLotEligibility | undefined
  isLoading: boolean
}) {
  const { t } = useTranslation()

  if (props.isLoading || props.eligibility == null) return null

  const missing = qyArray(props.eligibility.missing)
  if (props.eligibility.eligible && missing.length === 0) {
    // 符合条件时只留结论那一句。原来跟着的「这是按当前数据的预检结果，最终以
    // 提交时的判定为准」是一句**不可执行**的免责声明：读到它的用户做不出任何
    // 不同的动作，而它真正生效的那一刻（提交被拒）后端会返回一条具体的错误，
    // 界面上弹的是那条错误而不是这句话。
    return (
      <Alert>
        <CircleCheck />
        <AlertTitle>{t('qy_lot_elig_ok_title')}</AlertTitle>
      </Alert>
    )
  }

  return (
    <Alert variant='destructive'>
      <CircleAlert />
      <AlertTitle>{t('qy_lot_elig_missing_title')}</AlertTitle>
      <AlertDescription>
        <ul className='mt-1 space-y-1'>
          {missing.map((item) => (
            <li key={item.code}>{describe(item, t)}</li>
          ))}
        </ul>
      </AlertDescription>
    </Alert>
  )
}

/**
 * 一条缺失项的措辞。
 *
 * `need` / `have` 的单位由 `qyLotMissingValues` 判定：额度口径的先换算成站内
 * 余额（与钱包页、日志页同一格式），天数与次数原样透传。未登记的 code 回落成
 * 一句通用文案 + 原始 code，用户至少能把它贴给客服，而不是对着一行空白。
 */
function describe(
  missing: QyLotMissing,
  t: (key: string, options?: Record<string, unknown>) => string
): string {
  const key = qyLotMissingKey(missing)
  return t(key, {
    defaultValue: t('qy_lot_miss_unknown', { code: missing.code }),
    ...qyLotMissingValues(missing),
  })
}
