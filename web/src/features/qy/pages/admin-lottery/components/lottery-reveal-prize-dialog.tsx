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
import { useEffect, useId, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { CopyButton } from '@/components/copy-button'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'

import { QyResponsiveDialog } from '../../../components/qy-responsive-dialog'
import { qyErrorMessage } from '../../../lib/api'
import { revealQyLotPrizeSecret } from '../api'
import type { QyLotAdminTextPrize, QyLotSupersededSecret } from '../types'

/** 后端要求事由至少 4 个字符（与提现的收款信息揭示同一条口径）。 */
const MIN_REASON_RUNES = 4

/**
 * 兑换码明文查看。
 *
 * 与提现的收款信息揭示逐字同一套流程，理由也一样：这是**被审计的高敏操作**，
 * 所以刻意做成两步 —— 先填事由并确认，才请求明文。一键直出的话，管理员滑过
 * 列表时的随手点击会和真正的核对混在一起，事后的审计流水就失去了区分能力。
 *
 * 明文只存在于本组件的内存里，关闭即丢弃：不写进 react-query 缓存，避免它在
 * 其他页面被无意间读到，也避免刷新时被自动重放。
 */
export function QyLotRevealPrizeDialog(props: {
  onClose: () => void
  target: QyLotAdminTextPrize | null
}) {
  const { t } = useTranslation()
  const reasonId = useId()
  const [reason, setReason] = useState('')
  const [secret, setSecret] = useState<string | null>(null)
  const [superseded, setSuperseded] = useState<QyLotSupersededSecret[]>([])

  useEffect(() => {
    setReason('')
    setSecret(null)
    setSuperseded([])
  }, [props.target?.payout_no])

  const mutation = useMutation({
    mutationFn: revealQyLotPrizeSecret,
    onSuccess: (data) => {
      setSecret(data.secret)
      setSuperseded(data.superseded ?? [])
    },
    onError: (error) => toast.error(qyErrorMessage(error, t)),
  })

  const tooShort = [...reason.trim()].length < MIN_REASON_RUNES

  return (
    <QyResponsiveDialog
      open={props.target != null}
      onOpenChange={(open) => {
        if (!open) props.onClose()
      }}
      title={t('qy_lot_text_reveal_btn')}
    >
      <div className='space-y-4'>
        <Alert variant='destructive'>
          <ShieldAlert />
          <AlertTitle>{t('qy_lot_admin_reveal_audited')}</AlertTitle>
          <AlertDescription>{t('qy_wd_reveal_audit_desc')}</AlertDescription>
        </Alert>

        {secret == null ? (
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
              disabled={tooShort || mutation.isPending}
              onClick={() => {
                if (props.target == null) return
                mutation.mutate({
                  payout_no: props.target.payout_no,
                  reason: reason.trim(),
                })
              }}
            >
              {t('qy_wd_reveal_submit')}
            </Button>
          </div>
        ) : (
          <div className='space-y-3'>
            <div className='flex flex-wrap items-center gap-2 rounded-lg border p-3'>
              <span className='font-mono text-sm break-all'>{secret}</span>
              <CopyButton value={secret} />
            </div>
            {/*
              被顶替过的历史内容。这一段只在真的发生过「履行 → 撤销 → 再次履行」
              时出现，而那正是争议最集中的场景：用户说"我用的那串码失效了"，
              回答它需要看到当初发出去的那一串。
            */}
            {superseded.length > 0 && (
              <div className='space-y-2 rounded-lg border p-3'>
                <p className='text-sm font-medium'>
                  {t('qy_lot_reveal_superseded')}
                </p>
                {superseded.map((row) => (
                  <div
                    key={row.seq}
                    className='flex flex-wrap items-center gap-2'
                  >
                    <span className='text-muted-foreground text-xs'>
                      #{row.seq}
                    </span>
                    <span className='font-mono text-sm break-all'>
                      {row.secret}
                    </span>
                    <CopyButton value={row.secret} />
                  </div>
                ))}
              </div>
            )}
          </div>
        )}
      </div>
    </QyResponsiveDialog>
  )
}
