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
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Label } from '@/components/ui/label'

import { QyResponsiveDialog } from '../../../components/qy-responsive-dialog'
import type { QyMgImpact } from '../types'

/**
 * 删除一个模型分组的确认弹窗。
 *
 * ── 它为什么这么长 ──
 *
 * 项目方原话:「删除前必须显示影响面:多少令牌、多少用户分组、多少套餐会受影响。」
 * 上游那一页的删除是一个垃圾桶图标 + 直接从表格里去掉一行,没有任何确认 ——
 * 而这个动作会同时改掉分组倍率表、全局「用户可选分组」、auto 顺序、交叉倍率的
 * 内层键、用户分组 × 模型分组的授权、套餐解锁,并且让指着它的令牌全部变成孤儿。
 *
 * 所以这里的每一块都对应一条后果,而不是"顺便也显示一下":
 *
 *   blockers        不可覆盖的硬拦。有它就没有确认按钮,只有"去哪儿先处理"。
 *   两个勾选框      两道可覆盖闸门。**分开**而不是合成一个:一个是留下僵尸路由,
 *                   另一个是一批令牌当场 403,合并会让运营在只关心其中一件事的
 *                   时候顺手确认掉另一件。
 *   残留清单        各模块自己声明的表与行数。0 行的也列出来 ——
 *                   「这里本来就没有」与「这里没查」必须是两件事。
 */
export function QyMgDeleteDialog(props: {
  open: boolean
  onOpenChange: (open: boolean) => void
  impact: QyMgImpact | null
  isLoading: boolean
  isDeleting: boolean
  forceHasRoute: boolean
  forceOrphanTokens: boolean
  onForceHasRouteChange: (value: boolean) => void
  onForceOrphanTokensChange: (value: boolean) => void
  onConfirm: () => void
}) {
  const { t } = useTranslation()
  const impact = props.impact
  const blocked = (impact?.blockers.length ?? 0) > 0
  const missingForce =
    impact != null &&
    ((impact.needs_force_has_route && !props.forceHasRoute) ||
      (impact.needs_force_orphan_tokens && !props.forceOrphanTokens))

  return (
    <QyResponsiveDialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={t('qy_mg_delete_title', { name: impact?.name ?? '' })}
      description={t('qy_mg_delete_desc')}
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
              props.isDeleting ||
              impact == null ||
              blocked ||
              missingForce
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
          {blocked && (
            <Alert variant='destructive'>
              <AlertTriangle className='h-4 w-4' />
              <AlertTitle>{t('qy_mg_delete_blocked_title')}</AlertTitle>
              <AlertDescription>
                <ul className='list-disc space-y-1 pl-4'>
                  {impact.blockers.map((line) => (
                    <li key={line} className='whitespace-pre-wrap'>
                      {line}
                    </li>
                  ))}
                </ul>
              </AlertDescription>
            </Alert>
          )}

          <dl className='grid grid-cols-2 gap-x-4 gap-y-2'>
            <QyMgFact
              label={t('qy_mg_impact_tokens')}
              value={t('qy_mg_impact_tokens_value', {
                tokens: impact.tokens,
                owners: impact.token_owners,
              })}
            />
            <QyMgFact
              label={t('qy_mg_impact_route')}
              value={t('qy_mg_impact_route_value', {
                abilities: impact.ability_rows,
                channels: impact.enabled_channels,
              })}
            />
            <QyMgFact
              label={t('qy_mg_impact_pinned')}
              value={
                impact.pinned_by_user_groups.length === 0
                  ? t('qy_mg_impact_none')
                  : t('qy_mg_impact_pinned_value', {
                      groups: impact.pinned_by_user_groups.join('、'),
                      tokens: impact.pinned_empty_group_tokens,
                    })
              }
            />
            <QyMgFact
              label={t('qy_mg_impact_cross_ratio')}
              value={
                impact.cross_ratio_user_groups.length === 0
                  ? t('qy_mg_impact_none')
                  : impact.cross_ratio_user_groups.join('、')
              }
            />
            <QyMgFact
              label={t('qy_mg_impact_usable')}
              value={
                impact.in_usable_groups
                  ? t('qy_mg_impact_usable_yes')
                  : t('qy_mg_impact_none')
              }
            />
            <QyMgFact
              label={t('qy_mg_impact_auto')}
              value={
                impact.auto_position === 0
                  ? t('qy_mg_impact_none')
                  : t('qy_mg_impact_auto_value', {
                      position: impact.auto_position,
                    })
              }
            />
          </dl>

          <div className='space-y-1.5'>
            <p className='font-medium'>{t('qy_mg_impact_residues')}</p>
            <ul className='space-y-1'>
              {impact.residues.map((residue) => (
                <li
                  key={`${residue.module}/${residue.table}`}
                  className='flex flex-wrap items-center justify-between gap-2 rounded-md border px-2 py-1.5'
                >
                  <span className='min-w-0'>
                    {residue.label}
                    <code className='bg-muted mx-1 rounded px-1 text-xs'>
                      {residue.table}
                    </code>
                  </span>
                  <span className='tabular-nums'>
                    {t('qy_mg_impact_residue_rows', {
                      rows: residue.rows,
                      disposition: t(
                        `qy_mg_disposition_${residue.disposition}`
                      ),
                    })}
                  </span>
                </li>
              ))}
            </ul>
          </div>

          {/*
            两道可覆盖闸门。只在真的需要时渲染 —— 常驻两个未勾选的红色勾选框会
            让"这次删除其实没有任何风险"和"这次会让 200 个令牌当场挂掉"长得一样。
          */}
          {impact.needs_force_has_route && (
            <QyMgForceCheck
              id='qy-mg-force-route'
              checked={props.forceHasRoute}
              onChange={props.onForceHasRouteChange}
              label={t('qy_mg_force_has_route', {
                channels: impact.enabled_channels,
                abilities: impact.ability_rows,
              })}
            />
          )}
          {impact.needs_force_orphan_tokens && (
            <QyMgForceCheck
              id='qy-mg-force-tokens'
              checked={props.forceOrphanTokens}
              onChange={props.onForceOrphanTokensChange}
              label={t('qy_mg_force_orphan_tokens', {
                tokens: impact.tokens,
                owners: impact.token_owners,
              })}
            />
          )}
        </div>
      )}
    </QyResponsiveDialog>
  )
}

function QyMgFact(props: { label: string; value: string }) {
  return (
    <div className='min-w-0'>
      <dt className='text-muted-foreground text-xs'>{props.label}</dt>
      <dd className='break-words'>{props.value}</dd>
    </div>
  )
}

function QyMgForceCheck(props: {
  id: string
  checked: boolean
  onChange: (value: boolean) => void
  label: string
}) {
  return (
    <div className='border-destructive/40 flex items-start gap-2 rounded-md border p-2'>
      <Checkbox
        id={props.id}
        checked={props.checked}
        onCheckedChange={(value) => props.onChange(value === true)}
      />
      <Label htmlFor={props.id} className='text-sm leading-5 font-normal'>
        {props.label}
      </Label>
    </div>
  )
}
