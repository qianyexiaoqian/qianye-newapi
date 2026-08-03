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
import { zodResolver } from '@hookform/resolvers/zod'
import { useMutation } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { BookUser, CircleCheck, TriangleAlert } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { z } from 'zod'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import {
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'

import { QyAmountInput } from '../../../components/qy-amount-input'
import { QyAmountText } from '../../../components/qy-amount-text'
import { QyConfirmDialog } from '../../../components/qy-confirm-dialog'
import { QyMaskedUser } from '../../../components/qy-masked-user'
import { QyPayPasswordField } from '../../../components/qy-pay-password-field'
import { useQyAfterMoneyChange } from '../../../hooks/use-qy-after-money-change'
import { isQyError, qyErrorMessage } from '../../../lib/api'
import { qyTabTarget } from '../../../lib/pages'
import { QY_TRANSFER_REMARK_MAX_RUNES, qyRuneLength } from '../../lib/constants'
import { useQyRequestId } from '../../lib/request-id'
import { qyCreateTransfer, qyPreviewTransfer } from '../api'
import { qyTransferBlockedKey } from '../lib/blocked-reason'
import type { QyTransferLimits, QyTransferPreview } from '../types'
import { TransferContactsDialog } from './transfer-contacts'

type TransferFormProps = {
  limits: QyTransferLimits
  /** 扩展库降级时为 true：写接口一定吃 503，按钮直接禁用而不是让用户提交后才发现。 */
  degraded: boolean
}

/**
 * 划转表单。
 *
 * 三段式，缺一不可：
 *   1. 填收款人 + 金额 → `preview` 精确解析并回显**后端脱敏**的收款人；
 *   2. 二次确认弹窗复述"转给谁、多少、手续费多少"，并强制勾选"不可撤销"；
 *   3. 提交时带上打开弹窗那一刻生成的 `client_request_id`，重试沿用同一个。
 *
 * 为什么不能省掉第 1 步直接提交：用户 ID 差一位就是另一个人，而划转一旦成功
 * 就没有任何自助撤回手段。让人在提交前亲眼看到脱敏用户名，是这条链路上唯一
 * 能拦住"转错人"的环节。
 */
export function TransferForm(props: TransferFormProps) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const afterMoneyChange = useQyAfterMoneyChange()
  const requestId = useQyRequestId()

  const [preview, setPreview] = useState<QyTransferPreview | null>(null)
  const [confirmOpen, setConfirmOpen] = useState(false)
  // 支付密码只活在确认弹窗这一屏里(裁决 1:验密点只有执行入口一处)。
  // 不进 react-hook-form:表单值会随预览一起被回填/复用,而密码不该有生命周期
  // 长于"这一次提交"的存放处。弹窗一关就清掉,见下面的 onOpenChange。
  const [payPassword, setPayPassword] = useState('')
  const [payBlocked, setPayBlocked] = useState(false)
  const [contactsOpen, setContactsOpen] = useState(false)

  const limits = props.limits
  const acceptsEmail = limits.recipient_lookup === 'id_or_email'

  const schema = useMemo(
    () =>
      z.object({
        identifier: z
          .string()
          .trim()
          .min(1, 'qy_tr_err_recipient_required')
          // 与后端 maxIdentifierLen 对齐：更长的输入不可能命中任何账号。
          .max(64, 'qy_tr_err_recipient_too_long'),
        quota: z
          .number()
          .int()
          .min(Math.max(1, limits.min_quota), 'qy_tr_err_amount_range')
          .max(
            limits.max_per_tx_quota > 0
              ? limits.max_per_tx_quota
              : Number.MAX_SAFE_INTEGER,
            'qy_tr_err_amount_range'
          ),
        remark: z
          .string()
          .refine(
            (v) => qyRuneLength(v) <= QY_TRANSFER_REMARK_MAX_RUNES,
            'qy_tr_err_remark_too_long'
          ),
      }),
    [limits.max_per_tx_quota, limits.min_quota]
  )

  const form = useForm<z.infer<typeof schema>>({
    resolver: zodResolver(schema),
    defaultValues: { identifier: '', quota: 0, remark: '' },
  })

  // 收款人或金额一改，上一次的预校验结果立刻作废。
  // 不作废的话，用户可以先用 A 的 ID 通过校验、再把输入框改成 B 然后提交 ——
  // 弹窗里复述的仍是 A，而提交的 to_user_id 也还是 A，两边都在骗人。
  useEffect(() => {
    const subscription = form.watch((_, info) => {
      if (info.name === 'identifier' || info.name === 'quota') {
        setPreview(null)
      }
    })
    return () => subscription.unsubscribe()
  }, [form])

  const previewMutation = useMutation({
    mutationFn: qyPreviewTransfer,
    onSuccess: (data) => setPreview(data),
    onError: (error) => {
      setPreview(null)
      toast.error(qyErrorMessage(error, t))
    },
  })

  const createMutation = useMutation({
    mutationFn: qyCreateTransfer,
    onSuccess: async () => {
      setConfirmOpen(false)
      toast.success(t('qy_tr_ok'))
      // 这一笔已经落地，下一笔必须是新的幂等键，否则会被当成重复提交吞掉。
      requestId.renew()
      setPreview(null)
      form.reset({ identifier: '', quota: 0, remark: '' })
      await afterMoneyChange()
      // 划转记录现在就是隔壁那张标签：直接切过去，而不是先跳 /qy/transfer-logs
      // 再被重定向弹回来（那会把整个钱包页卸载重挂一次，看起来是一次白闪）。
      await navigate(qyTabTarget('/qy/transfer-logs'))
    },
    onError: async (error) => {
      toast.error(qyErrorMessage(error, t))
      if (!isQyError(error)) return
      if (error.kind === 'unavailable' || error.kind === 'rate_limited') {
        // 明确没生效：保留表单与幂等键，用户可以直接重试同一笔。
        return
      }
      if (error.kind === 'conflict') {
        setConfirmOpen(false)
      }
      // 其余情况（含 network）后端可能已经生效，必须回服务端取真值，
      // 绝不在本地推测余额。
      await afterMoneyChange()
    },
  })

  const senderBlockedKey = qyTransferBlockedKey(limits.blocked_reason)
  const cooling =
    limits.cooldown_until > 0 &&
    limits.cooldown_until > Math.floor(Date.now() / 1000)
  const outOfDailyCount =
    limits.daily_max_count > 0 && limits.remaining_daily_count <= 0
  const canSubmit =
    !props.degraded && senderBlockedKey == null && !cooling && !outOfDailyCount

  const recipientBlockedKey =
    preview == null || preview.receivable
      ? null
      : (qyTransferBlockedKey(preview.blocked_reason) ??
        'qy_tr_blk_unavailable')

  const remarkLength = qyRuneLength(form.watch('remark') ?? '')

  const openConfirm = () => {
    // 幂等键在"打开确认弹窗"这一刻生成（裁定 C10）。放到点击提交时生成，
    // 一次超时重试就会变成两笔真实划转。
    requestId.renew()
    setConfirmOpen(true)
  }

  return (
    <Card data-card-hover='false'>
      <CardHeader>
        <CardTitle>{t('qy_tr_form_title')}</CardTitle>
        <CardDescription>{t('qy_tr_form_desc')}</CardDescription>
      </CardHeader>
      <CardContent className='space-y-4'>
        {senderBlockedKey != null && (
          <Alert variant='destructive'>
            <TriangleAlert />
            <AlertTitle>{t('qy_tr_blocked_title')}</AlertTitle>
            <AlertDescription>{t(senderBlockedKey)}</AlertDescription>
          </Alert>
        )}

        <Form {...form}>
          <form
            className='space-y-4'
            onSubmit={form.handleSubmit((data) =>
              previewMutation.mutate({
                identifier: data.identifier,
                amount: data.quota,
              })
            )}
          >
            <FormField
              control={form.control}
              name='identifier'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('qy_tr_recipient')}</FormLabel>
                  {/* 通讯录从"平铺在表单上方的一整块"变成这一个按钮
                      （项目方：不要都在一个页面上，过度拉伸页面）。
                      按钮**不受 canSubmit 约束**：冷却中/日限额用尽时仍然可以
                      打开通讯录整理条目，只有"选中填表"那一步是禁用的，
                      由弹窗的 pickDisabled 承担。 */}
                  <div className='flex items-center gap-2'>
                    <FormControl>
                      <Input
                        {...field}
                        autoComplete='off'
                        inputMode={acceptsEmail ? 'text' : 'numeric'}
                        placeholder={
                          acceptsEmail
                            ? t('qy_tr_recipient_ph_id_email')
                            : t('qy_tr_recipient_ph_id')
                        }
                        disabled={!canSubmit}
                      />
                    </FormControl>
                    <Button
                      type='button'
                      variant='outline'
                      size='sm'
                      className='shrink-0'
                      onClick={() => setContactsOpen(true)}
                    >
                      <BookUser aria-hidden='true' />
                      {t('qy_tr_ct_title')}
                    </Button>
                  </div>
                  <FormDescription className='text-xs'>
                    {acceptsEmail
                      ? t('qy_tr_recipient_help_id_email')
                      : t('qy_tr_recipient_help_id')}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />

            <FormField
              control={form.control}
              name='quota'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('qy_common_amount')}</FormLabel>
                  <FormControl>
                    <QyAmountInput
                      value={field.value}
                      onChange={field.onChange}
                      minQuota={limits.min_quota}
                      maxQuota={limits.max_per_tx_quota}
                      disabled={!canSubmit}
                    />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />

            <FormField
              control={form.control}
              name='remark'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('qy_common_remark')}</FormLabel>
                  <FormControl>
                    <Textarea
                      {...field}
                      rows={2}
                      placeholder={t('qy_tr_remark_ph')}
                      disabled={!canSubmit}
                    />
                  </FormControl>
                  <FormDescription className='text-end text-xs tabular-nums'>
                    {t('qy_common_rune_counter', {
                      used: remarkLength,
                      max: QY_TRANSFER_REMARK_MAX_RUNES,
                    })}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />

            <Button
              type='submit'
              disabled={!canSubmit || previewMutation.isPending}
              className='w-full sm:w-auto'
            >
              {previewMutation.isPending
                ? t('qy_tr_checking')
                : t('qy_tr_check_recipient')}
            </Button>
          </form>
        </Form>

        {recipientBlockedKey != null && (
          <Alert variant='destructive'>
            <TriangleAlert />
            <AlertTitle>{t('qy_tr_recipient_blocked')}</AlertTitle>
            <AlertDescription>{t(recipientBlockedKey)}</AlertDescription>
          </Alert>
        )}

        {preview != null && preview.receivable && (
          <div className='border-success/40 bg-success/5 space-y-3 rounded-md border p-3'>
            <p className='text-success flex items-center gap-2 text-sm font-medium'>
              <CircleCheck className='size-4 shrink-0' aria-hidden='true' />
              {t('qy_tr_recipient_found')}
            </p>
            <TransferSummary preview={preview} />
            <Button
              variant='destructive'
              disabled={!canSubmit || createMutation.isPending}
              onClick={openConfirm}
            >
              {t('qy_tr_submit')}
            </Button>
          </div>
        )}
      </CardContent>

      {/* 联系人簿。选中一个联系人**只**把收款人输入框填好 —— 它不是信任
          凭据，之后仍要走 preview → 二次确认 → 提交（支付密码、分组限制、
          日限额）这条完整链路，一步不少（裁决 1）。
          这里刻意用 setValue 写回同一个受控字段，而不是另存一份"已选联系人"
          状态：多一份状态就多一条"显示的是 A、提交的是 B"的路径，
          而上面那个 watch 订阅会在 identifier 变化时作废上一次预校验，
          填表与手输走的是同一套作废逻辑。

          两个弹窗都写在 <Card> 内部而不是页面的插槽外 —— 这一层是普通 div，
          不存在上游 SectionPageLayout「认不出的 children 直接丢掉」那个坑。 */}
      <TransferContactsDialog
        open={contactsOpen}
        onOpenChange={setContactsOpen}
        acceptsEmail={acceptsEmail}
        pickDisabled={!canSubmit}
        degraded={props.degraded}
        onPick={(contact) => {
          form.setValue('identifier', String(contact.user_id), {
            shouldDirty: true,
            shouldValidate: true,
          })
        }}
      />

      <QyConfirmDialog
        open={confirmOpen}
        onOpenChange={(open) => {
          setConfirmOpen(open)
          // 关掉就清密码。留着的话,用户改完金额再打开弹窗会看到一个已填好的
          // 密码格 —— 那正是"刻意动作"退化成"连点两下"的开始。
          if (!open) setPayPassword('')
        }}
        irreversible
        isLoading={createMutation.isPending}
        title={t('qy_tr_confirm_title')}
        description={t('qy_tr_confirm_desc')}
        confirmText={t('qy_tr_confirm_submit')}
        confirmDisabled={payBlocked || payPassword.length === 0}
        details={
          preview == null ? null : (
            <div className='space-y-3'>
              <TransferSummary preview={preview} />
              {/* 支付密码格放在确认弹窗里而不是主表单:它对应的是"这一次提交",
                  而主表单的值会被预览流程反复复用。组件的 props 里没有收款人、
                  也没有联系人 —— 它压根不知道钱要转给谁,也就没有能力分流。 */}
              <QyPayPasswordField
                value={payPassword}
                onChange={setPayPassword}
                disabled={createMutation.isPending}
                onBlockedChange={setPayBlocked}
              />
            </div>
          )
        }
        onConfirm={() => {
          if (preview == null) return
          // 提交那一刻现取表单值，不用渲染期快照：快照会在"改了备注但没触发
          // 重渲染"的边角情况下把旧值发出去。
          const current = form.getValues()
          createMutation.mutate({
            to_user_id: preview.user_id,
            amount: current.quota,
            remark: current.remark,
            client_request_id: requestId.peek(),
            confirm: true,
            pay_password: payPassword,
          })
        }}
      />
    </Card>
  )
}

/**
 * 确认信息复述。
 *
 * 同时出现在页面内的预校验结果与弹窗里，必须是同一份渲染 —— 两处各写一遍
 * 迟早会出现"页面显示含手续费、弹窗显示不含"的分歧。
 */
function TransferSummary(props: { preview: QyTransferPreview }) {
  const { t } = useTranslation()
  const preview = props.preview

  const rows = [
    {
      key: 'recipient',
      label: t('qy_tr_d_recipient'),
      value: (
        <QyMaskedUser
          userId={preview.user_id}
          maskedName={preview.masked_username}
        />
      ),
    },
    {
      key: 'email',
      label: t('qy_tr_d_recipient_email'),
      value: preview.masked_email === '' ? '-' : preview.masked_email,
    },
    {
      key: 'amount',
      label: t('qy_tr_d_amount'),
      value: <QyAmountText quota={preview.amount} />,
    },
    {
      key: 'fee',
      label: t('qy_common_fee'),
      value: <QyAmountText quota={preview.fee_quota} />,
    },
    {
      key: 'total',
      label: t('qy_tr_d_total'),
      value: <QyAmountText quota={preview.total} className='font-semibold' />,
    },
  ]

  return (
    <dl className='divide-border divide-y text-sm'>
      {rows.map((row) => (
        <div
          key={row.key}
          className='flex items-center justify-between gap-3 py-1.5 first:pt-0 last:pb-0'
        >
          <dt className='text-muted-foreground'>{row.label}</dt>
          <dd className='min-w-0 truncate text-right'>{row.value}</dd>
        </div>
      ))}
    </dl>
  )
}
