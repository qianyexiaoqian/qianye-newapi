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

import { StaticDataTable } from '@/components/data-table'
import { Badge } from '@/components/ui/badge'

import { QyAmountText } from '../../../components/qy-amount-text'
import {
  isQyLotBallPoolValid,
  qyLotBallTierOdds,
  type QyLotBallPool,
} from '../lib/ball'
import {
  QY_LOT_PPM_DEN,
  isQyLotTextPrize,
  qyLotOptions,
  qyLotTiers,
  type QyLotOption,
  type QyLotSpecItem,
  type QyLotTier,
} from '../types'
import { QyLotFinePrint } from './lottery-fine-print'

/**
 * 奖档（抽奖）或选项盘口（竞猜）。
 *
 * 这份集合在 publish 时进了 `spec_hash`，此后只读 —— 所以它同时是"能赢多少"
 * 与"事后有没有被改过"的展示位。事后加一个选项、改一档奖金，都会让验证脚本
 * 立刻算出不一样的 `spec_hash`。
 */
export function QyLotSpecTable(props: {
  kind: 'draw' | 'guess'
  /** 线上的扁平 spec 数组；分组由 `qyLotTiers` / `qyLotOptions` 完成。 */
  spec: QyLotSpecItem[]
  /** 已公布的获胜选项号；0 = 还没录。仅竞猜有意义。 */
  winOptNo?: number
  /**
   * 双色球号池。给了它就换成双色球那套列（命中门槛 / 奖金形态 / 中奖概率）。
   *
   * 概率那一列是**本地按组合数算的**，后端一个概率数字都不下发 —— 那正是
   * 双色球唯一但决定性的优势：这个数不需要相信平台。
   */
  ballPool?: QyLotBallPool
  /** 本期可派发的池子。浮动奖档的金额由它现算，缺省时只显示占池比例。 */
  poolOpenQuota?: number
}) {
  const { t } = useTranslation()

  if (props.kind === 'draw' && props.ballPool != null) {
    return (
      <BallTierTable
        spec={props.spec}
        pool={props.ballPool}
        poolOpenQuota={props.poolOpenQuota ?? 0}
      />
    )
  }

  if (props.kind === 'draw') {
    const tiers = qyLotTiers(props.spec)
    // 概率列只在真的有概率时才出现（`lot-v1` 与 rank 模式恒为 0）。
    // 恒显示一列全是 0 的「中奖概率」，比不显示更容易被误读成"一定不中"。
    const hasPpm = tiers.some((tier) => (tier.win_ppm ?? 0) > 0)
    return (
      <StaticDataTable
        data={tiers}
        getRowKey={(row: QyLotTier) => row.tier}
        columns={[
          {
            id: 'tier',
            header: t('qy_lot_tier'),
            cellClassName: 'tabular-nums',
            cell: (row: QyLotTier) => t('qy_lot_tier_no', { no: row.tier }),
          },
          {
            id: 'name',
            header: t('qy_lot_prize_name'),
            cell: (row: QyLotTier) => (
              <span className='inline-flex flex-wrap items-center gap-1.5'>
                <span className='break-words'>{row.name}</span>
                {isQyLotTextPrize(row) && (
                  <Badge variant='outline'>{t('qy_lot_prize_type_text')}</Badge>
                )}
              </span>
            ),
          },
          {
            id: 'amount',
            header: t('qy_lot_prize_amount'),
            // 文本奖的 `amount_quota` 恒为 0。摆一个 0 出来会让人以为这一档
            // 是空的 —— 它的价值全在那段公开说明里，所以直接把说明显示在这。
            cell: (row: QyLotTier) =>
              isQyLotTextPrize(row) ? (
                <span className='text-sm break-words whitespace-pre-wrap'>
                  {row.text_desc}
                </span>
              ) : (
                <QyAmountText quota={row.amount_quota} />
              ),
          },
          ...(hasPpm
            ? [
                {
                  id: 'win_ppm',
                  header: t('qy_lot_win_ppm'),
                  cellClassName: 'tabular-nums',
                  cell: (row: QyLotTier) =>
                    `${(((row.win_ppm ?? 0) / QY_LOT_PPM_DEN) * 100).toFixed(4)}%`,
                },
              ]
            : []),
          {
            id: 'count',
            // 概率制下 `count` 的语义是**本档预算份数**而不是名额：命中人数
            // 超过它时，预算由全部命中者均分（概率恒等于公示值，浮动的是金额）。
            // 表头随之换掉，否则用户会以为"只有前 N 名拿得到"。
            header: hasPpm
              ? t('qy_lot_count_is_budget')
              : t('qy_lot_prize_count'),
            cellClassName: 'tabular-nums',
            cell: (row: QyLotTier) => row.count,
          },
        ]}
      />
    )
  }

  const options = qyLotOptions(props.spec)
  // 实时盘口是活动详情才有的字段，证据链里没有（它不进承诺）。拿不到时整列
  // 不渲染，而不是显示 0 —— 那是一个错的数，比没有数更糟。
  const hasPool = options.some((option) => option.bet_quota != null)

  return (
    <StaticDataTable
      data={options}
      getRowKey={(row: QyLotOption) => row.opt_no}
      columns={[
        {
          id: 'label',
          header: t('qy_lot_option'),
          cell: (row: QyLotOption) => (
            <span className='inline-flex flex-wrap items-center gap-1.5'>
              <span className='break-words'>{row.label}</span>
              {row.is_catch_all && (
                <Badge variant='outline'>{t('qy_lot_option_catch_all')}</Badge>
              )}
              {props.winOptNo != null && props.winOptNo === row.opt_no && (
                <Badge>{t('qy_lot_option_winner')}</Badge>
              )}
            </span>
          ),
        },
        ...(hasPool
          ? [
              {
                id: 'bet_quota',
                header: t('qy_lot_option_bet_quota'),
                cell: (row: QyLotOption) => (
                  <QyAmountText quota={row.bet_quota ?? 0} />
                ),
              },
              {
                id: 'bet_count',
                header: t('qy_lot_option_bet_count'),
                cellClassName: 'tabular-nums',
                cell: (row: QyLotOption) => row.bet_count ?? 0,
              },
            ]
          : []),
      ]}
    />
  )
}

/**
 * 双色球奖级表。
 *
 * 与普通抽奖的四列完全不同，所以是独立的一张表而不是往那张上补两列：
 * 「数量 / 金额」在这里既可能是固定奖（发满 N 份、每份 X），也可能是浮动奖
 * （占本期池子的万分比、由全部中签者均分），两种形态在同一格里必须说成两句话。
 *
 * ## 概率那一列
 *
 * 由 `qyLotBallTierOdds` 在**本地**按组合数算出，枚举 (红命中, 蓝命中) 的每一格
 * 再按后端 `MatchTier` 的「tier 升序、命中即停」规则归档。它不是估计值，也不是
 * 后端下发的数字——后端在这件事上没有输入通道，管理员因此无法在概率上撒谎。
 */
function BallTierTable(props: {
  spec: QyLotSpecItem[]
  pool: QyLotBallPool
  poolOpenQuota: number
}) {
  const { t } = useTranslation()
  const tiers = qyLotTiers(props.spec)
  const odds = new Map(
    qyLotBallTierOdds(props.pool, tiers).map((item) => [item.tier, item])
  )
  const poolKnown = isQyLotBallPoolValid(props.pool)

  return (
    <div className='flex flex-col gap-2'>
      <StaticDataTable
        data={tiers}
        getRowKey={(row: QyLotTier) => row.tier}
        columns={[
          {
            id: 'tier',
            header: t('qy_lot_tier'),
            cellClassName: 'tabular-nums',
            cell: (row: QyLotTier) => t('qy_lot_tier_no', { no: row.tier }),
          },
          {
            id: 'need',
            header: t('qy_lot_ball_need'),
            cellClassName: 'tabular-nums',
            cell: (row: QyLotTier) =>
              t('qy_lot_ball_tier_need', {
                red: row.red_match ?? 0,
                blue: row.blue_match ?? 0,
              }),
          },
          {
            // 表头**不能**是 `qy_lot_prize_amount`（「单份金额」）：浮动奖那一格
            // 摆的是 `pool_open × bps / 10000`，也就是后端交给 ballSplitEven 的
            // **整档预算**，而不是任何一个人拿到手的数。顶着"单份金额"显示预算，
            // 是这张表上唯一能直接让用户算错自己能拿多少的地方。
            id: 'prize',
            header: t('qy_lot_ball_prize_shape'),
            cell: (row: QyLotTier) =>
              (row.pool_share_bps ?? 0) > 0 ? (
                <span className='inline-flex flex-col gap-1'>
                  <span className='inline-flex flex-wrap items-center gap-1.5'>
                    <Badge variant='outline'>{t('qy_lot_ball_floating')}</Badge>
                    <span className='tabular-nums'>
                      {t('qy_lot_ball_pool_share', {
                        percent: ((row.pool_share_bps ?? 0) / 100).toFixed(2),
                      })}
                    </span>
                    {/* 占池比例是个抽象数，同屏把它换算成"此刻是多少额度"——
                        但必须挂上"整档预算"这个标签，否则它读起来就是单份金额。
                        池子会随投注变大，所以这是当下值而不是承诺值。 */}
                    {props.poolOpenQuota > 0 && (
                      <span className='inline-flex items-center gap-1'>
                        <span className='text-muted-foreground text-xs'>
                          {t('qy_lot_ball_tier_budget_label')}
                        </span>
                        <QyAmountText
                          quota={Math.floor(
                            (props.poolOpenQuota * (row.pool_share_bps ?? 0)) /
                              10000
                          )}
                        />
                      </span>
                    )}
                  </span>
                  <span className='text-muted-foreground text-xs'>
                    {t('qy_lot_ball_tier_split_even')}
                  </span>
                </span>
              ) : (
                <span className='inline-flex flex-wrap items-center gap-1.5'>
                  <Badge variant='outline'>{t('qy_lot_ball_fixed')}</Badge>
                  <QyAmountText quota={row.amount_quota} />
                </span>
              ),
          },
          {
            // 固定奖档的 `count` 是**预算份数**而不是名额：中签人数超过它时，
            // count × amount 的预算由全部中签者均分（概率恒等于组合数给出的值，
            // 浮动的是金额）。表头必须说成"份数"，否则会被读成"只有前 N 名拿得到"。
            id: 'count',
            header: t('qy_lot_count_is_budget'),
            cellClassName: 'tabular-nums',
            cell: (row: QyLotTier) =>
              (row.pool_share_bps ?? 0) > 0 ? '-' : row.count,
          },
          ...(poolKnown
            ? [
                {
                  id: 'odds',
                  header: t('qy_lot_ball_odds'),
                  cellClassName: 'tabular-nums',
                  cell: (row: QyLotTier) => {
                    const item = odds.get(row.tier)
                    if (item == null || item.probability <= 0) {
                      return t('qy_lot_ball_odds_never')
                    }
                    return t('qy_lot_ball_odds_value', {
                      percent: (item.probability * 100).toPrecision(3),
                      odds: item.odds,
                    })
                  },
                },
              ]
            : []),
        ]}
      />
      {/* 「奖金会被摊薄」这件事此前在整个买家侧一个字都没有出现过：大厅卡、
          购买弹窗、详情页、我的参与、为什么是这个结果，五处全无。它决定用户
          愿不愿意花这笔钱，所以必须钉在表下面，而不是只留在管理端。

          结论那一句（「同档中签人数越多，每份越少」）就是折叠位的触发文字 ——
          它永远可见；把这条规则推到浮动奖与固定奖两种形态上的完整推演在里面，
          一字未删。此前这两部分挤成一段 128 个字的灰色小字，读它需要的耐心
          远超它值得的：真正影响下注决定的只有前半句。 */}
      <QyLotFinePrint label={t('qy_lot_ball_split_note')}>
        <p>{t('qy_lot_ball_split_detail')}</p>
      </QyLotFinePrint>
    </div>
  )
}
