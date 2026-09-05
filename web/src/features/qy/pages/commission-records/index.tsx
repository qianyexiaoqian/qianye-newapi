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
import { Landmark, ScrollText } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import {
  StaticDataTable,
  staticDataTableClassNames,
  type StaticDataTableColumn,
} from '@/components/data-table'
import { Badge } from '@/components/ui/badge'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { formatTimestampToDate } from '@/lib/format'

import { QyAmountText } from '../../components/qy-amount-text'
import { QyPageBoundary } from '../../components/qy-page-boundary'
import { QySdAmount } from '../../components/qy-sd-amount'
import { QySdDecimal } from '../../components/qy-sd-decimal'
import { qyArray } from '../../lib/array'
import {
  qyCommissionCreditsQuery,
  qyCommissionRecordsQuery,
} from '../affiliate/api'
import type { QyCommissionCredit, QyCommissionRecord } from '../affiliate/types'
import { QyPager } from '../components/qy-pager'
import { QY_PAGE_SIZE } from '../lib/constants'
import { formatQyTs } from '../ops/format'
import { qyCommissionCreditBadge } from './lib/credit-status'

/** 与后端 `commission/model.go` 的来源类型一致；`manual` 是管理员手工增减落下的行。 */
const SOURCE_OPTIONS = [
  'topup',
  'redemption',
  'consume',
  'clawback',
  'manual',
] as const

/**
 * 「佣金明细」—— 「我的推广」选择夹的第三张标签（D-16）。
 *
 * 两张表合成一张标签下的两个次级标签：用户问"这笔星屑是从哪笔消费来的"时看
 * 第一张（佣金账本逐笔，一行 = 一笔计佣），问"我的可用余额什么时候到账的"
 * 时看第二张（自动入账记录，一行 = 一次入账）。次级标签**刻意不进 hash**：
 * hash 只表达"选择夹选了哪一格"这一层。
 *
 * 金额分两种单位，绝不混用：**计佣基数**是额度（下线实际花掉的那个数，走
 * `QyAmountText`），**佣金本身**是星屑（走 `QySdAmount`）。两列并排是刻意的 ——
 * "花了 X 额度、返了 Y 星屑"是用户唯一能自己验算的那条式子。
 *
 * 这一屏上没有「提现 / 打款 / 现金」—— 佣金到期自动记入星屑余额，没有任何要
 * 用户发起的动作。
 */
export function QyCommissionRecordsBody() {
  const { t } = useTranslation()

  return (
    <Tabs defaultValue='records' className='gap-3'>
      <TabsList>
        <TabsTrigger value='records'>{t('qy_aff_tab_records')}</TabsTrigger>
        <TabsTrigger value='credits'>{t('qy_aff_tab_credits')}</TabsTrigger>
      </TabsList>
      <TabsContent value='records'>
        <CommissionRecordsTable />
      </TabsContent>
      <TabsContent value='credits'>
        <CommissionCreditsTable />
      </TabsContent>
    </Tabs>
  )
}

function CommissionRecordsTable() {
  const { t } = useTranslation()
  const [page, setPage] = useState(1)
  const [sourceType, setSourceType] = useState('')
  const query = useQuery(
    qyCommissionRecordsQuery({
      p: page,
      page_size: QY_PAGE_SIZE,
      source_type: sourceType,
    })
  )
  const items = qyArray(query.data?.items)

  const columns: StaticDataTableColumn<QyCommissionRecord>[] = [
    {
      id: 'created_at',
      header: t('qy_common_time'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactMutedCell,
      cell: (row) => formatTimestampToDate(row.created_at),
    },
    {
      id: 'source',
      header: t('qy_aff_source'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactCell,
      cell: (row) => t(`qy_aff_src_${row.source_type}`, row.source_type),
    },
    {
      id: 'invitee',
      header: t('qy_aff_invitee'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactCell,
      // 用户名**已由后端脱敏**，前端不得再处理；后端刻意连 user_id 都没下发。
      cell: (row) => row.invitee_masked_name || row.invitee_ref || '-',
    },
    {
      id: 'base',
      header: t('qy_aff_base_quota'),
      className: staticDataTableClassNames.compactHeaderCellRight,
      cellClassName: staticDataTableClassNames.compactNumericCell,
      cell: (row) => <QyAmountText quota={row.base_quota} />,
    },
    {
      id: 'rate',
      header: t('qy_aff_rate'),
      className: staticDataTableClassNames.compactHeaderCellRight,
      cellClassName: staticDataTableClassNames.compactMutedNumericCell,
      cell: (row) => t('qy_aff_rate_value', { percent: row.rate_bps / 100 }),
    },
    {
      id: 'gross',
      header: t('qy_aff_gross'),
      className: staticDataTableClassNames.compactHeaderCellRight,
      cellClassName: staticDataTableClassNames.compactNumericCell,
      // gross 是**星屑**的 decimal 字符串（= base_quota × 费率 / 刻度），与左边
      // 那一列不是同一个单位。原样印 decimal(30,10) 换不来精度，只换来一列
      // `0.0000123400` 挨着一列 `$2.74`，所以按星屑口径裁到人能读的位数。
      cell: (row) => <QySdDecimal value={row.gross_amount} />,
    },
    {
      id: 'mature',
      header: t('qy_aff_mature_at'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactMutedCell,
      cell: (row) => formatTimestampToDate(row.mature_at),
    },
    {
      id: 'status',
      header: t('qy_common_status'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactCell,
      cell: (row) => (
        <Badge
          variant={row.status === 'risk_hold' ? 'destructive' : 'secondary'}
        >
          {t(`qy_aff_st_${row.status}`, row.status)}
        </Badge>
      ),
    },
  ]

  return (
    <div className='space-y-3'>
      <div className='flex flex-wrap items-center gap-2'>
        <NativeSelect
          size='sm'
          aria-label={t('qy_aff_source')}
          value={sourceType}
          onChange={(event) => {
            setPage(1)
            setSourceType(event.target.value)
          }}
        >
          <NativeSelectOption value=''>
            {t('qy_cm_all_sources')}
          </NativeSelectOption>
          {SOURCE_OPTIONS.map((value) => (
            <NativeSelectOption key={value} value={value}>
              {t(`qy_aff_src_${value}`)}
            </NativeSelectOption>
          ))}
        </NativeSelect>
      </div>
      <QyPageBoundary
        query={query}
        isEmpty={items.length === 0}
        emptyIcon={ScrollText}
        emptyTitle={t('qy_aff_empty_records_title')}
        emptyDescription={t('qy_aff_empty_records_desc')}
      >
        <div className='w-full overflow-x-auto'>
          <StaticDataTable
            columns={columns}
            data={items}
            getRowKey={(row) => row.accrual_no}
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
 * 自动入账记录：一行 = 一次「佣金可用余额 → 星屑余额」。
 *
 * 每一行都带着星屑流水号：拿它去「星屑 → 流水」按号一查就是同一笔。两张账本之间
 * 有这一条链，"这 30 星屑是哪来的"才答得上来。
 */
function CommissionCreditsTable() {
  const { t } = useTranslation()
  const [page, setPage] = useState(1)
  const query = useQuery(
    qyCommissionCreditsQuery({ p: page, page_size: QY_PAGE_SIZE })
  )
  const items = qyArray(query.data?.items)

  const columns: StaticDataTableColumn<QyCommissionCredit>[] = [
    {
      id: 'created_at',
      header: t('qy_common_time'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactMutedCell,
      cell: (row) => formatQyTs(row.created_at),
    },
    {
      id: 'credit_no',
      header: t('qy_aff_credit_no'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactMutedCodeCell,
      cell: (row) => row.credit_no,
    },
    {
      id: 'amount',
      header: t('qy_common_amount'),
      className: staticDataTableClassNames.compactHeaderCellRight,
      cellClassName: staticDataTableClassNames.compactNumericCell,
      cell: (row) => <QySdAmount amount={row.amount} signed />,
    },
    {
      id: 'ledger_no',
      header: t('qy_sd_col_ledger_no'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactMutedCodeCell,
      cell: (row) => (row.ledger_no === '' ? '-' : row.ledger_no),
    },
    {
      id: 'status',
      header: t('qy_common_status'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactCell,
      cell: (row) => {
        const badge = qyCommissionCreditBadge(row.status)
        return (
          <Badge variant={badge.variant}>
            {t(badge.labelKey, { defaultValue: row.status })}
          </Badge>
        )
      },
    },
    {
      id: 'finished_at',
      header: t('qy_aff_credit_finished'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactMutedCell,
      cell: (row) => (row.finished_at > 0 ? formatQyTs(row.finished_at) : '-'),
    },
    {
      id: 'remark',
      header: t('qy_common_remark'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactMutedCell,
      cell: (row) => (row.remark === '' ? '-' : row.remark),
    },
  ]

  return (
    <div className='space-y-3'>
      <p className='text-muted-foreground text-sm'>{t('qy_aff_credit_desc')}</p>
      <QyPageBoundary
        query={query}
        isEmpty={items.length === 0}
        emptyIcon={Landmark}
        emptyTitle={t('qy_aff_empty_credits_title')}
        emptyDescription={t('qy_aff_empty_credits_desc')}
      >
        <div className='w-full overflow-x-auto'>
          <StaticDataTable
            columns={columns}
            data={items}
            getRowKey={(row) => row.credit_no}
            tableClassName='min-w-[720px]'
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
