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
import { Eraser, Power, PowerOff, Trash2 } from 'lucide-react'
import { useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { CHANNEL_STATUS } from '@/features/channels/constants'

import { QyConfirmDialog } from '../components/qy-confirm-dialog'
import { qyErrorMessage } from '../lib/api'
import { qyBatchChannelStatus, qyBatchDeleteChannels } from './api'
import { useQyChannelBatchFinish } from './batch-finish'
import { ChannelNameList, type QyChannelTarget } from './channel-name-list'
import { QyChannelResetUsageDialog } from './reset-usage'
import { QyChannelBatchResultDialog } from './result-dialog'
import { useQyChannelBatchResultStore } from './result-store'

/**
 * 批次报告的挂载点。**必须渲染在 BulkActionsToolbar 之外。**
 *
 * 工具条在选中数归零时 `return null`，而批次收尾的第一件事就是清空选中态 ——
 * 报告若挂在工具条里，它连挂载的机会都没有。理由与取舍见 result-store.ts。
 *
 * 现在它挂在 `channels-table.tsx` 的页面级返回值上，而不是工具条旁边：
 * 直达入口（列表头 / 行内的清零）在「批量操作」开关关着时也能提交，
 * 而那时整个工具条根本没有被渲染 —— 报告挂在工具条旁边等于说"只有开了批量
 * 操作的人才看得到失败明细"。页面级只有一处挂载，也就不会出现两个实例
 * 同时渲染同一份报告。
 */
export function QyChannelBulkResultOutlet() {
  const report = useQyChannelBatchResultStore((s) => s.report)
  const close = useQyChannelBatchResultStore((s) => s.close)

  return (
    <QyChannelBatchResultDialog
      open={report != null}
      onOpenChange={(open) => !open && close()}
      title={report?.title ?? ''}
      result={report?.result ?? null}
    />
  )
}

/**
 * 渠道列表页的批量操作（项目方需求：批量删除 / 批量启用 / 批量重置余额与已用额度）。
 *
 * # 现状与本组件的分工
 *
 * 上游本来就有批量启用、批量停用、批量删除、批量打标签四个按钮，接口也是现成的
 * （`POST /api/channel/status/batch`、`POST /api/channel/batch`）。**没有新造端点
 * 去覆盖它们**能做的事，本组件替换掉前三个按钮只为解决两件上游做不到的事：
 *
 *  1. **逐条结果**。上游三个接口一律只回一个整数 count，"第 7 个失败了"在协议层
 *     就说不出来，前端只能写 `ids.length - count` 去猜。而那个减法在启停这条路上
 *     是错的：`model.UpdateChannelStatus` 对"已经是目标状态"和"真失败"都返回
 *     false，于是对一批本来就启用的渠道点「批量启用」，页面会红着脸报「N 个启用
 *     失败」。本组件的三档计数（成功 / 跳过 / 失败）就是冲这个来的。
 *  2. **危险度分级的二次确认**。上游的删除确认是一个普通对话框，只说"确定要删除
 *     N 个渠道吗"，既不列渠道名也没有刻意动作。删除与重置统计都不可逆，这里走
 *     `QyConfirmDialog` 的 `irreversible`：醒目警示 + 必须勾选才能提交。
 *
 * 打标签按钮留在上游，本组件不碰 —— 它没有上面两个问题。
 *
 * # 四个动作的危险度不是一样的，所以交互也不一样
 *
 *   启用      直接执行。它只会让更多渠道进入路由，做错了再停掉即可
 *   停用      普通二次确认。一批渠道同时退出路由的表现是"某些模型突然没渠道可用"，
 *             值得多问一句，但它可逆，不需要勾选闸门
 *   重置统计  不可逆确认 + 勾选。抹掉的是渠道成本核算的累计值，没有补算路径
 *   删除      不可逆确认 + 勾选 + 列出渠道名
 *
 * # 清零不是只有这一条路
 *
 * 「重置统计」的确认框与提交在 `reset-usage.tsx`，本组件只是它的**第三个**
 * 调用方：另外两个是列表头与行内的直达入口，它们**不经过「批量操作」开关**。
 * 工具条这条路保留是刻意的 —— 已经习惯它的人不该被打断。
 */
export function QyChannelBulkActions(props: {
  /**
   * 当前选中的渠道。
   *
   * `used_quota` 是列表页那一列的原值（quota 整数），确认框据此复述
   * "这一次要抹掉多少钱"。少了它，管理员在按下一个不可逆按钮之前，
   * 屏幕上只有一个条数 —— 而 20 个渠道可能对应 3 块钱，也可能对应 3 万块。
   */
  channels: QyChannelTarget[]
  /** 批次跑完之后清空选中态。 */
  onDone: () => void
  /** 无 `channel:sensitive_write` 时禁用删除与重置。真正说了算的是后端。 */
  canDelete: boolean
}) {
  const { t } = useTranslation()
  const ids = props.channels.map((channel) => channel.id)

  const [confirming, setConfirming] = useState<
    'delete' | 'disable' | 'reset' | null
  >(null)

  /**
   * 启停 / 删除两个动作的收尾。重置那一路的收尾在
   * `QyChannelResetUsageDialog` 里，同样走 `useQyChannelBatchFinish`。
   *
   * **一条都没成功时弹的是红色 toast，不是绿色。** 判据是响应体里的
   * succeeded/failed，而不是 HTTP 状态码好不好看。
   */
  const finish = useQyChannelBatchFinish(() => {
    setConfirming(null)
    props.onDone()
  })

  const onError = (error: unknown) => {
    setConfirming(null)
    toast.error(qyErrorMessage(error, t))
  }

  const statusMutation = useMutation({
    mutationFn: (status: number) => qyBatchChannelStatus(ids, status),
    onSuccess: (result, status) =>
      finish(
        status === CHANNEL_STATUS.ENABLED
          ? t('qy_chops_enable_title')
          : t('qy_chops_disable_title')
      )(result),
    onError,
  })

  const deleteMutation = useMutation({
    mutationFn: () => qyBatchDeleteChannels(ids),
    onSuccess: finish(t('qy_chops_delete_title')),
    onError,
  })

  const busy = statusMutation.isPending || deleteMutation.isPending

  return (
    <>
      <IconAction
        icon={<Power />}
        label={t('qy_chops_enable_action')}
        disabled={busy}
        onClick={() => statusMutation.mutate(CHANNEL_STATUS.ENABLED)}
      />
      <IconAction
        icon={<PowerOff />}
        label={t('qy_chops_disable_action')}
        disabled={busy}
        onClick={() => setConfirming('disable')}
      />
      <IconAction
        icon={<Eraser />}
        label={
          props.canDelete
            ? t('qy_chops_reset_action')
            : t('qy_chops_no_permission')
        }
        disabled={busy || !props.canDelete}
        onClick={() => setConfirming('reset')}
      />
      <IconAction
        icon={<Trash2 />}
        variant='destructive'
        label={
          props.canDelete
            ? t('qy_chops_delete_action')
            : t('qy_chops_no_permission')
        }
        disabled={busy || !props.canDelete}
        onClick={() => setConfirming('delete')}
      />

      <QyConfirmDialog
        open={confirming === 'disable'}
        onOpenChange={(open) => !open && setConfirming(null)}
        title={t('qy_chops_disable_title')}
        description={t('qy_chops_disable_desc', { count: ids.length })}
        confirmText={t('qy_chops_disable_action')}
        isLoading={statusMutation.isPending}
        onConfirm={() => statusMutation.mutate(CHANNEL_STATUS.MANUAL_DISABLED)}
      />

      {/* 与列表头 / 行内那两个直达入口**共用同一个确认框与同一个接口**。
          三处各写一份确认框，就是三处各自会漂移的不可逆闸门。 */}
      <QyChannelResetUsageDialog
        open={confirming === 'reset'}
        onOpenChange={(open) => !open && setConfirming(null)}
        channels={props.channels}
        onDone={props.onDone}
      />

      <QyConfirmDialog
        open={confirming === 'delete'}
        onOpenChange={(open) => !open && setConfirming(null)}
        title={t('qy_chops_delete_title')}
        description={t('qy_chops_delete_desc', { count: ids.length })}
        confirmText={t('qy_chops_delete_action')}
        irreversible
        isLoading={deleteMutation.isPending}
        details={<ChannelNameList channels={props.channels} />}
        onConfirm={() => deleteMutation.mutate()}
      />
    </>
  )
}

/** 工具条上的一个图标按钮。与上游那排按钮同样的 `size-8` 尺寸。 */
function IconAction(props: {
  disabled: boolean
  icon: ReactNode
  label: string
  onClick: () => void
  variant?: 'destructive' | 'outline'
}) {
  return (
    <Tooltip>
      <TooltipTrigger
        render={
          <Button
            variant={props.variant ?? 'outline'}
            size='icon'
            className='size-8'
            disabled={props.disabled}
            onClick={props.onClick}
            aria-label={props.label}
            title={props.label}
          />
        }
      >
        {props.icon}
        <span className='sr-only'>{props.label}</span>
      </TooltipTrigger>
      <TooltipContent>
        <p>{props.label}</p>
      </TooltipContent>
    </Tooltip>
  )
}
