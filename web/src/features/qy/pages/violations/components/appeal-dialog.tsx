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

import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'

import { QyResponsiveDialog } from '../../../components/qy-responsive-dialog'
import { qyOpsErrorMessage } from '../../ops/errors'
import { createQyViolationAppeal, QY_APPEAL_MIN_RUNES } from '../api'
import type { QyMyViolationRecord } from '../types'

/**
 * 误判申诉。
 *
 * 理由长度用 `Array.from` 数码点而不是 `length`：后端按 rune 计数，
 * 一个 emoji 在 `String.length` 里是 2 而在 rune 里是 1，用 `length`
 * 会让前端放行、后端拒绝，用户对着一个「已经写够了」的表单反复失败。
 */
export function QyViolationAppealDialog(props: {
  record: QyMyViolationRecord | null
  onClose: () => void
  onDone: () => void
}) {
  const { t } = useTranslation()
  const reasonId = useId()
  const [reason, setReason] = useState('')

  useEffect(() => {
    setReason('')
  }, [props.record])

  const runeCount = [...reason.trim()].length
  const tooShort = runeCount < QY_APPEAL_MIN_RUNES

  const mutation = useMutation({
    mutationFn: () =>
      createQyViolationAppeal({
        record_id: props.record?.id ?? 0,
        reason: reason.trim(),
      }),
    onSuccess: () => {
      toast.success(t('qy_vio_my_appeal_sent'))
      props.onDone()
      props.onClose()
    },
    onError: (error) => toast.error(qyOpsErrorMessage(error, t)),
  })

  return (
    <QyResponsiveDialog
      open={props.record != null}
      onOpenChange={(open) => {
        if (!open) props.onClose()
      }}
      title={t('qy_vio_my_appeal_title')}
      description={props.record?.reason}
      footer={
        <>
          <Button type='button' variant='outline' onClick={props.onClose}>
            {t('qy_common_cancel')}
          </Button>
          <Button
            type='button'
            disabled={tooShort || mutation.isPending}
            onClick={() => mutation.mutate()}
          >
            {t('qy_common_submit')}
          </Button>
        </>
      }
    >
      <div className='space-y-2'>
        <Label htmlFor={reasonId}>{t('qy_vio_my_appeal_reason')}</Label>
        <Textarea
          id={reasonId}
          rows={5}
          value={reason}
          onChange={(event) => setReason(event.target.value)}
          placeholder={t('qy_vio_my_appeal_placeholder', {
            min: QY_APPEAL_MIN_RUNES,
          })}
        />
        <p
          className={
            tooShort
              ? 'text-destructive text-xs'
              : 'text-muted-foreground text-xs'
          }
        >
          {/* 参数名刻意不叫 `count`：i18next 见到 count 会走复数解析，
              去找 `_one` / `_other` 后缀的键，平白多一层回落。 */}
          {t('qy_vio_my_appeal_count', {
            n: runeCount,
            min: QY_APPEAL_MIN_RUNES,
          })}
        </p>
      </div>
    </QyResponsiveDialog>
  )
}
