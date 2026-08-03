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
import { useCallback, useEffect, useId, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { QyConfirmDialog } from '@/features/qy/components/qy-confirm-dialog'
import { isQyError, qyErrorMessage } from '@/features/qy/lib/api'

import { deletePlan, getPlanUsage } from '../../api'
import type { PlanUsage } from '../../types'
import { useSubscriptions } from '../subscriptions-provider'

/**
 * 删除套餐。两段式：先算影响面，再决定要不要放行。
 *
 * 为什么不能像"停用"那样一句话确认就删：套餐行被删掉之后，上游有两处会**整体**
 * 失败而不是跳过这一条 ——
 *   · 预扣费遍历用户全部 active 订阅时按 plan_id 反查套餐，查不到就 return 整个
 *     事务的错误，于是该用户**其它套餐的订阅**也一起用不了；
 *   · 支付回调在把订单置为 success 之前查套餐，查不到直接返回，钱已经收了但
 *     订阅永远发不出去，订单卡死在 pending。
 * 所以默认（force=false）只要还有活跃订阅或待处理订单就由后端拒绝；管理员确实要
 * 删时必须先看见具体数字，再勾选并写明事由，走 force=true 的级联路径。
 *
 * 事由只在有占用（也就是 force）时收集，与后端的校验口径逐字一致：后端只在
 * `force==true` 时要求 reason 非空。两侧口径一旦相反，删掉一个没人用的空套餐
 * ——最常见的那条删除路径——会 400，而界面上没有任何地方能填事由。
 *
 * 影响面读不到时按钮保持禁用：拿不准有没有占用就按"有"处理，这一步宁可挡住。
 */
export function DeletePlanDialog() {
  const { t } = useTranslation()
  const { open, setOpen, currentRow, triggerRefresh } = useSubscriptions()
  const reasonId = useId()
  const [usage, setUsage] = useState<PlanUsage | null>(null)
  const [usageFailed, setUsageFailed] = useState(false)
  const [reason, setReason] = useState('')
  const [deleting, setDeleting] = useState(false)

  const isOpen = open === 'delete'
  const planId = currentRow?.plan?.id

  const loadUsage = useCallback(
    async (id: number) => {
      try {
        setUsage(await getPlanUsage(id))
        setUsageFailed(false)
      } catch {
        setUsage(null)
        setUsageFailed(true)
      }
    },
    [setUsage, setUsageFailed]
  )

  useEffect(() => {
    if (!isOpen || planId == null || planId <= 0) return
    // 每次重新打开都从零开始：上一条套餐的影响面数字和上一次填的事由留在这里，
    // 会让管理员对着 A 套餐的数字删掉 B 套餐。
    setUsage(null)
    setUsageFailed(false)
    setReason('')
    void loadUsage(planId)
  }, [isOpen, planId, loadUsage])

  if (!isOpen || !currentRow) return null

  const plan = currentRow.plan
  const planLabel = plan.title || `#${plan.id}`
  const occupied =
    usage != null &&
    (usage.active_subscriptions > 0 || usage.pending_orders > 0)
  const loadingUsage = usage == null && !usageFailed
  const reasonMissing = reason.trim() === ''

  const handleConfirm = async () => {
    setDeleting(true)
    try {
      const result = await deletePlan(plan.id, {
        force: occupied,
        reason: reason.trim(),
      })
      toast.success(
        t('qy_plan_delete_ok', {
          subscriptions: result.cancelled_subscriptions,
          orders: result.failed_orders,
        })
      )
      triggerRefresh()
      setOpen(null)
    } catch (error) {
      toast.error(qyErrorMessage(error, t))
      // 409 = 后端在我们读到影响面之后发现又有人买了/下单了。刷新影响面而不是
      // 关闭弹窗：管理员看到的数字必须是刚刚导致拒绝的那一份，否则他会对着一份
      // 过期的"可以安全删除"反复点同一个按钮。刷新后界面自动切到强制删除那一档
      // （出现警示块与事由框）。
      if (isQyError(error) && error.status === 409) {
        await loadUsage(plan.id)
      }
    } finally {
      setDeleting(false)
    }
  }

  return (
    <QyConfirmDialog
      open
      onOpenChange={(nextOpen) => !nextOpen && setOpen(null)}
      title={t('qy_plan_delete_title')}
      description={t('qy_plan_delete_desc', { plan: planLabel })}
      confirmText={
        occupied
          ? t('qy_plan_delete_force_confirm')
          : t('qy_plan_delete_confirm')
      }
      // 有占用才算不可逆：那一档会连带作废别人已付款的订阅，必须走强制勾选。
      // 没占用时删的是一个没人在用的空套餐，多一道勾选只会让人对警示脱敏。
      irreversible={occupied}
      confirmDisabled={
        loadingUsage || usageFailed || (occupied && reasonMissing)
      }
      isLoading={deleting}
      onConfirm={() => void handleConfirm()}
      details={
        // 手机上这一段是全弹窗最高的部分（明细 + 警示 + 事由框）。上游的
        // AlertDialogContent 既没有 max-height 也不滚动，超出的部分会被从上下
        // 两端裁掉且滚不到 —— 事由框或确认按钮可能直接落在视口外。这里把可滚动
        // 区域限制在自己身上，不去改那个被十几个弹窗共用的上游组件。
        <div className='max-h-[45dvh] space-y-3 overflow-y-auto'>
          {loadingUsage && (
            <p className='text-muted-foreground text-sm'>
              {t('qy_plan_delete_checking')}
            </p>
          )}

          {usageFailed && (
            <p className='text-destructive text-sm'>
              {t('qy_plan_delete_check_failed')}
            </p>
          )}

          {usage != null && (
            <dl className='divide-border divide-y rounded-md border px-3 text-sm'>
              <div className='flex justify-between gap-3 py-2'>
                <dt className='text-muted-foreground'>
                  {t('qy_plan_delete_active_subs')}
                </dt>
                <dd className='tabular-nums'>{usage.active_subscriptions}</dd>
              </div>
              <div className='flex justify-between gap-3 py-2'>
                <dt className='text-muted-foreground'>
                  {t('qy_plan_delete_pending_orders')}
                </dt>
                <dd className='tabular-nums'>{usage.pending_orders}</dd>
              </div>
            </dl>
          )}

          {usage != null && !occupied && (
            <p className='text-muted-foreground text-sm'>
              {t('qy_plan_delete_no_impact')}
            </p>
          )}

          {occupied && (
            <div className='border-destructive/40 bg-destructive/5 space-y-3 rounded-md border p-3'>
              <p className='text-destructive flex items-start gap-2 text-sm font-medium'>
                <TriangleAlert className='mt-0.5 size-4 shrink-0' aria-hidden />
                {t('qy_plan_delete_cascade_warning', {
                  subscriptions: usage?.active_subscriptions ?? 0,
                  orders: usage?.pending_orders ?? 0,
                })}
              </p>

              <div className='space-y-1.5'>
                <Label htmlFor={reasonId}>{t('qy_common_reason')}</Label>
                <Textarea
                  id={reasonId}
                  rows={3}
                  value={reason}
                  aria-invalid={reasonMissing}
                  placeholder={t('qy_plan_delete_reason_ph')}
                  onChange={(event) => setReason(event.target.value)}
                />
              </div>
            </div>
          )}
        </div>
      }
    />
  )
}
