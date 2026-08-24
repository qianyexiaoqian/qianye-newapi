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
import { useQuery } from '@tanstack/react-query'
import { Download, Loader2 } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { CopyButton } from '@/components/copy-button'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'

import { QyResponsiveDialog } from '../../../components/qy-responsive-dialog'
import { QyKeyValue } from '../../ops/qy-ops-ui'
import { qyLotFullProofQuery, qyLotProofDownloadUrl } from '../api'
import { qyLotBallFormatPick } from '../lib/ball'
import { explainQyLotResult, type QyLotExplain } from '../lib/explain'
import { QY_LOT_PPM_DEN } from '../types'
import { QyLotBallNumbers } from './lottery-ball-numbers'
import { QyLotFinePrint } from './lottery-fine-print'

type WhyResultDialogProps = {
  /** 打开哪一条参与记录的解释；`null` = 关闭。 */
  target: { actNo: string; entryNo: string; title: string } | null
  onClose: () => void
}

/**
 * 「为什么是这个结果」。
 *
 * ## 这个弹窗存在的全部理由
 *
 * 概率制的公正性如果不能在**"我没中"的那一刻**被立刻看到，那么活动详情页里
 * 那个「历史公正查询」入口就退化成了平台的一面之词 —— 没有人会为了一次没中
 * 去下载 NDJSON、装 Python、读一份验证脚本。所以复算被搬到了结果旁边：
 * 用户点一下，浏览器当场把种子、票面、摇号量、区间归属重算一遍。
 *
 * ## 每一个数字都是本地算的
 *
 * 唯一的网络请求是把**公开的**证据链取下来（`/lottery/public/:act_no/proof`，
 * 匿名可访问）。名次、摇号量 r、开奖号全部由 `lib/explain.ts` 用 WebCrypto
 * 在这台机器上算出，一个都不来自后端的判定字段。信后端下发的结论，等于把
 * "自己验"换成"让平台说它验过了"。
 *
 * ## 落选也是一个正数
 *
 * 概率制下这里显示的不是"你不在名单里"，而是「r = 384217，各档区间是
 * [0,1000) 与 [1000,11000)，384217 落在全部区间之外」。落选者与中奖者走的是
 * 同一段代码、同一份输入，平台无法制造一个只有失败者看不到的暗门。
 */
export function QyLotWhyResultDialog(props: WhyResultDialogProps) {
  const { t } = useTranslation()
  const actNo = props.target?.actNo ?? ''
  const entryNo = props.target?.entryNo ?? ''

  const query = useQuery(qyLotFullProofQuery(actNo, actNo !== ''))
  const proof = query.data
  const [explain, setExplain] = useState<QyLotExplain | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    setExplain(null)
    setError(null)
    if (proof == null || entryNo === '') return
    let alive = true
    void explainQyLotResult(proof, entryNo)
      .then((result) => {
        if (alive) setExplain(result)
      })
      .catch((cause: unknown) => {
        // WebCrypto 不可用（非安全上下文）等硬失败**必须说出来**。悄悄回落到
        // 一个自己写的实现，等于让用户以为验过了而实际上验的是另一套东西。
        if (alive) setError(String(cause))
      })
    return () => {
      alive = false
    }
  }, [proof, entryNo])

  return (
    <QyResponsiveDialog
      open={props.target != null}
      onOpenChange={(open) => {
        if (!open) props.onClose()
      }}
      title={t('qy_lot_why_title')}
      description={props.target?.title}
    >
      <div className='space-y-4'>
        {query.isLoading && (
          <p className='text-muted-foreground flex items-center gap-2 text-sm'>
            <Loader2 aria-hidden='true' className='size-4 animate-spin' />
            {t('qy_lot_why_loading')}
          </p>
        )}

        {error != null && (
          <p className='text-destructive font-mono text-xs break-all'>
            {error}
          </p>
        )}

        {explain != null && explain.blocked != null && (
          <p className='text-muted-foreground text-sm'>
            {t(`qy_lot_why_blocked_${explain.blocked}`)}
          </p>
        )}

        {explain != null && explain.blocked == null && (
          <div className='space-y-4'>
            {explain.mode === 'rank' && (
              <div className='space-y-3'>
                <QyKeyValue label={t('qy_lot_why_ticket')}>
                  <span className='font-mono text-xs break-all'>
                    {explain.ticket}
                  </span>
                </QyKeyValue>
                <QyKeyValue label={t('qy_lot_why_rank')}>
                  {explain.rank === 0
                    ? t('qy_lot_why_rank_deduped')
                    : t('qy_lot_why_rank_value', {
                        rank: explain.rank,
                        total: explain.rankedTotal,
                      })}
                </QyKeyValue>
                <ul className='space-y-1 text-sm'>
                  {explain.tierRanges.map((range) => (
                    <li key={range.tier} className='flex flex-wrap gap-2'>
                      <span className='text-muted-foreground'>
                        {t('qy_lot_tier_no', { no: range.tier })} {range.name}
                      </span>
                      <span className='tabular-nums'>
                        {t('qy_lot_why_rank_range', {
                          from: range.from,
                          to: range.to,
                        })}
                      </span>
                    </li>
                  ))}
                </ul>
              </div>
            )}

            {explain.mode === 'prob' && (
              <div className='space-y-3'>
                <QyKeyValue label={t('qy_lot_why_ticket')}>
                  <span className='font-mono text-xs break-all'>
                    {explain.ticket}
                  </span>
                </QyKeyValue>
                <QyKeyValue label={t('qy_lot_why_roll')}>
                  <span className='font-mono tabular-nums'>{explain.roll}</span>
                </QyKeyValue>
                <ul className='space-y-1 text-sm'>
                  {explain.bands.map((band) => (
                    <li key={band.tier} className='flex flex-wrap gap-2'>
                      <span className='text-muted-foreground'>
                        {t('qy_lot_tier_no', { no: band.tier })} {band.name}
                      </span>
                      <span className='tabular-nums'>
                        {t('qy_lot_why_interval', {
                          lo: band.loPpm,
                          hi: band.hiPpm,
                          percent: (
                            ((band.hiPpm - band.loPpm) / QY_LOT_PPM_DEN) *
                            100
                          ).toFixed(4),
                        })}
                      </span>
                      {explain.hitTier === band.tier && (
                        <Badge>{t('qy_lot_why_won')}</Badge>
                      )}
                    </li>
                  ))}
                </ul>
                {explain.hitTier == null && (
                  <p className='text-sm'>
                    {t('qy_lot_why_not_won', { roll: explain.roll })}
                  </p>
                )}
                {/* 截断偏差不掩饰：2^64 个均匀值分进 10^6 个桶，桶大小最多
                    相差 1，相对偏差 < 2^-44。写出来比让人自己发现要诚实 ——
                    但会追究这个量级的人一定会点开，而对其余所有人它只是一行
                    读不懂的公式挡在结论前面。 */}
                <QyLotFinePrint label={t('qy_lot_roll_bias_label')}>
                  <p>{t('qy_lot_roll_bias_notice')}</p>
                </QyLotFinePrint>
              </div>
            )}

            {explain.mode === 'ball' && (
              <div className='space-y-3'>
                {/* 两组号并排画成球，命中的高亮 —— 与详情页、我的参与、
                    公开名单是同一个组件、同一条视觉规则。区别只在数据来源：
                    这里的开奖号是**本地从公开种子重摇**出来的，一个数字都不
                    来自后端，所以它同时是"平台报的那组号对不对"的检验点。 */}
                <QyKeyValue label={t('qy_lot_ball_result')}>
                  <QyLotBallNumbers
                    pick={qyLotBallFormatPick({
                      blues: explain.drawnBlues,
                      reds: explain.drawnReds,
                    })}
                    size='sm'
                  />
                </QyKeyValue>
                <QyKeyValue label={t('qy_lot_ball_my_pick')}>
                  <QyLotBallNumbers
                    hits={{
                      blues: explain.matchedBlues,
                      reds: explain.matchedReds,
                    }}
                    pick={qyLotBallFormatPick({
                      blues: explain.myBlues,
                      reds: explain.myReds,
                    })}
                    size='sm'
                  />
                </QyKeyValue>
                <QyKeyValue label={t('qy_lot_why_ball_match')}>
                  {t('qy_lot_ball_match_value', {
                    red: explain.matchRed,
                    blue: explain.matchBlue,
                  })}
                </QyKeyValue>
                <ul className='space-y-1 text-sm'>
                  {explain.tierNeeds.map((need) => (
                    <li key={need.tier} className='flex flex-wrap gap-2'>
                      <span className='text-muted-foreground'>
                        {t('qy_lot_tier_no', { no: need.tier })} {need.name}
                      </span>
                      <span className='tabular-nums'>
                        {t('qy_lot_ball_tier_need', {
                          red: need.red,
                          blue: need.blue,
                        })}
                      </span>
                      {/* 命中即停：能同时满足多档门槛时只有最靠前的那一档亮。
                          这里的判定与后端 MatchTier 是同一条规则的两次实现。 */}
                      {explain.hitTier === need.tier && (
                        <Badge>{t('qy_lot_why_won')}</Badge>
                      )}
                    </li>
                  ))}
                </ul>
                {explain.hitTier == null && (
                  <p className='text-sm'>{t('qy_lot_ball_no_tier')}</p>
                )}
                {/* 档位是本地算得出来的，金额不是：浮动奖发多少取决于本期奖池与
                    同档中签人数，那两个量不在这份证据链里。不假装能算 ——
                    但这句话压成一行就够了，原来那 69 个字里有一半在复述
                    「浮动奖怎么分」，而那件事在奖级表下面已经讲过一次。 */}
                <p className='text-muted-foreground text-xs'>
                  {t('qy_lot_ball_amount_not_local')}
                </p>
              </div>
            )}

            {/*
              「再验一遍」整块折起来。它由三样东西组成：一段 59 字的说明、
              一条 50 多个字符的 python 命令、一颗下载按钮 —— 加起来是这个弹窗
              里最长的一块，而弹窗要回答的问题只有一个：**我为什么是这个结果**。
              上面那几行已经把它答完了（票面、摇号量、区间归属），这一块是给
              少数要走到离线复算那一步的人准备的。

              命令与下载都留在里面，一字未删：给一段"你可以自己验"的说明而不给
              命令，等于没给。
            */}
            <QyLotFinePrint label={t('qy_lot_why_verify_label')}>
              <p>{t('qy_lot_why_verify_hint')}</p>
              <div className='flex flex-wrap items-center gap-2'>
                <CopyButton
                  value={`python3 lottery-verify.py ${actNo}-proof.ndjson --explain ${entryNo}`}
                />
                <span className='font-mono break-all'>
                  {`python3 lottery-verify.py ${actNo}-proof.ndjson --explain ${entryNo}`}
                </span>
              </div>
              <Button
                type='button'
                size='sm'
                variant='outline'
                render={
                  <a
                    href={qyLotProofDownloadUrl(actNo)}
                    download={`${actNo}-proof.ndjson`}
                  />
                }
              >
                <Download aria-hidden='true' />
                {t('qy_lot_download_proof')}
              </Button>
            </QyLotFinePrint>
          </div>
        )}
      </div>
    </QyResponsiveDialog>
  )
}
