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
import { AlertTriangle, Info } from 'lucide-react'
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
import {
  QY_UGR_MIGRATE_GATE_KEY,
  qyUgrGroupChanges,
  qyUgrMigrateBlock,
  qyUgrRefillSources,
} from '../lib/gates'
import type { QyUgrImpact } from '../types'

/**
 * 「把这一档人整体挪到另一档」的确认弹窗。
 *
 * ── 项目方原话 ──
 *
 * 「既然 default 的用户分组无法删除，那么你就在用户分组这里增加一个用户分组迁移
 * 的功能，显示这个用户组有多少人，一键迁移。到哪个用户组。」
 *
 * ── 它与删除弹窗的关系 ──
 *
 * 两者共用同一份影响面接口与同一份服务端迁移实现，但它们回答的是两个问题，
 * 所以闸门与文案都不一样：
 *
 *   删除弹窗  「这一档能不能没了」——default 恒为不能，套餐引用与冻结残留都拦
 *   这一个    「这一档的人能不能挪走」——与上面四条一条都不相干
 *
 * 把删除那套闸门搬过来，这个入口就会在**最需要它的那一行**（`default`，707 人）
 * 上灰掉，而那正是这次报上来的缺陷本身。
 *
 * ── 屏幕上必须同时有的四件事 ──
 *
 *   人数 / 空分组令牌数  项目方点名的数字。空分组令牌是迁移后行为变化最直接的
 *                        一批(它们的模型分组由 users.group 解析)。
 *   目标选择             带上每一档的在册人数与可用模型分组数：迁进一个 usable=0
 *                        的分组等于让这批人的全部令牌当场停摆。
 *   迁移后的差异         选中目标之后服务端现算，与鉴权和计费同源。
 *   「迁完之后源分组还在」这句话必须常驻。运营对"迁移"最自然的读法是"挪完就
 *                        没了"，而这里源分组连同它的全部配置都留着。
 */
export function QyUgrMigrateDialog(props: {
  open: boolean
  onOpenChange: (open: boolean) => void
  impact: QyUgrImpact | null
  /** 第一次拉取、屏幕上还什么都没有。整块显示 `Loading...`。 */
  isLoading: boolean
  /** 换了目标、新的一份还在路上，而屏幕上仍是上一个目标的差异。 */
  isRefreshing: boolean
  isMigrating: boolean
  target: string
  onTargetChange: (target: string) => void
  ack: boolean
  onAckChange: (ack: boolean) => void
  onConfirm: () => void
}) {
  const { t } = useTranslation()
  const impact = props.impact
  const block = qyUgrMigrateBlock(impact, props.target, props.ack)
  const changes = qyUgrGroupChanges(impact?.diff)
  /*
    ── 「迁完之后还会有人被放回来」的**两类**来源 ──────────────────────────

    这一屏此前只画了套餐那一半，另一半（处置为 `rewrite` 的残留，最典型的就是
    「新注册用户默认分组」）要到迁移**成功之后**才由一条 toast 说出来 —— 而那
    句话恰恰是运营做这个决定的前提。判据收在 `qyUgrRefillSources` 里，与成功
    之后那条 toast 读同一份数据，两处不可能各说各的。
  */
  const refills = qyUgrRefillSources(impact)
  const hasRefill = refills.plans.length > 0 || refills.residues.length > 0

  let gateKey: string | null = null
  if (block != null) gateKey = QY_UGR_MIGRATE_GATE_KEY[block]
  else if (props.isRefreshing) gateKey = 'qy_ugr_gate_refreshing'

  return (
    <QyResponsiveDialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={t('qy_ugr_migrate_title', { name: impact?.name ?? '' })}
      description={t('qy_ugr_migrate_desc')}
      contentClassName='sm:max-w-2xl'
      footer={
        <>
          <Button variant='outline' onClick={() => props.onOpenChange(false)}>
            {t('Cancel')}
          </Button>
          <Button
            disabled={
              props.isLoading ||
              props.isRefreshing ||
              props.isMigrating ||
              block != null
            }
            onClick={props.onConfirm}
          >
            {props.isMigrating ? t('Saving...') : t('qy_ugr_migrate_confirm')}
          </Button>
        </>
      }
    >
      <div className='space-y-4 text-sm'>
        {props.isLoading || impact == null ? (
          <p className='text-muted-foreground text-sm'>{t('Loading...')}</p>
        ) : (
          <div className='space-y-4 text-sm'>
            {/*
              ── 「这不是删除」必须是屏幕上第一句话 ────────────────────────
              迁完之后源分组仍然存在、配置完整，而且指向它的套餐与「新注册用户
              默认分组」都没有动 —— 也就是说这一档会被重新填回来。不先说清楚，
              运营会以为挪完这一档就消失了，然后在几天后发现人数又长了回去。
            */}
            <Alert>
              <Info className='h-4 w-4' />
              <AlertTitle>{t('qy_ugr_migrate_keeps_source_title')}</AlertTitle>
              <AlertDescription className='whitespace-pre-wrap'>
                {t('qy_ugr_migrate_keeps_source_desc', { name: impact.name })}
              </AlertDescription>
            </Alert>

            <dl className='grid grid-cols-2 gap-x-4 gap-y-2'>
              <QyUgrMigrateFact
                label={t('qy_ugr_impact_users')}
                value={String(impact.users)}
              />
              <QyUgrMigrateFact
                label={t('qy_ugr_impact_tokens')}
                value={t('qy_ugr_impact_tokens_value', {
                  tokens: impact.tokens,
                  empty: impact.empty_group_tokens,
                })}
              />
            </dl>
            {/*
              空分组令牌单独说一句：它们没有指定令牌分组，模型分组由 users.group
              (或这一档的默认模型分组)解析 —— 迁移之后行为变化最直接的就是这一批。
            */}
            {impact.empty_group_tokens > 0 && (
              <p className='text-muted-foreground text-xs leading-5'>
                {t('qy_ugr_migrate_empty_tokens_hint', {
                  count: impact.empty_group_tokens,
                })}
              </p>
            )}

            <div className='space-y-1.5'>
              <Label htmlFor='qy-ugr-migrate-target'>
                {t('qy_ugr_migrate_to_label', { users: impact.users })}
              </Label>
              <Select
                value={props.target === '' ? undefined : props.target}
                onValueChange={(value) => props.onTargetChange(value ?? '')}
              >
                <SelectTrigger id='qy-ugr-migrate-target'>
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
              {impact.targets.length === 0 && (
                <p className='text-destructive text-xs leading-5'>
                  {t('qy_ugr_migrate_no_target')}
                </p>
              )}
            </div>

            {/*
              迁移前后的可用清单与倍率差 —— 与删除弹窗**同一份**服务端计算。
              只显示「把 N 个人从 A 挪到 B」而不显示这一段，运营就是在盲按：
              少掉的每一个模型分组是那批人手上令牌的当场 403，多出来的每一格
              倍率差是从下一秒开始的账单变化。
            */}
            {impact.diff != null && (
              <div className='space-y-1.5'>
                <p className='font-medium'>
                  {t('qy_ugr_diff_title', {
                    from: impact.diff.from,
                    to: impact.diff.to,
                  })}
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
                    <QyUgrMigrateChangeRow
                      label={t('qy_ugr_diff_removed')}
                      tone='destructive'
                      names={changes.removed.map(
                        (change) => change.model_group
                      )}
                    />
                    <QyUgrMigrateChangeRow
                      label={t('qy_ugr_diff_added')}
                      tone='outline'
                      names={changes.added.map((change) => change.model_group)}
                    />
                    <QyUgrMigrateChangeRow
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

            {/*
              「迁完之后还会有人被放回来」的两个来源。它们在删除路径上分别被硬拦
              (套餐)与改写(新注册默认分组)，迁移一个都不动 —— 所以这里只能说，
              不能拦：拦掉的话 `default` 这一档又变成挪不走的了。
            */}
            {hasRefill && (
              <Alert data-slot='qy-ugr-migrate-refill'>
                <Info className='h-4 w-4' />
                <AlertTitle>{t('qy_ugr_migrate_refill_title')}</AlertTitle>
                <AlertDescription className='space-y-1.5 whitespace-pre-wrap'>
                  {refills.plans.length > 0 && (
                    <span className='block'>
                      {t('qy_ugr_migrate_refill_plans', {
                        name: impact.name,
                        plans: refills.plans.join('、'),
                      })}
                    </span>
                  )}
                  {refills.residues.length > 0 && (
                    <span className='block'>
                      {t('qy_ugr_migrate_refill_residues', {
                        name: impact.name,
                        sources: refills.residues
                          .map((residue) =>
                            t('qy_ugr_migrate_refill_residue_item', {
                              label: residue.label,
                              table: residue.table,
                              rows: residue.rows,
                            })
                          )
                          .join('、'),
                      })}
                    </span>
                  )}
                </AlertDescription>
              </Alert>
            )}

            {impact.diff?.loses_everything === true && (
              <div className='border-destructive/40 flex items-start gap-2 rounded-md border p-2'>
                <Checkbox
                  id='qy-ugr-migrate-ack'
                  checked={props.ack}
                  onCheckedChange={(value) => props.onAckChange(value === true)}
                />
                <Label
                  htmlFor='qy-ugr-migrate-ack'
                  className='text-sm leading-5 font-normal'
                >
                  {t('qy_ugr_ack_loses_everything', {
                    target: impact.diff.to,
                    users: impact.users,
                  })}
                </Label>
              </div>
            )}
          </div>
        )}

        {gateKey != null && (
          <p
            data-slot='qy-ugr-migrate-gate'
            className='text-destructive text-xs leading-5'
          >
            {t(gateKey)}
          </p>
        )}
      </div>
    </QyResponsiveDialog>
  )
}

function QyUgrMigrateFact(props: { label: string; value: string }) {
  return (
    <div className='min-w-0'>
      <dt className='text-muted-foreground text-xs'>{props.label}</dt>
      <dd className='break-words'>{props.value}</dd>
    </div>
  )
}

function QyUgrMigrateChangeRow(props: {
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
