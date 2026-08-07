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
import { Link } from '@tanstack/react-router'
import { Gift, HelpCircle, TicketCheck } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { StaticDataTable } from '@/components/data-table'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { useCopyToClipboard } from '@/hooks/use-copy-to-clipboard'

import { QyAmountText } from '../../components/qy-amount-text'
import { QyPageBoundary } from '../../components/qy-page-boundary'
import { QyStatusBadge } from '../../components/qy-status-badge'
import { qyArray } from '../../lib/array'
import { QyPager } from '../components/qy-pager'
import { qyLotMyEntriesQuery } from '../lottery/api'
import { QyLotMyTextPrizeDialog } from '../lottery/components/my-text-prize-dialog'
import { QyLotWhyResultDialog } from '../lottery/components/why-result-dialog'
import { qyLotEntryBadgeStatus } from '../lottery/lib/display'
import type { QyLotMyEntry } from '../lottery/types'
import { formatQyTs } from '../ops/format'

const PAGE_SIZE = 20

/**
 * 我的参与与派奖（选择夹的第三张标签）。
 *
 * ## 为什么 `chain_hash` 要一直摆在这里
 *
 * 它是用户手里唯一一份**平台自己签发的**参与证明。哈希链的全部威慑力来自
 * "事后动名单必须同时改掉 N 个用户已经看到过的值"，而这句话成立的前提是
 * 用户真的看得到、留得下。弹一个 toast 就没了的凭据等于没有凭据。
 *
 * ## 为什么中奖与退款要区分显示
 *
 * `won.kind` 有四种：`prize`（抽奖中奖）、`win`（竞猜赔付）、`refund`（退款）、
 * `text`（文本奖）。全都写成"已到账"会让流局退款看起来像中奖，用户下一场就会
 * 按错误的预期下注；而文本奖压根没有到账这回事，它等着人来履行。
 *
 * ## 为什么两类票必须挂 risk 徽章
 *
 * 抽奖与竞猜合并进同一个菜单之后，这张表里两类票并排。抽奖是"参与费不退"，
 * 竞猜是"可能亏本金"——最坏结果差一个量级。**不能靠卡片配色暗示**，
 * 所以每一行自己带一个徽章。
 *
 * ## 「为什么是这个结果」
 *
 * 每一行都有，不只是没中的那些行。公正性只在失败时才提供，本身就是一种偏袒。
 */
export function QyLotteryRecordsBody() {
  const { t } = useTranslation()
  const { copyToClipboard } = useCopyToClipboard()
  const [page, setPage] = useState(1)
  const [whyTarget, setWhyTarget] = useState<{
    actNo: string
    entryNo: string
    title: string
  } | null>(null)
  const [prizePayoutNo, setPrizePayoutNo] = useState<string | null>(null)

  const params = { p: page, page_size: PAGE_SIZE }
  const query = useQuery(qyLotMyEntriesQuery(params))
  const items = qyArray(query.data?.items)
  // 「我选的号」只在这一页真的有双色球票时才出一列。整张表恒挂一列空白，
  // 对只玩过普通抽奖的人就是一列永远没内容的噪音。
  const hasPick = items.some((row) => (row.pick ?? '') !== '')

  return (
    <>
      <QyPageBoundary
        query={query}
        isEmpty={query.data != null && items.length === 0}
        emptyIcon={TicketCheck}
        emptyTitle={t('qy_lot_records_empty')}
        emptyDescription={t('qy_lot_records_empty_desc')}
      >
        <div className='space-y-3'>
          <StaticDataTable
            data={items}
            getRowKey={(row: QyLotMyEntry) => row.entry_no}
            columns={[
              {
                id: 'created_at',
                header: t('qy_common_time'),
                cell: (row: QyLotMyEntry) => formatQyTs(row.created_at),
              },
              {
                id: 'title',
                header: t('qy_lot_activity'),
                cell: (row: QyLotMyEntry) => (
                  <span className='inline-flex flex-wrap items-center gap-1.5'>
                    <Link
                      to='/qy/lottery/$actNo'
                      params={{ actNo: row.act_no }}
                      className='hover:underline'
                    >
                      {row.title}
                    </Link>
                    {/* 抽奖与竞猜在这张表里并排，而两者的最坏结果差一个
                          量级。徽章挂在行上而不是靠配色暗示 —— 配色在色弱、
                          深色模式、以及"我记得上次是这样"面前都不成立。 */}
                    <Badge
                      variant={row.kind === 'guess' ? 'destructive' : 'outline'}
                    >
                      {row.kind === 'guess'
                        ? t('qy_lot_risk_badge_may_lose_principal')
                        : t('qy_lot_risk_badge_stake_lost')}
                    </Badge>
                  </span>
                ),
              },
              {
                id: 'entry_no',
                header: t('qy_lot_entry_no'),
                cellClassName: 'font-mono text-xs',
                cell: (row: QyLotMyEntry) => row.entry_no,
              },
              {
                id: 'amount',
                header: t('qy_common_amount'),
                cell: (row: QyLotMyEntry) => (
                  <QyAmountText quota={row.amount} />
                ),
              },
              // 选号是这张票唯一由用户决定的内容，而事后争议的第一句话永远是
              // 「我买的明明是那一组」。回执弹窗关掉就没了，这份列表才是留得住
              // 的那一份，所以它必须长期可见而不是只在弹窗里出现一次。
              ...(hasPick
                ? [
                    {
                      id: 'pick',
                      header: t('qy_lot_ball_my_pick'),
                      cellClassName: 'font-mono text-xs tabular-nums',
                      cell: (row: QyLotMyEntry) => row.pick ?? '',
                    },
                  ]
                : []),
              {
                id: 'status',
                header: t('qy_common_status'),
                cell: (row: QyLotMyEntry) => (
                  <QyStatusBadge status={qyLotEntryBadgeStatus(row.status)} />
                ),
              },
              {
                id: 'result',
                header: t('qy_lot_result'),
                cell: (row: QyLotMyEntry) =>
                  row.won == null ? (
                    <span className='text-muted-foreground'>
                      {t('qy_lot_result_none')}
                    </span>
                  ) : (
                    <span className='inline-flex flex-wrap items-center gap-1.5'>
                      <span className='text-xs'>
                        {t(`qy_lot_payout_kind_${row.won.kind}`, {
                          defaultValue: row.won.kind,
                        })}
                      </span>
                      {/* 文本奖没有"到账"这回事：它的金额恒为 0，摆一个
                            +0 出来会让用户以为自己中了个空气。 */}
                      {row.won.kind === 'text' ? (
                        <Button
                          type='button'
                          variant='outline'
                          size='sm'
                          disabled={row.won.payout_no == null}
                          onClick={() => {
                            setPrizePayoutNo(row.won?.payout_no ?? null)
                          }}
                        >
                          <Gift aria-hidden='true' />
                          {/* 未履行时按钮就直说"等履行"，而不是让人点进去
                              看到一个空框再自己猜。 */}
                          {row.won.fulfilled === true
                            ? t('qy_lot_text_view_btn')
                            : t('qy_lot_text_pending')}
                        </Button>
                      ) : (
                        <QyAmountText quota={row.won.amount} signed />
                      )}
                    </span>
                  ),
              },
              {
                id: 'why',
                header: t('qy_lot_why_col'),
                cell: (row: QyLotMyEntry) => (
                  <Button
                    type='button'
                    variant='ghost'
                    size='icon-sm'
                    aria-label={t('qy_lot_why_title')}
                    title={t('qy_lot_why_title')}
                    onClick={() =>
                      setWhyTarget({
                        actNo: row.act_no,
                        entryNo: row.entry_no,
                        title: row.title,
                      })
                    }
                  >
                    <HelpCircle aria-hidden='true' />
                  </Button>
                ),
              },
              {
                id: 'chain_hash',
                header: t('qy_lot_chain_hash'),
                cell: (row: QyLotMyEntry) => (
                  <Button
                    type='button'
                    variant='ghost'
                    size='sm'
                    className='font-mono text-xs'
                    title={row.chain_hash}
                    onClick={() => {
                      void copyToClipboard(row.chain_hash)
                    }}
                  >
                    {row.chain_hash.slice(0, 12)}…
                  </Button>
                ),
              },
            ]}
          />
          <QyPager
            page={page}
            pageSize={PAGE_SIZE}
            total={query.data?.total ?? 0}
            onPageChange={setPage}
            disabled={query.isFetching}
          />
          <p className='text-muted-foreground text-xs'>
            {t('qy_lot_records_chain_note')}
          </p>
          {/* 「已结束」的活动状态显示 finished 时，含义是**钱都清了**，
                不是**奖品都履行完了**：文本奖靠管理端的待履行队列盯人，
                没有 SLA。不写出来会有人拿 finished 当履行完毕的凭据。 */}
          <p className='text-muted-foreground text-xs'>
            {t('qy_lot_finished_means_funds_only')}
          </p>
        </div>
      </QyPageBoundary>

      <QyLotWhyResultDialog
        target={whyTarget}
        onClose={() => setWhyTarget(null)}
      />
      <QyLotMyTextPrizeDialog
        payoutNo={prizePayoutNo}
        onClose={() => setPrizePayoutNo(null)}
      />
    </>
  )
}
