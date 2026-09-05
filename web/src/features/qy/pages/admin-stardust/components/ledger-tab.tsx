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
import { ScrollText } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import {
  StaticDataTable,
  staticDataTableClassNames,
  type StaticDataTableColumn,
} from '@/components/data-table'
import { Input } from '@/components/ui/input'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'

import { QyPageBoundary } from '../../../components/qy-page-boundary'
import { QySdAmount } from '../../../components/qy-sd-amount'
import { qyArray } from '../../../lib/array'
import { QyPager } from '../../components/qy-pager'
import { QY_PAGE_SIZE } from '../../lib/constants'
import { formatQyTs } from '../../ops/format'
import { QY_SD_LEDGER_KINDS, qySdKindKey } from '../../stardust/lib/display'
import { qyAdminStardustLedgerQuery } from '../api'
import type { QyStardustAdminLedgerRow } from '../types'

type LedgerTabProps = {
  /** 从余额表点「流水」过来时预填的用户 ID；`0` = 不筛。 */
  initialUserId: number
}

/**
 * 「流水」—— 全站账本，一行一条记账。
 *
 * 与用户端那张表同一形状，多一列用户与三个筛选（用户 / 种类 / 活动号）。
 * 对账时最常用的一条路径是：余额表上某个人的数字对不上 → 点「流水」带着
 * user_id 过来 → 按种类收窄 → 找到那一笔。
 */
export function QySdAdminLedgerTab(props: LedgerTabProps) {
  const { t } = useTranslation()
  const [page, setPage] = useState(1)
  const [userId, setUserId] = useState(
    props.initialUserId > 0 ? String(props.initialUserId) : ''
  )
  const [kind, setKind] = useState('')
  const [actNo, setActNo] = useState('')

  const query = useQuery(
    qyAdminStardustLedgerQuery({
      page,
      page_size: QY_PAGE_SIZE,
      ...(/^\d+$/.test(userId) ? { user_id: Number(userId) } : {}),
      ...(kind === '' ? {} : { kind }),
      ...(actNo.trim() === '' ? {} : { act_no: actNo.trim() }),
    })
  )
  const items = qyArray(query.data?.items)

  const columns: StaticDataTableColumn<QyStardustAdminLedgerRow>[] = [
    {
      id: 'created_at',
      header: t('qy_common_time'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactMutedCell,
      cell: (row) => formatQyTs(row.created_at),
    },
    {
      id: 'user',
      header: t('qy_common_user'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactCell,
      cell: (row) => `#${row.user_id}`,
    },
    {
      id: 'kind',
      header: t('qy_sd_col_kind'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactCell,
      cell: (row) => t(qySdKindKey(row.kind), { defaultValue: row.kind }),
    },
    {
      id: 'amount',
      header: t('qy_common_amount'),
      className: staticDataTableClassNames.compactHeaderCellRight,
      cellClassName: staticDataTableClassNames.compactNumericCell,
      cell: (row) => <QySdAmount amount={row.amount} signed />,
    },
    {
      id: 'balance_after',
      header: t('qy_sd_col_balance_after'),
      className: staticDataTableClassNames.compactHeaderCellRight,
      cellClassName: staticDataTableClassNames.compactMutedNumericCell,
      cell: (row) => <QySdAmount amount={row.balance_after} />,
    },
    {
      id: 'ref',
      header: t('qy_sd_col_ref'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactMutedCodeCell,
      cell: (row) => (
        <span className='flex flex-col'>
          {row.act_no !== '' && (
            <Link
              to='/qy/admin/lottery/$actNo'
              params={{ actNo: row.act_no }}
              className='hover:underline'
            >
              {row.act_no}
            </Link>
          )}
          {row.ref_no !== '' && (
            <span>
              {row.ref_type === ''
                ? row.ref_no
                : `${row.ref_type}:${row.ref_no}`}
            </span>
          )}
          {row.peer_user_id > 0 && (
            <span className='text-muted-foreground text-xs'>
              {t('qy_sdadm_peer_user', { id: row.peer_user_id })}
            </span>
          )}
          {row.act_no === '' && row.ref_no === '' && row.peer_user_id <= 0 && (
            <span>-</span>
          )}
        </span>
      ),
    },
    {
      id: 'remark',
      header: t('qy_common_remark'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactMutedCell,
      cell: (row) => (row.remark === '' ? '-' : row.remark),
    },
    {
      id: 'ledger_no',
      header: t('qy_sd_col_ledger_no'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactMutedCodeCell,
      cell: (row) => row.ledger_no,
    },
  ]

  return (
    <div className='space-y-3'>
      <div className='flex flex-wrap items-center gap-2'>
        <Input
          className='h-8 w-40'
          inputMode='numeric'
          value={userId}
          placeholder={t('qy_sdadm_user_id_ph')}
          onChange={(event) => {
            setPage(1)
            setUserId(event.target.value.replaceAll(/\D/g, ''))
          }}
        />
        <NativeSelect
          size='sm'
          aria-label={t('qy_sd_col_kind')}
          value={kind}
          onChange={(event) => {
            setPage(1)
            setKind(event.target.value)
          }}
        >
          <NativeSelectOption value=''>{t('qy_common_all')}</NativeSelectOption>
          {QY_SD_LEDGER_KINDS.map((value) => (
            <NativeSelectOption key={value} value={value}>
              {t(qySdKindKey(value), { defaultValue: value })}
            </NativeSelectOption>
          ))}
        </NativeSelect>
        <Input
          className='h-8 w-48'
          value={actNo}
          placeholder={t('qy_sdadm_act_no_ph')}
          onChange={(event) => {
            setPage(1)
            setActNo(event.target.value)
          }}
        />
      </div>

      <QyPageBoundary
        query={query}
        isEmpty={query.data != null && items.length === 0}
        emptyIcon={ScrollText}
        emptyTitle={t('qy_sd_ledger_empty_title')}
        emptyDescription={t('qy_sdadm_ledger_empty_desc')}
      >
        <div className='w-full overflow-x-auto'>
          <StaticDataTable
            columns={columns}
            data={items}
            getRowKey={(row) => row.ledger_no}
            tableClassName='min-w-[1100px]'
          />
        </div>
        <QyPager
          page={page}
          pageSize={QY_PAGE_SIZE}
          total={query.data?.total ?? 0}
          disabled={query.isFetching}
          onPageChange={setPage}
        />
      </QyPageBoundary>
    </div>
  )
}
