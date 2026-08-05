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
import { Info, ShieldAlert } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'

import type { QyUsdScale } from '../lib/quota-usd'

/**
 * 配置页顶部的换算率说明。划转 / 佣金 / 抽奖三个配置页共用。
 *
 * # 为什么这一条必须显示
 *
 * 换算率（`quotaPerUnit`）是站点级配置，运营改得动。改完之后这些页面上每一个
 * 已存门槛的**语义**都变了 —— 同一个 500000 额度从 $1 变成别的 —— 而存储里
 * 那个整数一动没动，界面也不会有任何提示。不把这个前提摆在眼前，USD 显示
 * 就成了一个会悄悄改变含义的数字，比原来那串大额整数更危险。
 *
 * 换算率无法无损表示时（见 `lib/quota-usd.ts`），这里换成红色告警并说明
 * 页面已退回额度单位录入 —— 否则运营会以为界面坏了。
 */
export function QyUsdScaleNotice(props: { scale: QyUsdScale }) {
  const { t } = useTranslation()

  if (!props.scale.usable) {
    // 播出去的换算率一律用 configuredRate（站点实际配置的原值），不是
    // quotaPerUnit —— 后者在这条分支上已经被归零，拿它去填就会得到
    // "站点配置的是 1 USD = 0 额度"，而运营在系统设置里看到的是 2.5。
    // 整页唯一一处必须说真话的汇率数字，不能在这里说假话。
    const rate = props.scale.configuredRate
    return (
      <Alert variant='destructive'>
        <ShieldAlert />
        <AlertTitle>{t('qy_cfg_usd_unavailable_title')}</AlertTitle>
        <AlertDescription>
          {rate == null
            ? // 站点根本没有配出一个正数。此时"这个换算率没法无损表示"是句
              // 答非所问的话 —— 该说的是"没有换算率可用"。
              t('qy_cfg_usd_unavailable_no_rate')
            : t('qy_cfg_usd_unavailable_desc', { rate })}
        </AlertDescription>
      </Alert>
    )
  }

  return (
    <Alert>
      <Info />
      <AlertTitle>{t('qy_cfg_usd_rate_title')}</AlertTitle>
      <AlertDescription>
        {t('qy_cfg_usd_rate_desc', { rate: props.scale.quotaPerUnit })}
      </AlertDescription>
    </Alert>
  )
}
