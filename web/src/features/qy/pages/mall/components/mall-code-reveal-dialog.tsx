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
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { CopyButton } from '@/components/copy-button'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'

import { QyPayPasswordField } from '../../../components/qy-pay-password-field'
import { QyResponsiveDialog } from '../../../components/qy-responsive-dialog'
import { qyErrorMessage } from '../../../lib/api'
import { revealQyMallOrderCode } from '../api'

/**
 * 揭示兑换码。
 *
 * 码是星屑**变现出平台**的出口（可以是任意第三方卡密），所以这一步验支付密码
 * （D-12：`GET /mall/orders/:no/code` 挂 `paypass.Middleware()`，密码走
 * `X-Qy-Pay-Password` 请求头）。验密与揭示是同一次请求：没有"先验密再拿码"
 * 的两段式 —— 那会留下一个已验密的窗口。
 *
 * 码只存在于本组件的内存里，关闭即丢弃：不进 react-query 缓存（`api.ts` 的
 * `revealQyMallOrderCode` 刻意不是 queryOptions），换一张单必须清掉上一张的码
 * 与输入的密码。
 */
export function QyMallCodeRevealDialog(props: {
  orderNo: string | null
  onClose: () => void
}) {
  const { t } = useTranslation()
  const [payPassword, setPayPassword] = useState('')
  const [blocked, setBlocked] = useState(false)
  const [code, setCode] = useState<string | null>(null)

  useEffect(() => {
    setPayPassword('')
    setCode(null)
  }, [props.orderNo])

  const reveal = useMutation({
    mutationFn: () => {
      if (props.orderNo == null) throw new Error('no order')
      return revealQyMallOrderCode(props.orderNo, payPassword)
    },
    onSuccess: (data) => {
      setCode(data.code)
      setPayPassword('')
    },
    onError: (error) => toast.error(qyErrorMessage(error, t)),
  })

  const canReveal =
    props.orderNo != null &&
    !reveal.isPending &&
    !blocked &&
    payPassword.length > 0

  return (
    <QyResponsiveDialog
      open={props.orderNo != null}
      onOpenChange={(open) => {
        if (!open) props.onClose()
      }}
      title={t('qy_ml_code_reveal_title')}
      description={props.orderNo ?? undefined}
      contentClassName='sm:max-w-lg'
      footer={
        code == null ? (
          <>
            <Button type='button' variant='outline' onClick={props.onClose}>
              {t('qy_common_cancel')}
            </Button>
            <Button
              type='button'
              disabled={!canReveal}
              onClick={() => reveal.mutate()}
            >
              {t('qy_ml_code_reveal_submit')}
            </Button>
          </>
        ) : (
          <Button type='button' onClick={props.onClose}>
            {t('qy_common_close')}
          </Button>
        )
      }
    >
      <div className='space-y-4'>
        <Alert>
          <ShieldAlert />
          <AlertTitle>{t('qy_ml_code_reveal_notice_title')}</AlertTitle>
          <AlertDescription>{t('qy_ml_code_reveal_notice')}</AlertDescription>
        </Alert>

        {code == null ? (
          <QyPayPasswordField
            value={payPassword}
            onChange={setPayPassword}
            disabled={reveal.isPending}
            onBlockedChange={setBlocked}
          />
        ) : (
          <div className='space-y-2 rounded-lg border p-3'>
            <div className='flex flex-wrap items-center gap-2'>
              <span className='font-mono text-sm break-all'>{code}</span>
              <CopyButton value={code} />
            </div>
            <p className='text-muted-foreground text-xs'>
              {t('qy_ml_code_reveal_again')}
            </p>
          </div>
        )}
      </div>
    </QyResponsiveDialog>
  )
}
