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
import { ScrollText } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import {
  StaticDataTable,
  staticDataTableClassNames,
  type StaticDataTableColumn,
} from '@/components/data-table'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'

import { QyMaskedUser } from '../../components/qy-masked-user'
import { QyPageBoundary } from '../../components/qy-page-boundary'
import { QySdAmount } from '../../components/qy-sd-amount'
import { qyArray } from '../../lib/array'
import { qyInviteRecordsQuery } from '../affiliate/api'
import { QY_INVITE_KINDS, type QyInviteRecord } from '../affiliate/types'
import { QyPager } from '../components/qy-pager'
import { QY_PAGE_SIZE } from '../lib/constants'
import { formatQyTs } from '../ops/format'
import { QySdLedgerKindIcon } from '../stardust/components/ledger-kind-icon'
import { qySdKindKey } from '../stardust/lib/display'

/**
 * 「返星屑明细」—— 「我的推广」选择夹的第三张标签（D-14）。
 *
 * 每一行是我的星屑流水里 kind ∈ 邀请类五种的一条记账（`ledger_no`）：它与
 * 「星屑 → 流水」那一页是**同一本账**的一个切面，不是第二份账本 —— 用户在这里
 * 看到的每一笔，去星屑流水里按流水号都找得到。所以这里不再印"变动后余额"：
 * 那是整本账的事，这一页只回答"哪个下线、哪一种、给了我多少"。
 *
 * 五种 kind 各有一句人话（`qy_sd_kind_*`，与星屑流水共用），筛选下拉按
 * `QY_INVITE_KINDS` 的顺序渲染。下线由后端从 ref / 日结行反查，拿不到给 0 ——
 * 此时那一格印 `-`，不编一个人出来。
 */
export function QyInviteRecordsBody() {
  const { t } = useTranslation()
  const [page, setPage] = useState(1)
  const [kind, setKind] = useState('')

  const query = useQuery(
    qyInviteRecordsQuery({
      p: page,
      page_size: QY_PAGE_SIZE,
      ...(kind === '' ? {} : { kind }),
    })
  )
  const items = qyArray(query.data?.items)

  const columns: StaticDataTableColumn<QyInviteRecord>[] = [
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
      id: 'invitee',
      header: t('qy_inv_col_invitee'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactCell,
      cell: (row) => (
        <QyMaskedUser userId={row.invitee_id} maskedName={row.invitee_masked} />
      ),
    },
    {
      id: 'amount',
      header: t('qy_common_amount'),
      className: staticDataTableClassNames.compactHeaderCellRight,
      cellClassName: staticDataTableClassNames.compactNumericCell,
      cell: (row) => <QySdAmount amount={row.amount} signed />,
    },
    {
      id: 'ref',
      header: t('qy_sd_col_ref'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactMutedCodeCell,
      cell: (row) => (row.ref_no === '' ? '-' : row.ref_no),
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
          {QY_INVITE_KINDS.map((value) => (
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
        emptyTitle={t('qy_inv_records_empty_title')}
        emptyDescription={t('qy_inv_records_empty_desc')}
      >
        <div className='w-full overflow-x-auto'>
          <StaticDataTable
            columns={columns}
            data={items}
            getRowKey={(row) => row.ledger_no}
            tableClassName='min-w-[860px]'
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
