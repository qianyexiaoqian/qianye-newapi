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
import { QyPayPasswordField } from '../../../components/qy-pay-password-field'
import { useQyAfterMoneyChange } from '../../../hooks/use-qy-after-money-change'
import { isQyError, qyErrorMessage } from '../../../lib/api'
import { qyTabTarget } from '../../../lib/pages'
import { QyFiatText } from '../../components/qy-fiat-text'
import { qyRuneLength } from '../../lib/constants'
import { useQyRequestId } from '../../lib/request-id'
import { qyCreateWithdrawal, qyWithdrawPayeesQuery } from '../api'
import { QY_PAYEE_NEW } from '../lib/payee-spec'
import { QY_PROOF_NONE, type QyProofSelection } from '../lib/proof'
import { normalizeQyPayee, validateQyPayee } from '../lib/validate'
import type {
  QyPayeeChannel,
  QyWithdrawConfig,
  QyWithdrawCreateRequest,
  QyWithdrawMethod,
} from '../types'
import { PayeeSection } from './payee-section'
import { ProofUploadField } from './proof-upload-field'

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
  const [proof, setProof] = useState<QyProofSelection>(QY_PROOF_NONE)
  /** 提交成功后靠换 key 把上传控件整个重挂，顺带触发它的 revokeObjectURL。 */
  const [proofKey, setProofKey] = useState(0)
  const [payPassword, setPayPassword] = useState('')
  /** 支付密码处于"提交一定失败"的状态（未设置 / 已锁定 / 状态读不到）。 */
  const [payBlocked, setPayBlocked] = useState(false)

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
      setProof(QY_PROOF_NONE)
      setProofKey((value) => value + 1)
      // 支付密码只服务于**这一次**提交，成功后必须立刻从内存里清掉：
      // 留着的话下一笔申请会带着上一次输入的密码静默通过确认弹窗。
      setPayPassword('')
      await afterMoneyChange()
      // 同上：提现记录是「推广佣金」选择夹里的隔壁一张标签。
      await navigate(qyTabTarget('/qy/withdrawals'))
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
      // 提现是钱**离开站点**的那条路，后端 handleCreate 在进事务之前就要验密。
      // 不 trim：支付密码里的空格是密码的一部分，替用户去掉等于改了他的密码。
      pay_password: payPassword,
    }
    // 凭证只服务于法币打款。**quota 单绝不能带 proof_ref** —— 后端
    // `acceptCreate` 对此直接报错而不是静默忽略（静默忽略会让那张图永远停在
    // 未绑定状态：用户以为交了、审核看不到，最后被孤儿清理悄悄删掉）。
    // 因此这一行必须留在下面这个 early return 之后。
    if (method !== 'fiat') return body
    if (proof.ref != null) body.proof_ref = proof.ref
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
            onValueChange={(value) => {
              const next = String(value) as QyWithdrawMethod
              setMethod(next)
              // 切离法币路径时必须把凭证一起丢掉。上传控件挂在 fiat 分支里，
              // 切走即卸载并撤销预览，而 ref 留在这里的话会变成一张
              // "用户已经看不见、却仍会随申请提交上去"的图。
              if (next !== 'fiat') setProof(QY_PROOF_NONE)
            }}
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
            {/* 后端 ProofOn() = methods 含 fiat && proof_enabled，站点没开就整块不渲染。 */}
            {config.fiat?.proof_enabled === true && (
              <ProofUploadField
                key={proofKey}
                fiat={config.fiat}
                disabled={!canSubmit}
                selection={proof}
                onSelectionChange={setProof}
              />
            )}
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

        {/* 上传在途时必须禁掉：放行的话请求会带着一个还没拿到的 ref 走，
            而用户以为凭证已经交上去了。 */}
        <Button
          disabled={!canSubmit || createMutation.isPending || proof.uploading}
          onClick={openConfirm}
        >
          {proof.uploading ? t('qy_wd_proof_uploading') : t('qy_wd_submit')}
        </Button>
      </CardContent>

      <QyConfirmDialog
        open={confirmOpen}
        onOpenChange={setConfirmOpen}
        isLoading={createMutation.isPending}
        title={t('qy_wd_confirm_title')}
        description={t('qy_wd_confirm_desc')}
        confirmText={t('qy_wd_submit')}
        confirmDisabled={payBlocked || payPassword.length === 0}
        details={
          <div className='space-y-3'>
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
            {/* 支付密码格放在确认弹窗里而不是主表单：它对应的是"这一次提交"，
                而主表单的值（金额、收款人、凭证）会被反复修改与复用。
                与划转、抽奖同一个组件、同一个后端闸门 —— 三条出钱路径共用一处
                判定，不存在第二套。 */}
            <QyPayPasswordField
              value={payPassword}
              onChange={setPayPassword}
              disabled={createMutation.isPending}
              onBlockedChange={setPayBlocked}
            />
          </div>
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
            // 方向必须写出来。这一行原来只印一个裸数字「当前参考汇率：1」，
            // 而这个数正是把额度折成法币的那个系数（quota / quota_per_unit ×
            // 它）—— 站点没维护过汇率时它就是 1，页面照样标着 CNY，看起来
            // 完全正常。写成「1 USD = 1 CNY」，配错的那一刻就自己暴露了。
            <li>
              {t('qy_wd_rate_preview', {
                rate: fiat.preview_fx_rate,
                currency: fiat.currency,
              })}
            </li>
          )}
          {fiat.fee_bps > 0 && (
            <li>{t('qy_wd_rate_fee', { percent: fiat.fee_bps / 100 })}</li>
          )}
          {fiat.min_amount !== '' && (
            // 法币口径，不能走额度展示件（那会按当前汇率再折算一次）。
            // 但也不能裸印后端的 decimal 字符串 —— 那是 `10.000000`。
            <li className='inline-flex flex-wrap items-center gap-1'>
              {t('qy_wd_rate_min_label')}
              <QyFiatText amount={fiat.min_amount} currency={fiat.currency} />
            </li>
          )}
          <li>{t('qy_wd_rate_frozen_note')}</li>
        </ul>
      </AlertDescription>
    </Alert>
  )
}
