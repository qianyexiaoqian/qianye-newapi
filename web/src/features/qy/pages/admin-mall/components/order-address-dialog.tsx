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

import { QyResponsiveDialog } from '../../../components/qy-responsive-dialog'
import { qyErrorMessage } from '../../../lib/api'
import { revealQyMallOrderAddress } from '../api'
import type { QyMallOrderAddress } from '../types'

/**
 * 收货地址明文。
 *
 * 这是**被审计的高敏读取**（后端 `sensitiveReads`，每次读都写一行审计），所以
 * 刻意做成两步：先看到警示、再按一次"查看"才请求明文。一键直出会让管理员滑过
 * 列表时的随手点击与真正的发货核对混在一起，事后的审计就失去了区分能力。
 *
 * 明文只存在于本组件的内存里，关闭即丢弃：不进 react-query 缓存
 * （`api.ts` 的 `revealQyMallOrderAddress` 刻意不是 queryOptions），换一张单
 * 必须清掉上一张的地址 —— 否则会出现"看的是 A 的单、屏幕上还留着 B 的地址"。
 *
 * 保留期已过的单后端回 410 `qy_ml_address_pruned`：那不是"再试一次"能解决的，
 * 文案已登记在 `QY_ERROR_CODE_I18N`。
 */
export function QyMallOrderAddressDialog(props: {
  orderNo: string | null
  onClose: () => void
}) {
  const { t } = useTranslation()
  const [plain, setPlain] = useState<QyMallOrderAddress | null>(null)

  useEffect(() => {
    setPlain(null)
  }, [props.orderNo])

  const reveal = useMutation({
    mutationFn: () => {
      if (props.orderNo == null) throw new Error('no order')
      return revealQyMallOrderAddress(props.orderNo)
    },
    onSuccess: (data) => setPlain(data),
    onError: (error) => toast.error(qyErrorMessage(error, t)),
  })

  return (
    <QyResponsiveDialog
      open={props.orderNo != null}
      onOpenChange={(open) => {
        if (!open) props.onClose()
      }}
      title={t('qy_mladm_address_title')}
      description={props.orderNo ?? undefined}
      contentClassName='sm:max-w-lg'
      footer={
        <>
          <Button type='button' variant='outline' onClick={props.onClose}>
            {t('qy_common_close')}
          </Button>
          {plain == null && (
            <Button
              type='button'
              variant='destructive'
              disabled={reveal.isPending}
              onClick={() => reveal.mutate()}
            >
              {t('qy_mladm_address_reveal')}
            </Button>
          )}
        </>
      }
    >
      <div className='space-y-4'>
        <Alert variant='destructive'>
          <ShieldAlert />
          <AlertTitle>{t('qy_mladm_address_audit_title')}</AlertTitle>
          <AlertDescription>
            {t('qy_mladm_address_audit_desc')}
          </AlertDescription>
        </Alert>

        {plain != null && (
          <dl className='divide-border divide-y text-sm'>
            <div className='flex items-start justify-between gap-3 py-1.5'>
              <dt className='text-muted-foreground shrink-0'>
                {t('qy_ml_address')}
              </dt>
              <dd className='flex min-w-0 items-start gap-1'>
                <span className='min-w-0 break-words whitespace-pre-wrap'>
                  {plain.address}
                </span>
                <CopyButton
                  value={plain.address}
                  className='size-6 shrink-0'
                  iconClassName='size-3'
                />
              </dd>
            </div>
            <div className='flex items-start justify-between gap-3 py-1.5'>
              <dt className='text-muted-foreground shrink-0'>
                {t('qy_ml_contact')}
              </dt>
              <dd className='flex min-w-0 items-start gap-1'>
                <span className='min-w-0 break-words'>{plain.contact}</span>
                <CopyButton
                  value={plain.contact}
                  className='size-6 shrink-0'
                  iconClassName='size-3'
                />
              </dd>
            </div>
          </dl>
        )}
      </div>
    </QyResponsiveDialog>
  )
}
