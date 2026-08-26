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
import { Copy, Plus, Shuffle, TriangleAlert, X } from 'lucide-react'
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
  qyLotBallRandomPick,
  type QyLotBallPick,
} from '../lib/ball'
import { qyLotGuessBoard } from '../lib/guess'
import {
  qyLotBatchSeconds,
  qyLotSeatCap,
  type QyLotSeatCap,
} from '../lib/seats'
import type { QyLotActivityDetail, QyLotEntryBatch } from '../types'
import { QyLotBallPicker } from './lottery-ball-picker'
import { QyLotGuessLine } from './lottery-guess-board'

/** 需要用户补输支付密码的两个 code。 */
const PAY_PASSWORD_CODES = new Set(['qy_pay_pwd_required', 'qy_pay_pwd_wrong'])

/**
 * 报名 / 投注。
 *
 * ## 幂等键在这里生成，而且只生成一次
 *
 * `client_request_id` 是**每次打开弹窗**生成一次、重试沿用 —— 这是整条链路上
 * 唯一能把"同一次意图的两次请求"归并起来的东西。在 `mutationFn` 里生成会让每次
 * 重试都变成一次全新的参与（对允许多次参与的活动，那就是真的多扣了一笔钱）。
 *
 * 多注同理，而且更要紧：N 注的派生键是服务端按 `crid#i` 算出来的，所以整批
 * 重放会逐注幂等命中。**这正是"买多注"放在一次请求里而不是让前端连打 N 次的
 * 全部理由** —— 前端每一次点击都要自己造一个新 crid，一次超时重发就是真的
 * 多扣一笔；何况这条路由挂着按账号的关键操作限流，连打 N 次会被自己打成 429。
 *
 * ## 一次买几注：屏幕上的那个数必须等于后端要扣的那个数
 *
 * 用户在按下确认之前唯一关心的量是"一共花多少"。所以这一屏把注数与总额并排
 * 放在参与费上面，而不是让他自己拿单价乘注数 —— 而"注数"取的是**将要提交的
 * 那一份**（已加入的几注 + 正在选且已选满的这一注），不是已加入的条数：
 * 选满了却还没点"加入"的那一注会照常被买走，界面不认它就等于报少了钱。
 *
 * ## 机选是纯前端，而且不进公正性承诺链
 *
 * 服务端不区分自选与机选（`qianye/modules/lottery/entry.go` 的 `acceptPick`）。
 * 机选只决定**用户想买哪一组号**，号码一旦提交就与自选走同一条路：进哈希链、
 * 进名单原像、开奖时与开奖号比对。开奖号本身来自后端 commit-reveal 的
 * `final_seed`，与这里的 `crypto.getRandomValues` 没有任何关系。
 *
 * ## 三种"没成功"必须说成三句话
 *
 *   · `qy_lot_in_progress`  上一次还没落定 → 别再点，去记录里看；
 *   · `qy_lot_idem_conflict` 换了选项却复用了请求号 → 关掉重开；
 *   · `qy_lot_not_settled`   主库动了钱但扩展库还没回写 → **既不能说成功、
 *     也不能说失败**，只能说"稍后在记录里复核"。
 * 把它们混成一句"提交失败"，第三种会让用户以为没扣钱而重复提交。
 *
 * 多注还多出**第四种**：部分成交（HTTP 200，`accepted < requested`）。它不是
 * 失败——前面几注真的买成了、后面几注一分钱没扣。回执屏必须把这件事说全，
 * 否则用户会以为整批都没成而再提交一次。
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
  const [lines, setLines] = useState<QyLotBallPick[]>([])
  const [payPassword, setPayPassword] = useState('')
  const [needsPayPassword, setNeedsPayPassword] = useState(false)
  const [payPasswordBlocked, setPayPasswordBlocked] = useState(false)
  const [batch, setBatch] = useState<QyLotEntryBatch | null>(null)

  const isBall = activity.draw_mode === 'ball'
  const ballPool = qyLotBallPoolOf(activity)

  // 每次**打开**重置一次：请求号在这一刻定死，后续重试沿用同一个。
  useEffect(() => {
    if (!props.open) return
    setRequestId(crypto.randomUUID())
    setOptNo(0)
    setPick({ reds: [], blues: [] })
    setLines([])
    setPayPassword('')
    setNeedsPayPassword(false)
    setBatch(null)
  }, [props.open])

  // 正在选的这一注只有**选满**才算数。没选满就静默丢掉是对的：它还不是一注号，
  // 但界面必须说出来（下面的 pendingIncomplete 提示），否则用户会以为它也买了。
  const pendingComplete = isBall && isQyLotBallPickComplete(pick, ballPool)
  const submitLines = pendingComplete ? [...lines, pick] : lines
  const count = isBall ? submitLines.length : 1
  const totalQuota = activity.stake_quota * count

  // 三条闸门（单次批量 / 每人上限 / 全场名额）取更紧的那一条，**并且记住是哪一条**
  // —— 用户读完要做的下一个动作按闸门不同完全相反，口径与理由见 lib/seats.ts。
  const seats = qyLotSeatCap(activity)
  const seatCap = seats.cap
  const openSlots = Math.max(0, seatCap - submitLines.length)
  // 这一批要在服务端串行跑多久。N 注 = N 次独立扣费，999 注就是三十几秒 ——
  // 一个转了半分钟的按钮与一个卡死的页面在屏幕上长得一模一样，所以必须先说。
  const batchSeconds = qyLotBatchSeconds(count)

  const mutation = useMutation({
    mutationFn: () =>
      submitQyLotEntry(activity.act_no, {
        client_request_id: requestId,
        opt_no: activity.kind === 'guess' ? optNo : 0,
        // 非双色球一律不带号：后端对带号的普通抽奖是**拒绝**而不是忽略。
        // 双色球一律走 picks（哪怕只有一注），于是"一注"与"多注"在前端也只有
        // 一条代码路径 —— 两条路径意味着其中一条迟早会漏掉一个字段。
        picks: isBall ? submitLines.map(qyLotBallFormatPick) : undefined,
        pay_password: needsPayPassword ? payPassword : undefined,
      }),
    onSuccess: (data) => {
      setBatch(data)
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
    (!isBall || (count > 0 && count <= seatCap)) &&
    (!needsPayPassword || (!payPasswordBlocked && payPassword.length > 0))

  return (
    <QyResponsiveDialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={batch == null ? t('qy_lot_join_title') : t('qy_lot_receipt_title')}
      description={batch == null ? activity.title : t('qy_lot_receipt_desc')}
      footer={
        batch == null ? (
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
              {/*
                提交中要换一句话，而且要带上注数：999 注是一次三十几秒的请求，
                一颗写着「确认参与」的灰按钮什么都没说，用户会以为页面卡住了。
              */}
              {mutation.isPending && isBall && count > 1
                ? t('qy_lot_ball_submitting_n', { count })
                : t('qy_lot_join_confirm')}
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
      {batch == null ? (
        <div className='space-y-4'>
          <div>
            {isBall && (
              // 注数与总额排在参与费**上面**：这一屏要回答的第一个问题是
              // "我一共花多少"，而单注参与费只是它的一个因子。
              <>
                <QyKeyValue label={t('qy_lot_ball_line_count')}>
                  {t('qy_lot_ball_lines_n', { count })}
                </QyKeyValue>
                <QyKeyValue label={t('qy_lot_ball_total_due')}>
                  <QyAmountText quota={totalQuota} variant='hero' />
                </QyKeyValue>
              </>
            )}
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
            <div className='space-y-3'>
              {/*
                「你还能买几注」必须在按下确认**之前**出现。改造前每人上限只在
                后端的活动行锁内判定，用户唯一知道自己超了的方式是提交完被顶
                回来 —— 单注时那是一次白点击，多注时那是"我选了十注，凭什么
                只买成三注"。这一行就是把那句话提到前面来。
              */}
              <QyLotSeatHint seats={seats} />

              {lines.length > 0 && (
                <QyLotPickedLines
                  lines={lines}
                  disabled={mutation.isPending}
                  onRemove={(index) =>
                    setLines(lines.filter((_, i) => i !== index))
                  }
                  onClear={() => setLines([])}
                />
              )}

              <div className='space-y-2'>
                <Label>{t('qy_lot_ball_pick_title')}</Label>
                <QyLotBallPicker
                  pool={ballPool}
                  value={pick}
                  onChange={setPick}
                  // 已加入的注数占满上限时锁住选号盘：留着它就等于让用户选完
                  // 一注再被告知这一注不算，而这张票是要花钱的。
                  disabled={mutation.isPending || lines.length >= seatCap}
                />
              </div>

              <div className='flex flex-wrap gap-2'>
                <Button
                  type='button'
                  variant='outline'
                  size='sm'
                  // 「加入」只是把正在选的这一注挪进列表，注数不变，所以它
                  // 不受剩余名额约束 —— 约束在选号盘那一侧。
                  disabled={mutation.isPending || !pendingComplete}
                  onClick={() => {
                    setLines([...lines, pick])
                    setPick({ reds: [], blues: [] })
                  }}
                >
                  <Plus aria-hidden='true' />
                  {t('qy_lot_ball_add_line')}
                </Button>
                <Button
                  type='button'
                  variant='outline'
                  size='sm'
                  disabled={mutation.isPending || openSlots <= 0}
                  onClick={() =>
                    setLines([
                      ...lines,
                      ...Array.from({ length: openSlots }, () =>
                        qyLotBallRandomPick(ballPool)
                      ),
                    ])
                  }
                >
                  <Shuffle aria-hidden='true' />
                  {t('qy_lot_ball_fill_random', { count: openSlots })}
                </Button>
              </div>

              {/*
                选满了但没点「加入」的那一注**会被买走**（它算进 count 与总额），
                所以这里只在"没选满"时提醒 —— 那一注不会被买，而用户以为会。
              */}
              {!pendingComplete &&
                (pick.reds.length > 0 || pick.blues.length > 0) && (
                  <p className='text-muted-foreground text-xs'>
                    {t('qy_lot_ball_line_incomplete')}
                  </p>
                )}

              {/*
                大批量要先把时间代价说出来。N 注在服务端是 N 次**串行**扣费
                （每一注一张独立资金单、一条链环、一份可复算回执 —— 那正是每一注
                各自可复算的实现方式，不能为了快合并掉），所以 999 注就是三十几秒。
                不说的话，一个转了半分钟的按钮与一个卡死的页面在屏幕上长得一模一样，
                而用户下一步会做的事是刷新或者再点一次。

                门槛定在 5 秒：低于它没人会觉得慢，而多一行字要挤掉别的字。
              */}
              {batchSeconds >= 5 && (
                <p className='text-muted-foreground text-xs'>
                  {t('qy_lot_ball_batch_time_hint', {
                    count,
                    seconds: batchSeconds,
                  })}
                </p>
              )}
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

              金额写进这句话里，而且写的是**本次总额**而不是单注参与费：
              真正决定用户按不按这颗按钮的是"这一下要花多少、退不退"。

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
                // 一注都还没选时用**单注**参与费：这句话是一条价格与退款口径，
                // 而 "$0 立即扣除" 什么都没说。选了之后立刻换成本次总额 ——
                // 决定按不按这颗按钮的始终是"这一下要花多少"。
                {
                  amount: formatQyQuotaLedger(
                    count > 0 ? totalQuota : activity.stake_quota
                  ),
                }
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
              一眼看懂"这串东西要留着"，并且能一键复制。 */}
          <Alert>
            <AlertDescription>{t('qy_lot_receipt_keep_line')}</AlertDescription>
          </Alert>

          {/* 部分成交:HTTP 200 但只买成了前面几注。这**不是失败** ——
              买成的那几注真的成交了，没买成的一分钱都没扣。不说清楚的话，
              用户会以为整批都没成而再提交一次（而那一次是真会扣钱的）。 */}
          {batch.accepted < batch.requested && (
            <Alert variant='destructive'>
              <TriangleAlert />
              <AlertDescription>
                {t('qy_lot_receipt_partial', {
                  accepted: batch.accepted,
                  requested: batch.requested,
                })}
                {batch.failed_message == null || batch.failed_message === ''
                  ? null
                  : ` ${batch.failed_message}`}
              </AlertDescription>
            </Alert>
          )}

          <div>
            <QyKeyValue label={t('qy_lot_ball_line_count')}>
              {t('qy_lot_ball_lines_n', { count: batch.accepted })}
            </QyKeyValue>
            <QyKeyValue label={t('qy_lot_receipt_total_charged')}>
              {/* 总额取后端回的那个数，**绝不在前端拿单价乘注数**：部分成交时
                  后者是错的，而错的方向是多报。 */}
              <QyAmountText quota={batch.total_quota} variant='hero' />
            </QyKeyValue>
            <QyKeyValue label={t('qy_lot_user_ref')}>
              <span className='font-mono text-xs'>
                {batch.entries[0]?.user_ref ?? ''}
              </span>
            </QyKeyValue>
            <QyKeyValue label={t('qy_lot_commit_hash')}>
              <span className='font-mono text-xs'>
                {batch.entries[0]?.commit_hash ?? ''}
              </span>
            </QyKeyValue>
          </div>

          {/* 逐注的凭据。user_ref 与 commit_hash 对整批是同一个（同一个人、
              同一场活动），只印一次；而 entry_no / seq / pick / chain_hash
              每一注各不相同 —— 那四样才是"这一张票"的凭据。 */}
          <ul className='space-y-2'>
            {batch.entries.map((entry) => (
              <li key={entry.entry_no} className='rounded-lg border p-3'>
                <div className='flex items-baseline gap-2'>
                  <span className='text-muted-foreground shrink-0 text-xs tabular-nums'>
                    #{entry.seq}
                  </span>
                  {(entry.pick ?? '') === '' ? null : (
                    <span className='font-mono text-sm break-all tabular-nums'>
                      {/* 显示的是后端**归一化之后**的那一串，不是提交时的输入：
                          进链的是归一化后的字节，两者不一致时用户拿手里的串去
                          比对证据链会得出"平台改了我的号"的错误结论。 */}
                      {entry.pick}
                    </span>
                  )}
                </div>
                <p className='text-muted-foreground mt-1 font-mono text-xs break-all'>
                  {entry.entry_no}
                </p>
                <p className='mt-1 font-mono text-xs break-all'>
                  {entry.chain_hash}
                </p>
              </li>
            ))}
          </ul>

          <Button
            type='button'
            variant='outline'
            size='sm'
            onClick={() => {
              void copyToClipboard(
                [
                  `user_ref=${batch.entries[0]?.user_ref ?? ''}`,
                  `commit_hash=${batch.entries[0]?.commit_hash ?? ''}`,
                  ...batch.entries.map((entry) =>
                    [
                      `seq=${entry.seq}`,
                      `entry_no=${entry.entry_no}`,
                      ...((entry.pick ?? '') === ''
                        ? []
                        : [`pick=${entry.pick}`]),
                      `chain_hash=${entry.chain_hash}`,
                    ].join(' ')
                  ),
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

/**
 * 「你这一次还能买几注」，以及**是哪一条闸门定的**。
 *
 * 三条闸门在屏幕上必须说成三句不同的话，因为用户能做的事完全不同：
 *
 *  - 单次批量到顶 → 再提交一次就能接着买；
 *  - 每人上限到顶 → 这一场你买够了，再提交多少次都没用；
 *  - 全场名额到顶 → 手快有手慢无，而且它可能在你选号的这一分钟里被别人买光。
 *
 * 说成同一句"还能买 N 注"，用户读完会做出错的下一个动作。判定口径在 lib/seats.ts。
 */
function QyLotSeatHint(props: { seats: QyLotSeatCap }) {
  const { t } = useTranslation()
  const { seats } = props
  // 单次只能买一注时这句话没有信息量（"一次最多买 1 注"），而这一屏的字数
  // 预算是逐字算过的：一句零信息量的提示挤掉的是真正要被读到的那几个数。
  const capHint =
    seats.perRequestCap > 1
      ? t('qy_lot_ball_per_request_cap', { count: seats.perRequestCap })
      : ''

  if (seats.cap <= 0) {
    return (
      <p className='text-destructive text-xs'>
        {t(
          seats.binding === 'total'
            ? 'qy_lot_ball_total_full'
            : 'qy_lot_ball_seats_full'
        )}
      </p>
    )
  }
  // 绑在单次批量上时没有别的话可说 —— 那一句就是 capHint 本身。
  if (seats.binding === 'per_request' || seats.binding === 'none') {
    return capHint === '' ? null : (
      <p className='text-muted-foreground text-xs'>{capHint}</p>
    )
  }
  return (
    <p className='text-muted-foreground text-xs'>
      {t(
        seats.binding === 'total'
          ? 'qy_lot_ball_total_left'
          : 'qy_lot_ball_seats_left',
        { count: seats.cap }
      )}
      {capHint === '' ? null : ` · ${capHint}`}
    </p>
  )
}

/**
 * 已加入的那几注。
 *
 * ## 为什么默认只画前 20 行
 *
 * 这一格现在能装到 999 行。999 个 `<li>` 连同 999 颗删除按钮塞进一个对话框，
 * 结果是滚动条变成一根头发丝、真正要看的"合计多少钱"被推到屏幕外，而用户在
 * 这一屏要回答的问题从来不是"逐行核对 999 组号"——那件事在回执与「我的参与」
 * 里做。所以默认折叠到前 20 行 + 一颗「展开全部」，展开之后套一个固定高度的
 * 滚动区，屏幕上的其余内容位置不变。
 *
 * 不引虚拟滚动库：展开态最多 999 个结构极简的节点，而引一个新依赖要为它的
 * 键盘可达性、屏幕阅读器行为与打包体积各付一次账。
 */
function QyLotPickedLines(props: {
  lines: QyLotBallPick[]
  disabled: boolean
  onRemove: (index: number) => void
  onClear: () => void
}) {
  const { t } = useTranslation()
  const [expanded, setExpanded] = useState(false)
  const PREVIEW = 20
  const shown = expanded ? props.lines : props.lines.slice(0, PREVIEW)
  const hidden = props.lines.length - shown.length

  return (
    <div className='space-y-1.5'>
      <div className='flex items-center justify-between gap-2'>
        <Label>{t('qy_lot_ball_lines_title')}</Label>
        {/*
          「全部清空」在 999 注上不是便利，是必需：靠逐行删除清掉 999 注要点
          999 次，而用户想重来一遍的唯一办法本来是关掉弹窗 —— 那会连同
          client_request_id 一起重置，也就是把已经生成的幂等键丢掉。
        */}
        <Button
          type='button'
          variant='ghost'
          size='sm'
          disabled={props.disabled}
          onClick={props.onClear}
        >
          {t('qy_lot_ball_lines_clear')}
        </Button>
      </div>
      <ul
        className={
          expanded ? 'max-h-64 space-y-1.5 overflow-y-auto pr-1' : 'space-y-1.5'
        }
      >
        {shown.map((line, index) => (
          <li
            // 号码可以重复（真实彩票允许买同号），所以 key 必须带上
            // 下标 —— 只用号码做 key 会让两注同号的行在删除时错位。
            key={`${qyLotBallFormatPick(line)}#${index}`}
            className='flex items-center gap-2 rounded-lg border px-3 py-2'
          >
            <span className='text-muted-foreground w-10 shrink-0 text-xs tabular-nums'>
              {index + 1}
            </span>
            <span className='min-w-0 flex-1 font-mono text-sm break-all tabular-nums'>
              {qyLotBallFormatPick(line)}
            </span>
            <Button
              type='button'
              variant='ghost'
              size='icon'
              disabled={props.disabled}
              aria-label={t('qy_lot_ball_line_remove', { index: index + 1 })}
              onClick={() => props.onRemove(index)}
            >
              <X aria-hidden='true' />
            </Button>
          </li>
        ))}
      </ul>
      {hidden > 0 && (
        <Button
          type='button'
          variant='ghost'
          size='sm'
          onClick={() => setExpanded(true)}
        >
          {t('qy_lot_ball_lines_expand', { count: hidden })}
        </Button>
      )}
      {expanded && props.lines.length > PREVIEW && (
        <Button
          type='button'
          variant='ghost'
          size='sm'
          onClick={() => setExpanded(false)}
        >
          {t('qy_lot_ball_lines_collapse')}
        </Button>
      )}
    </div>
  )
}
