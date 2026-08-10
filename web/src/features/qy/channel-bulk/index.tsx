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
import { Eraser, Power, PowerOff, Trash2 } from 'lucide-react'
import { useId, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Label } from '@/components/ui/label'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { CHANNEL_STATUS } from '@/features/channels/constants'
import { channelsQueryKeys } from '@/features/channels/lib'

import { QyAmountText } from '../components/qy-amount-text'
import { QyConfirmDialog } from '../components/qy-confirm-dialog'
import { qyErrorMessage } from '../lib/api'
import { formatQyQuotaLedger } from '../lib/format'
import {
  qyBatchChannelStatus,
  qyBatchDeleteChannels,
  qyBatchResetChannelUsage,
  type QyChannelBatchResult,
} from './api'
import { QyChannelBatchResultDialog } from './result-dialog'
import { useQyChannelBatchResultStore } from './result-store'

/**
 * 批次报告的挂载点。**必须渲染在 BulkActionsToolbar 之外。**
 *
 * 工具条在选中数归零时 `return null`，而批次收尾的第一件事就是清空选中态 ——
 * 报告若挂在工具条里，它连挂载的机会都没有。理由与取舍见 result-store.ts。
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
 */
export function QyChannelBulkActions(props: {
  /**
   * 当前选中的渠道。
   *
   * `used_quota` 是列表页那一列的原值（quota 整数），确认框据此复述
   * "这一次要抹掉多少钱"。少了它，管理员在按下一个不可逆按钮之前，
   * 屏幕上只有一个条数 —— 而 20 个渠道可能对应 3 块钱，也可能对应 3 万块。
   */
  channels: Array<{ id: number; name: string; used_quota: number }>
  /** 批次跑完之后清空选中态。 */
  onDone: () => void
  /** 无 `channel:sensitive_write` 时禁用删除与重置。真正说了算的是后端。 */
  canDelete: boolean
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const ids = props.channels.map((channel) => channel.id)

  const [confirming, setConfirming] = useState<
    'delete' | 'disable' | 'reset' | null
  >(null)
  // 报告的状态住在组件树之外(见 result-store.ts):本组件在选中数归零的那一刻
  // 连同上游工具条一起被卸载,而"清空选中"正是批次收尾要做的第一件事。
  const openReport = useQyChannelBatchResultStore((s) => s.open)

  // 重置项的勾选状态。已用额度默认勾上、余额默认不勾 —— 理由见 ResetOptions。
  const [resetUsedQuota, setResetUsedQuota] = useState(true)
  const [resetBalance, setResetBalance] = useState(false)

  /**
   * 三个动作共用的收尾。
   *
   * **一条都没成功时弹的是红色 toast，不是绿色。** 这一条是本轮的重点：
   * 接口回 200 不等于事情做成了，判据是响应体里的 succeeded/failed，
   * 而不是 HTTP 状态码好不好看。
   */
  const finish =
    <T extends QyChannelBatchResult>(
      title: string,
      /**
       * 全成功时的提示文案。默认只说条数；重置那一路覆盖它，
       * 把后端回来的**真正被抹掉的金额**说出来 —— 确认框里那个数是估算，
       * 结果不能把估算值复述一遍当成事实。
       */
      okMessage?: (result: T) => string
    ) =>
    (result: T) => {
      // 报告先开:openReport 写的是组件树之外的 store,而下面的 props.onDone()
      // 会把选中数清成 0、连带卸载本组件。顺序反过来的那一版里,这一句 setState
      // 落在已卸载的实例上被静默丢弃 —— 屏幕上只剩一句红色 toast,而它指向的
      // "明细"没有任何入口。判据见 channel-bulk/__tests__/result-store.test.ts。
      if (result.failed > 0 || result.skipped > 0) {
        openReport({ result, title })
      }
      queryClient.invalidateQueries({ queryKey: channelsQueryKeys.lists() })
      setConfirming(null)
      props.onDone()

      if (result.failed > 0) {
        // 有失败就一定把明细摊开：toast 装不下 20 行，也留不住，而管理员
        // 接下来要做的事需要那份名单一直在屏幕上。
        toast.error(t('qy_chops_toast_partial', { count: result.failed }))
        return
      }
      if (result.skipped > 0) {
        // 全是"本来就不用动"也要说出来：它常常意味着选错范围了。
        toast.success(
          t('qy_chops_toast_with_skipped', {
            done: result.succeeded,
            skipped: result.skipped,
          })
        )
        return
      }
      toast.success(
        okMessage
          ? okMessage(result)
          : t('qy_chops_toast_ok', { count: result.succeeded })
      )
    }

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

  const resetMutation = useMutation({
    mutationFn: () =>
      qyBatchResetChannelUsage(ids, { resetUsedQuota, resetBalance }),
    onSuccess: finish(t('qy_chops_reset_title'), (result) =>
      t('qy_chops_toast_reset_ok', {
        count: result.succeeded,
        amount: formatQyQuotaLedger(result.cleared_used_quota),
      })
    ),
    onError,
  })

  const busy =
    statusMutation.isPending ||
    deleteMutation.isPending ||
    resetMutation.isPending

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

      <QyConfirmDialog
        open={confirming === 'reset'}
        onOpenChange={(open) => !open && setConfirming(null)}
        title={t('qy_chops_reset_title')}
        description={t('qy_chops_reset_desc', { count: ids.length })}
        confirmText={t('qy_chops_reset_action')}
        irreversible
        isLoading={resetMutation.isPending}
        // 一项都没勾时不让提交。后端同样会拒（qy_chops_nothing_to_reset），
        // 这里只是不让用户白按一次。
        confirmDisabled={!resetUsedQuota && !resetBalance}
        details={
          <div className='space-y-4'>
            <ResetImpact channels={props.channels} willClear={resetUsedQuota} />
            <ResetOptions
              usedQuota={resetUsedQuota}
              balance={resetBalance}
              onUsedQuotaChange={setResetUsedQuota}
              onBalanceChange={setResetBalance}
            />
          </div>
        }
        onConfirm={() => resetMutation.mutate()}
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

/**
 * 「这一次要抹掉多少」的复述。
 *
 * # 为什么条数不够
 *
 * `used_quota` 清零没有任何补算路径：日志表里还有逐条流水，但渠道行上那个累计值
 * 回不来。而"选中 20 个渠道"这句话完全不足以让人判断该不该按下去 —— 同样是
 * 20 个渠道，可能对应 3 块钱，也可能对应 3 万块。金额必须在按下确认之前
 * 出现在同一屏上，而且要用站内余额口径（`QyAmountText` → 上游
 * `formatQuotaWithCurrency`），与列表页那一列、钱包页、日志页完全一致；
 * 直接把 quota 整数摊出来等于让管理员自己心算汇率。
 *
 * 已经是 0 的那些单独计数：它们不会有任何变化，混进总数会让人以为
 * "20 个渠道都有消耗"。
 *
 * `willClear` 为 false（没勾「清空已用额度」）时，这一屏必须说清楚上面那笔钱
 * 这次**不会**被清 —— 否则金额与不可逆警示同屏出现，读起来就是"要清这么多"。
 */
function ResetImpact(props: {
  channels: Array<{ id: number; name: string; used_quota: number }>
  willClear: boolean
}) {
  const { t } = useTranslation()
  const total = props.channels.reduce(
    (sum, channel) =>
      sum + (Number.isFinite(channel.used_quota) ? channel.used_quota : 0),
    0
  )
  const withUsage = props.channels.filter((channel) => channel.used_quota > 0)
  const alreadyZero = props.channels.length - withUsage.length

  return (
    <div className='space-y-2 rounded-md border p-3'>
      <div className='flex items-center justify-between gap-4 text-sm'>
        <span className='text-muted-foreground'>
          {t('qy_chops_reset_selected_count')}
        </span>
        <span className='font-medium tabular-nums'>
          {t('qy_chops_reset_channel_count', { count: props.channels.length })}
        </span>
      </div>
      <div className='flex items-center justify-between gap-4 text-sm'>
        <span className='text-muted-foreground'>
          {t('qy_chops_reset_total_used')}
        </span>
        <QyAmountText quota={total} variant='ledger' className='font-medium' />
      </div>
      {alreadyZero > 0 && (
        <p className='text-muted-foreground text-xs'>
          {t('qy_chops_reset_already_zero', { count: alreadyZero })}
        </p>
      )}
      {!props.willClear && (
        <p className='text-muted-foreground text-xs'>
          {t('qy_chops_reset_used_quota_not_selected')}
        </p>
      )}
      {withUsage.length > 0 && (
        <ChannelNameList channels={withUsage} showUsedQuota />
      )}
    </div>
  )
}

/**
 * 重置项的两个勾选框。
 *
 * # 为什么这两项必须分开勾，而且默认值不一样
 *
 * 它们的语义完全不同（判据来自后端代码，不是字面意思）：
 *
 *   已用额度 `used_quota`  本站自己记的账，每次结算累加。渠道列表页那一列就是它。
 *                          清零 = 真的抹掉累计值，**没有任何补算路径**。
 *                          这是这个功能唯一真正有用的部分，所以默认勾上。
 *
 *   上游余额 `balance`     只是一份**缓存的展示值**，唯一的写入者是「更新余额」
 *                          那个动作，而它只对 8 类渠道有实现（OpenAI 系、
 *                          DeepSeek、SiliconFlow、OpenRouter、Moonshot 等），
 *                          其余渠道从建库那天起就恒为 0。也就是说：对绝大多数
 *                          渠道，清它等于什么都没做；对那 8 类，下一次「更新余额」
 *                          会把它原样覆盖回来。默认不勾。
 *
 * 把这段话直接写进界面，是因为"清零余额"看起来比"清零已用额度"更像正事，
 * 而事实恰好相反。不说清楚的话，管理员会去点一个几乎没有效果的按钮，
 * 然后以为自己已经把统计清干净了。
 */
function ResetOptions(props: {
  balance: boolean
  onBalanceChange: (value: boolean) => void
  onUsedQuotaChange: (value: boolean) => void
  usedQuota: boolean
}) {
  const { t } = useTranslation()
  const usedQuotaId = useId()
  const balanceId = useId()

  return (
    <div className='space-y-3'>
      <div className='space-y-1'>
        <Label htmlFor={usedQuotaId} className='text-sm font-normal'>
          <Checkbox
            id={usedQuotaId}
            checked={props.usedQuota}
            onCheckedChange={(checked) =>
              props.onUsedQuotaChange(checked === true)
            }
          />
          {t('qy_chops_reset_used_quota')}
        </Label>
        <p className='text-muted-foreground pl-6 text-xs'>
          {t('qy_chops_reset_used_quota_hint')}
        </p>
      </div>
      <div className='space-y-1'>
        <Label htmlFor={balanceId} className='text-sm font-normal'>
          <Checkbox
            id={balanceId}
            checked={props.balance}
            onCheckedChange={(checked) =>
              props.onBalanceChange(checked === true)
            }
          />
          {t('qy_chops_reset_balance')}
        </Label>
        <p className='text-muted-foreground pl-6 text-xs'>
          {t('qy_chops_reset_balance_hint')}
        </p>
      </div>
    </div>
  )
}

/**
 * 受影响渠道的清单。
 *
 * 需求点名要「明确显示影响条数与渠道名」：只说"确定删除 20 个渠道吗"，
 * 用户无法发现自己多勾了一行 —— 而删除之后没有撤销键。
 *
 * `showUsedQuota` 打开时逐行带上这个渠道的已用额度：重置那一屏的合计能回答
 * "一共多少"，但回答不了"是哪一个渠道占了大头"，而后者恰恰是发现选错范围
 * （比如误勾了主力渠道）唯一的信号。
 *
 * 超过 12 条只列前 12 个再加一句"还有 N 个"：全量铺开会把确认按钮顶出屏幕，
 * 而那正是这一屏最需要用户看见的东西。重置这一路按金额从大到小排，
 * 被折叠掉的因此永远是最小的那些。
 */
function ChannelNameList(props: {
  channels: Array<{ id: number; name: string; used_quota?: number }>
  showUsedQuota?: boolean
}) {
  const { t } = useTranslation()
  const ordered =
    props.showUsedQuota === true
      ? [...props.channels].sort(
          (a, b) => (b.used_quota ?? 0) - (a.used_quota ?? 0)
        )
      : props.channels
  const shown = ordered.slice(0, 12)
  const rest = ordered.length - shown.length

  return (
    <div className='space-y-2'>
      <ul className='max-h-48 space-y-1 overflow-y-auto rounded-md border p-2 text-sm'>
        {shown.map((channel) => (
          <li key={channel.id} className='flex items-center gap-2'>
            <span className='min-w-0 flex-1 truncate'>
              <span className='font-medium'>
                {channel.name === '' ? `#${channel.id}` : channel.name}
              </span>
              <span className='text-muted-foreground ml-1'>#{channel.id}</span>
            </span>
            {props.showUsedQuota === true && (
              <QyAmountText
                quota={channel.used_quota ?? 0}
                variant='ledger'
                className='shrink-0 text-xs'
              />
            )}
          </li>
        ))}
      </ul>
      {rest > 0 && (
        <p className='text-muted-foreground text-xs'>
          {t('qy_chops_more_channels', { count: rest })}
        </p>
      )}
    </div>
  )
}
