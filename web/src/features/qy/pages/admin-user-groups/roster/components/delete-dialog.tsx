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
import { AlertTriangle } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

import { QyResponsiveDialog } from '../../../../components/qy-responsive-dialog'
import { qyUgrDeleteBlock, qyUgrGroupChanges } from '../lib/gates'
import type { QyUgrImpact } from '../types'

/**
 * 删除一个用户分组的确认弹窗 —— 需求 1 的本体。
 *
 * ── 项目方原话 ──
 *
 * 「用户分组删除时,若存在用户,弹出选择窗口,这批用户要迁移到哪个用户分组?」
 *
 * ── 为什么这里不是一个「确定要删除吗?」 ──
 *
 * 删掉一个用户分组同时是一次**批量改价**和一次**批量权限变更**,而这两件事
 * 在「删除分组」这句话里一个字都没提到。迁过去之后:
 *
 *   · 少掉的每一个模型分组 = 那批人手上对应令牌的当场 403;
 *   · 多出来的每一格倍率差 = 从下一秒开始的账单变化。
 *
 * 所以这个弹窗把四件事摆在同一屏上,而不是分两次请求、分两个页面:
 *
 *   人数 / 令牌数    项目方点名的两个数字。它们同时是后端的一致性闸门 ——
 *                    回传的人数与此刻不符会返回 409,而不是默默按新数字删。
 *   迁移目标         有人时**必填**。直接删会让这批账号的 users.group 指向一个
 *                    不存在的分组,而分组倍率对孤儿是 fail-open 返回 1.0:
 *                    他们会静默按原价扣费,零告警。
 *   迁移后的差异     选中目标之后现算(服务端与鉴权、计费同源),不选就没有。
 *   连带清理清单     各模块自己声明的表与处置。0 行的也列出来 ——
 *                    「这里本来就没有」与「这里没查」必须是两件事。
 */
export function QyUgrDeleteDialog(props: {
  open: boolean
  onOpenChange: (open: boolean) => void
  impact: QyUgrImpact | null
  /** 第一次拉取、屏幕上还什么都没有。整块显示 `Loading...`。 */
  isLoading: boolean
  /**
   * 换了迁移目标、新的一份还在路上，而屏幕上仍是**上一个目标**的差异。
   *
   * 它必须与 `isLoading` 分开：整块塌成一行 `Loading...` 会让"在两个候选目标
   * 之间比较三堆差异"这件事变得不可能，而那是这个弹窗存在的唯一理由。
   * 代价是屏幕上这一份可能过期，所以刷新期间既要标出来，也要禁掉删除键。
   */
  isRefreshing: boolean
  isDeleting: boolean
  target: string
  onTargetChange: (target: string) => void
  ack: boolean
  onAckChange: (ack: boolean) => void
  onConfirm: () => void
}) {
  const { t } = useTranslation()
  const impact = props.impact
  const block = qyUgrDeleteBlock(impact, props.target, props.ack)
  const changes = qyUgrGroupChanges(impact?.diff)

  return (
    <QyResponsiveDialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={t('qy_ugr_delete_title', { name: impact?.name ?? '' })}
      description={t('qy_ugr_delete_desc')}
      contentClassName='sm:max-w-2xl'
      footer={
        <>
          <Button variant='outline' onClick={() => props.onOpenChange(false)}>
            {t('Cancel')}
          </Button>
          <Button
            variant='destructive'
            disabled={
              props.isLoading ||
              props.isRefreshing ||
              props.isDeleting ||
              block != null
            }
            onClick={props.onConfirm}
          >
            {props.isDeleting ? t('Saving...') : t('Delete')}
          </Button>
        </>
      }
    >
      {props.isLoading || impact == null ? (
        <p className='text-muted-foreground text-sm'>{t('Loading...')}</p>
      ) : (
        <div className='space-y-4 text-sm'>
          {!impact.deletable && (
            <Alert variant='destructive'>
              <AlertTriangle className='h-4 w-4' />
              <AlertTitle>{t('qy_ugr_delete_blocked_title')}</AlertTitle>
              <AlertDescription className='whitespace-pre-wrap'>
                {impact.block_reason}
              </AlertDescription>
            </Alert>
          )}

          <dl className='grid grid-cols-2 gap-x-4 gap-y-2'>
            <QyUgrFact
              label={t('qy_ugr_impact_users')}
              value={String(impact.users)}
            />
            <QyUgrFact
              label={t('qy_ugr_impact_tokens')}
              value={t('qy_ugr_impact_tokens_value', {
                tokens: impact.tokens,
                empty: impact.empty_group_tokens,
              })}
            />
            <QyUgrFact
              label={t('qy_ugr_impact_subscriptions')}
              value={String(impact.subscriptions)}
            />
            <QyUgrFact
              label={t('qy_ugr_impact_plans')}
              value={
                impact.blocking_plans.length === 0
                  ? t('qy_ugr_impact_none')
                  : impact.blocking_plans.join('、')
              }
            />
          </dl>

          {/*
            迁移目标。**有人时必填** —— 这一段就是项目方点名的那个「选择窗口」。

            下拉里带上每一档的在册人数与可用模型分组数:迁进一个 usable=0 的
            分组等于让这批人的全部令牌当场停摆,而光看名字看不出来这件事。
          */}
          {impact.users > 0 && (
            <div className='space-y-1.5'>
              <Label htmlFor='qy-ugr-target'>
                {t('qy_ugr_migrate_to_label', { users: impact.users })}
              </Label>
              <Select
                value={props.target === '' ? undefined : props.target}
                onValueChange={(value) => props.onTargetChange(value ?? '')}
              >
                <SelectTrigger id='qy-ugr-target'>
                  <SelectValue placeholder={t('qy_ugr_migrate_to_empty')} />
                </SelectTrigger>
                <SelectContent>
                  {impact.targets.map((target) => (
                    <SelectItem key={target.name} value={target.name}>
                      {t('qy_ugr_migrate_target_option', {
                        name: target.name,
                        users: target.users,
                        usable: target.usable_groups,
                      })}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              <p className='text-muted-foreground text-xs leading-5'>
                {t('qy_ugr_migrate_to_hint')}
              </p>
            </div>
          )}

          {/*
            迁移前后的可用清单与倍率差。

            只显示「把 N 个人从 A 迁到 B」而不显示这一段,运营就是在盲按。
            三堆分开列,因为后果的方向完全不同(见 `qyUgrGroupChanges`)。
          */}
          {impact.diff != null && (
            <div className='space-y-1.5'>
              <p className='font-medium'>
                {t('qy_ugr_diff_title', {
                  from: impact.diff.from,
                  to: impact.diff.to,
                })}
                {/*
                  换目标之后这一整段仍然是**上一个目标**的差异，直到新的一份
                  回来为止。不标出来的话，运营会拿着 A 的「失去/获得/改价」
                  按下删到 B 的按钮。删除键在同一条件下是禁用的。
                */}
                {props.isRefreshing && (
                  <span className='text-muted-foreground ml-2 text-xs font-normal'>
                    {t('qy_ugr_diff_stale')}
                  </span>
                )}
              </p>
              {impact.diff.loses_everything && (
                <Alert variant='destructive'>
                  <AlertTriangle className='h-4 w-4' />
                  <AlertDescription>
                    {t('qy_ugr_diff_loses_everything', {
                      target: impact.diff.to,
                      users: impact.users,
                    })}
                  </AlertDescription>
                </Alert>
              )}
              {impact.diff.changes.length === 0 ? (
                <p className='text-muted-foreground'>
                  {t('qy_ugr_diff_unchanged', {
                    count: impact.diff.unchanged,
                  })}
                </p>
              ) : (
                <div className='space-y-1.5'>
                  <QyUgrChangeRow
                    label={t('qy_ugr_diff_removed')}
                    tone='destructive'
                    names={changes.removed.map((change) => change.model_group)}
                  />
                  <QyUgrChangeRow
                    label={t('qy_ugr_diff_added')}
                    tone='outline'
                    names={changes.added.map((change) => change.model_group)}
                  />
                  <QyUgrChangeRow
                    label={t('qy_ugr_diff_repriced')}
                    tone='secondary'
                    names={changes.repriced.map((change) =>
                      t('qy_ugr_diff_repriced_item', {
                        group: change.model_group,
                        from: change.from_ratio,
                        to: change.to_ratio,
                      })
                    )}
                  />
                </div>
              )}
            </div>
          )}

          <div className='space-y-1.5'>
            <p className='font-medium'>{t('qy_ugr_impact_residues')}</p>
            <ul className='space-y-1'>
              {impact.residues.map((residue) => (
                <li
                  key={`${residue.module}/${residue.table}/${residue.label}`}
                  className='flex flex-wrap items-center justify-between gap-2 rounded-md border px-2 py-1.5'
                >
                  <span className='min-w-0'>
                    {residue.label}
                    <code className='bg-muted mx-1 rounded px-1 text-xs'>
                      {residue.table}
                    </code>
                  </span>
                  <span className='tabular-nums'>
                    {t('qy_ugr_impact_residue_rows', {
                      rows: residue.rows,
                      disposition: t(
                        `qy_ugr_disposition_${residue.disposition}`
                      ),
                    })}
                  </span>
                </li>
              ))}
            </ul>
          </div>

          {/*
            只在真的会发生时渲染这个勾选框:常驻一个未勾选的红框会让
            「这次迁移毫无风险」与「这 700 个人的令牌下一秒全部 403」长得一样。
          */}
          {impact.diff?.loses_everything === true && (
            <div className='border-destructive/40 flex items-start gap-2 rounded-md border p-2'>
              <Checkbox
                id='qy-ugr-ack'
                checked={props.ack}
                onCheckedChange={(value) => props.onAckChange(value === true)}
              />
              <Label
                htmlFor='qy-ugr-ack'
                className='text-sm leading-5 font-normal'
              >
                {t('qy_ugr_ack_loses_everything', {
                  target: impact.diff.to,
                  users: impact.users,
                })}
              </Label>
            </div>
          )}

          {block === 'needs_target' && (
            <p className='text-destructive text-xs leading-5'>
              {t('qy_ugr_gate_needs_target')}
            </p>
          )}
        </div>
      )}
    </QyResponsiveDialog>
  )
}

function QyUgrFact(props: { label: string; value: string }) {
  return (
    <div className='min-w-0'>
      <dt className='text-muted-foreground text-xs'>{props.label}</dt>
      <dd className='break-words'>{props.value}</dd>
    </div>
  )
}

function QyUgrChangeRow(props: {
  label: string
  tone: 'destructive' | 'outline' | 'secondary'
  names: string[]
}) {
  if (props.names.length === 0) return null
  return (
    <div className='flex flex-wrap items-center gap-1.5'>
      <span className='text-muted-foreground text-xs'>{props.label}</span>
      {props.names.map((name) => (
        <Badge key={name} variant={props.tone} className='max-w-64 truncate'>
          {name}
        </Badge>
      ))}
    </div>
  )
}
