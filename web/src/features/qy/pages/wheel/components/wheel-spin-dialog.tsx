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
import { Link } from '@tanstack/react-router'
import {
  Copy,
  ExternalLink,
  PackageX,
  Shuffle,
  Sparkles,
  TriangleAlert,
  Trophy,
} from 'lucide-react'
import { useCallback, useEffect, useId, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { useCopyToClipboard } from '@/hooks/use-copy-to-clipboard'

import { QyBurst } from '../../../components/art/qy-burst'
import { QyPayPasswordField } from '../../../components/qy-pay-password-field'
import { QyResponsiveDialog } from '../../../components/qy-responsive-dialog'
import { QySdAmount } from '../../../components/qy-sd-amount'
import { useStardustName } from '../../../hooks/use-stardust-name'
import { isQyError, qyErrorMessage } from '../../../lib/api'
import { formatSdWithUnit } from '../../../lib/format-sd'
import { qyKeys } from '../../../lib/query-keys'
import { QyLotFinePrint } from '../../lottery/components/lottery-fine-print'
import { qyLotTiers, type QyLotActivityDetail } from '../../lottery/types'
import { QyMallKindBadge } from '../../mall/components/mall-kind-badge'
import { QyMallPrizeAddressForm } from '../../mall/components/mall-prize-address-form'
import { qyMallOrderLink } from '../../mall/lib/order-focus'
import { QyKeyValue } from '../../ops/qy-ops-ui'
import { spinQyWheel } from '../api'
import {
  QY_WHEEL_CLIENT_SEED_MAX,
  QY_WHEEL_OUTCOME_I18N,
  isQyWheelClientSeedValid,
  qyWheelOutcomeOf,
  qyWheelRandomClientSeed,
  qyWheelTierName,
} from '../lib/spin'
import { useQyReducedMotion } from '../lib/use-reduced-motion'
import type { QyWheelSpinResult } from '../types'
import { QyWheelFace } from './wheel-face'

/** 需要用户补输支付密码的两个 code。与报名弹窗同一组。 */
const PAY_PASSWORD_CODES = new Set(['qy_pay_pwd_required', 'qy_pay_pwd_wrong'])

/**
 * 转一次。
 *
 * ## 幂等键与客户端种子都在打开弹窗那一刻定死
 *
 * `client_request_id` 每次**打开**生成一次、重试沿用 —— 这是整条链路上唯一能把
 * "同一次意图的两次请求"归并起来的东西；在 `mutationFn` 里生成会让每次重试都
 * 变成一次新的转动（真的再扣一笔）。同一个请求号原样重放拿回的是**原来那一转**
 * （`replayed: true`），服务端不会再摇；换了 `client_seed` 再用同一个请求号是 409。
 *
 * `client_seed` 默认随机生成 16 位、允许改。它进票面原像 `HMAC(seed, act_no ‖ seq
 * ‖ client_seed)` 与链原像，是这一转里**唯一由用户决定**的输入，回执与「我的转动」
 * 都原样带着它 —— 事后拿证据链复算这一转要用。它不承担任何公正性：能读到种子的
 * 人本来就能挑一份种子（decisions.md D-13），协议只保证可复算。
 *
 * ## 验密不猜
 *
 * 阈值只有后端知道（`pay_password_threshold_stardust`）。先不带密码提交，吃到
 * `qy_pay_pwd_required` 才把那一格显示出来，第二次带上密码、请求号不变。
 *
 * ## 结果先转再说
 *
 * 响应回来时结果已经落定（钱已扣、奖已到账），盘面只是把 `ppm` 那一点转到指针下
 * 再把文字说出来 —— 它是**展示**，不是产生结果的原因。`prefers-reduced-motion`
 * 下不旋转，直接显示。
 */
export function QyWheelSpinDialog(props: {
  activity: QyLotActivityDetail
  open: boolean
  onOpenChange: (open: boolean) => void
  /** 活动此刻仍受理新转动：结果屏才给「再转一次」。 */
  canSpinAgain: boolean
}) {
  const { activity } = props
  const { t } = useTranslation()
  const unit = useStardustName()
  const queryClient = useQueryClient()
  const { copyToClipboard } = useCopyToClipboard()
  const reducedMotion = useQyReducedMotion()
  const seedId = useId()

  const [requestId, setRequestId] = useState('')
  const [clientSeed, setClientSeed] = useState('')
  const [payPassword, setPayPassword] = useState('')
  const [needsPayPassword, setNeedsPayPassword] = useState(false)
  const [payPasswordBlocked, setPayPasswordBlocked] = useState(false)
  const [result, setResult] = useState<QyWheelSpinResult | null>(null)
  const [revealed, setRevealed] = useState(false)
  const [addressSaved, setAddressSaved] = useState(false)

  // 每次打开（以及「再转一次」）重置：请求号与种子在这一刻定死，后续重试沿用。
  //
  // 密码格的初值取详情页下发的 `pay_password_required`（按活动的基准参与费判定，
  // 转盘每一转扣的正是这个数）：后端明说要验时第一次就把格子摆出来，省一次必然
  // 失败的往返；没说要时不猜，吃到 `qy_pay_pwd_required` 再显示。「再转一次」
  // 保留这一格（这一场要不要验密不会在两转之间变），只清掉已输入的密码。
  const payPasswordRequired = activity.pay_password_required === true
  const reset = useCallback(
    (keepPasswordGate: boolean) => {
      setRequestId(crypto.randomUUID())
      setClientSeed(qyWheelRandomClientSeed())
      setPayPassword('')
      if (!keepPasswordGate) setNeedsPayPassword(payPasswordRequired)
      setResult(null)
      setRevealed(false)
      setAddressSaved(false)
    },
    [payPasswordRequired]
  )
  useEffect(() => {
    if (props.open) reset(false)
  }, [props.open, reset])

  const mutation = useMutation({
    mutationFn: () =>
      spinQyWheel(activity.act_no, {
        client_request_id: requestId,
        client_seed: clientSeed,
        pay_password: needsPayPassword ? payPassword : undefined,
      }),
    onSuccess: (data) => {
      setResult(data)
      // 余额、各档库存、我的转动、活动计数都变了：qy 的 key 统一以 'qy' 开头，
      // 正是为了这一刻能全量失效而不是逐个猜。
      void queryClient.invalidateQueries({ queryKey: qyKeys.all })
    },
    onError: (error) => {
      if (
        isQyError(error) &&
        error.code != null &&
        PAY_PASSWORD_CODES.has(error.code)
      ) {
        // 第一次被要求验密不是一次失败，只是"这一格现在要填"——不弹红色 toast；
        // 密码输错了（第二次起）才提示。
        const firstAsk = !needsPayPassword
        setNeedsPayPassword(true)
        if (firstAsk && error.code === 'qy_pay_pwd_required') return
      }
      toast.error(qyErrorMessage(error, t))
    },
  })

  const settle = useCallback(() => setRevealed(true), [])

  const seedValid = isQyWheelClientSeedValid(clientSeed)
  const canSubmit =
    !mutation.isPending &&
    requestId !== '' &&
    seedValid &&
    (!needsPayPassword || (!payPasswordBlocked && payPassword.length > 0))

  const balance = activity.stardust_balance
  const balanceShort = balance != null && balance < activity.stake_quota

  // 中的那一档（商品奖要看它的形态）与当场生成的商城订单号；非商品奖两者皆空。
  const wonTier =
    result != null && qyWheelOutcomeOf(result) === 'won'
      ? qyLotTiers(activity.spec).find(
          (item) => item.tier === result.result_tier
        )
      : undefined
  const prizeOrderNo =
    result?.prize_type === 'product' ? (result.mall_order_no ?? '') : ''

  return (
    <QyResponsiveDialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={
        result == null
          ? t('qy_lot_wheel_spin_title')
          : t('qy_lot_wheel_result_title')
      }
      description={
        result == null ? activity.title : t('qy_lot_wheel_result_desc')
      }
      footer={
        result == null ? (
          <>
            <Button
              type='button'
              variant='outline'
              onClick={() => props.onOpenChange(false)}
            >
              {t('qy_common_cancel')}
            </Button>
            <Button
              type='button'
              disabled={!canSubmit}
              onClick={() => mutation.mutate()}
            >
              {t('qy_lot_wheel_spin_confirm')}
            </Button>
          </>
        ) : (
          <>
            {props.canSpinAgain && (
              <Button
                type='button'
                variant='outline'
                disabled={!revealed}
                onClick={() => reset(true)}
              >
                {t('qy_lot_wheel_spin_again')}
              </Button>
            )}
            <Button type='button' onClick={() => props.onOpenChange(false)}>
              {t('qy_common_close')}
            </Button>
          </>
        )
      }
    >
      {result == null ? (
        <div className='space-y-4'>
          <div>
            <QyKeyValue label={t('qy_lot_wheel_stake_label')}>
              <QySdAmount amount={activity.stake_quota} variant='hero' />
            </QyKeyValue>
            {/* 余额与参与费并排：钱从这里扣，"够不够"必须在按下确认之前看得见。
                放行与否仍以后端为准（星屑不足时整笔回滚、不占序号）。 */}
            {balance != null && (
              <QyKeyValue label={t('qy_sd_balance_current', { unit })}>
                <span className='inline-flex flex-wrap items-center gap-2'>
                  <QySdAmount amount={balance} />
                  {balanceShort && (
                    <span className='text-destructive text-xs'>
                      {t('qy_sd_balance_short')}
                    </span>
                  )}
                </span>
              </QyKeyValue>
            )}
          </div>

          <div className='space-y-1'>
            <div className='flex items-center justify-between gap-2'>
              <Label htmlFor={seedId}>{t('qy_lot_wheel_seed_label')}</Label>
              <Button
                type='button'
                variant='ghost'
                size='sm'
                disabled={mutation.isPending}
                onClick={() => setClientSeed(qyWheelRandomClientSeed())}
              >
                <Shuffle aria-hidden='true' />
                {t('qy_lot_wheel_seed_reroll')}
              </Button>
            </div>
            <Input
              id={seedId}
              value={clientSeed}
              maxLength={QY_WHEEL_CLIENT_SEED_MAX}
              autoComplete='off'
              spellCheck={false}
              aria-invalid={!seedValid}
              disabled={mutation.isPending}
              className='font-mono'
              onChange={(event) => setClientSeed(event.target.value)}
            />
            <p className='text-muted-foreground text-xs'>
              {t('qy_lot_wheel_seed_help')}
            </p>
            {!seedValid && (
              <p className='text-destructive text-xs'>
                {t('qy_lot_err_bad_client_seed')}
              </p>
            )}
          </div>

          {/* 不可逆这件事要在按下确认之前说清：每一转的参与费**立即**从星屑扣走，
              结果当场开出，没有撤单、没有反悔、没有"整场取消退款"这一说 ——
              转盘结构上没有流局退款（design-15 §7.3）。 */}
          <Alert>
            <TriangleAlert />
            <AlertDescription>
              {t('qy_lot_wheel_spin_warn_line', {
                amount: formatSdWithUnit(activity.stake_quota, unit),
              })}
            </AlertDescription>
          </Alert>

          {needsPayPassword && (
            <QyPayPasswordField
              value={payPassword}
              onChange={setPayPassword}
              disabled={mutation.isPending}
              onBlockedChange={setPayPasswordBlocked}
            />
          )}
        </div>
      ) : (
        <div className='space-y-4'>
          <div className='relative'>
            <QyWheelFace
              spec={activity.spec}
              targetPpm={result.ppm}
              reducedMotion={reducedMotion}
              onSettled={settle}
            />
            {/* 中了才放粒子：落空与"摇中但已发完"不是值得庆祝的事。
                key 绑到 entry_no，「再转一次」中了会再放一次。 */}
            {revealed && qyWheelOutcomeOf(result) === 'won' && (
              <QyBurst key={result.entry_no} className='top-0 max-h-64' />
            )}
          </div>
          <div aria-live='polite'>
            {revealed ? (
              <QyWheelOutcome activity={activity} result={result} />
            ) : (
              <p className='text-muted-foreground text-center text-sm'>
                {t('qy_lot_wheel_spinning')}
              </p>
            )}
          </div>

          {/* 实物商品奖：收货地址就在结果屏上填，不必先去找订单。这张 0 星屑的
              订单在中奖那一刻已经生成，没有地址它永远发不出去；表单与「我的订单」
              详情里那份是同一个组件，填过一次之后这里换成一句"已填写"。 */}
          {revealed &&
            prizeOrderNo !== '' &&
            wonTier?.product_kind === 'physical' &&
            (addressSaved ? (
              <p className='text-muted-foreground rounded-lg border p-3 text-sm'>
                {t('qy_ml_prize_address_done')}
              </p>
            ) : (
              <QyMallPrizeAddressForm
                orderNo={prizeOrderNo}
                onSaved={() => setAddressSaved(true)}
              />
            ))}

          {revealed && (
            <>
              {/* 「留好凭据」是解释而不是决策：折进说明里，凭据本身仍在明面上。 */}
              <QyLotFinePrint>
                <p>{t('qy_lot_wheel_receipt_keep')}</p>
              </QyLotFinePrint>
              <div className='qy-fx-rise'>
                <QyKeyValue label={t('qy_lot_wheel_seq')}>
                  <span className='tabular-nums'>#{result.seq}</span>
                </QyKeyValue>
                <QyKeyValue label={t('qy_lot_wheel_entry_no')}>
                  <span className='font-mono text-xs break-all'>
                    {result.entry_no}
                  </span>
                </QyKeyValue>
                <QyKeyValue label={t('qy_lot_wheel_ppm')}>
                  <span className='tabular-nums'>{result.ppm}</span>
                </QyKeyValue>
                <QyKeyValue label={t('qy_lot_wheel_seed_label')}>
                  <span className='font-mono text-xs break-all'>
                    {clientSeed === ''
                      ? t('qy_lot_wheel_seed_empty')
                      : clientSeed}
                  </span>
                </QyKeyValue>
                <QyKeyValue label={t('qy_lot_wheel_chain_hash')}>
                  <span className='font-mono text-xs break-all'>
                    {result.chain_head}
                  </span>
                </QyKeyValue>
              </div>
              <Button
                type='button'
                variant='outline'
                size='sm'
                onClick={() => {
                  void copyToClipboard(
                    [
                      `act_no=${activity.act_no}`,
                      `seq=${result.seq}`,
                      `entry_no=${result.entry_no}`,
                      `client_seed=${clientSeed}`,
                      `ppm=${result.ppm}`,
                      `result_tier=${result.result_tier}`,
                      `exhausted_tier=${result.exhausted_tier}`,
                      `chain_hash=${result.chain_head}`,
                    ].join('\n')
                  )
                }}
              >
                <Copy aria-hidden='true' />
                {t('qy_lot_receipt_copy')}
              </Button>
            </>
          )}
        </div>
      )}
    </QyResponsiveDialog>
  )
}

/**
 * 三种结局：一个图形 + 一行标题 + 一个数额。
 *
 * 「摇中了但已发完」尤其不能塌进「谢谢参与」：它是转盘特有的结局，证据链里的
 * `exhausted_tier` 正是它为真的证据 —— 用户拿这一位去复算，就能证明"落空是因为
 * 没货、不是平台改了结果"。三种结局三个图形（奖杯 / 空箱 / 星屑），不看字也
 * 分得开；落空那一句"为什么"仍然写出来，那是这一转的结论而不是装饰。
 */
const OUTCOME_ICON = {
  won: Trophy,
  exhausted: PackageX,
  none: Sparkles,
} as const

/** 商品奖按形态各说一句"接下来"。未知形态回落到通用那句（去订单里看）。 */
const PRODUCT_WON_NOTE: Record<string, string> = {
  plan: 'qy_lot_wheel_won_product_plan_note',
  code: 'qy_lot_wheel_won_product_code_note',
  physical: 'qy_lot_wheel_won_product_physical_note',
}

function productWonNoteKey(kind: string): string {
  return PRODUCT_WON_NOTE[kind] ?? 'qy_lot_wheel_won_product_note'
}

function QyWheelOutcome(props: {
  activity: QyLotActivityDetail
  result: QyWheelSpinResult
}) {
  const { t } = useTranslation()
  const { result } = props
  const outcome = qyWheelOutcomeOf(result)
  const tierNo = outcome === 'won' ? result.result_tier : result.exhausted_tier
  const name = qyWheelTierName(props.activity.spec, tierNo)
  const tier = qyLotTiers(props.activity.spec).find(
    (item) => item.tier === result.result_tier
  )
  const Icon = OUTCOME_ICON[outcome]

  return (
    <Alert className='qy-fx-rise' data-outcome={outcome}>
      <Icon aria-hidden='true' />
      <AlertTitle className='text-base'>
        {t(QY_WHEEL_OUTCOME_I18N[outcome], { name })}
      </AlertTitle>
      <AlertDescription className='space-y-1'>
        {outcome === 'won' && result.prize_type === 'quota' && (
          <span className='inline-flex flex-wrap items-baseline gap-2'>
            <QySdAmount
              amount={result.amount}
              variant='hero'
              className='text-2xl'
            />
            <span>{t('qy_lot_wheel_won_quota_note')}</span>
          </span>
        )}
        {outcome === 'won' && result.prize_type === 'text' && (
          <span className='inline-flex flex-col gap-1'>
            <Badge variant='outline' className='w-fit'>
              {t('qy_lot_prize_type_text')}
            </Badge>
            {(tier?.text_desc ?? '') !== '' && (
              <span className='break-words whitespace-pre-wrap'>
                {tier?.text_desc}
              </span>
            )}
            <span>{t('qy_lot_wheel_won_text_note')}</span>
          </span>
        )}
        {/* 商品奖：中的是哪件商品、什么形态，三种形态各说各的下一步 ——
            套餐已生效 / 兑换码到订单里验密看 / 实物先填地址。订单当场就生成了，
            「去查看」直接落到那一单上。 */}
        {outcome === 'won' && result.prize_type === 'product' && (
          <span className='inline-flex flex-col gap-1.5'>
            <span className='inline-flex flex-wrap items-center gap-1.5'>
              <QyMallKindBadge kind={tier?.product_kind ?? ''} />
              <span className='font-medium break-words'>
                {tier?.product_title || result.product_no}
              </span>
            </span>
            <span>{t(productWonNoteKey(tier?.product_kind ?? ''))}</span>
            {(result.mall_order_no ?? '') !== '' && (
              <Button
                type='button'
                variant='outline'
                size='sm'
                className='w-fit'
                render={
                  <Link {...qyMallOrderLink(result.mall_order_no ?? '')} />
                }
              >
                <ExternalLink aria-hidden='true' />
                {t('qy_lot_wheel_won_product_view')}
              </Button>
            )}
          </span>
        )}
        {outcome === 'exhausted' && (
          <span>{t('qy_lot_wheel_exhausted_note')}</span>
        )}
        {result.replayed && <span>{t('qy_lot_wheel_replayed_note')}</span>}
      </AlertDescription>
    </Alert>
  )
}
