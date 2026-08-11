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
import { TriangleAlert } from 'lucide-react'
import { useEffect, useId, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { Checkbox } from '@/components/ui/checkbox'
import { Label } from '@/components/ui/label'

type QyConfirmDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  title: ReactNode
  /** 一句话说明本次操作。资金类必须复述"给谁、多少"。 */
  description: ReactNode
  /**
   * 明细区。资金类二次确认应当在这里复述收款人 ID、脱敏用户名、
   * 输入金额与换算后的 quota —— 用户只会看这一屏。
   */
  details?: ReactNode
  /**
   * 不可逆操作。打开后显示醒目警示、按钮变 destructive，
   * 并强制用户勾选确认框才能提交。
   */
  irreversible?: boolean
  /**
   * 覆盖警示块里那句正文。
   *
   * 默认那句是资金口径的（"确认后资金会立即变动"），因为 qy 这边绝大多数不可逆
   * 动作确实在动钱。但"不可逆"不等于"动钱"：清空渠道统计抹掉的是本站自己记的
   * 累计数，谁的余额都不变。照抄资金口径会让人以为按下去要扣钱或退钱，而同屏
   * 另一句说明写的又是"上游的真实用量不会被改动"—— 两句话互相打架，比不说更糟。
   * 所以不可逆但不动钱的动作在这里换掉正文。
   */
  irreversibleDesc?: ReactNode
  confirmText?: string
  cancelText?: string
  isLoading?: boolean
  /**
   * 额外的提交前置条件（与"必须勾选"是并列关系，两者都满足才能提交）。
   *
   * 用途是 details 区里放了必填输入时——例如划转的支付密码格。
   * 这只是不让用户白按一次，**不是权限判断**：真正说了算的永远是后端。
   */
  confirmDisabled?: boolean
  onConfirm: () => void
}

/**
 * qy 的二次确认弹窗。
 *
 * 在上游 `ConfirmDialog` 之上只加两件事：不可逆警示样式，以及"必须勾选才能
 * 提交"的闸门。资金操作没有撤销键，多一次刻意动作换来的是少一次误转。
 */
export function QyConfirmDialog(props: QyConfirmDialogProps) {
  const { t } = useTranslation()
  const acknowledgeId = useId()
  const [acknowledged, setAcknowledged] = useState(false)

  // 每次重新打开都要求重新勾选：上一次的勾选状态残留会让"刻意动作"退化成
  // 一次无意义的渲染。
  useEffect(() => {
    if (props.open) setAcknowledged(false)
  }, [props.open])

  const needsAcknowledge = props.irreversible === true

  return (
    <ConfirmDialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      // 上游 AlertDialogContent 是 `fixed top-1/2 -translate-y-1/2 grid`，
      // **既没有 max-height 也没有 overflow**。内容一旦高过视口，上下两端就被
      // 等量裁掉且滚不到 —— 而这个组件专门用于不可逆操作：被裁掉的恰恰是底部的
      // 强制勾选框与确认键，用户既提交不了也点不到取消（只剩 Esc）。
      //
      // 触发它不需要什么极端条件：矮屏（≤600px 高）上一次「批量删除 20 个渠道」
      // 就是标题 + 说明 + 渠道名清单 + 不可逆警示块 + 竖排按钮 ≈ 600px。
      //
      // QyResponsiveDialog 早就是这个形状（`max-h-[calc(100dvh-2rem)]` + 内部滚动），
      // 只是 QyConfirmDialog 这条路没接上。className 是上游 ConfirmDialog 自己
      // 暴露的入参，所以这里补齐**不需要改任何上游文件**。
      className='max-h-[calc(100dvh-2rem)] overflow-y-auto'
      title={props.title}
      desc={<div className='space-y-3'>{props.description}</div>}
      destructive={props.irreversible}
      isLoading={props.isLoading}
      disabled={
        (needsAcknowledge && !acknowledged) || props.confirmDisabled === true
      }
      confirmText={props.confirmText}
      cancelBtnText={props.cancelText}
      handleConfirm={props.onConfirm}
    >
      <div className='space-y-3'>
        {props.details}
        {needsAcknowledge && (
          <div className='border-destructive/40 bg-destructive/5 space-y-2 rounded-md border p-3'>
            <p className='text-destructive flex items-center gap-2 text-sm font-medium'>
              <TriangleAlert className='size-4 shrink-0' aria-hidden='true' />
              {t('qy_common_irreversible')}
            </p>
            <p className='text-muted-foreground text-xs'>
              {props.irreversibleDesc ?? t('qy_common_irreversible_desc')}
            </p>
            <Label htmlFor={acknowledgeId} className='text-sm font-normal'>
              <Checkbox
                id={acknowledgeId}
                checked={acknowledged}
                onCheckedChange={(checked) => setAcknowledged(checked === true)}
              />
              {t('qy_common_acknowledge')}
            </Label>
          </div>
        )}
      </div>
    </ConfirmDialog>
  )
}
