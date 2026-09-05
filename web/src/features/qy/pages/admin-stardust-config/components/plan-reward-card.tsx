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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useEffect, useId, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'

import { QyConfirmDialog } from '../../../components/qy-confirm-dialog'
import { QyPageBoundary } from '../../../components/qy-page-boundary'
import { useStardustName } from '../../../hooks/use-stardust-name'
import { qyKeys } from '../../../lib/query-keys'
import { qySdBpsPercent } from '../../stardust/lib/display'
import {
  deleteQyStardustPlanReward,
  putQyStardustPlanReward,
  qyAdminStardustPlanListQuery,
  qyAdminStardustPlanRewardQuery,
} from '../api'
import {
  QY_SD_PLAN_REWARD_SOURCES,
  qySdBoundContains,
  qySdParseDraftText,
} from '../lib/draft'
import { qySdAdminErrorMessage } from '../lib/errors'
import type { QyStardustBound, QyStardustPlanReward } from '../types'

/**
 * 「套餐返还」—— 单个套餐买了之后返多少（`qy_sd_plan_reward`）。
 *
 * 一次只编辑一个套餐：先选套餐（下拉从上游套餐清单来，也可以直接填 plan_id），
 * 再改买家比例 / 上线比例 / 来源。没配过的套餐显示默认口径（买家 1:1、上线 0、
 * 只有 order + balance 两个来源返），并明说"这是默认，不是你配的"。
 *
 * 来源是四个固定复选项、不给自由文本：闭集之外的词后端整个拒绝
 * （`qy_sd_bad_source`），而且 `stardust`（商城自购）刻意不在里面 ——
 * 星屑买套餐再返星屑是自供回路，不允许被配置打开（D-F）。
 */
export function QySdPlanRewardCard(props: {
  bpsBound: QyStardustBound | null
}) {
  const { t } = useTranslation()
  const selectId = useId()
  const inputId = useId()

  const plans = useQuery(qyAdminStardustPlanListQuery())
  const [planIdText, setPlanIdText] = useState('')
  const [planId, setPlanId] = useState(0)
  const reward = useQuery(qyAdminStardustPlanRewardQuery(planId))

  const options = plans.data ?? []
  const typedId = qySdParseDraftText(planIdText)

  return (
    <Card data-card-hover='false'>
      <CardHeader>
        <CardTitle>{t('qy_sdadm_pr_title')}</CardTitle>
        <CardDescription>{t('qy_sdadm_pr_desc')}</CardDescription>
      </CardHeader>
      <CardContent className='space-y-4'>
        <div className='grid gap-3 sm:grid-cols-2'>
          <div className='space-y-1.5'>
            <Label htmlFor={selectId}>{t('qy_sdadm_pr_pick')}</Label>
            <NativeSelect
              id={selectId}
              className='w-full'
              value={
                options.some((plan) => plan.id === planId) ? String(planId) : ''
              }
              disabled={options.length === 0}
              onChange={(event) => {
                const next = Number(event.target.value)
                if (!Number.isSafeInteger(next) || next <= 0) return
                setPlanId(next)
                setPlanIdText(String(next))
              }}
            >
              <NativeSelectOption value=''>
                {options.length === 0
                  ? t('qy_sdadm_pr_pick_unavailable')
                  : t('qy_sdadm_pr_pick_placeholder')}
              </NativeSelectOption>
              {options.map((plan) => (
                <NativeSelectOption key={plan.id} value={String(plan.id)}>
                  {plan.enabled
                    ? `#${plan.id} ${plan.title}`
                    : `#${plan.id} ${plan.title} · ${t('qy_sdadm_pr_plan_disabled')}`}
                </NativeSelectOption>
              ))}
            </NativeSelect>
          </div>
          <div className='space-y-1.5'>
            <Label htmlFor={inputId}>{t('qy_sdadm_pr_plan_id')}</Label>
            <div className='flex items-center gap-2'>
              <Input
                id={inputId}
                inputMode='numeric'
                value={planIdText}
                placeholder={t('qy_sdadm_pr_plan_id_ph')}
                onChange={(event) =>
                  setPlanIdText(event.target.value.replaceAll(/\D/g, ''))
                }
                onKeyDown={(event) => {
                  if (event.key === 'Enter' && typedId != null && typedId > 0) {
                    setPlanId(typedId)
                  }
                }}
              />
              <Button
                size='sm'
                variant='outline'
                disabled={typedId == null || typedId <= 0}
                onClick={() => {
                  if (typedId != null) setPlanId(typedId)
                }}
              >
                {t('qy_sdadm_pr_load')}
              </Button>
            </div>
          </div>
        </div>

        {planId <= 0 ? (
          <p className='text-muted-foreground text-xs'>
            {t('qy_sdadm_pr_pick_first')}
          </p>
        ) : (
          <QyPageBoundary query={reward}>
            {reward.data != null && (
              <PlanRewardEditor
                key={planId}
                reward={reward.data}
                bpsBound={props.bpsBound}
              />
            )}
          </QyPageBoundary>
        )}
      </CardContent>
    </Card>
  )
}

/**
 * 一个套餐的编辑器。`key={planId}` 让换套餐时整个表单重建，草稿不会串到另一个
 * 套餐上 —— 那正是"以为改的是 A、实际写进了 B"的形状。
 */
function PlanRewardEditor(props: {
  reward: QyStardustPlanReward
  bpsBound: QyStardustBound | null
}) {
  const { t } = useTranslation()
  const unit = useStardustName()
  const queryClient = useQueryClient()
  const buyerId = useId()
  const inviterId = useId()
  const reward = props.reward

  const [buyer, setBuyer] = useState(String(reward.buyer_bps))
  const [inviter, setInviter] = useState(String(reward.inviter_bps))
  const [sources, setSources] = useState<string[]>(reward.sources)
  const [resetOpen, setResetOpen] = useState(false)

  // 服务端值到达（保存 / 删除之后重新取到）时重置草稿：保留旧草稿会让运营
  // 基于过期基线再改一次。
  useEffect(() => {
    setBuyer(String(reward.buyer_bps))
    setInviter(String(reward.inviter_bps))
    setSources(reward.sources)
  }, [reward])

  const invalidate = () =>
    queryClient.invalidateQueries({
      queryKey: qyKeys.adminStardustPlanReward(reward.plan_id),
    })

  const save = useMutation({
    mutationFn: () =>
      putQyStardustPlanReward(reward.plan_id, {
        buyer_bps: qySdParseDraftText(buyer) ?? 0,
        inviter_bps: qySdParseDraftText(inviter) ?? 0,
        sources,
      }),
    onSuccess: async () => {
      toast.success(t('qy_sdadm_pr_saved', { plan: reward.plan_id }))
      await invalidate()
    },
    onError: (error) => toast.error(qySdAdminErrorMessage(error, t)),
  })

  const reset = useMutation({
    mutationFn: () => deleteQyStardustPlanReward(reward.plan_id),
    onSuccess: async () => {
      setResetOpen(false)
      toast.success(t('qy_sdadm_pr_reset_done', { plan: reward.plan_id }))
      await invalidate()
    },
    onError: (error) => toast.error(qySdAdminErrorMessage(error, t)),
  })

  const parseBps = (text: string): number | null => {
    const value = qySdParseDraftText(text)
    if (value == null) return null
    if (props.bpsBound != null && !qySdBoundContains(props.bpsBound, value)) {
      return null
    }
    return value
  }
  const buyerValue = parseBps(buyer)
  const inviterValue = parseBps(inviter)
  const invalid = buyerValue == null || inviterValue == null
  // 一个来源都不勾时后端会按默认口径（order + balance）存 —— 那与运营看到的
  // 空勾选框对不上，所以这里直接不让保存，而不是让它静默变成默认。
  const noSource = sources.length === 0

  const toggleSource = (source: string, checked: boolean) => {
    setSources((prev) => {
      const without = prev.filter((item) => item !== source)
      // 按闭集顺序存，别按勾选顺序：审计里的 before/after 才对得上。
      return checked
        ? QY_SD_PLAN_REWARD_SOURCES.filter(
            (item) => item === source || without.includes(item)
          )
        : without
    })
  }

  return (
    <div className='space-y-4 rounded-lg border p-3'>
      <div className='flex flex-wrap items-center gap-2'>
        <span className='font-medium'>
          {t('qy_sdadm_pr_editing', { plan: reward.plan_id })}
        </span>
        {reward.exists ? (
          <Badge variant='secondary'>{t('qy_sdadm_pr_state_custom')}</Badge>
        ) : (
          <Badge variant='outline'>{t('qy_sdadm_pr_state_default')}</Badge>
        )}
      </div>

      <div className='grid gap-3 sm:grid-cols-2'>
        <BpsField
          id={buyerId}
          label={t('qy_sdadm_pr_buyer_bps')}
          hint={t('qy_sdadm_pr_buyer_hint')}
          value={buyer}
          parsed={buyerValue}
          bound={props.bpsBound}
          onChange={setBuyer}
        />
        <BpsField
          id={inviterId}
          label={t('qy_sdadm_pr_inviter_bps')}
          hint={t('qy_sdadm_pr_inviter_hint')}
          value={inviter}
          parsed={inviterValue}
          bound={props.bpsBound}
          onChange={setInviter}
        />
      </div>

      <div className='space-y-2'>
        <p className='text-sm font-medium'>{t('qy_sdadm_pr_sources')}</p>
        <p className='text-muted-foreground text-xs'>
          {t('qy_sdadm_pr_sources_hint', { unit })}
        </p>
        <div className='grid gap-2 sm:grid-cols-2'>
          {QY_SD_PLAN_REWARD_SOURCES.map((source) => (
            <Label
              key={source}
              htmlFor={`${buyerId}-src-${source}`}
              className='flex items-start gap-2 rounded-md border p-2 text-sm font-normal'
            >
              <Checkbox
                id={`${buyerId}-src-${source}`}
                checked={sources.includes(source)}
                onCheckedChange={(checked) =>
                  toggleSource(source, checked === true)
                }
              />
              <span className='min-w-0'>
                <span className='block'>
                  {t(`qy_sdadm_pr_src_${source}`, { defaultValue: source })}
                </span>
                <span className='text-muted-foreground block text-xs'>
                  {t(`qy_sdadm_pr_src_h_${source}`, { defaultValue: '' })}
                </span>
              </span>
            </Label>
          ))}
        </div>
        {noSource && (
          <p className='text-destructive text-xs'>
            {t('qy_sdadm_pr_no_source')}
          </p>
        )}
      </div>

      <div className='flex flex-wrap gap-2'>
        <Button
          disabled={invalid || noSource || save.isPending}
          onClick={() => save.mutate()}
        >
          {t('qy_sdadm_cfg_save')}
        </Button>
        {reward.exists && (
          <Button
            variant='outline'
            disabled={reset.isPending}
            onClick={() => setResetOpen(true)}
          >
            {t('qy_sdadm_pr_reset')}
          </Button>
        )}
      </div>

      <QyConfirmDialog
        open={resetOpen}
        onOpenChange={setResetOpen}
        title={t('qy_sdadm_pr_reset')}
        description={t('qy_sdadm_pr_reset_desc', { plan: reward.plan_id })}
        isLoading={reset.isPending}
        onConfirm={() => reset.mutate()}
      />
    </div>
  )
}

function BpsField(props: {
  id: string
  label: string
  hint: string
  value: string
  parsed: number | null
  bound: QyStardustBound | null
  onChange: (value: string) => void
}) {
  const { t } = useTranslation()
  return (
    <div className='space-y-1.5'>
      <Label htmlFor={props.id}>{props.label}</Label>
      <Input
        id={props.id}
        inputMode='numeric'
        value={props.value}
        aria-invalid={props.parsed == null}
        onChange={(event) =>
          props.onChange(event.target.value.replaceAll(/\D/g, ''))
        }
      />
      <p className='text-muted-foreground text-xs'>
        <span className='block'>{props.hint}</span>
        {props.parsed != null && (
          <span className='block'>
            {t('qy_sdadm_cfg_bps_hint', {
              percent: qySdBpsPercent(props.parsed),
            })}
          </span>
        )}
        {props.bound != null && (
          <span
            className={
              props.parsed == null ? 'text-destructive block' : 'block'
            }
          >
            {t('qy_common_amount_range', {
              min: props.bound.lo,
              max: props.bound.hi,
            })}
          </span>
        )}
      </p>
    </div>
  )
}
