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
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { StaticDataTable } from '@/components/data-table'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

import { QyAmountText } from '../../../components/qy-amount-text'
import { QyMaskedUser } from '../../../components/qy-masked-user'
import { QyPageBoundary } from '../../../components/qy-page-boundary'
import { QyStatusBadge } from '../../../components/qy-status-badge'
import { qyArray } from '../../../lib/array'
import { QyPager } from '../../components/qy-pager'
import { qyLotEntryBadgeStatus } from '../../lottery/lib/display'
import { QY_EMPTY_TEXT, formatQyTs } from '../../ops/format'
import { QyFilterBar, QyFilterField } from '../../ops/qy-ops-ui'
import { qyAdminLotEntriesQuery } from '../api'
import type { QyLotAdminEntry } from '../types'

const PAGE_SIZE = 20
const ALL = 'all'

/**
 * 参与名单（管理端）。
 *
 * 与用户端那份的差别只有两点：这里能看到真实用户与资金单号。**没有任何删除或
 * 编辑动作**，这不是漏做 —— 名单只进不出是 grinding 防御的基石，也是哈希链
 * 成立的前提。要处置某个人只能整场取消。
 */
export function QyLotEntriesTab(props: { actNo: string }) {
  const { t } = useTranslation()
  const [page, setPage] = useState(1)
  const [status, setStatus] = useState(ALL)

  const params = {
    p: page,
    page_size: PAGE_SIZE,
    status: status === ALL ? undefined : status,
  }
  const query = useQuery(qyAdminLotEntriesQuery(props.actNo, params))
  const items = qyArray(query.data?.items)

  return (
    <div className='space-y-3'>
      <QyFilterBar>
        <QyFilterField label={t('qy_common_status')}>
          <Select
            value={status}
            onValueChange={(value) => {
              setStatus(value ?? ALL)
              setPage(1)
            }}
          >
            <SelectTrigger className='w-36'>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL}>{t('qy_common_all')}</SelectItem>
              <SelectItem value='success'>{t('qy_lot_es_success')}</SelectItem>
              <SelectItem value='pending'>{t('qy_lot_es_pending')}</SelectItem>
              <SelectItem value='failed'>{t('qy_lot_es_failed')}</SelectItem>
              <SelectItem value='excluded'>
                {t('qy_lot_es_excluded')}
              </SelectItem>
              <SelectItem value='refunded'>
                {t('qy_lot_es_refunded')}
              </SelectItem>
            </SelectContent>
          </Select>
        </QyFilterField>
      </QyFilterBar>

      <QyPageBoundary
        query={query}
        isEmpty={query.data != null && items.length === 0}
        emptyTitle={t('qy_lot_a_entries_empty')}
      >
        <div className='space-y-3'>
          <StaticDataTable
            data={items}
            getRowKey={(row: QyLotAdminEntry) => row.entry_no}
            columns={[
              {
                id: 'seq',
                header: t('qy_lot_seq'),
                cellClassName: 'tabular-nums',
                cell: (row: QyLotAdminEntry) => row.seq,
              },
              {
                id: 'entry_no',
                header: t('qy_lot_entry_no'),
                cellClassName: 'font-mono text-xs',
                cell: (row: QyLotAdminEntry) => row.entry_no,
              },
              {
                id: 'user',
                header: t('qy_common_user'),
                cell: (row: QyLotAdminEntry) => (
                  <QyMaskedUser
                    userId={row.user_id}
                    maskedName={row.username}
                    copyable
                  />
                ),
              },
              {
                id: 'opt_no',
                header: t('qy_lot_option'),
                cellClassName: 'tabular-nums',
                cell: (row: QyLotAdminEntry) =>
                  row.opt_no === 0 ? QY_EMPTY_TEXT : row.opt_no,
              },
              {
                id: 'amount',
                header: t('qy_common_amount'),
                cell: (row: QyLotAdminEntry) => (
                  <QyAmountText quota={row.amount} />
                ),
              },
              {
                id: 'status',
                header: t('qy_common_status'),
                cell: (row: QyLotAdminEntry) => (
                  <QyStatusBadge status={qyLotEntryBadgeStatus(row.status)} />
                ),
              },
              {
                id: 'order_no',
                header: t('qy_common_order_no'),
                // 跨库对账的锚点：资金单裁决台按单号直查，两边必须能对上。
                cell: (row: QyLotAdminEntry) =>
                  row.order_no === '' ? (
                    QY_EMPTY_TEXT
                  ) : (
                    <Link
                      to='/qy/admin/fund-orders'
                      className='font-mono text-xs hover:underline'
                    >
                      {row.order_no}
                    </Link>
                  ),
              },
              {
                id: 'fail_code',
                header: t('qy_lot_fail_code'),
                cell: (row: QyLotAdminEntry) =>
                  row.fail_code === '' ? QY_EMPTY_TEXT : row.fail_code,
              },
              {
                id: 'created_at',
                header: t('qy_common_time'),
                cell: (row: QyLotAdminEntry) => formatQyTs(row.created_at),
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
        </div>
      </QyPageBoundary>
    </div>
  )
}
