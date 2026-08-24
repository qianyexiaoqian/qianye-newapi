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
import { Link } from '@tanstack/react-router'
import { Copy, TriangleAlert } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { RadioGroup, RadioGroupItem } from '@/components/ui/radio-group'
import { useCopyToClipboard } from '@/hooks/use-copy-to-clipboard'

import { QyAmountText } from '../../../components/qy-amount-text'
import { QyPayPasswordField } from '../../../components/qy-pay-password-field'
import { QyResponsiveDialog } from '../../../components/qy-responsive-dialog'
import { isQyError, qyErrorMessage } from '../../../lib/api'
import { formatQyQuotaLedger } from '../../../lib/format'
import { qyTabTarget } from '../../../lib/pages'
import { qyKeys } from '../../../lib/query-keys'
import { QyKeyValue } from '../../ops/qy-ops-ui'
import { submitQyLotEntry } from '../api'
import {
  isQyLotBallPickComplete,
  qyLotBallFormatPick,
  qyLotBallPoolOf,
  type QyLotBallPick,
} from '../lib/ball'
import { qyLotGuessBoard } from '../lib/guess'
import type { QyLotActivityDetail, QyLotEntryReceipt } from '../types'
import { QyLotBallPicker } from './lottery-ball-picker'
import { QyLotGuessLine } from './lottery-guess-board'

/** 需要用户补输支付密码的两个 code。 */
const PAY_PASSWORD_CODES = new Set(['qy_pay_pwd_required', 'qy_pay_pwd_wrong'])

/**
 * 报名 / 投注。
 *
 * ## 幂等键在这里生成，而且只生成一次
 *
 * `client_request_id` 是**每次点击**生成一次、重试沿用 —— 这是整条链路上唯一
 * 能把"同一次意图的两次请求"归并起来的东西。在 `mutationFn` 里生成会让每次重试
 * 都变成一次全新的参与（对允许多次参与的活动，那就是真的多扣了一笔钱）。
 *
 * ## 三种"没成功"必须说成三句话
 *
 *   · `qy_lot_in_progress`  上一次还没落定 → 别再点，去记录里看；
 *   · `qy_lot_idem_conflict` 换了选项却复用了请求号 → 关掉重开；
 *   · `qy_lot_not_settled`   主库动了钱但扩展库还没回写 → **既不能说成功、
 *     也不能说失败**，只能说"稍后在记录里复核"。
 * 把它们混成一句"提交失败"，第三种会让用户以为没扣钱而重复提交。
 */
export function QyLotEntryDialog(props: {
  activity: QyLotActivityDetail
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const { activity } = props
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const { copyToClipboard } = useCopyToClipboard()

  const [requestId, setRequestId] = useState('')
  const [optNo, setOptNo] = useState(0)
  const [pick, setPick] = useState<QyLotBallPick>({ reds: [], blues: [] })
  const [payPassword, setPayPassword] = useState('')
  const [needsPayPassword, setNeedsPayPassword] = useState(false)
  const [payPasswordBlocked, setPayPasswordBlocked] = useState(false)
  const [receipt, setReceipt] = useState<QyLotEntryReceipt | null>(null)

  const isBall = activity.draw_mode === 'ball'
  const ballPool = qyLotBallPoolOf(activity)

  // 每次**打开**重置一次：请求号在这一刻定死，后续重试沿用同一个。
  useEffect(() => {
    if (!props.open) return
    setRequestId(crypto.randomUUID())
    setOptNo(0)
    setPick({ reds: [], blues: [] })
    setPayPassword('')
    setNeedsPayPassword(false)
    setReceipt(null)
  }, [props.open])

  const mutation = useMutation({
    mutationFn: () =>
      submitQyLotEntry(activity.act_no, {
        client_request_id: requestId,
        opt_no: activity.kind === 'guess' ? optNo : 0,
        // 非双色球一律不带 pick：后端对带号的普通抽奖是**拒绝**而不是忽略。
        pick: isBall ? qyLotBallFormatPick(pick) : undefined,
        pay_password: needsPayPassword ? payPassword : undefined,
      }),
    onSuccess: (data) => {
      setReceipt(data)
      // 参与会同时改变余额、活动盘口、我的记录。qy 的 key 统一以 'qy' 开头
      // 正是为了这一刻能全量失效，而不是逐个猜哪些视图受影响。
      void queryClient.invalidateQueries({ queryKey: qyKeys.all })
    },
    onError: (error) => {
      if (isQyError(error) && error.code != null) {
        if (PAY_PASSWORD_CODES.has(error.code)) {
          // 阈值只有后端知道（它在 YAML 里，不在引导端点上）。所以不猜：
          // 先不带密码提交，后端说要才把这一格显示出来。
          setNeedsPayPassword(true)
        }
      }
      toast.error(qyErrorMessage(error, t))
    },
  })

  // 盘口按**这一注**算：用户在这一屏问的是"我押下去会怎样"，
  // 而不是"某个抽象的一注会怎样"。
  const guessRows = qyLotGuessBoard({
    spec: activity.spec,
    poolQuota: activity.pool_quota,
    feeBps: activity.fee_bps,
    stakeQuota: activity.stake_quota,
  })
  const canSubmit =
    !mutation.isPending &&
    requestId !== '' &&
    (activity.kind !== 'guess' || optNo > 0) &&
    (!isBall || isQyLotBallPickComplete(pick, ballPool)) &&
    (!needsPayPassword || (!payPasswordBlocked && payPassword.length > 0))

  return (
    <QyResponsiveDialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={
        receipt == null ? t('qy_lot_join_title') : t('qy_lot_receipt_title')
      }
      description={receipt == null ? activity.title : t('qy_lot_receipt_desc')}
      footer={
        receipt == null ? (
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
              disabled={!canSubmit}
              onClick={() => mutation.mutate()}
            >
              {t('qy_lot_join_confirm')}
            </Button>
          </>
        ) : (
          <>
            {/*
              走 qyTabTarget 而不是裸 `to='/qy/lottery-records'`：后者会先卸载整个
              /qy/lottery 宿主页去加载旧路由，再被 beforeLoad 弹回来 —— 表现是一次
              白闪加三张标签的查询全部重发，而目标只是切到隔壁那张标签。
            */}
            <Button
              type='button'
              variant='outline'
              render={<Link {...qyTabTarget('/qy/lottery-records')} />}
            >
              {t('qy_nav_lottery_records')}
            </Button>
            <Button type='button' onClick={() => props.onOpenChange(false)}>
              {t('qy_common_close')}
            </Button>
          </>
        )
      }
    >
      {receipt == null ? (
        <div className='space-y-4'>
          <div>
            <QyKeyValue label={t('qy_lot_stake')}>
              <QyAmountText quota={activity.stake_quota} />
            </QyKeyValue>
            <QyKeyValue label={t('qy_lot_kind')}>
              {isBall
                ? t('qy_lot_mode_ball')
                : t(`qy_lot_kind_${activity.kind}`)}
            </QyKeyValue>
            {isBall && (
              // 奖池必须与参与费并排：双色球的「能赢多少」全在这个数上，
              // 而它随本期投注实时变大，参与之前看到的就是当下那一份。
              <QyKeyValue label={t('qy_lot_ball_pool_open')}>
                <QyAmountText
                  quota={activity.pool_open_quota ?? 0}
                  variant='hero'
                />
              </QyKeyValue>
            )}
            {activity.kind === 'guess' && (
              // 竞猜同理，而且更要紧：这个数就是**全部押注之和**，
              // 也就是"赢家分的是谁的钱"的答案。押下去之前必须看得到。
              <QyKeyValue label={t('qy_lot_pool')}>
                <QyAmountText quota={activity.pool_quota} variant='hero' />
              </QyKeyValue>
            )}
          </div>

          {isBall && (
            <div className='space-y-2'>
              <Label>{t('qy_lot_ball_pick_title')}</Label>
              <QyLotBallPicker
                pool={ballPool}
                value={pick}
                onChange={setPick}
                disabled={mutation.isPending}
              />
            </div>
          )}

          {activity.kind === 'guess' && (
            <div className='space-y-2'>
              {/*
                标签从「选择你的答案」换成「押哪一项」。这不是修辞：一个写着
                "答案"的单选组就是一道选择题，而这一步真正发生的事是把钱压进
                一个池子。加上每一行的分布条与实时赔率，一屏之内不用再解释
                "钱从哪来"。
              */}
              <Label>{t('qy_lot_pick_option')}</Label>
              <RadioGroup
                value={optNo === 0 ? '' : String(optNo)}
                onValueChange={(value) => setOptNo(Number(value ?? 0))}
                className='gap-2'
              >
                {guessRows.map((row) => (
                  <label
                    key={row.opt_no}
                    className='hover:bg-muted/40 flex cursor-pointer items-start gap-3 rounded-lg border p-3'
                  >
                    <RadioGroupItem
                      value={String(row.opt_no)}
                      className='mt-0.5'
                    />
                    {/* 与详情页盘口共用同一个组件：两处各写一份的结果是
                        "详情页 ×3.00、弹窗 ×2.85"这种最伤信任的不一致。 */}
                    <div className='min-w-0 flex-1'>
                      <QyLotGuessLine row={row} />
                    </div>
                  </label>
                ))}
              </RadioGroup>
            </div>
          )}

          {/* 一次性把不可逆这件事说清楚。钱是**立即从主额度扣走**的，
              没有撤单、没有反悔，这条必须在按下确认之前看到。

              压成一句，并且**把金额写进这句话里**：改造前是一个标题加一段
              38 字的说明，两者说的是同一件事，而真正决定用户按不按这颗按钮的
              是"多少钱、退不退"这两个具体的量。

              抽奖与竞猜必须说成两句话。抽奖那句「只有整场取消或流局时才全额
              退款」对竞猜是**错的**：竞猜的钱不是参与费而是本金，它进了奖池，
              押错时归了押中的人，而全场押中同一项或全场都押错时原样退回 ——
              后两种既不是取消也不是流局。用同一句话盖住两类活动，等于在钱
              真正动之前的最后一屏上给竞猜用户一个假的退款口径。 */}
          <Alert>
            <TriangleAlert />
            <AlertDescription>
              {t(
                activity.kind === 'guess'
                  ? 'qy_lot_bet_warn_line'
                  : 'qy_lot_join_warn_line',
                { amount: formatQyQuotaLedger(activity.stake_quota) }
              )}
            </AlertDescription>
          </Alert>

          {needsPayPassword && (
            <QyPayPasswordField
              value={payPassword}
              onChange={setPayPassword}
              disabled={mutation.isPending}
              onBlockedChange={setPayPasswordBlocked}
            />
          )}
        </div>
      ) : (
        <div className='space-y-3'>
          {/* 回执 = 用户手里的凭据。平台事后想动名单，必须同时改掉 N 个用户
              已经看到并可截图的 chain_hash —— 做不到。所以这一屏必须让人
              一眼看懂"这串东西要留着"，并且能一键复制。

              一句话说完："留好链哈希，「我的参与」里长期可查"。改造前是标题
              加一段 60 字的说明，后半段讲的是哈希链为什么防得住篡改 ——
              那是活动页「公正性验证」那一整块的主题，不该在一个刚扣完钱的
              回执上再讲一遍。 */}
          <Alert>
            <AlertDescription>{t('qy_lot_receipt_keep_line')}</AlertDescription>
          </Alert>
          <div>
            <QyKeyValue label={t('qy_lot_entry_no')}>
              <span className='font-mono text-xs'>{receipt.entry_no}</span>
            </QyKeyValue>
            <QyKeyValue label={t('qy_lot_seq')}>{receipt.seq}</QyKeyValue>
            {/* 回执上显示的是后端**归一化之后**的那一串，不是提交时的输入：
                进链的是归一化后的字节，两者不一致时用户拿手里的串去比对证据链
                会得出"平台改了我的号"的错误结论。 */}
            {(receipt.pick ?? '') !== '' && (
              <QyKeyValue label={t('qy_lot_ball_my_pick')}>
                <span className='font-mono text-xs tabular-nums'>
                  {receipt.pick}
                </span>
              </QyKeyValue>
            )}
            <QyKeyValue label={t('qy_lot_user_ref')}>
              <span className='font-mono text-xs'>{receipt.user_ref}</span>
            </QyKeyValue>
            <QyKeyValue label={t('qy_common_amount')}>
              <QyAmountText quota={receipt.amount} />
            </QyKeyValue>
            <QyKeyValue label={t('qy_lot_chain_hash')}>
              <span className='font-mono text-xs'>{receipt.chain_hash}</span>
            </QyKeyValue>
            <QyKeyValue label={t('qy_lot_commit_hash')}>
              <span className='font-mono text-xs'>{receipt.commit_hash}</span>
            </QyKeyValue>
          </div>
          <Button
            type='button'
            variant='outline'
            size='sm'
            onClick={() => {
              void copyToClipboard(
                [
                  `entry_no=${receipt.entry_no}`,
                  `seq=${receipt.seq}`,
                  `user_ref=${receipt.user_ref}`,
                  ...((receipt.pick ?? '') === ''
                    ? []
                    : [`pick=${receipt.pick}`]),
                  `chain_hash=${receipt.chain_hash}`,
                  `commit_hash=${receipt.commit_hash}`,
                ].join('\n')
              )
            }}
          >
            <Copy aria-hidden='true' />
            {t('qy_lot_receipt_copy')}
          </Button>
        </div>
      )}
    </QyResponsiveDialog>
  )
}
