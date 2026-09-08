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
import { qyKeys } from '../../../lib/query-keys'
import { QyKeyValue } from '../../ops/qy-ops-ui'
import { takeQyMallCode } from '../api'
import type { QyMallAdminCode } from '../types'

/**
 * 管理员提卡：把一枚未使用的码从库里提走。
 *
 * ## 为什么要验支付密码
 *
 * 与用户端揭示自己买到的码同一档（D-12，密码走 `X-Qy-Pay-Password` 请求头）：
 * 码可以是任意第三方卡密，一次揭示即离开平台。一个被盗的管理员会话不该只凭
 * cookie 就能把整库码捞走。验密与揭示是**同一次请求** —— 没有"先验密再拿码"
 * 的两段式，那会留下一个已验密的窗口。
 *
 * ## 提走就是出库
 *
 * 成功之后那一枚变成 `taken`：不再计入可售库存，也不会再发给任何用户。这句话
 * 必须写在按下之前的正文里 —— 运营点"提卡"时想的往往是"我看一眼"，而这个动作
 * 不可撤销。
 *
 * ## 明文只活在这个组件的内存里
 *
 * 不进 react-query 缓存（`takeQyMallCode` 刻意不是 queryOptions）、不进
 * localStorage，换一枚码 / 关窗即丢弃。
 */
export function QyMallCodeTakeDialog(props: {
  productNo: string
  code: QyMallAdminCode | null
  onClose: () => void
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [payPassword, setPayPassword] = useState('')
  const [blocked, setBlocked] = useState(false)
  const [plain, setPlain] = useState<string | null>(null)

  const codeId = props.code?.id ?? null
  useEffect(() => {
    setPayPassword('')
    setPlain(null)
  }, [codeId])

  const take = useMutation({
    mutationFn: () => {
      if (props.code == null) throw new Error('no code')
      return takeQyMallCode(props.productNo, props.code.id, payPassword)
    },
    onSuccess: async (data) => {
      setPlain(data.code)
      setPayPassword('')
      // 列表与商品库存计数都要刷：这一枚已经不在可售库存里了。
      await queryClient.invalidateQueries({ queryKey: qyKeys.all })
    },
    onError: (error) => toast.error(qyErrorMessage(error, t)),
  })

  const canTake =
    props.code != null && !take.isPending && !blocked && payPassword.length > 0

  return (
    <QyResponsiveDialog
      open={props.code != null}
      onOpenChange={(open) => {
        if (!open) props.onClose()
      }}
      dismissible={plain == null}
      title={t('qy_mladm_code_take_title')}
      contentClassName='sm:max-w-lg'
      footer={
        plain == null ? (
          <>
            <Button type='button' variant='outline' onClick={props.onClose}>
              {t('qy_common_cancel')}
            </Button>
            <Button
              type='button'
              disabled={!canTake}
              onClick={() => take.mutate()}
            >
              {t('qy_mladm_code_take_submit')}
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
          <AlertTitle>{t('qy_mladm_code_take_notice_title')}</AlertTitle>
          <AlertDescription>
            {t('qy_mladm_code_take_notice')}
          </AlertDescription>
        </Alert>

        {props.code != null && (
          <div>
            <QyKeyValue label={t('qy_mladm_code_id')}>
              <span className='font-mono text-xs'>#{props.code.id}</span>
            </QyKeyValue>
          </div>
        )}

        {plain == null ? (
          <QyPayPasswordField
            value={payPassword}
            onChange={setPayPassword}
            disabled={take.isPending}
            onBlockedChange={setBlocked}
          />
        ) : (
          <div className='space-y-2 rounded-lg border p-3'>
            <div className='flex flex-wrap items-center gap-2'>
              <span className='font-mono text-sm break-all'>{plain}</span>
              <CopyButton value={plain} />
            </div>
            <p className='text-muted-foreground text-xs'>
              {t('qy_mladm_code_take_again')}
            </p>
          </div>
        )}
      </div>
    </QyResponsiveDialog>
  )
}
