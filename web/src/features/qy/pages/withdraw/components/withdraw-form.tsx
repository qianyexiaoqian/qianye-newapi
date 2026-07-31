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
import { useMutation, useQuery } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { Info } from 'lucide-react'
import { useId, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { Label } from '@/components/ui/label'
import { RadioGroup, RadioGroupItem } from '@/components/ui/radio-group'
import { Textarea } from '@/components/ui/textarea'
import { cn } from '@/lib/utils'

import { QyAmountInput } from '../../../components/qy-amount-input'
import { QyAmountText } from '../../../components/qy-amount-text'
import { QyConfirmDialog } from '../../../components/qy-confirm-dialog'
import { useQyAfterMoneyChange } from '../../../hooks/use-qy-after-money-change'
import { isQyError, qyErrorMessage } from '../../../lib/api'
import { qyRuneLength } from '../../lib/constants'
import { useQyRequestId } from '../../lib/request-id'
import { qyCreateWithdrawal, qyWithdrawPayeesQuery } from '../api'
import { QY_PAYEE_NEW } from '../lib/payee-spec'
import { normalizeQyPayee, validateQyPayee } from '../lib/validate'
import type {
  QyPayeeChannel,
  QyWithdrawConfig,
  QyWithdrawCreateRequest,
  QyWithdrawMethod,
} from '../types'
import { PayeeSection } from './payee-section'

type WithdrawFormProps = {
  config: QyWithdrawConfig
  degraded: boolean
}

/**
 * 提现申请表单。
 *
 * 方式（quota / fiat）由后端 `withdraw_options.methods` 动态决定，前端不写死：
 * 站点只开了法币时，渲染一个用不了的"兑换额度"选项只会制造工单。
 *
 * 表单状态用 `useState` 而不是 React Hook Form：收款信息的字段集合随渠道变化
 * （支付宝 2 个字段、银行卡 4 个），schema 得跟着渠道重建，用 RHF 反而要为
 * 动态 key 写一层适配。校验逻辑独立在 `lib/validate.ts`，可单独测试。
 */
export function WithdrawForm(props: WithdrawFormProps) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const afterMoneyChange = useQyAfterMoneyChange()
  const requestId = useQyRequestId()
  const remarkId = useId()

  const config = props.config
  const fiatChannels = config.fiat?.payee_channels ?? []

  const [method, setMethod] = useState<QyWithdrawMethod>(
    () => config.methods[0] ?? 'quota'
  )
  const [quota, setQuota] = useState(0)
  const [remark, setRemark] = useState('')
  const [payeeRef, setPayeeRef] = useState<string>(QY_PAYEE_NEW)
  const [channel, setChannel] = useState<QyPayeeChannel>(
    () => fiatChannels[0] ?? 'alipay'
  )
  const [payeeValues, setPayeeValues] = useState<Record<string, string>>({})
  const [payeeErrors, setPayeeErrors] = useState<Record<string, string>>({})
  const [amountErrorKey, setAmountErrorKey] = useState<string | null>(null)
  const [confirmOpen, setConfirmOpen] = useState(false)

  const payeesQuery = useQuery({
    ...qyWithdrawPayeesQuery(),
    // 只有法币路径才需要收款信息；quota 路径拉这个列表等于凭空多一次请求。
    enabled: config.methods.includes('fiat'),
  })
  const accounts = payeesQuery.data ?? []

  const remarkMax = config.remark_max_runes > 0 ? config.remark_max_runes : 200
  const remarkLength = qyRuneLength(remark)
  const outOfDailyCount =
    config.daily_max_count > 0 && config.used_today >= config.daily_max_count
  const canSubmit =
    !props.degraded && !outOfDailyCount && config.withdrawable_quota > 0

  const createMutation = useMutation({
    mutationFn: qyCreateWithdrawal,
    onSuccess: async () => {
      setConfirmOpen(false)
      toast.success(t('qy_wd_ok'))
      requestId.renew()
      setQuota(0)
      setRemark('')
      setPayeeValues({})
      await afterMoneyChange()
      await navigate({ to: '/qy/withdrawals' })
    },
    onError: async (error) => {
      toast.error(qyErrorMessage(error, t))
      if (!isQyError(error)) return
      if (error.kind === 'unavailable' || error.kind === 'rate_limited') return
      if (error.kind === 'conflict') setConfirmOpen(false)
      // 佣金冻结可能已经落库，必须回服务端取真值。
      await afterMoneyChange()
    },
  })

  const buildBody = (): QyWithdrawCreateRequest | null => {
    const body: QyWithdrawCreateRequest = {
      client_request_id: requestId.peek(),
      method,
      quota,
      remark: remark.trim(),
    }
    if (method !== 'fiat') return body
    if (payeeRef !== QY_PAYEE_NEW) {
      body.payee_ref = payeeRef
      return body
    }
    const errors = validateQyPayee(channel, payeeValues)
    if (Object.keys(errors).length > 0) {
      setPayeeErrors(errors)
      return null
    }
    setPayeeErrors({})
    body.payee_channel = channel
    body.payee = normalizeQyPayee(channel, payeeValues)
    return body
  }

  const openConfirm = () => {
    // 金额校验放在打开弹窗之前：让用户在看到"确认提现"之后才被告知
    // "低于最低额度"是最糟糕的顺序。
    if (quota < Math.max(1, config.min_quota)) {
      setAmountErrorKey('qy_wd_err_amount_too_small')
      return
    }
    if (quota > config.withdrawable_quota) {
      setAmountErrorKey('qy_wd_err_amount_exceeds')
      return
    }
    if (remarkLength > remarkMax) {
      setAmountErrorKey(null)
      toast.error(t('qy_wd_err_remark_too_long'))
      return
    }
    setAmountErrorKey(null)
    if (buildBody() == null) return
    requestId.renew()
    setConfirmOpen(true)
  }

  return (
    <Card data-card-hover='false'>
      <CardHeader>
        <CardTitle>{t('qy_wd_form_title')}</CardTitle>
        <CardDescription>{t('qy_wd_form_desc')}</CardDescription>
      </CardHeader>
      <CardContent className='space-y-4'>
        {outOfDailyCount && (
          <Alert variant='destructive'>
            <Info />
            <AlertTitle>{t('qy_wd_daily_limit_title')}</AlertTitle>
            <AlertDescription>
              {t('qy_wd_daily_limit_desc', { max: config.daily_max_count })}
            </AlertDescription>
          </Alert>
        )}

        <div className='space-y-2'>
          <Label>{t('qy_wd_method')}</Label>
          <RadioGroup
            value={method}
            disabled={!canSubmit}
            onValueChange={(value) =>
              setMethod(String(value) as QyWithdrawMethod)
            }
            className='sm:grid-cols-2'
          >
            {config.methods.map((value) => (
              <div
                key={value}
                className={cn(
                  'flex items-start gap-2 rounded-md border p-2.5',
                  method === value && 'border-primary'
                )}
              >
                <RadioGroupItem value={value} id={`wd-method-${value}`} />
                <Label
                  htmlFor={`wd-method-${value}`}
                  className='min-w-0 flex-1 cursor-pointer flex-col items-start gap-0.5 font-normal'
                >
                  <span className='text-sm font-medium'>
                    {t(`qy_wd_m_${value}`)}
                  </span>
                  <span className='text-muted-foreground text-xs'>
                    {t(`qy_wd_m_${value}_desc`)}
                  </span>
                </Label>
              </div>
            ))}
          </RadioGroup>
        </div>

        <div className='space-y-1.5'>
          <Label>{t('qy_wd_amount')}</Label>
          <QyAmountInput
            value={quota}
            onChange={setQuota}
            minQuota={config.min_quota}
            maxQuota={config.withdrawable_quota}
            disabled={!canSubmit}
          />
          {amountErrorKey != null && (
            <p className='text-destructive text-sm'>{t(amountErrorKey)}</p>
          )}
        </div>

        {method === 'fiat' && (
          <>
            <PayeeSection
              channels={fiatChannels}
              accounts={accounts}
              accountMax={config.payee_account_max}
              selectedRef={payeeRef}
              onSelectedRefChange={setPayeeRef}
              channel={channel}
              onChannelChange={setChannel}
              values={payeeValues}
              onValuesChange={setPayeeValues}
              errors={payeeErrors}
              disabled={!canSubmit}
            />
            <FiatRateNotice config={config} />
          </>
        )}

        <div className='space-y-1.5'>
          <Label htmlFor={remarkId}>{t('qy_wd_remark')}</Label>
          <Textarea
            id={remarkId}
            rows={3}
            value={remark}
            disabled={!canSubmit}
            placeholder={t('qy_wd_remark_ph')}
            aria-invalid={remarkLength > remarkMax}
            onChange={(event) => setRemark(event.target.value)}
          />
          {/* 按字符计数（中文/emoji 各算 1），与后端 utf8.RuneCountInString 同口径。 */}
          <p
            className={cn(
              'text-end text-xs tabular-nums',
              remarkLength > remarkMax
                ? 'text-destructive'
                : 'text-muted-foreground'
            )}
          >
            {t('qy_common_rune_counter', {
              used: remarkLength,
              max: remarkMax,
            })}
          </p>
        </div>

        <Button
          disabled={!canSubmit || createMutation.isPending}
          onClick={openConfirm}
        >
          {t('qy_wd_submit')}
        </Button>
      </CardContent>

      <QyConfirmDialog
        open={confirmOpen}
        onOpenChange={setConfirmOpen}
        isLoading={createMutation.isPending}
        title={t('qy_wd_confirm_title')}
        description={t('qy_wd_confirm_desc')}
        confirmText={t('qy_wd_submit')}
        details={
          <dl className='divide-border divide-y text-sm'>
            <ConfirmRow
              label={t('qy_wd_method')}
              value={t(`qy_wd_m_${method}`)}
            />
            <ConfirmRow
              label={t('qy_wd_amount')}
              value={<QyAmountText quota={quota} />}
            />
            {method === 'fiat' && (
              <ConfirmRow
                label={t('qy_wd_fiat_amount')}
                value={t('qy_wd_fiat_amount_pending')}
              />
            )}
          </dl>
        }
        onConfirm={() => {
          const body = buildBody()
          if (body == null) {
            setConfirmOpen(false)
            return
          }
          createMutation.mutate(body)
        }}
      />
    </Card>
  )
}

function ConfirmRow(props: { label: string; value: ReactNode }) {
  return (
    <div className='flex items-center justify-between gap-3 py-1.5 first:pt-0 last:pb-0'>
      <dt className='text-muted-foreground'>{props.label}</dt>
      <dd className='min-w-0 truncate text-right'>{props.value}</dd>
    </div>
  )
}

/**
 * 法币计价参数提示。
 *
 * **刻意不在前端估算到账金额。** 后端下发的 `preview_fx_rate` 只是预览值，
 * 真正生效的是提交那一刻冻结进单据的汇率；而且把 decimal 字符串
 * `parseFloat` 之后再做乘除，得到的尾数与后端的 decimal 运算必然对不上，
 * 用户会拿着一个前端算出来的数字来投诉少给了钱。
 *
 * 提交成功后，单据本身就带着后端算好的 `gross/fee/net`，那才是可以展示的数。
 */
function FiatRateNotice(props: { config: QyWithdrawConfig }) {
  const { t } = useTranslation()
  const fiat = props.config.fiat
  if (fiat == null) return null

  return (
    <Alert>
      <Info />
      <AlertTitle>{t('qy_wd_rate_title')}</AlertTitle>
      <AlertDescription>
        <ul className='list-inside list-disc space-y-0.5'>
          <li>{t('qy_wd_rate_currency', { currency: fiat.currency })}</li>
          {fiat.preview_fx_rate != null && (
            <li>{t('qy_wd_rate_preview', { rate: fiat.preview_fx_rate })}</li>
          )}
          {fiat.fee_bps > 0 && (
            <li>{t('qy_wd_rate_fee', { percent: fiat.fee_bps / 100 })}</li>
          )}
          {fiat.min_amount !== '' && (
            <li>
              {t('qy_wd_rate_min', {
                amount: fiat.min_amount,
                currency: fiat.currency,
              })}
            </li>
          )}
          <li>{t('qy_wd_rate_frozen_note')}</li>
        </ul>
      </AlertDescription>
    </Alert>
  )
}
