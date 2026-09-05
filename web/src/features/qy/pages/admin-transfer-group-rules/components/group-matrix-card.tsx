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
import { ChevronRight, CircleCheck } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import { cn } from '@/lib/utils'

import type { QyTransferGroupMatrixRow } from '../types'

type QyGroupMatrixCardProps = {
  matrix: QyTransferGroupMatrixRow[]
  /**
   * 行与列的取值域，由后端下发。
   *
   * **包含规则表自己引用到的分组**：只取「站点定义过的分组」时，刚给一个未定义
   * 分组配好的规则在这张表里既不成行也不成列 —— 那一行看起来就是「谁都转不了」，
   * 而实际判定是放行的。结论与判定说的话必须是同一句。
   */
  knownGroups: string[]
  /** 站点没定义过的分组名（已归一）。只打黄标，不影响任何结论。 */
  unknownGroups: Set<string>
}

/**
 * 「当前谁能转给谁」。
 *
 * 这一块是本页存在的理由。规则的形式是「策略 + 名单」，而运营真正要回答的问题
 * 是「A 组现在到底能转给谁」—— 兜底规则、`@self`、黑名单三者叠加之后，靠肉眼
 * 从规则列表推这个结论极易出错，而配错的直接后果是钱转到了不该去的地方。
 *
 * ## 为什么是结论列表而不是行×列矩阵
 *
 * 上一版画的是全量矩阵：分组一多（几十个组就是上千格 ✓/—）整卡横向滚动、
 * 什么都读不出来。而绝大多数格子说的是同一句话「不受限制」。所以现在只把
 * **受限的分组**逐行列出来，每行一句人话（只能转给谁 / 不能转给谁 / 不能发起），
 * 不受限的分组折叠成一行汇总 —— 信息量不变，噪音归零。
 *
 * 每行的结论都来自**后端用真正的判定函数**算出的 `to_groups`（`buildGroupMatrix`
 * 逐格调 `allowsGroup`），前端只做分桶与取补集，不自己从规则推。前端自己推会
 * 与后端分家，而那比噪音更危险：它会让人放心地配错。
 */
export function QyGroupMatrixCard(props: QyGroupMatrixCardProps) {
  const { t } = useTranslation()

  // 后端算出的放行名单覆盖了全部已知分组 ⇒ 这一行没有任何实际限制。
  // to_groups ⊆ knownGroups 且两边都已去重，比长度即可。
  const restricted = props.matrix.filter(
    (row) => row.to_groups.length < props.knownGroups.length
  )
  const openGroups = props.matrix
    .filter((row) => row.to_groups.length >= props.knownGroups.length)
    .map((row) => row.from_group)

  return (
    <Card data-card-hover='false'>
      <CardHeader>
        <CardTitle>{t('qy_trg_matrix_title')}</CardTitle>
        <CardDescription>{t('qy_trg_matrix_desc')}</CardDescription>
      </CardHeader>
      <CardContent className='space-y-3'>
        {restricted.length === 0 ? (
          <p className='text-muted-foreground flex items-center gap-2 text-sm'>
            <CircleCheck
              className='text-success size-4 shrink-0'
              aria-hidden='true'
            />
            {t('qy_trg_list_all_open')}
          </p>
        ) : (
          <ul className='divide-y rounded-md border'>
            {restricted.map((row) => (
              <RestrictedRow
                key={row.from_group}
                row={row}
                knownGroups={props.knownGroups}
                unknownGroups={props.unknownGroups}
              />
            ))}
          </ul>
        )}

        {restricted.length > 0 && openGroups.length > 0 && (
          <OpenGroupsFold
            groups={openGroups}
            unknownGroups={props.unknownGroups}
          />
        )}

        <p className='text-muted-foreground text-xs'>
          {t('qy_trg_matrix_note')}
        </p>
      </CardContent>
    </Card>
  )
}

/**
 * 一个受限分组的结论行。
 *
 * 白名单显示放行名单（「只能转给」），黑名单显示**补集**（「不能转给」）——
 * 黑名单通常远短于放行名单，且与规则本身的语义同向；两种说法都是对同一份
 * 后端结论的完整复述，只是挑了更短的那半边。
 */
function RestrictedRow(props: {
  row: QyTransferGroupMatrixRow
  knownGroups: string[]
  unknownGroups: Set<string>
}) {
  const { t } = useTranslation()
  const row = props.row
  const showDenied = row.policy === 'deny_list'
  const allowed = new Set(row.to_groups)
  const targets = showDenied
    ? props.knownGroups.filter((group) => !allowed.has(group))
    : row.to_groups

  return (
    <li className='flex flex-wrap items-center gap-x-2 gap-y-1.5 p-2.5 text-sm'>
      <GroupTag
        name={row.from_group}
        unknown={props.unknownGroups.has(row.from_group)}
        strong
      />
      <Badge variant={row.policy === 'deny_all' ? 'destructive' : 'outline'}>
        {t(`qy_trg_policy_${row.policy}`)}
      </Badge>
      {row.to_groups.length === 0 ? (
        <span className='text-muted-foreground'>
          {t('qy_trg_list_blocked')}
        </span>
      ) : (
        <>
          <span className='text-muted-foreground'>
            {t(showDenied ? 'qy_trg_list_not_to' : 'qy_trg_list_only_to')}
          </span>
          {targets.map((group) => (
            <GroupTag
              key={group}
              name={group}
              unknown={props.unknownGroups.has(group)}
            />
          ))}
        </>
      )}
    </li>
  )
}

/**
 * 不受限分组的折叠汇总。
 *
 * 默认收起：这一段回答的是「除了上面那些，别人怎么样」，一句带数字的汇总
 * 已经是完整答案；名单本体只在有人想核对时才需要，而 Base UI 的 Collapsible
 * 收起时不挂载面板，长名单不会一直压在页面上。
 */
function OpenGroupsFold(props: {
  groups: string[]
  unknownGroups: Set<string>
}) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)

  return (
    <Collapsible open={open} onOpenChange={setOpen}>
      <CollapsibleTrigger className='text-muted-foreground hover:text-foreground inline-flex items-center gap-1 text-xs'>
        <ChevronRight
          aria-hidden='true'
          className={cn('size-3 transition-transform', open && 'rotate-90')}
        />
        {t('qy_trg_list_open_rest', { count: props.groups.length })}
      </CollapsibleTrigger>
      <CollapsibleContent className='mt-2 flex flex-wrap gap-1'>
        {props.groups.map((group) => (
          <GroupTag
            key={group}
            name={group}
            unknown={props.unknownGroups.has(group)}
          />
        ))}
      </CollapsibleContent>
    </Collapsible>
  )
}

/**
 * 一个分组名，未定义时打黄标。
 *
 * 黄标必须配 `title` 与 `sr-only` 文字：只靠颜色区分「站点定义过 / 没定义过」，
 * 读屏用户与色觉障碍用户拿到的是一串一模一样的名字。
 */
function GroupTag(props: { name: string; unknown: boolean; strong?: boolean }) {
  const { t } = useTranslation()
  const hint = props.unknown ? t('qy_trg_unknown_group_hint') : undefined

  if (props.strong === true) {
    return (
      <span
        className={cn('font-medium', props.unknown && 'text-warning')}
        title={hint}
      >
        {props.name}
        {props.unknown && <span className='sr-only'> {hint}</span>}
      </span>
    )
  }
  return (
    <Badge
      variant={props.unknown ? 'warning' : 'secondary'}
      className='font-normal'
      title={hint}
    >
      {props.name}
      {props.unknown && <span className='sr-only'> {hint}</span>}
    </Badge>
  )
}
