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

import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { cn } from '@/lib/utils'

import { QySdAmount } from '../../../components/qy-sd-amount'
import { useStardustName } from '../../../hooks/use-stardust-name'
import { formatSdWithUnit } from '../../../lib/format-sd'
import { QySdLedgerKindIcon } from '../../stardust/components/ledger-kind-icon'
import { qySdKindKey } from '../../stardust/lib/display'
import {
  QY_INVITE_SUMMARY_PARTS,
  type QyInviteSummary,
  type QyInviteSummaryPart,
} from '../types'

/**
 * 每一块的色条，顺序与 {@link QY_INVITE_SUMMARY_PARTS} 一致。
 *
 * 颜色取主题的图表色（`--chart-*`），与上游用量图表同一套：Midnight Signal
 * 只允许一支强调色，这几格靠明度阶梯区分，不另起调色板。
 */
const PART_BAR_CLASS: Readonly<Record<QyInviteSummaryPart, string>> = {
  invite_register: 'bg-chart-4',
  plan_inviter: 'bg-chart-5',
  legacy: 'bg-chart-2',
}

/** 每一块的文案键。历史那一块合成一行；图标借用下线消费返的（它占大头）。 */
function partLabelKey(part: QyInviteSummaryPart): string {
  return part === 'legacy' ? 'qy_inv_part_legacy' : qySdKindKey(part)
}

/**
 * 「星屑账本上因下线得到的」按来源的图形化分布：一条堆叠条 + 图例。
 *
 * 下线充值 / 兑换码 / 消费带来的返还只走推广佣金，不在这里；这里只有注册奖、
 * 套餐返上线，以及已下线的星屑侧下线返留下的历史（> 0 才出现那一行 —— 对没有
 * 历史的人，一行写着 0 的"已停用"只会让人以为丢了什么）。
 *
 * 条子画比例，数字留给图例（它仍然是可复制、可读屏的那一份）；每一格带
 * `role='img'` + `aria-label`。全 0 时条子换成斜纹（`.qy-art-hatch`）：空白轨道与
 * "还在加载"长得一样，而"还没返过"是一个必须一眼认出的终态。
 */
export function QyInviteKindBreakdown(props: {
  totals: QyInviteSummary['totals']
}) {
  const { t } = useTranslation()
  const unit = useStardustName()
  const totals = props.totals
  const parts = QY_INVITE_SUMMARY_PARTS.filter(
    (part) => part !== 'legacy' || (totals.legacy ?? 0) > 0
  )
  // 分母用各项之和而不是后端的 `all`：两者理应相等，但万一后端多算了一种
  // 前端还不认识的来源，按 `all` 分母会让各格加起来不满一条。
  const sum = parts.reduce(
    (acc, part) => acc + Math.max(0, totals[part] ?? 0),
    0
  )

  return (
    <Card data-card-hover='false'>
      <CardHeader>
        <CardTitle>{t('qy_inv_breakdown_title')}</CardTitle>
        <CardDescription>
          {t('qy_inv_breakdown_desc', { unit })}
        </CardDescription>
      </CardHeader>
      <CardContent className='space-y-3'>
        <div
          className={cn(
            'flex h-3 w-full overflow-hidden rounded-full',
            sum === 0 ? 'qy-art-hatch' : 'bg-muted'
          )}
          data-empty={sum === 0 ? 'true' : undefined}
        >
          {sum > 0 &&
            parts.map((kind) => {
              const value = Math.max(0, totals[kind] ?? 0)
              if (value === 0) return null
              const percent = (value / sum) * 100
              return (
                <div
                  key={kind}
                  role='img'
                  data-segment={kind}
                  aria-label={t('qy_inv_breakdown_segment', {
                    kind: t(partLabelKey(kind), { defaultValue: kind }),
                    amount: formatSdWithUnit(value, unit),
                    percent: Math.round(percent),
                  })}
                  className={cn('qy-fx-bar h-full', PART_BAR_CLASS[kind])}
                  style={{ width: `${percent}%` }}
                />
              )
            })}
        </div>
        <dl className='divide-border divide-y text-sm'>
          {parts.map((kind) => {
            const value = totals[kind] ?? 0
            return (
              <div
                key={kind}
                className='flex items-center justify-between gap-3 py-1.5 first:pt-0 last:pb-0'
              >
                <dt className='text-muted-foreground inline-flex min-w-0 items-center gap-2'>
                  <span
                    aria-hidden='true'
                    className={cn(
                      'size-2 shrink-0 rounded-full',
                      PART_BAR_CLASS[kind]
                    )}
                  />
                  <QySdLedgerKindIcon
                    kind={kind === 'legacy' ? 'invite_consume' : kind}
                    className='size-6'
                  />
                  <span className='truncate'>
                    {t(partLabelKey(kind), { defaultValue: kind })}
                  </span>
                </dt>
                <dd className='shrink-0 text-right'>
                  <QySdAmount amount={value} />
                  <span className='text-muted-foreground ml-2 text-xs tabular-nums'>
                    {sum === 0 ? '-' : `${Math.round((value / sum) * 100)}%`}
                  </span>
                </dd>
              </div>
            )
          })}
        </dl>
      </CardContent>
    </Card>
  )
}
