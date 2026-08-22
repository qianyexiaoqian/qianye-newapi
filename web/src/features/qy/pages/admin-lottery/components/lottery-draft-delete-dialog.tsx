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
import { useNavigate } from '@tanstack/react-router'
import { useEffect, useId, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'

import { QyResponsiveDialog } from '../../../components/qy-responsive-dialog'
import { qyErrorMessage } from '../../../lib/api'
import { qyKeys } from '../../../lib/query-keys'
import { deleteQyLotActivity } from '../api'
import type { QyLotAdminActivity } from '../types'

/**
 * 删掉一份**草稿**。
 *
 * ## 为什么它不长得像另一个删除弹窗
 *
 * {@link QyLotDeleteDialog}（已结束的场次）列四条代价、要求把活动编号原样敲一遍。
 * 那四条代价里没有一条对草稿成立：
 *
 *   · 证据链：草稿从没对外公布过承诺（`commit_hash` 恒为空串），
 *     公示端点在封盘之前根本不下发，没有任何人验证过它；
 *   · 奖金：草稿收不到一条报名（报名的原子 UPDATE 带着 `status='published'`），
 *     没有一分钱进出；
 *   · 十一张表：草稿期只有活动行、种子、奖档/选项、创建事件那几行；
 *   · 审计遗物：仍然写（后端把审计当删除的前置条件），但它证明的是一份
 *     没人见过的草稿。
 *
 * 把一个零代价的动作套上同样的仪式，只会训练运营对确认框整体失去敏感 ——
 * 而真正需要读完的那一个（删已结束的场次）就在同一排按钮上。所以这一档
 * **不要求回填编号、不强制填理由**，后端对 `draft` 同样不要求（两边一致，
 * 前端这一层从来不是权威）。
 *
 * ## 它不是"软删"
 *
 * 仍然不可逆、仍然写审计、仍然一次清掉九张表。少的只是确认强度，不是后果。
 */
export function QyLotDraftDeleteDialog(props: {
  activity: QyLotAdminActivity
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const reasonId = useId()
  const [reason, setReason] = useState('')

  useEffect(() => {
    if (props.open) setReason('')
  }, [props.open])

  const mutation = useMutation({
    mutationFn: () =>
      deleteQyLotActivity(props.activity.act_no, { reason: reason.trim() }),
    onSuccess: async () => {
      toast.success(t('qy_lot_draft_delete_done'))
      props.onOpenChange(false)
      await queryClient.invalidateQueries({ queryKey: qyKeys.all })
      // 详情页指向的那一行已经不存在了，留在原地只会得到一个 404。
      await navigate({ to: '/qy/admin/lottery' })
    },
    onError: (error) => toast.error(qyErrorMessage(error, t)),
  })

  return (
    <QyResponsiveDialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={t('qy_lot_draft_delete_title')}
      description={props.activity.title}
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
            variant='destructive'
            disabled={mutation.isPending}
            onClick={() => mutation.mutate()}
          >
            {t('qy_lot_draft_delete_confirm')}
          </Button>
        </>
      }
    >
      <div className='space-y-3'>
        {/* 一句话说清"没有什么会消失"，而不是一张吓人的红色清单 ——
            那张清单留给真的会毁掉证据链的那一个。 */}
        <p className='text-sm'>{t('qy_lot_draft_delete_desc')}</p>
        <p className='text-muted-foreground text-xs'>
          {t('qy_lot_draft_delete_irreversible')}
        </p>
        <div className='space-y-1'>
          <Label htmlFor={reasonId}>{t('qy_lot_draft_delete_reason')}</Label>
          <Textarea
            id={reasonId}
            rows={2}
            maxLength={200}
            value={reason}
            onChange={(event) => setReason(event.target.value)}
          />
          <p className='text-muted-foreground text-xs'>
            {t('qy_lot_draft_delete_reason_hint')}
          </p>
        </div>
      </div>
    </QyResponsiveDialog>
  )
}
