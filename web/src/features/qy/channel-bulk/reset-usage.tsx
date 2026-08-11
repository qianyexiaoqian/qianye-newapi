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
import { Eraser } from 'lucide-react'
import { useId, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Label } from '@/components/ui/label'
import {
  ADMIN_PERMISSION_ACTIONS,
  ADMIN_PERMISSION_RESOURCES,
  hasPermission,
} from '@/lib/admin-permissions'
import { useAuthStore } from '@/stores/auth-store'

import { QyAmountText } from '../components/qy-amount-text'
import { QyConfirmDialog } from '../components/qy-confirm-dialog'
import { QyResponsiveDialog } from '../components/qy-responsive-dialog'
import { qyErrorMessage } from '../lib/api'
import { formatQyQuotaLedger } from '../lib/format'
import { qyBatchResetChannelUsage } from './api'
import { useQyChannelBatchFinish } from './batch-finish'
import { ChannelNameList, type QyChannelTarget } from './channel-name-list'
import { useQyChannelBulkVisible } from './visible'

/*
 * 「清空已用额度」的**直达入口**。
 *
 * # 这个文件为什么存在
 *
 * 功能本身早就做完了：后端 `POST /api/qy/admin/channels/batch-reset-usage`
 * 带权限、写审计、实测可用，前端也有确认框与逐条结果。项目方却连着提了三次
 * "还是没有清零功能"，三次都是对着列表里「已使用 / 剩余」那一列问的。
 *
 * 原因不是功能缺失，是**入口埋得太深**：
 *
 *   ① 右上角「批量操作」开关默认关 → ② 打开后行首才出现勾选框 →
 *   ③ 勾几行 → ④ 底部才浮出工具条，清零按钮在里面
 *
 * 四步之后才够得着的东西，等于不存在。所以这里加的是**另一条更短的路**，
 * 不是搬家：工具条那条路原样保留（已经习惯它的人不该被打断），
 * 而下面这两个入口都长在他真正在看的那一列上，且**都不经过「批量操作」开关**：
 *
 *   `QyChannelResetUsageColumnAction`  列表头上的橡皮擦 → 自带勾选的挑选面板
 *   `QyChannelResetUsageRowAction`     每一行已用额度旁边的橡皮擦 → 就清这一个
 *
 * 三个入口共用同一个确认框、同一个 API 客户端、同一套收尾（batch-finish.ts），
 * 后端也仍然是那一个 `batch-reset-usage`（单渠道就是 ids 里只放一个）。
 *
 * # 界面上必须让人一眼知道自己在清什么
 *
 * 那一列里的 `$488.54  $0` 是**两个不同的数**：前者是 used_quota（本站自己记的
 * 账），后者是 balance（上游余额的缓存展示值）。橡皮擦只挂在前者旁边，
 * 而后者旁边原有的"点一下更新余额"保持不动 —— 两个动作的落点分开，
 * 才不会让人以为点一下两个都清。确认框里再复述一次这条区分。
 */

/**
 * 直达入口的可见性闸门。
 *
 * 两个条件缺一不可：扩展启用（关掉时 `/api/qy/**` 一律 404），以及当前管理员
 * 有 `channel:sensitive_write`。**真正说了算的永远是后端**，这里只是不把一个
 * 必然 403 的按钮摆在人眼前。
 */
function useQyChannelResetEntryVisible(): boolean {
  const enabled = useQyChannelBulkVisible()
  const currentUser = useAuthStore((s) => s.auth.user)

  return (
    enabled &&
    hasPermission(
      currentUser,
      ADMIN_PERMISSION_RESOURCES.CHANNEL,
      ADMIN_PERMISSION_ACTIONS.SENSITIVE_WRITE
    )
  )
}

/**
 * 「清空已用额度」的二次确认 + 提交。**三个入口共用这一份。**
 *
 * 危险度是不可逆档：醒目警示 + 必须勾选才能提交（`QyConfirmDialog irreversible`）。
 * 入口变短了，这道闸门一步都不能省 —— 变短的是"找到它"的路径，
 * 不是"按下去"的代价。
 */
export function QyChannelResetUsageDialog(props: {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** 本次要清的渠道。单渠道路径就放一个。 */
  channels: QyChannelTarget[]
  /** 提交完成后的收尾（清空选中态 / 清空挑选面板）。 */
  onDone?: () => void
}) {
  const { t } = useTranslation()
  const ids = props.channels.map((channel) => channel.id)

  // 重置项的勾选状态。已用额度默认勾上、余额默认不勾 —— 理由见 ResetOptions。
  const [resetUsedQuota, setResetUsedQuota] = useState(true)
  const [resetBalance, setResetBalance] = useState(false)

  const finish = useQyChannelBatchFinish(() => {
    props.onOpenChange(false)
    props.onDone?.()
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
    onError: (error: unknown) => {
      props.onOpenChange(false)
      toast.error(qyErrorMessage(error, t))
    },
  })

  const single = props.channels.length === 1 ? props.channels[0] : null

  return (
    <QyConfirmDialog
      open={props.open}
      onOpenChange={(open) => !open && props.onOpenChange(false)}
      title={t('qy_chops_reset_title')}
      description={
        single
          ? t('qy_chops_reset_desc_one', {
              name: single.name === '' ? `#${single.id}` : single.name,
            })
          : t('qy_chops_reset_desc', { count: ids.length })
      }
      confirmText={t('qy_chops_reset_action')}
      irreversible
      // 不可逆，但**不动任何人的钱**：抹掉的是本站自己记的累计数。默认那句
      // "确认后资金会立即变动"在这里是错的，而且与同屏的 scope hint
      //（"上游的真实用量不会被改动"）直接打架。
      irreversibleDesc={t('qy_chops_reset_irreversible_desc')}
      isLoading={resetMutation.isPending}
      // 一项都没勾、或者一个渠道都没选时不让提交。后端同样会拒
      //（qy_chops_nothing_to_reset / qy_chops_no_ids），这里只是不让用户白按一次。
      confirmDisabled={
        (!resetUsedQuota && !resetBalance) || props.channels.length === 0
      }
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
  )
}

/**
 * 行内的单渠道清零入口 —— 就挂在这一行的已用额度旁边。
 *
 * # 为什么必须有它
 *
 * 大多数时候运营只想清某一条。为此要先开「批量操作」开关、再勾这一行、
 * 再去底部工具条找按钮，是三步做一件一步的事，而且前两步都在页面上
 * 离这一行很远的地方。
 *
 * 后端**复用同一个 `batch-reset-usage`**（ids 里只放一个），不新写接口：
 * 审计、权限、锁内重读金额那一整套都在那条路上，另起一条只会多一份要维护的
 * 不变量。
 */
export function QyChannelResetUsageRowAction(props: {
  channel: QyChannelTarget
}) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  const visible = useQyChannelResetEntryVisible()

  if (!visible) return null

  return (
    <>
      <Button
        variant='ghost'
        size='icon'
        data-qy-reset-entry='row'
        className='text-muted-foreground hover:text-destructive size-5 shrink-0'
        onClick={() => setOpen(true)}
        aria-label={t('qy_chops_reset_one_action')}
        title={t('qy_chops_reset_one_action')}
      >
        <Eraser className='size-3.5' />
      </Button>
      <QyChannelResetUsageDialog
        open={open}
        onOpenChange={setOpen}
        channels={[props.channel]}
      />
    </>
  )
}

/**
 * 列表头上的直达入口：橡皮擦 → 自带勾选的挑选面板 → 确认框。
 *
 * 只在**表格视图**里存在（卡片视图没有表头），所以工具条上还有一份同样的入口，
 * 见 `QyChannelResetUsageToolbarAction`。
 */
export function QyChannelResetUsageColumnAction(props: {
  /** 当前页可清零的渠道（标签聚合行已在调用方展开成子渠道）。 */
  channels: QyChannelTarget[]
}) {
  return <ChannelResetUsagePickEntry channels={props.channels} at='column' />
}

/**
 * 工具条上的直达入口：同一个挑选面板，但带文字标签。
 *
 * # 为什么表头有了还要再放一个
 *
 * 渠道页 `enableCardView` 且没有指定 `defaultViewMode`，而 `DataTablePage` 在这种
 * 情况下默认的是**卡片视图**（见 layout/data-table-page.tsx）。也就是说：清掉
 * localStorage 之后第一次进这一页，桌面端看到的也是卡片，**页面上根本没有表头**，
 * 表头上那个入口一个像素都不存在。只把入口挂在表头上，等于把"能不能找到它"
 * 押在用户此前恰好切过一次视图上 —— 而这次要修的正是"找不到"。
 *
 * 工具条两种视图下都渲染，所以多渠道那条路在这里再放一份，并且带文字：
 * 项目方找了三次，一个没有标签的图标不足以被第四次找到。
 */
export function QyChannelResetUsageToolbarAction(props: {
  channels: QyChannelTarget[]
}) {
  return <ChannelResetUsagePickEntry channels={props.channels} at='toolbar' />
}

/**
 * 多渠道直达入口的本体：一个按钮 → 自带勾选的挑选面板 → 确认框。
 *
 * # 为什么自带勾选，而不是复用外面那个「批量操作」开关
 *
 * 那个开关是**前置步骤**：不打开它，行首根本没有勾选框，底部也永远不浮出
 * 工具条。把"选哪些渠道"做成入口自己的一部分之后，这条路从"看到这一页"到
 * "发起清零"只有一次点击，而且完全不依赖页面上任何别的开关状态。
 *
 * 这条独立性是硬要求，`__tests__/reset-direct-entry.test.ts` 会在有人把它挪回
 * 工具条那个只在批量模式下渲染的文件、或者让它跟着 `batchMode` 走的时候变红。
 */
function ChannelResetUsagePickEntry(props: {
  channels: QyChannelTarget[]
  at: 'column' | 'toolbar'
}) {
  const { t } = useTranslation()
  const [pickerOpen, setPickerOpen] = useState(false)
  const [confirmOpen, setConfirmOpen] = useState(false)
  const [picked, setPicked] = useState<number[]>([])
  const visible = useQyChannelResetEntryVisible()

  // 页面翻页 / 筛选之后，上一屏勾的 id 可能已经不在这一页了。执行范围只认
  // 当前这一页真实存在的行 —— 否则确认框会复述一个与实际执行范围不同的条数。
  const pickedChannels = useMemo(
    () => props.channels.filter((channel) => picked.includes(channel.id)),
    [props.channels, picked]
  )

  if (!visible) return null

  const allPicked =
    props.channels.length > 0 && pickedChannels.length === props.channels.length

  return (
    <>
      {props.at === 'toolbar' ? (
        <Button
          variant='outline'
          size='sm'
          data-qy-reset-entry='toolbar'
          className='hover:text-destructive h-8'
          onClick={() => setPickerOpen(true)}
        >
          <Eraser className='size-3.5' />
          {t('qy_chops_reset_action')}
        </Button>
      ) : (
        <Button
          variant='ghost'
          size='icon'
          data-qy-reset-entry='column'
          className='text-muted-foreground hover:text-destructive size-5 shrink-0'
          onClick={() => setPickerOpen(true)}
          aria-label={t('qy_chops_reset_pick_title')}
          title={t('qy_chops_reset_pick_title')}
        >
          <Eraser className='size-3.5' />
        </Button>
      )}

      <QyResponsiveDialog
        open={pickerOpen}
        onOpenChange={setPickerOpen}
        title={t('qy_chops_reset_pick_title')}
        description={t('qy_chops_reset_pick_desc')}
        contentClassName='sm:max-w-xl'
        footer={
          <>
            <Button variant='outline' onClick={() => setPickerOpen(false)}>
              {t('qy_common_cancel')}
            </Button>
            <Button
              variant='destructive'
              disabled={pickedChannels.length === 0}
              onClick={() => {
                setPickerOpen(false)
                setConfirmOpen(true)
              }}
            >
              {t('qy_chops_reset_pick_next', {
                count: pickedChannels.length,
              })}
            </Button>
          </>
        }
      >
        <QyChannelResetPicker
          channels={props.channels}
          picked={picked}
          allPicked={allPicked}
          onToggle={(id, checked) =>
            setPicked((current) =>
              checked
                ? [...current.filter((item) => item !== id), id]
                : current.filter((item) => item !== id)
            )
          }
          onToggleAll={(checked) =>
            setPicked(
              checked ? props.channels.map((channel) => channel.id) : []
            )
          }
        />
      </QyResponsiveDialog>

      <QyChannelResetUsageDialog
        open={confirmOpen}
        onOpenChange={setConfirmOpen}
        channels={pickedChannels}
        onDone={() => setPicked([])}
      />
    </>
  )
}

/**
 * 挑选面板的清单。
 *
 * 每一行都带着这个渠道的已用额度：这一屏要回答的问题是"该清哪几个"，
 * 而没有金额的话它只是一串名字。按金额从大到小排 —— 想清的通常是占大头的那些。
 */
function QyChannelResetPicker(props: {
  channels: QyChannelTarget[]
  picked: number[]
  allPicked: boolean
  onToggle: (id: number, checked: boolean) => void
  onToggleAll: (checked: boolean) => void
}) {
  const { t } = useTranslation()
  const allId = useId()
  const ordered = [...props.channels].sort(
    (a, b) => b.used_quota - a.used_quota
  )
  const pickedTotal = props.channels.reduce(
    (sum, channel) =>
      props.picked.includes(channel.id) && Number.isFinite(channel.used_quota)
        ? sum + channel.used_quota
        : sum,
    0
  )

  if (props.channels.length === 0) {
    return (
      <p className='text-muted-foreground text-sm'>
        {t('qy_chops_reset_pick_empty')}
      </p>
    )
  }

  return (
    <div className='space-y-3'>
      <div className='flex items-center justify-between gap-4'>
        <Label htmlFor={allId} className='text-sm font-normal'>
          <Checkbox
            id={allId}
            checked={props.allPicked}
            indeterminate={!props.allPicked && props.picked.length > 0}
            onCheckedChange={(checked) => props.onToggleAll(checked === true)}
          />
          {t('qy_chops_reset_pick_all')}
        </Label>
        <span className='text-sm'>
          <span className='text-muted-foreground mr-2'>
            {t('qy_chops_reset_pick_selected', {
              count: props.picked.length,
            })}
          </span>
          <QyAmountText
            quota={pickedTotal}
            variant='ledger'
            className='font-medium'
          />
        </span>
      </div>

      <ul className='divide-y rounded-md border'>
        {ordered.map((channel) => (
          <li key={channel.id}>
            <Label
              htmlFor={`qy-reset-pick-${channel.id}`}
              className='flex w-full items-center gap-2 p-2 text-sm font-normal'
            >
              <Checkbox
                id={`qy-reset-pick-${channel.id}`}
                checked={props.picked.includes(channel.id)}
                onCheckedChange={(checked) =>
                  props.onToggle(channel.id, checked === true)
                }
              />
              <span className='min-w-0 flex-1 truncate'>
                <span className='font-medium'>
                  {channel.name === '' ? `#${channel.id}` : channel.name}
                </span>
                <span className='text-muted-foreground ml-1'>
                  #{channel.id}
                </span>
              </span>
              <QyAmountText
                quota={channel.used_quota}
                variant='ledger'
                className='shrink-0 text-xs'
              />
            </Label>
          </li>
        ))}
      </ul>
    </div>
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
  channels: QyChannelTarget[]
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
      {/* 清的是「已使用」，不是「剩余」。那一列里挨着的两个数长得一样，
          而它们的含义与来源完全不同 —— 这句话必须出现在按下确认之前。 */}
      <p className='text-muted-foreground text-xs'>
        {t('qy_chops_reset_scope_hint')}
      </p>
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
 *   已用额度 `used_quota`  本站自己记的账，每次结算累加。渠道列表页「已使用」
 *                          那半列就是它。清零 = 真的抹掉累计值，
 *                          **没有任何补算路径**。这是这个功能唯一真正有用的
 *                          部分，所以默认勾上。
 *
 *   上游余额 `balance`     那一列里「剩余」那半个数，只是一份**缓存的展示值**，
 *                          唯一的写入者是「更新余额」那个动作，而它只对 8 类
 *                          渠道有实现（OpenAI 系、DeepSeek、SiliconFlow、
 *                          OpenRouter、Moonshot 等），其余渠道从建库那天起就恒为
 *                          0。也就是说：对绝大多数渠道，清它等于什么都没做；
 *                          对那 8 类，下一次「更新余额」会把它原样覆盖回来。
 *                          默认不勾。
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
