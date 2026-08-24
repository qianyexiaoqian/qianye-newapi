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
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'

import { QyAmountText } from '../../../components/qy-amount-text'
import { qyLotGuessBoard, type QyLotGuessRow } from '../lib/guess'
import type { QyLotSpecItem } from '../types'

/**
 * 竞猜盘口。
 *
 * # 它替掉的是什么
 *
 * 改造前这里是一张三列表格（选项 / 该选项投注额 / 投注人次）。三个数全都
 * 在，但它们是**平铺的事实**，没有一处把「你赢的钱来自押错的人」「押的人越多
 * 每份越少」讲出来 —— 于是一排单选按钮读起来就是选择题，项目方自己的第一
 * 反应也是「这不是给人送钱吗」。
 *
 * 现在每一行多两样东西，都是数字而不是说明：
 *
 *   · **分布条**。看到「77.8% 的钱押在这一项」就自然懂了押它赢不了多少。
 *     这是最省字的解释，一个字都不用写。
 *   · **实时赔率**（押中约得 X · ×N）。它会随盘口变化，而且在没有对手盘时
 *     恰好退化成「暂无对手盘，原样退回」—— 那一刻界面自己说清了
 *     "没有输家就没有奖金"。
 *
 * 算法在 `lib/guess.ts`，与后端 `SplitPool` 逐条对齐（逐笔向零截断、
 * winSum == pool 全额退回）。
 */
export function QyLotGuessBoard(props: {
  spec: QyLotSpecItem[] | undefined
  poolQuota: number
  feeBps: number
  /** 用来算赔率的那一注。通常是活动的单注额。 */
  stakeQuota: number
  /** 已公布的获胜选项号；0 / undefined = 还没录。 */
  winOptNo?: number
  /**
   * 结果是否已经公布。
   *
   * 为真时整块盘口从「押中约得」切换成「已按此赔付 / 未中 / 原样退回」。
   * 少了这一维的表现是：结算完的活动在「已判定获胜」徽章旁边继续显示一个
   * **前瞻**赔率（它把"再押一注"算了进去），而那个数与真正打进账户的钱不符
   * —— 实测 pool 3000 / 手续费 5% 的一场，界面写 ×1.90，实付 ×2.85。
   */
  resultAnnounced?: boolean
}) {
  const rows = qyLotGuessBoard({
    spec: props.spec,
    poolQuota: props.poolQuota,
    feeBps: props.feeBps,
    stakeQuota: props.stakeQuota,
    winOptNo: props.winOptNo,
    resultAnnounced: props.resultAnnounced,
  })

  return (
    <ul className='flex flex-col gap-3'>
      {rows.map((row) => (
        <li key={row.opt_no}>
          <QyLotGuessLine row={row} winOptNo={props.winOptNo} />
        </li>
      ))}
    </ul>
  )
}

/**
 * 一个选项的完整一行：标题 + 占比 / 分布条 / 实时赔率。
 *
 * 详情页的盘口与参与弹窗的单选项共用它 —— 两处各写一份的结果是"详情页上
 * ×3.00、弹窗里 ×2.85"这种最伤信任的不一致，而它不会有任何东西报错。
 */
export function QyLotGuessLine(props: {
  row: QyLotGuessRow
  winOptNo?: number
}) {
  const { t } = useTranslation()
  const { row } = props
  // 盘口数字在证据链端点上不下发（它不进承诺原像）。拿不到时只画标题：
  // 画一根 0% 的条子是一个**错的**数，比没有数更糟。
  const hasBoard = row.bet_quota != null

  return (
    <div className='space-y-1.5'>
      <div className='flex flex-wrap items-center justify-between gap-x-2 gap-y-1'>
        <span className='inline-flex min-w-0 flex-wrap items-center gap-1.5'>
          <span className='text-sm break-words'>{row.label}</span>
          {row.is_catch_all && (
            <Badge variant='outline'>{t('qy_lot_option_catch_all')}</Badge>
          )}
          {props.winOptNo != null && props.winOptNo === row.opt_no && (
            <Badge>{t('qy_lot_option_winner')}</Badge>
          )}
        </span>
        {hasBoard && (
          // 百分数是**可见文字**而不是只挂在条子的 CSS 宽度上：
          // 分布条是装饰，读屏与页内搜索都读不到宽度。
          <span className='text-muted-foreground inline-flex shrink-0 items-center gap-1.5 text-xs tabular-nums'>
            <span className='text-foreground font-medium'>
              {(row.share * 100).toFixed(1)}%
            </span>
            <QyAmountText quota={row.bet_quota ?? 0} />
            <span aria-hidden='true'>·</span>
            <span>{t('qy_lot_guess_bets', { count: row.bet_count ?? 0 })}</span>
          </span>
        )}
      </div>
      {hasBoard && (
        <>
          <QyLotGuessBar share={row.share} />
          <QyLotGuessOdds row={row} />
        </>
      )}
    </div>
  )
}

/**
 * 分布条。
 *
 * `aria-hidden`：它表达的量已经由紧邻的百分数写成文字了，再给读屏播一遍
 * 只是重复。宽度走内联 style —— 比例是运行期的连续值，Tailwind 的原子类
 * 表达不了。
 */
function QyLotGuessBar(props: { share: number }) {
  const pct = Math.min(100, Math.max(0, props.share * 100))
  return (
    <div
      aria-hidden='true'
      className='bg-muted h-1.5 w-full overflow-hidden rounded-full'
    >
      <div
        className='bg-primary h-full rounded-full transition-[width]'
        style={{ width: `${pct}%` }}
      />
    </div>
  )
}

/**
 * 赔率那一行。
 *
 * 三件事必须分开说，因为它们回答的是三个不同的问题：
 *
 *   · **还没开奖**（`pending`）——「现在押一注，押中约得 X」。没有对手盘时
 *     显示「暂无对手盘，原样退回」而不是 ×1.00：后者读起来像"稳赚不赔的
 *     1 倍"，而真实语义是这一场还不成立。
 *   · **已经开奖、这一项开出**（`won`）——「已按此赔付 Y」。这里的 Y 是
 *     **已经发生**的那一次结算，不再把"再押一注"算进去。原先整屏只有前瞻
 *     赔率，中奖者看到的数比到账少 33%，而这一屏正是运营被质疑时会被截图的
 *     那一屏。
 *   · **已经开奖、这一项没开出**（`lost`）—— 一个赔率都不该有。挂一个从未
 *     发生过的 ×1.27 比不写更糟。
 */
function QyLotGuessOdds(props: { row: QyLotGuessRow }) {
  const { t } = useTranslation()
  const { quote, outcome } = props.row

  if (outcome === 'lost') {
    return (
      <p className='text-muted-foreground text-xs'>{t('qy_lot_guess_lost')}</p>
    )
  }
  if (outcome === 'refunded') {
    return (
      <p className='text-muted-foreground text-xs'>
        {t('qy_lot_guess_refunded')}
      </p>
    )
  }
  if (quote.refund) {
    return (
      <p className='text-muted-foreground text-xs'>
        {t('qy_lot_guess_no_counterparty')}
      </p>
    )
  }

  return (
    <p className='text-muted-foreground flex flex-wrap items-baseline gap-1.5 text-xs'>
      <span>
        {outcome === 'won' ? t('qy_lot_guess_paid') : t('qy_lot_guess_pays')}
      </span>
      <QyAmountText quota={quote.payoutQuota} className='text-foreground' />
      <span className='tabular-nums'>×{quote.multiple.toFixed(2)}</span>
    </p>
  )
}
