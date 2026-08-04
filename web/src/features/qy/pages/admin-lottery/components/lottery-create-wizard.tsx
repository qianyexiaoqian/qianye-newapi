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
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { Plus, TriangleAlert, X } from 'lucide-react'
import { useEffect, useId, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'

import { QyAmountInput } from '../../../components/qy-amount-input'
import { QyAmountText } from '../../../components/qy-amount-text'
import { QyConfirmDialog } from '../../../components/qy-confirm-dialog'
import { QyResponsiveDialog } from '../../../components/qy-responsive-dialog'
import { qyErrorMessage } from '../../../lib/api'
import { qyKeys } from '../../../lib/query-keys'
import { formatQyTs } from '../../ops/format'
import { QyKeyValue } from '../../ops/qy-ops-ui'
import { createQyLotActivity } from '../api'
import { qyLotFromLocalInput, qyLotToLocalInput } from '../lib/datetime'
import {
  qyLotBreakEvenEntries,
  qyLotDraftToInput,
  qyLotEmptyDraft,
  qyLotNewOption,
  qyLotTotalPrizeQuota,
  qyLotValidateDraft,
  type QyLotDraft,
} from '../lib/draft'
import type { QyLotAdminConfig } from '../types'
import { QyLotRulesEditor } from './lottery-rules-editor'

const STEPS = ['basic', 'spec', 'rules', 'review'] as const
type QyLotStep = (typeof STEPS)[number]

/**
 * 创建活动向导。
 *
 * ## 为什么是四步而不是一张长表
 *
 * 要填的字段有四十来个，一张长表的实际后果是运营滚到底、随手点保存，然后在
 * publish 那一刻才发现奖品总额多了一个零 —— 而 publish 是**不可逆**的。
 * 分步的价值全在最后一步：它把「即将被永久冻结的东西」与「本场最坏情况净增发
 * 多少额度」并排摆出来，再要一次刻意的确认。
 *
 * ## 创建 ≠ 发布
 *
 * 这个向导只产出 `draft`。草稿期一切可改、种子已生成但没有承诺。真正不可逆的
 * 是详情页上的「发布」——两件事分开是刻意的：合成一步的话，"我先建个草稿看看"
 * 这个再正常不过的动作就会变成一次无法撤销的承诺。
 */
export function QyLotCreateWizard(props: {
  open: boolean
  onOpenChange: (open: boolean) => void
  config: QyLotAdminConfig | undefined
}) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const queryClient = useQueryClient()

  const defaultFee = props.config?.effective.default_guess_fee_bps ?? 500
  const [draft, setDraft] = useState<QyLotDraft>(() =>
    qyLotEmptyDraft(defaultFee)
  )
  const [step, setStep] = useState<QyLotStep>('basic')
  const [confirmOpen, setConfirmOpen] = useState(false)

  useEffect(() => {
    if (!props.open) return
    setDraft(qyLotEmptyDraft(defaultFee))
    setStep('basic')
    setConfirmOpen(false)
  }, [props.open, defaultFee])

  const patch = (next: Partial<QyLotDraft>) => {
    setDraft((prev) => ({ ...prev, ...next }))
  }

  const errors = qyLotValidateDraft(
    draft,
    props.config?.yaml_readonly,
    props.config?.effective.max_total_prize_quota ?? 0,
    props.config?.effective.max_guess_fee_bps ?? 0
  )

  const mutation = useMutation({
    mutationFn: () => createQyLotActivity(qyLotDraftToInput(draft)),
    onSuccess: async (data) => {
      toast.success(t('qy_lot_created'))
      setConfirmOpen(false)
      props.onOpenChange(false)
      await queryClient.invalidateQueries({ queryKey: qyKeys.all })
      // 直接落到详情页：下一步（发布）在那里，而且那一步才是不可逆的。
      await navigate({
        to: '/qy/admin/lottery/$actNo',
        params: { actNo: data.act_no },
      })
    },
    onError: (error) => toast.error(qyErrorMessage(error, t)),
  })

  const index = STEPS.indexOf(step)
  const totalPrize = qyLotTotalPrizeQuota(draft)
  const breakEven = qyLotBreakEvenEntries(draft)
  const alertQuota = props.config?.effective.large_prize_alert_quota ?? 0

  return (
    <>
      <QyResponsiveDialog
        open={props.open}
        onOpenChange={props.onOpenChange}
        title={t('qy_lot_create_title')}
        description={t(`qy_lot_step_${step}_desc`)}
        contentClassName='sm:max-w-3xl'
        footer={
          <>
            <Button
              type='button'
              variant='outline'
              disabled={index === 0}
              onClick={() => setStep(STEPS[Math.max(0, index - 1)])}
            >
              {t('qy_lot_step_prev')}
            </Button>
            {step === 'review' ? (
              <Button
                type='button'
                disabled={errors.length > 0 || mutation.isPending}
                onClick={() => setConfirmOpen(true)}
              >
                {t('qy_lot_create_submit')}
              </Button>
            ) : (
              <Button
                type='button'
                onClick={() =>
                  setStep(STEPS[Math.min(STEPS.length - 1, index + 1)])
                }
              >
                {t('qy_lot_step_next')}
              </Button>
            )}
          </>
        }
      >
        <div className='space-y-4'>
          <ol className='text-muted-foreground flex flex-wrap gap-2 text-xs'>
            {STEPS.map((item, position) => (
              <li
                key={item}
                className={
                  position === index ? 'text-foreground font-medium' : ''
                }
              >
                {position + 1}. {t(`qy_lot_step_${item}`)}
              </li>
            ))}
          </ol>

          {step === 'basic' && <BasicStep draft={draft} onChange={patch} />}
          {step === 'spec' && <SpecStep draft={draft} onChange={patch} />}
          {step === 'rules' && (
            <QyLotRulesEditor draft={draft} onChange={patch} />
          )}
          {step === 'review' && (
            <ReviewStep
              draft={draft}
              errors={errors}
              totalPrize={totalPrize}
              breakEven={breakEven}
              alertQuota={alertQuota}
            />
          )}
        </div>
      </QyResponsiveDialog>

      <QyConfirmDialog
        open={confirmOpen}
        onOpenChange={setConfirmOpen}
        title={t('qy_lot_create_confirm_title')}
        description={t('qy_lot_create_confirm_desc')}
        isLoading={mutation.isPending}
        details={
          <div>
            <QyKeyValue label={t('qy_lot_kind')}>
              {t(`qy_lot_kind_${draft.kind}`)}
            </QyKeyValue>
            <QyKeyValue label={t('qy_lot_stake')}>
              <QyAmountText quota={draft.stake_quota} />
            </QyKeyValue>
            {draft.kind === 'draw' && (
              <>
                <QyKeyValue label={t('qy_lot_total_prize')}>
                  <QyAmountText quota={totalPrize} />
                </QyKeyValue>
                <QyKeyValue label={t('qy_lot_break_even')}>
                  {breakEven}
                </QyKeyValue>
              </>
            )}
          </div>
        }
        onConfirm={() => mutation.mutate()}
      />
    </>
  )
}

// ───────────────────────────── 第一步 ─────────────────────────────

function BasicStep(props: {
  draft: QyLotDraft
  onChange: (patch: Partial<QyLotDraft>) => void
}) {
  const { draft } = props
  const { t } = useTranslation()
  const id = useId()

  return (
    <div className='space-y-3'>
      <div className='space-y-1'>
        <Label htmlFor={`${id}-kind`}>{t('qy_lot_kind')}</Label>
        <Select
          value={draft.kind}
          onValueChange={(value) =>
            props.onChange({ kind: value === 'guess' ? 'guess' : 'draw' })
          }
        >
          <SelectTrigger id={`${id}-kind`}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value='draw'>{t('qy_lot_kind_draw')}</SelectItem>
            <SelectItem value='guess'>{t('qy_lot_kind_guess')}</SelectItem>
          </SelectContent>
        </Select>
        <p className='text-muted-foreground text-xs'>
          {t(`qy_lot_kind_${draft.kind}_hint`)}
        </p>
      </div>

      <div className='space-y-1'>
        <Label htmlFor={`${id}-title`}>{t('qy_lot_title_field')}</Label>
        <Input
          id={`${id}-title`}
          value={draft.title}
          maxLength={120}
          onChange={(event) => props.onChange({ title: event.target.value })}
        />
      </div>

      <div className='space-y-1'>
        <Label htmlFor={`${id}-intro`}>{t('qy_lot_intro_field')}</Label>
        <Textarea
          id={`${id}-intro`}
          rows={3}
          value={draft.intro}
          onChange={(event) => props.onChange({ intro: event.target.value })}
        />
        {/* 简介按纯文本渲染，不解析富文本 —— 说明白，免得运营贴一段 HTML
            然后奇怪为什么没生效。 */}
        <p className='text-muted-foreground text-xs'>
          {t('qy_lot_intro_plain_hint')}
        </p>
      </div>

      <div className='space-y-1'>
        <Label htmlFor={`${id}-stake`}>{t('qy_lot_stake')}</Label>
        <QyAmountInput
          id={`${id}-stake`}
          value={draft.stake_quota}
          onChange={(quota) => props.onChange({ stake_quota: quota })}
        />
        <p className='text-muted-foreground text-xs'>
          {t('qy_lot_stake_hint')}
        </p>
      </div>

      <div className='grid gap-3 sm:grid-cols-2'>
        <TimeField
          label={t('qy_lot_open_at')}
          value={draft.open_at}
          onChange={(value) => props.onChange({ open_at: value })}
        />
        <TimeField
          label={t('qy_lot_close_at')}
          value={draft.close_at}
          onChange={(value) => props.onChange({ close_at: value })}
        />
        <TimeField
          label={t('qy_lot_draw_at')}
          hint={t('qy_lot_draw_at_hint')}
          value={draft.draw_at}
          onChange={(value) => props.onChange({ draw_at: value })}
        />
        <TimeField
          label={t('qy_lot_settle_deadline')}
          hint={t('qy_lot_settle_deadline_hint')}
          value={draft.settle_deadline}
          onChange={(value) => props.onChange({ settle_deadline: value })}
        />
      </div>
    </div>
  )
}

function TimeField(props: {
  label: string
  hint?: string
  value: number
  onChange: (value: number) => void
}) {
  const id = useId()
  return (
    <div className='space-y-1'>
      <Label htmlFor={id}>{props.label}</Label>
      <Input
        id={id}
        type='datetime-local'
        value={qyLotToLocalInput(props.value)}
        onChange={(event) =>
          props.onChange(qyLotFromLocalInput(event.target.value))
        }
      />
      {props.hint != null && (
        <p className='text-muted-foreground text-xs'>{props.hint}</p>
      )}
    </div>
  )
}

// ───────────────────────────── 第二步 ─────────────────────────────

function SpecStep(props: {
  draft: QyLotDraft
  onChange: (patch: Partial<QyLotDraft>) => void
}) {
  const { draft } = props
  const { t } = useTranslation()
  const id = useId()

  if (draft.kind === 'draw') {
    return (
      <div className='space-y-3'>
        {draft.tiers.map((tier, index) => (
          <div key={tier.tier} className='space-y-2 rounded-lg border p-3'>
            <div className='flex items-center justify-between'>
              <span className='text-sm font-medium'>
                {t('qy_lot_tier_no', { no: tier.tier })}
              </span>
              <Button
                type='button'
                variant='ghost'
                size='icon-sm'
                aria-label={t('qy_common_delete')}
                disabled={draft.tiers.length <= 1}
                onClick={() =>
                  props.onChange({
                    tiers: draft.tiers.filter((_, i) => i !== index),
                  })
                }
              >
                <X aria-hidden='true' />
              </Button>
            </div>
            <div className='grid gap-2 sm:grid-cols-3'>
              <div className='space-y-1'>
                <Label>{t('qy_lot_prize_name')}</Label>
                <Input
                  value={tier.name}
                  maxLength={80}
                  onChange={(event) =>
                    props.onChange({
                      tiers: draft.tiers.map((item, i) =>
                        i === index
                          ? { ...item, name: event.target.value }
                          : item
                      ),
                    })
                  }
                />
              </div>
              <div className='space-y-1'>
                <Label>{t('qy_lot_prize_amount')}</Label>
                <QyAmountInput
                  value={tier.amount_quota}
                  onChange={(quota) =>
                    props.onChange({
                      tiers: draft.tiers.map((item, i) =>
                        i === index ? { ...item, amount_quota: quota } : item
                      ),
                    })
                  }
                />
              </div>
              <div className='space-y-1'>
                <Label>{t('qy_lot_prize_count')}</Label>
                <Input
                  inputMode='numeric'
                  value={String(tier.count)}
                  onChange={(event) => {
                    const digits = event.target.value.replaceAll(/\D/g, '')
                    props.onChange({
                      tiers: draft.tiers.map((item, i) =>
                        i === index
                          ? {
                              ...item,
                              count: digits === '' ? 0 : Number(digits),
                            }
                          : item
                      ),
                    })
                  }}
                />
              </div>
            </div>
          </div>
        ))}

        <Button
          type='button'
          variant='outline'
          size='sm'
          onClick={() =>
            props.onChange({
              tiers: [
                ...draft.tiers,
                {
                  tier:
                    draft.tiers.reduce(
                      (max, item) => Math.max(max, item.tier),
                      0
                    ) + 1,
                  name: '',
                  amount_quota: 0,
                  count: 1,
                },
              ],
            })
          }
        >
          <Plus aria-hidden='true' />
          {t('qy_lot_add_tier')}
        </Button>

        <div className='flex items-start justify-between gap-4 rounded-lg border p-3'>
          <div className='min-w-0'>
            <Label htmlFor={`${id}-multi`}>{t('qy_lot_allow_multi_win')}</Label>
            <p className='text-muted-foreground text-xs'>
              {t('qy_lot_allow_multi_win_hint')}
            </p>
          </div>
          <Switch
            id={`${id}-multi`}
            checked={draft.allow_multi_win}
            onCheckedChange={(checked) =>
              props.onChange({ allow_multi_win: checked })
            }
          />
        </div>

        <div className='space-y-1'>
          <Label htmlFor={`${id}-min-entries`}>
            {t('qy_lot_min_entries_field')}
          </Label>
          <Input
            id={`${id}-min-entries`}
            inputMode='numeric'
            value={String(draft.min_entries_to_hold)}
            onChange={(event) => {
              const digits = event.target.value.replaceAll(/\D/g, '')
              props.onChange({
                min_entries_to_hold: digits === '' ? 0 : Number(digits),
              })
            }}
          />
          {/* 平台侧唯一的止损阀，而且对用户完全公平：不足即流局、全额退款。
              没有它就会出现"3 个人参加、平台净亏一个一等奖"。 */}
          <p className='text-muted-foreground text-xs'>
            {t('qy_lot_min_entries_hint_field')}
          </p>
        </div>
      </div>
    )
  }

  const catchAllCount = draft.options.filter(
    (option) => option.is_catch_all
  ).length

  return (
    <div className='space-y-3'>
      {draft.options.map((option, index) => (
        <div key={option.id} className='space-y-2 rounded-lg border p-3'>
          <div className='flex items-center gap-2'>
            <Input
              value={option.label}
              maxLength={80}
              placeholder={t('qy_lot_option_placeholder')}
              onChange={(event) =>
                props.onChange({
                  options: draft.options.map((item, i) =>
                    i === index ? { ...item, label: event.target.value } : item
                  ),
                })
              }
            />
            <Button
              type='button'
              variant='ghost'
              size='icon-sm'
              aria-label={t('qy_common_delete')}
              disabled={draft.options.length <= 2}
              onClick={() =>
                props.onChange({
                  options: draft.options.filter((_, i) => i !== index),
                })
              }
            >
              <X aria-hidden='true' />
            </Button>
          </div>
          <div className='flex items-center justify-between gap-3'>
            <Label className='text-xs'>{t('qy_lot_option_catch_all')}</Label>
            <Switch
              checked={option.is_catch_all}
              onCheckedChange={(checked) =>
                props.onChange({
                  options: draft.options.map((item, i) =>
                    i === index ? { ...item, is_catch_all: checked } : item
                  ),
                })
              }
            />
          </div>
        </div>
      ))}

      <Button
        type='button'
        variant='outline'
        size='sm'
        onClick={() =>
          props.onChange({
            options: [...draft.options, qyLotNewOption()],
          })
        }
      >
        <Plus aria-hidden='true' />
        {t('qy_lot_add_option')}
      </Button>

      {/* 没有兜底项时「全部猜错」会频繁发生，届时全场退款、平台零收益。
          这条代价必须印在界面上 —— 删掉它的人不会去读文档。 */}
      {catchAllCount === 0 && (
        <Alert>
          <TriangleAlert />
          <AlertTitle>{t('qy_lot_no_catch_all_title')}</AlertTitle>
          <AlertDescription>{t('qy_lot_no_catch_all_desc')}</AlertDescription>
        </Alert>
      )}

      <div className='space-y-1'>
        <Label htmlFor={`${id}-fee`}>{t('qy_lot_fee_bps')}</Label>
        <Input
          id={`${id}-fee`}
          inputMode='numeric'
          value={String(draft.fee_bps)}
          onChange={(event) => {
            const digits = event.target.value.replaceAll(/\D/g, '')
            props.onChange({ fee_bps: digits === '' ? 0 : Number(digits) })
          }}
        />
        <p className='text-muted-foreground text-xs'>
          {t('qy_lot_fee_bps_hint', {
            percent: (draft.fee_bps / 100).toFixed(2),
          })}
        </p>
      </div>

      <div className='space-y-1'>
        <Label htmlFor={`${id}-guess-min`}>
          {t('qy_lot_min_entries_field')}
        </Label>
        <Input
          id={`${id}-guess-min`}
          inputMode='numeric'
          value={String(draft.min_entries_to_hold)}
          onChange={(event) => {
            const digits = event.target.value.replaceAll(/\D/g, '')
            props.onChange({
              min_entries_to_hold: digits === '' ? 0 : Number(digits),
            })
          }}
        />
        <p className='text-muted-foreground text-xs'>
          {t('qy_lot_min_entries_guess_hint')}
        </p>
      </div>
    </div>
  )
}

// ───────────────────────────── 第四步 ─────────────────────────────

function ReviewStep(props: {
  draft: QyLotDraft
  errors: string[]
  totalPrize: number
  breakEven: number
  alertQuota: number
}) {
  const { draft } = props
  const { t } = useTranslation()

  return (
    <div className='space-y-3'>
      {props.errors.length > 0 && (
        <Alert variant='destructive'>
          <TriangleAlert />
          <AlertTitle>{t('qy_lot_review_invalid_title')}</AlertTitle>
          <AlertDescription>
            <ul className='mt-1 space-y-1'>
              {props.errors.map((key) => (
                <li key={key}>{t(key)}</li>
              ))}
            </ul>
          </AlertDescription>
        </Alert>
      )}

      {/* 发布之后再也改不了的那些字段单独列一屏。它们全部进 `commit_hash`
          原像 —— 事后改任何一项，验证脚本都会算出不一样的哈希。 */}
      <div className='rounded-lg border p-3'>
        <p className='mb-2 text-sm font-medium'>
          {t('qy_lot_review_frozen_title')}
        </p>
        <QyKeyValue label={t('qy_lot_kind')}>
          {t(`qy_lot_kind_${draft.kind}`)}
        </QyKeyValue>
        <QyKeyValue label={t('qy_lot_stake')}>
          <QyAmountText quota={draft.stake_quota} />
        </QyKeyValue>
        <QyKeyValue label={t('qy_lot_open_at')}>
          {formatQyTs(draft.open_at)}
        </QyKeyValue>
        <QyKeyValue label={t('qy_lot_close_at')}>
          {formatQyTs(draft.close_at)}
        </QyKeyValue>
        <QyKeyValue label={t('qy_lot_draw_at')}>
          {formatQyTs(draft.draw_at)}
        </QyKeyValue>
        <QyKeyValue label={t('qy_lot_settle_deadline')}>
          {formatQyTs(draft.settle_deadline)}
        </QyKeyValue>
        <QyKeyValue label={t('qy_lot_allow_multi_win')}>
          {draft.allow_multi_win ? t('qy_common_on') : t('qy_common_off')}
        </QyKeyValue>
        {draft.kind === 'guess' && (
          <QyKeyValue label={t('qy_lot_fee_bps')}>{draft.fee_bps}</QyKeyValue>
        )}
        <QyKeyValue label={t('qy_lot_min_entries_field')}>
          {draft.min_entries_to_hold}
        </QyKeyValue>
      </div>

      {draft.kind === 'draw' && (
        <div className='rounded-lg border p-3'>
          <p className='mb-2 text-sm font-medium'>
            {t('qy_lot_review_money_title')}
          </p>
          {/* 抽奖是「平台收参与费、平台出奖品」，两边不守恒是正常的：
              派奖是对用户额度的**净增发**，本仓不存在平台账户。 */}
          <QyKeyValue label={t('qy_lot_total_prize')}>
            <QyAmountText quota={props.totalPrize} />
          </QyKeyValue>
          <QyKeyValue label={t('qy_lot_break_even')}>
            {props.breakEven === 0 ? '-' : props.breakEven}
          </QyKeyValue>
          <p className='text-muted-foreground mt-2 text-xs'>
            {t('qy_lot_break_even_note', { count: props.breakEven })}
          </p>
          {props.alertQuota > 0 && props.totalPrize > props.alertQuota && (
            <Alert className='mt-2'>
              <TriangleAlert />
              <AlertTitle>{t('qy_lot_large_prize_title')}</AlertTitle>
              <AlertDescription>
                {t('qy_lot_large_prize_desc')}
              </AlertDescription>
            </Alert>
          )}
        </div>
      )}

      <p className='text-muted-foreground text-xs'>
        {t('qy_lot_create_draft_note')}
      </p>
    </div>
  )
}
