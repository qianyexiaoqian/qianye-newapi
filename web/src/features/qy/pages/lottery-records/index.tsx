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
import { TicketCheck } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { StaticDataTable } from '@/components/data-table'
import { Button } from '@/components/ui/button'
import { useCopyToClipboard } from '@/hooks/use-copy-to-clipboard'

import { QyAmountText } from '../../components/qy-amount-text'
import { QyPageBoundary } from '../../components/qy-page-boundary'
import { QySectionPageLayout } from '../../components/qy-section-page-layout'
import { QyStatusBadge } from '../../components/qy-status-badge'
import { qyArray } from '../../lib/array'
import { QyPager } from '../components/qy-pager'
import { qyLotMyEntriesQuery } from '../lottery/api'
import { qyLotEntryBadgeStatus } from '../lottery/lib/display'
import type { QyLotMyEntry } from '../lottery/types'
import { formatQyTs } from '../ops/format'

const PAGE_SIZE = 20

/**
 * 我的参与与派奖。
 *
 * ## 为什么 `chain_hash` 要一直摆在这里
 *
 * 它是用户手里唯一一份**平台自己签发的**参与证明。哈希链的全部威慑力来自
 * "事后动名单必须同时改掉 N 个用户已经看到过的值"，而这句话成立的前提是
 * 用户真的看得到、留得下。弹一个 toast 就没了的凭据等于没有凭据。
 *
 * ## 为什么中奖与退款要区分显示
 *
 * `won.kind` 有三种：`prize`（抽奖中奖）、`win`（竞猜赔付）、`refund`（退款）。
 * 全都写成"已到账"会让流局退款看起来像中奖，用户下一场就会按错误的预期下注。
 */
export function QyLotteryRecords() {
  const { t } = useTranslation()
  const { copyToClipboard } = useCopyToClipboard()
  const [page, setPage] = useState(1)

  const params = { p: page, page_size: PAGE_SIZE }
  const query = useQuery(qyLotMyEntriesQuery(params))
  const items = qyArray(query.data?.items)

  return (
    <QySectionPageLayout>
      <QySectionPageLayout.Title>
        {t('qy_nav_lottery_records')}
      </QySectionPageLayout.Title>
      <QySectionPageLayout.Actions>
        <Button size='sm' variant='outline' render={<Link to='/qy/lottery' />}>
          {t('qy_nav_lottery')}
        </Button>
      </QySectionPageLayout.Actions>
      <QySectionPageLayout.Content>
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
                    <Link
                      to='/qy/lottery/$actNo'
                      params={{ actNo: row.act_no }}
                      className='hover:underline'
                    >
                      {row.title}
                    </Link>
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
                        <QyAmountText quota={row.won.amount} signed />
                      </span>
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
          </div>
        </QyPageBoundary>
      </QySectionPageLayout.Content>
    </QySectionPageLayout>
  )
}
