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
import { useMutation } from '@tanstack/react-query'
import { ShieldAlert } from 'lucide-react'
import { useCallback, useEffect, useId, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { CopyButton } from '@/components/copy-button'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  SecureVerificationDialog,
  useSecureVerification,
} from '@/features/auth/secure-verification'

import { QyResponsiveDialog } from '../../../components/qy-responsive-dialog'
import { qyErrorMessage } from '../../../lib/api'
import { qyPayeeChannelKey } from '../../withdraw/lib/payee-spec'
import type { QyPayeePlain } from '../../withdraw/types'
import { qyRevealPayee } from '../api'

/** 后端 `handleAdminRevealPayee` 要求事由至少 4 个字符。 */
const MIN_REASON_RUNES = 4

type RevealPayeeDialogProps = {
  withdrawalId: number | null
  onClose: () => void
}

/**
 * 收款信息明文查看。
 *
 * 这是**被审计的高敏操作**，所以流程刻意做成两步：先填事由并确认，才请求明文。
 * 一键直出的话，管理员滑过列表时的随手点击会和真正的核对混在一起，
 * 事后的 `qy_pii_audits` 就失去了区分能力。
 *
 * 明文只存在于本组件的内存里，关闭即丢弃：不写进 react-query 缓存，
 * 避免它在其他页面被无意间读到，也避免刷新时被自动重放。
 */
export function RevealPayeeDialog(props: RevealPayeeDialogProps) {
  const { t } = useTranslation()
  const reasonId = useId()
  const [reason, setReason] = useState('')
  const [plain, setPlain] = useState<QyPayeePlain | null>(null)

  // 换一张单必须清空上一张的明文与事由，否则会出现"看的是 A 的单、
  // 屏幕上还留着 B 的银行卡号"。
  useEffect(() => {
    setReason('')
    setPlain(null)
  }, [props.withdrawalId])

  const {
    open: verificationOpen,
    methods: verificationMethods,
    state: verificationState,
    executeVerification,
    withVerification,
    cancel: cancelVerification,
    setCode: setVerificationCode,
    switchMethod: switchVerificationMethod,
  } = useSecureVerification()

  const revealMutation = useMutation({
    mutationFn: qyRevealPayee,
    onSuccess: (data) => setPlain(data),
    onError: (error) => toast.error(qyErrorMessage(error, t)),
  })

  const reasonTooShort = [...reason.trim()].length < MIN_REASON_RUNES

  /**
   * 明文必须先过一次 2FA / Passkey。
   *
   * 后端 `handleAdminRevealPayee` 把安全证明放在最前面，没有证明一律 403 ——
   * 所以这里不是"顺手加一层"，而是这条路唯一能走通的形状。证明按
   * `withdraw.payee.read` 这个 scope 单独签发，用完即弃：`useSecureVerification`
   * 只把它作为局部变量传给一次调用，不落 localStorage、不进 react-query 缓存。
   */
  const requestReveal = useCallback(
    async (proofToken?: string) => {
      if (props.withdrawalId == null) return
      if (!proofToken) throw new Error(t('qy_wd_reveal_proof_required'))
      return revealMutation.mutateAsync({
        id: props.withdrawalId,
        reason: reason.trim(),
        proofToken,
      })
    },
    [props.withdrawalId, reason, revealMutation, t]
  )

  return (
    <QyResponsiveDialog
      open={props.withdrawalId != null}
      onOpenChange={(open) => {
        if (!open) props.onClose()
      }}
      title={t('qy_wd_reveal_title')}
      description={t('qy_wd_reveal_desc')}
    >
      <div className='space-y-4'>
        <Alert variant='destructive'>
          <ShieldAlert />
          <AlertTitle>{t('qy_wd_reveal_audit_title')}</AlertTitle>
          <AlertDescription>{t('qy_wd_reveal_audit_desc')}</AlertDescription>
        </Alert>

        {plain == null ? (
          <div className='space-y-2'>
            <Label htmlFor={reasonId}>{t('qy_wd_reveal_reason')}</Label>
            <Input
              id={reasonId}
              value={reason}
              autoComplete='off'
              placeholder={t('qy_wd_reveal_reason_ph')}
              onChange={(event) => setReason(event.target.value)}
            />
            <p className='text-muted-foreground text-xs'>
              {t('qy_wd_reveal_reason_hint', { min: MIN_REASON_RUNES })}
            </p>
            <Button
              variant='destructive'
              disabled={reasonTooShort || revealMutation.isPending}
              onClick={() => {
                if (props.withdrawalId == null) return
                void withVerification(requestReveal, {
                  scope: 'withdraw.payee.read',
                  preferredMethod: 'passkey',
                  title: t('qy_wd_reveal_verify_title'),
                  description: t('qy_wd_reveal_verify_desc'),
                }).catch(() => {
                  // withVerification 失败时 hook 自己已经 toast 过一次，
                  // 这里只负责别让未捕获的 rejection 冒到控制台。
                })
              }}
            >
              {t('qy_wd_reveal_submit')}
            </Button>
          </div>
        ) : (
          <dl className='divide-border divide-y text-sm'>
            <div className='flex items-center justify-between gap-3 py-1.5'>
              <dt className='text-muted-foreground'>{t('qy_wd_channel')}</dt>
              <dd>{t(qyPayeeChannelKey(plain.channel), plain.channel)}</dd>
            </div>
            {Object.entries(plain.payee).map(([key, value]) => (
              <div
                key={key}
                className='flex items-center justify-between gap-3 py-1.5'
              >
                <dt className='text-muted-foreground'>
                  {t(`qy_wd_f_${key}`, key)}
                </dt>
                <dd className='flex min-w-0 items-center gap-1'>
                  <span className='min-w-0 truncate font-mono'>{value}</span>
                  <CopyButton
                    value={value}
                    className='size-6'
                    iconClassName='size-3'
                  />
                </dd>
              </div>
            ))}
          </dl>
        )}
      </div>

      <SecureVerificationDialog
        open={verificationOpen}
        onOpenChange={(open) => {
          if (!open) cancelVerification()
        }}
        methods={verificationMethods}
        state={verificationState}
        onVerify={async (method, code) => {
          await executeVerification(method, code)
        }}
        onCancel={cancelVerification}
        onCodeChange={setVerificationCode}
        onMethodChange={switchVerificationMethod}
      />
    </QyResponsiveDialog>
  )
}
