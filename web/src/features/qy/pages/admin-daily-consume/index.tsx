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
import { useMutation, useQuery } from '@tanstack/react-query'
import { AlertTriangle, Download } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import {
  StaticDataTable,
  staticDataTableClassNames,
  type StaticDataTableColumn,
} from '@/components/data-table'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'

import { QyAmountText } from '../../components/qy-amount-text'
import { QySectionPageLayout } from '../../components/qy-section-page-layout'
import { qyErrorMessage } from '../../lib/api'
import { QyPager } from '../components/qy-pager'
import { QY_PAGE_SIZE } from '../lib/constants'
import { exportQyDailyConsume, qyAdminDailyConsumeQuery } from './api'
import type { QyDailyConsumeRow, QyDailyConsumeSort } from './types'

/** 与后端 `dailyConsumeSorts` 的键集合逐字一致。 */
const SORT_OPTIONS: readonly QyDailyConsumeSort[] = [
  'consume_quota',
  'uncounted_quota',
  'commission_base_quota',
  'request_count',
  'user_id',
]

/**
 * 日期输入用原生 `type='date'`（yyyy-mm-dd），发给后端时去掉连字符。
 *
 * 不自己造日期选择器：运营在这张表上最常做的事是“把昨天改成前天”，
 * 原生控件带键盘输入与系统本地化，比任何自造的都可靠。
 */
function toApiDate(value: string): string {
  return value.replaceAll('-', '')
}

/** 昨天的 `yyyy-mm-dd`，用作两个日期框的初值。 */
function yesterdayInputValue(): string {
  const d = new Date()
  d.setDate(d.getDate() - 1)
  return d.toISOString().slice(0, 10)
}

/**
 * 日消费明细 —— 「昨天哪个用户消费了多少」。
 *
 * # 这张表上有两个金额列，它们**本来就不相等**
 *
 * 「消费额」来自主库 `logs`（真实扣掉的钱），「计佣基数」来自计佣表。
 * 后者恒 ≤ 前者，差额单独成列（「未计佣」）。差额的来源在页面上写死成一段
 * 说明，因为运营看到两个不一样的数时的下一个动作一定是来问，而这个问题
 * 有七个确定的答案：没有邀请人、自我邀请、关系被拉黑、绑定未成熟、
 * 分组费率 0%、违规扣费 / 渠道测试、订阅额度消费。
 *
 * 让这两列并排、而不是二选一，是这一页存在的全部意义:只给计佣基数的话,
 * 0% 分组与没有上线的客户在报表里根本不存在;只给消费额的话,运营对不上
 * 佣金账。
 */
export function QyAdminDailyConsume() {
  const { t } = useTranslation()

  const [startDate, setStartDate] = useState(yesterdayInputValue)
  const [endDate, setEndDate] = useState(yesterdayInputValue)
  const [keyword, setKeyword] = useState('')
  const [sort, setSort] = useState<QyDailyConsumeSort>('consume_quota')
  const [order, setOrder] = useState<'asc' | 'desc'>('desc')
  const [page, setPage] = useState(1)

  const filters = {
    start_date: toApiDate(startDate),
    end_date: toApiDate(endDate),
    keyword: keyword.trim(),
    sort,
    order,
  }
  const query = useQuery(
    qyAdminDailyConsumeQuery({ ...filters, p: page, page_size: QY_PAGE_SIZE })
  )
  const data = query.data
  const rows = data?.items ?? []

  const exportMutation = useMutation({
    mutationFn: () => exportQyDailyConsume(filters),
    onSuccess: (blob) => {
      const url = URL.createObjectURL(blob)
      const link = document.createElement('a')
      link.href = url
      link.download = `qy-daily-consume-${filters.start_date}-${filters.end_date}.csv`
      link.click()
      URL.revokeObjectURL(url)
    },
    onError: (error) => toast.error(qyErrorMessage(error, t)),
  })

  const columns: StaticDataTableColumn<QyDailyConsumeRow>[] = [
    {
      id: 'user',
      header: t('qy_dc_user'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactCell,
      cell: (row) => (
        <span className='inline-flex flex-col'>
          <span className='inline-flex items-center gap-1.5'>
            {row.display_name || row.username || `#${row.user_id}`}
            {row.account_removed && (
              <span className='text-muted-foreground border-border rounded border px-1 text-[10px] leading-4'>
                {t('qy_dc_account_removed')}
              </span>
            )}
          </span>
          <span className='text-muted-foreground text-xs'>
            {`#${row.user_id}`}
            {row.email !== '' ? ` · ${row.email}` : ''}
          </span>
        </span>
      ),
    },
    {
      id: 'group',
      header: t('qy_dc_group'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactCell,
      cell: (row) => row.user_group || '-',
    },
    {
      id: 'requests',
      header: t('qy_dc_requests'),
      className: staticDataTableClassNames.compactHeaderCellRight,
      cellClassName: staticDataTableClassNames.compactNumericCell,
      cell: (row) => row.request_count,
    },
    {
      id: 'consume',
      header: t('qy_dc_consume'),
      className: staticDataTableClassNames.compactHeaderCellRight,
      cellClassName: staticDataTableClassNames.compactNumericCell,
      cell: (row) => <QyAmountText quota={row.consume_quota} />,
    },
    {
      id: 'base',
      header: t('qy_dc_commission_base'),
      className: staticDataTableClassNames.compactHeaderCellRight,
      cellClassName: staticDataTableClassNames.compactNumericCell,
      cell: (row) => <QyAmountText quota={row.commission_base_quota} />,
    },
    {
      id: 'uncounted',
      header: t('qy_dc_uncounted'),
      className: staticDataTableClassNames.compactHeaderCellRight,
      cellClassName: staticDataTableClassNames.compactNumericCell,
      // 未计佣额为 0 是最常见也最不需要注意的情形，所以只在 > 0 时才强调。
      cell: (row) =>
        row.uncounted_quota > 0 ? (
          <QyAmountText quota={row.uncounted_quota} />
        ) : (
          <span className='text-muted-foreground'>-</span>
        ),
    },
    {
      id: 'inviter',
      header: t('qy_dc_inviter'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactCell,
      // “没有上线”与“有上线但一分钱佣金都没产生”是两件不同的事，必须分开显示：
      // 前者是这个人根本不在返佣体系里，后者才是需要去查费率/拉黑的信号。
      cell: (row) =>
        row.inviter_id > 0 ? (
          <span className='inline-flex items-center gap-1.5'>
            {row.inviter_username || `#${row.inviter_id}`}
            {!row.has_commission && (
              <Badge variant='outline'>{t('qy_dc_no_commission')}</Badge>
            )}
          </span>
        ) : (
          <span className='text-muted-foreground'>{t('qy_dc_no_inviter')}</span>
        ),
    },
  ]

  const resetPage = () => setPage(1)

  return (
    <QySectionPageLayout>
      <QySectionPageLayout.Title>{t('qy_dc_title')}</QySectionPageLayout.Title>
      <QySectionPageLayout.Actions>
        <Button
          variant='outline'
          size='sm'
          disabled={exportMutation.isPending || rows.length === 0}
          onClick={() => exportMutation.mutate()}
        >
          <Download aria-hidden='true' />
          {t('qy_dc_export')}
        </Button>
      </QySectionPageLayout.Actions>

      <QySectionPageLayout.Content>
        {/* 两个金额列为什么对不上 —— 写死在页面上，而不是等运营来问。 */}
        <p className='text-muted-foreground text-sm'>{t('qy_dc_gap_hint')}</p>

        {data?.index_ready === false && (
          <p className='text-destructive flex items-center gap-1.5 text-sm'>
            <AlertTriangle aria-hidden='true' className='size-4' />
            {t('qy_dc_index_missing')}
          </p>
        )}
        {(data?.accrual_users_without_logs ?? 0) > 0 && (
          <p className='text-muted-foreground text-sm'>
            {t('qy_dc_logs_pruned', {
              count: data?.accrual_users_without_logs ?? 0,
            })}
          </p>
        )}

        <div className='flex flex-wrap items-end gap-2'>
          <label className='flex flex-col gap-1 text-xs'>
            {t('qy_dc_start_date')}
            <Input
              type='date'
              value={startDate}
              onChange={(e) => {
                setStartDate(e.target.value)
                resetPage()
              }}
            />
          </label>
          <label className='flex flex-col gap-1 text-xs'>
            {t('qy_dc_end_date')}
            <Input
              type='date'
              value={endDate}
              onChange={(e) => {
                setEndDate(e.target.value)
                resetPage()
              }}
            />
          </label>
          <label className='flex flex-col gap-1 text-xs'>
            {t('qy_dc_keyword')}
            <Input
              value={keyword}
              placeholder={t('qy_dc_keyword_ph')}
              onChange={(e) => {
                setKeyword(e.target.value)
                resetPage()
              }}
            />
          </label>
          <label className='flex flex-col gap-1 text-xs'>
            {t('qy_dc_sort')}
            <NativeSelect
              value={sort}
              onChange={(e) => {
                setSort(e.target.value as QyDailyConsumeSort)
                resetPage()
              }}
            >
              {SORT_OPTIONS.map((key) => (
                <NativeSelectOption key={key} value={key}>
                  {t(`qy_dc_sort_${key}`)}
                </NativeSelectOption>
              ))}
            </NativeSelect>
          </label>
          <label className='flex flex-col gap-1 text-xs'>
            {t('qy_dc_order')}
            <NativeSelect
              value={order}
              onChange={(e) => {
                setOrder(e.target.value === 'asc' ? 'asc' : 'desc')
                resetPage()
              }}
            >
              <NativeSelectOption value='desc'>
                {t('qy_dc_order_desc')}
              </NativeSelectOption>
              <NativeSelectOption value='asc'>
                {t('qy_dc_order_asc')}
              </NativeSelectOption>
            </NativeSelect>
          </label>
        </div>

        {data !== undefined && (
          <p className='text-muted-foreground text-sm'>
            {t('qy_dc_summary', {
              days: data.range.days,
              users: data.summary.user_count,
              requests: data.summary.request_count,
            })}
          </p>
        )}

        <StaticDataTable
          columns={columns}
          data={rows}
          getRowKey={(row) => row.user_id}
        />
        <QyPager
          page={data?.p ?? page}
          pageSize={data?.page_size ?? QY_PAGE_SIZE}
          total={data?.total ?? 0}
          onPageChange={setPage}
        />
      </QySectionPageLayout.Content>
    </QySectionPageLayout>
  )
}
