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
import { useEffect, useId, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'

import { QyResponsiveDialog } from '../../../components/qy-responsive-dialog'
import { qyErrorMessage } from '../../../lib/api'
import { QyKeyValue } from '../../ops/qy-ops-ui'
import { fulfillQyLotPrize } from '../api'
import type { QyLotAdminTextPrize } from '../types'

/**
 * 履行一份文本奖：把实际的兑换码填进去。
 *
 * ## 填进去之后会发生什么
 *
 * 中奖者本人（且只有本人）可以在「我的参与」里看到它。**不发邮件、不调任何
 * 外部接口**：自动发码是一条站在资金路径旁边、失败语义完全不同的新经路，
 * 它要自己的幂等、重试、补偿与转人工出口。不做它，混合奖档才根本不存在
 * "码发了钱没发"这个跨系统两阶段问题。
 *
 * ## 为什么撤销不会撤回明文
 *
 * 用户可能已经看到并用掉了这个码。所以下面那句警告是认真的：填进去这个动作
 * 在实际效果上是**不可逆**的，撤销只能纠正账面。
 */
export function QyLotFulfillDialog(props: {
  onClose: () => void
  onDone: () => void
  target: QyLotAdminTextPrize | null
}) {
  const { t } = useTranslation()
  const secretId = useId()
  const noteId = useId()
  const [secret, setSecret] = useState('')
  const [note, setNote] = useState('')

  // 换一行必须清空上一行的输入，否则会出现"填的是 A 的码、界面上是 B 的单"。
  useEffect(() => {
    setSecret('')
    setNote('')
  }, [props.target?.payout_no])

  const mutation = useMutation({
    mutationFn: fulfillQyLotPrize,
    onSuccess: () => props.onDone(),
    onError: (error) => toast.error(qyErrorMessage(error, t)),
  })

  return (
    <QyResponsiveDialog
      open={props.target != null}
      onOpenChange={(open) => {
        if (!open) props.onClose()
      }}
      title={t('qy_lot_fulfill_btn')}
      description={props.target?.title}
      footer={
        <Button
          disabled={secret.trim() === '' || mutation.isPending}
          onClick={() => {
            if (props.target == null) return
            mutation.mutate({
              payout_no: props.target.payout_no,
              secret: secret.trim(),
              note: note.trim(),
            })
          }}
        >
          {t('qy_lot_fulfill_btn')}
        </Button>
      }
    >
      <div className='space-y-4'>
        {props.target != null && (
          <div>
            <QyKeyValue label={t('qy_lot_payout_no')}>
              <span className='font-mono text-xs'>
                {props.target.payout_no}
              </span>
            </QyKeyValue>
            <QyKeyValue label={t('qy_lot_tier')}>
              {t('qy_lot_tier_no', { no: props.target.tier })}
            </QyKeyValue>
          </div>
        )}

        <div className='space-y-2'>
          <Label htmlFor={secretId}>{t('qy_lot_fulfill_secret')}</Label>
          <Input
            id={secretId}
            value={secret}
            autoComplete='off'
            onChange={(event) => setSecret(event.target.value)}
          />
        </div>

        <div className='space-y-2'>
          <Label htmlFor={noteId}>{t('qy_lot_fulfill_note')}</Label>
          <Textarea
            id={noteId}
            rows={3}
            value={note}
            onChange={(event) => setNote(event.target.value)}
          />
        </div>

        <Alert>
          <AlertDescription>
            {t('qy_lot_text_no_commit_notice')}
          </AlertDescription>
        </Alert>
      </div>
    </QyResponsiveDialog>
  )
}
