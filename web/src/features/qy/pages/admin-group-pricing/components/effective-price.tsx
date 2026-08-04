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
import {
  ArrowDownRight,
  ArrowUpRight,
  Minus,
  TriangleAlert,
} from 'lucide-react'
import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'
import { cn } from '@/lib/utils'

import { QyAmountText } from '../../../components/qy-amount-text'
import { QY_EMPTY_TEXT } from '../../ops/format'
import {
  qyCompareDecimalStrings,
  qyFormatDeltaPercent,
  type QyPriceDirection,
} from '../lib/pricing-math'
import type { QyGpEffective, QyGpUserGroupEffective } from '../types'
import { QyGpEffectiveByUserGroup } from './effective-by-user-group'

/**
 * 「分组级价 × 分组倍率 = 实际扣费」的展示。
 *
 * 用户拍板了相乘方案，我提出的附加条件是录入时必须实时看到折算结果 ——
 * 这个文件就是那个条件，不是装饰。三条行为不能省：
 *
 *  1. **把乘法写出来**，而不是只给一个结果数字。运营要能自己复核这一步。
 *  2. **同时给「改前」**（全局价 × 同一个分组倍率）。只看改后无法回答
 *     「这次调价是涨还是跌、涨多少」，而那才是运营真正要决定的事。
 *  3. **数字全部来自后端 `Effective`**。前端不重算一遍相乘：两处实现只要有
 *     一处漏乘分组倍率，管理端显示的价就与实际扣费不一致。
 */

const DIRECTION_ICON = {
  down: ArrowDownRight,
  flat: Minus,
  up: ArrowUpRight,
} as const

/**
 * 涨跌配色：涨用 destructive、跌用 success。
 *
 * 这不是「涨=坏」的价值判断，而是站在**用户被扣钱**的视角 —— 这一页改的是
 * 别人账户里的余额，让改价的人始终从被扣费一方的角度看数字。
 */
const DIRECTION_CLASS: Record<QyPriceDirection, string> = {
  down: 'text-success',
  flat: 'text-muted-foreground',
  up: 'text-destructive',
}

/** 改前 → 改后的方向。后端不直接给方向，但给了两侧的最终生效价。 */
function directionOf(effective: QyGpEffective): QyPriceDirection | null {
  return qyCompareDecimalStrings(
    effective.global_effective,
    effective.rule_effective
  )
}

type QyGpEffectivePanelProps = {
  effective: QyGpEffective
  /** 逐个用户分组的展开。缺省时只显示头条那一份折算。 */
  byUserGroup?: QyGpUserGroupEffective[]
  groupName: string
  modelName: string
  /** true 时在结果旁标注「当前不生效，仅记录」。 */
  shadowMode: boolean
  className?: string
}

/** 录入面板里的完整折算区。 */
export function QyGpEffectivePanel(props: QyGpEffectivePanelProps) {
  const { t } = useTranslation()
  const effective = props.effective
  const direction = directionOf(effective)

  return (
    <div className={cn('space-y-3 rounded-lg border p-3', props.className)}>
      <div className='flex flex-wrap items-center justify-between gap-2'>
        <span className='text-sm font-medium'>{t('qy_gp_preview_title')}</span>
        {props.shadowMode && (
          <Badge variant='outline' className='text-warning border-warning/50'>
            {t('qy_gp_mode_shadow_badge')}
          </Badge>
        )}
      </div>

      {/* 口径必须写在数字上面，不是脚注。
          「这个数是站在谁的角度算的」决定了下面每一个数字的含义；
          放在结果下方等于让人先读完再回头修正理解。 */}
      <QyGpScopeLine effective={effective} />

      <div className='space-y-1.5 text-sm'>
        <QyGpFormulaRow
          label={t('qy_gp_preview_before')}
          base={effective.global_value}
          ratio={effective.group_ratio}
          result={effective.global_effective}
        />
        <QyGpFormulaRow
          label={t('qy_gp_preview_after')}
          base={effective.rule_value}
          ratio={effective.group_ratio}
          result={effective.rule_effective}
          emphasis
          trailing={
            direction == null ? null : (
              <QyGpDeltaBadge
                direction={direction}
                percent={effective.delta_percent}
              />
            )
          }
        />
      </div>

      <p className='text-muted-foreground text-xs'>
        {t('qy_gp_preview_hint', {
          group: props.groupName === '' ? QY_EMPTY_TEXT : props.groupName,
          model: props.modelName === '' ? QY_EMPTY_TEXT : props.modelName,
          unit: effective.unit,
        })}
      </p>

      {/* 按次口径额外给一个 quota：运营看的是余额数字，美元是他们心里的换算，
          quota 才是账面上真正减少的量。 */}
      {effective.quota_per_call != null && (
        <p className='text-muted-foreground text-xs'>
          {t('qy_gp_preview_quota_per_call')}
          <QyAmountText
            quota={effective.quota_per_call}
            className='text-foreground ms-1 font-medium'
          />
        </p>
      )}

      {/* 各用户分组的展开。这块曾经是一条免责声明（「真正计费时可能走另一个
          倍率，本面板不覆盖」）—— 那是在用文字掩盖一个错的数字。现在后端按
          (用户分组, 模型分组) 算，把那一组值原样摊开，声明就不需要了。 */}
      {props.byUserGroup != null && (
        <QyGpEffectiveByUserGroup
          rows={props.byUserGroup}
          highlight={effective.user_group}
        />
      )}

      {effective.global_effective == null && (
        <QyGpNote tone='warning' text={t('qy_gp_preview_no_global')} />
      )}
      {/* 后端的 warning 是「这条规则配了也不会生效」的唯一来源（它读得到模型
          当前的全局计费口径，前端读不到）。原文透出，不做二次转译。 */}
      {effective.warning != null && effective.warning !== '' && (
        <QyGpNote tone='danger' text={effective.warning} />
      )}
      {/* ratio_warning 说的是另一件事：这个倍率数字本身可不可信
          （模型分组不在倍率表里 → 上游按 1.0 静默计费；交叉格 → Task 差额追扣）。
          与 warning 分开显示，运营才知道该去改规则还是去改分组倍率表。 */}
      {effective.ratio_warning != null && effective.ratio_warning !== '' && (
        <QyGpNote tone='danger' text={effective.ratio_warning} />
      )}
    </div>
  )
}

/**
 * 折算口径行：这个数是站在谁的角度算的、倍率从哪来、对不对所有人成立。
 *
 * 三件事缺一不可：
 *
 *  1. **口径**。`user_group` 为空 = 兜底口径，必须原样说出来。一个只对
 *     「没配专属倍率的用户分组」成立的数字，不许继续叫「最终价」。
 *  2. **来源**。倍率是继承兜底还是命中了专属倍率 —— 运营看到 0 时要能一眼
 *     分清「我配的 0」和「兜底就是 0」。
 *  3. **跨度**。`uniform === false` 意味着头条那个数对一部分用户不成立，
 *     必须当场标出区间，而不是等人自己去翻明细。
 */
function QyGpScopeLine(props: { effective: QyGpEffective }) {
  const { t } = useTranslation()
  const effective = props.effective
  const baseOnly = effective.user_group === ''

  return (
    <div className='flex flex-wrap items-center gap-1.5 text-xs'>
      <Badge variant='outline' className='font-normal'>
        {baseOnly
          ? t('qy_group_pricing_effective_base_only')
          : t('qy_gp_effective_for_user_group', {
              group: effective.user_group,
            })}
      </Badge>
      <Badge
        variant='outline'
        className={cn(
          'font-normal',
          effective.ratio_source === 'override' &&
            'border-primary/50 text-primary'
        )}
      >
        {t(`qy_gp_ratio_source_${effective.ratio_source}`)}
      </Badge>
      {!effective.uniform && (
        <Badge
          variant='outline'
          className='text-warning border-warning/50 font-normal'
        >
          {t('qy_group_pricing_effective_range', {
            min: effective.group_ratio_min,
            max: effective.group_ratio_max,
          })}
        </Badge>
      )}
    </div>
  )
}

type QyGpFormulaRowProps = {
  label: string
  base: string | null | undefined
  ratio: string
  result: string | null | undefined
  emphasis?: boolean
  trailing?: ReactNode
}

/** 一行乘法：`基准值 × 分组倍率 = 实际扣费`。 */
function QyGpFormulaRow(props: QyGpFormulaRowProps) {
  const { t } = useTranslation()
  const missing = props.base == null || props.base === ''

  return (
    <div className='flex flex-wrap items-center gap-x-2 gap-y-1'>
      <span className='text-muted-foreground w-20 shrink-0 text-xs'>
        {props.label}
      </span>
      <span className='tabular-nums'>
        {missing ? QY_EMPTY_TEXT : props.base}
      </span>
      <span className='text-muted-foreground'>×</span>
      <span className='tabular-nums'>{props.ratio}</span>
      <span className='text-muted-foreground'>=</span>
      {props.result == null || props.result === '' ? (
        <span className='text-muted-foreground'>
          {t('qy_gp_preview_uncomputable')}
        </span>
      ) : (
        <span
          className={cn(
            'tabular-nums',
            props.emphasis === true && 'text-base font-semibold'
          )}
        >
          {props.result}
        </span>
      )}
      {props.trailing}
    </div>
  )
}

type QyGpDeltaBadgeProps = {
  direction: QyPriceDirection
  /** 后端算好的涨跌幅（形如 `-25.00`）。缺省时只显示方向文案。 */
  percent?: string
  className?: string
}

/** 涨跌徽章。表格与录入面板共用，两处口径必须一致。 */
function QyGpDeltaBadge(props: QyGpDeltaBadgeProps) {
  const { t } = useTranslation()
  const Icon = DIRECTION_ICON[props.direction]

  let text = qyFormatDeltaPercent(props.percent)
  if (props.direction === 'flat') text = t('qy_gp_preview_flat')
  if (text === '') text = t(`qy_gp_preview_dir_${props.direction}`)

  return (
    <span
      className={cn(
        'inline-flex items-center gap-0.5 text-xs font-medium tabular-nums',
        DIRECTION_CLASS[props.direction],
        props.className
      )}
    >
      <Icon className='size-3.5 shrink-0' aria-hidden='true' />
      {text}
    </span>
  )
}

type QyGpEffectiveCellProps = {
  effective: QyGpEffective
  shadowMode: boolean
  /** 规则未启用。折算结果照显示，但要标出它现在不算数。 */
  enabled: boolean
}

/**
 * 表格里的「实际扣费」单元格。
 *
 * 与录入面板读的是同一个 `Effective`，因此列表和表单永远不会给出两个不同的
 * 最终价。
 *
 * **列表里的这个数是兜底口径**（后端按 `user_group === ''` 折算）。
 * `uniform === false` 时它对配了专属倍率的用户不成立，必须当场标出来 ——
 * 让一个只对一部分人成立的数字继续冒充「最终价」，正是本轮要修的缺陷。
 */
export function QyGpEffectiveCell(props: QyGpEffectiveCellProps) {
  const { t } = useTranslation()
  const effective = props.effective
  const direction = directionOf(effective)

  return (
    <span className='flex flex-col gap-0.5'>
      <span className='flex items-center gap-1.5'>
        <span className='font-semibold tabular-nums'>
          {effective.rule_effective}
        </span>
        <span className='text-muted-foreground text-[11px]'>
          {effective.unit}
        </span>
        {direction != null && (
          <QyGpDeltaBadge
            direction={direction}
            percent={effective.delta_percent}
          />
        )}
      </span>
      {!effective.uniform && (
        <span className='text-warning text-[11px]'>
          {t('qy_group_pricing_effective_range', {
            min: effective.group_ratio_min,
            max: effective.group_ratio_max,
          })}
        </span>
      )}
      {/* 这两行回答的是同一个问题的两半：这个数字现在算不算数。
          未启用 = 这条规则不参与；影子模式 = 全部规则都不参与实际扣费。 */}
      {!props.enabled && (
        <span className='text-muted-foreground text-[11px]'>
          {t('qy_gp_effective_rule_off')}
        </span>
      )}
      {props.enabled && props.shadowMode && (
        <span className='text-warning text-[11px]'>
          {t('qy_gp_mode_shadow_badge')}
        </span>
      )}
    </span>
  )
}

type QyGpNoteProps = {
  tone: 'danger' | 'warning'
  text: string
}

/** 折算前提 / 不生效告警的提示条。 */
function QyGpNote(props: QyGpNoteProps) {
  return (
    <p
      className={cn(
        'flex items-start gap-1.5 text-xs',
        props.tone === 'danger' ? 'text-destructive' : 'text-warning'
      )}
    >
      <TriangleAlert className='mt-0.5 size-3.5 shrink-0' aria-hidden='true' />
      <span>{props.text}</span>
    </p>
  )
}
