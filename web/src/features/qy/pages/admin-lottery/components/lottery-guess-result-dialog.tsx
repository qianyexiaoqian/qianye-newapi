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
import { useEffect, useId, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Label } from '@/components/ui/label'
import { RadioGroup, RadioGroupItem } from '@/components/ui/radio-group'
import { Textarea } from '@/components/ui/textarea'

import { QyResponsiveDialog } from '../../../components/qy-responsive-dialog'
import { qyErrorMessage } from '../../../lib/api'
import { qyArray } from '../../../lib/array'
import { qyKeys } from '../../../lib/query-keys'
import type { QyLotOption } from '../../lottery/types'
import { setQyLotGuessResult } from '../api'
import type { QyLotAdminActivity } from '../types'

/**
 * 录入竞猜结果。
 *
 * ## 这是全模块最大的信任缺口，界面必须承认这一点
 *
 * 抽奖的公正性可以用密码学证明，竞猜不行 —— 没有任何算法能证明"世界杯到底谁
 * 赢了"。能做的只有三条，这个弹窗把三条都落到位：
 *   ① 选项集合与费率在发布时已进承诺，事后加选项会自证不一致；
 *   ② `evidence` 强制填写并对用户公开；
 *   ③ **一经写入不可修改**（后端 CAS `WHERE win_option_id=0`），录错只能整场
 *      作废 + 全额退款 + 公示。
 *
 * 这把作弊面从"悄悄地反复调整"压缩到"一次性地公开撒谎"。所以这里用的是
 * 「勾选确认 + 明写不可改」而不是一个轻飘飘的保存按钮。
 */
export function QyLotGuessResultDialog(props: {
  activity: QyLotAdminActivity
  /** 选项集合独立下发（它是独立的表，与活动行的生命周期不同）。 */
  options: QyLotOption[]
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const { activity } = props
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const ackId = useId()
  const evidenceId = useId()

  const [optNo, setOptNo] = useState(0)
  const [evidence, setEvidence] = useState('')
  const [acknowledged, setAcknowledged] = useState(false)

  useEffect(() => {
    if (!props.open) return
    setOptNo(0)
    setEvidence('')
    setAcknowledged(false)
  }, [props.open])

  const mutation = useMutation({
    mutationFn: () =>
      setQyLotGuessResult(activity.act_no, {
        opt_no: optNo,
        evidence: evidence.trim(),
      }),
    onSuccess: async () => {
      toast.success(t('qy_lot_result_saved'))
      props.onOpenChange(false)
      await queryClient.invalidateQueries({ queryKey: qyKeys.all })
    },
    onError: (error) => toast.error(qyErrorMessage(error, t)),
  })

  const options = qyArray(props.options)

  return (
    <QyResponsiveDialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={t('qy_lot_result_title')}
      description={activity.title}
      footer={
        <>
          <Button
            type='button'
            variant='outline'
            onClick={() => props.onOpenChange(false)}
          >
            {t('qy_common_cancel')}
          </Button>
          <Button
            type='button'
            disabled={
              optNo === 0 ||
              evidence.trim() === '' ||
              !acknowledged ||
              mutation.isPending
            }
            onClick={() => mutation.mutate()}
          >
            {t('qy_lot_result_confirm')}
          </Button>
        </>
      }
    >
      <div className='space-y-4'>
        <Alert variant='destructive'>
          <AlertTitle>{t('qy_lot_result_warn_title')}</AlertTitle>
          <AlertDescription>{t('qy_lot_result_warn_desc')}</AlertDescription>
        </Alert>

        <div className='space-y-2'>
          <Label>{t('qy_lot_result_option')}</Label>
          <RadioGroup
            value={optNo === 0 ? '' : String(optNo)}
            onValueChange={(value) => setOptNo(Number(value ?? 0))}
            className='gap-2'
          >
            {options.map((option) => (
              <label
                key={option.opt_no}
                className='hover:bg-muted/40 flex cursor-pointer items-start gap-3 rounded-lg border p-3'
              >
                <RadioGroupItem
                  value={String(option.opt_no)}
                  className='mt-0.5'
                />
                <span className='min-w-0 flex-1 text-sm break-words'>
                  {option.label}
                </span>
              </label>
            ))}
          </RadioGroup>
        </div>

        <div className='space-y-1'>
          <Label htmlFor={evidenceId}>{t('qy_lot_result_evidence')}</Label>
          <Textarea
            id={evidenceId}
            rows={3}
            maxLength={1000}
            value={evidence}
            onChange={(event) => setEvidence(event.target.value)}
          />
          <p className='text-muted-foreground text-xs'>
            {t('qy_lot_result_evidence_hint')}
          </p>
        </div>

        <label
          htmlFor={ackId}
          className='flex cursor-pointer items-start gap-3 rounded-lg border p-3'
        >
          <Checkbox
            id={ackId}
            checked={acknowledged}
            onCheckedChange={(checked) => setAcknowledged(checked === true)}
          />
          <span className='text-sm'>{t('qy_lot_result_ack')}</span>
        </label>
      </div>
    </QyResponsiveDialog>
  )
}
