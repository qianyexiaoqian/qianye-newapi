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
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'

import { QyPageBoundary } from '../../../components/qy-page-boundary'
import { QySdAmount } from '../../../components/qy-sd-amount'
import { qyArray } from '../../../lib/array'
import { QyPager } from '../../components/qy-pager'
import { QY_PAGE_SIZE } from '../../lib/constants'
import { formatQyTs } from '../../ops/format'
import { qyStardustLedgerQuery } from '../api'
import { QY_SD_LEDGER_KINDS, qySdKindKey } from '../lib/display'
import type { QyStardustLedgerRow } from '../types'
import { QySdLedgerKindIcon } from './ledger-kind-icon'

/**
 * 「流水」—— 星屑选择夹的第二张标签。
 *
 * 每一行是账本上的一条记账（`ledger_no`），金额带符号、旁边印着记完之后的余额：
 * 用户对账时问的是"这一笔之后我还剩多少"，只给增量他得自己从头累加。
 * 十二种 kind 各有一句人话（见 `lib/display.ts`），筛选下拉按契约顺序渲染。
 */
export function QyStardustLedgerBody() {
  const { t } = useTranslation()
  const [page, setPage] = useState(1)
  const [kind, setKind] = useState('')

  const query = useQuery(
    qyStardustLedgerQuery({
      page,
      page_size: QY_PAGE_SIZE,
      ...(kind === '' ? {} : { kind }),
    })
  )
  const items = qyArray(query.data?.items)

  const columns: StaticDataTableColumn<QyStardustLedgerRow>[] = [
    {
      id: 'created_at',
      header: t('qy_common_time'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactMutedCell,
      cell: (row) => formatQyTs(row.created_at),
    },
    {
      id: 'kind',
      header: t('qy_sd_col_kind'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactCell,
      // 图标列：种类的人话进 aria-label / title，筛选下拉里仍是文字。
      cell: (row) => <QySdLedgerKindIcon kind={row.kind} />,
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
      cell: (row) => <LedgerRef row={row} />,
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
      </div>

      <QyPageBoundary
        query={query}
        isEmpty={query.data != null && items.length === 0}
        emptyIcon={ScrollText}
        emptyTitle={t('qy_sd_ledger_empty_title')}
        emptyDescription={t('qy_sd_ledger_empty_desc')}
      >
        <div className='w-full overflow-x-auto'>
          <StaticDataTable
            columns={columns}
            data={items}
            getRowKey={(row) => row.ledger_no}
            tableClassName='min-w-[900px]'
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

/**
 * 一条流水挂在哪张单据上。
 *
 * 活动号优先：抽奖 / 转盘的那几种 kind 用户要回去看的是那一场活动，链接直达；
 * 其余（商城订单、结算 run_date、手调的操作人号）只印单号。全空印 `-`，
 * 不留一格空白让人以为这一行少了字段。
 */
function LedgerRef(props: { row: QyStardustLedgerRow }) {
  const row = props.row
  if (row.act_no !== '') {
    return (
      <Link
        to='/qy/lottery/$actNo'
        params={{ actNo: row.act_no }}
        className='hover:underline'
      >
        {row.act_no}
      </Link>
    )
  }
  if (row.ref_no !== '') {
    return (
      <span>
        {row.ref_type === '' ? row.ref_no : `${row.ref_type}:${row.ref_no}`}
      </span>
    )
  }
  return <span>-</span>
}
