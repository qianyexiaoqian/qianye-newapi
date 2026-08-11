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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, useSearch } from '@tanstack/react-router'
import { Link2, ScrollText, Settings2, Wallet } from 'lucide-react'
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
import { formatTimestampToDate } from '@/lib/format'

import { QyAmountText } from '../../components/qy-amount-text'
import { QyPageBoundary } from '../../components/qy-page-boundary'
import { QySectionPageLayout } from '../../components/qy-section-page-layout'
import { qyErrorMessage } from '../../lib/api'
import { qyKeys } from '../../lib/query-keys'
import {
  qyAdminAccrualsQuery,
  qyBlockInviteRelation,
  qySettleCommission,
} from '../admin-commission/api'
import type { QyAdminAccrual } from '../admin-commission/types'
import { QyPager } from '../components/qy-pager'
import { QY_PAGE_SIZE } from '../lib/constants'
import { ClawbackDialog } from './components/clawback-dialog'

/** 与后端 `commission/model.go` 的四个计佣行状态一致。 */
const STATUS_OPTIONS = ['accrued', 'settled', 'risk_hold', 'voided'] as const
// manual 是管理员手工增减佣金落下的账目行（`api_admin_adjust.go`）。它必须能被
// 单独筛出来：那是唯一没有业务单据触发的一类佣金，对账时第一个要看的就是它。
const SOURCE_OPTIONS = [
  'topup',
  'redemption',
  'consume',
  'clawback',
  'manual',
] as const

/**
 * 佣金审核。
 *
 * 三个动作的语义边界必须分清，否则会误伤：
 *   - **冲正**：写一条负额计佣行并扣减余额，是"把已经发出去的钱要回来"；
 *   - **拉黑关系**：只停止未来计佣，**不回收已发放的佣金**；
 *   - **立即结算**：只是把已成熟的计佣提前落账，不改变任何金额。
 */
export function QyAdminCommissionRecords() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()

  // 从佣金余额那张表下钻进来时,URL 上带着 `?inviter_id=412`。只拿它做**初值**、
  // 之后由输入框自己接管:双向同步会让每敲一个字符就压一条历史记录,而运营改完
  // 筛选按返回键期望回到上一个页面,不是回到"少打一个字"的那一帧。
  const search = useSearch({
    from: '/_authenticated/qy/admin/commission-records/',
  })

  const [page, setPage] = useState(1)
  const [status, setStatus] = useState('')
  const [sourceType, setSourceType] = useState('')
  const [inviterId, setInviterId] = useState(search.inviter_id ?? '')
  const [clawbackTarget, setClawbackTarget] = useState<QyAdminAccrual | null>(
    null
  )

  const query = useQuery(
    qyAdminAccrualsQuery({
      p: page,
      page_size: QY_PAGE_SIZE,
      status,
      source_type: sourceType,
      inviter_id: inviterId.trim(),
    })
  )
  const items = query.data?.items ?? []

  const settleMutation = useMutation({
    mutationFn: qySettleCommission,
    onSuccess: async () => {
      toast.success(t('qy_cm_settle_ok'))
      await queryClient.invalidateQueries({ queryKey: qyKeys.all })
    },
    onError: (error) => toast.error(qyErrorMessage(error, t)),
  })

  const blockMutation = useMutation({
    mutationFn: qyBlockInviteRelation,
    onSuccess: async (result) => {
      toast.success(
        result.blocked ? t('qy_cm_block_ok') : t('qy_cm_unblock_ok')
      )
      await queryClient.invalidateQueries({ queryKey: qyKeys.all })
    },
    onError: (error) => toast.error(qyErrorMessage(error, t)),
  })

  const columns: StaticDataTableColumn<QyAdminAccrual>[] = [
    {
      id: 'created_at',
      header: t('qy_common_time'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactMutedCell,
      cell: (row) => formatTimestampToDate(row.created_at),
    },
    {
      id: 'inviter',
      header: t('qy_cm_inviter'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactCell,
      cell: (row) => `#${row.inviter_id}`,
    },
    {
      id: 'invitee',
      header: t('qy_aff_invitee'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactCell,
      cell: (row) => `#${row.invitee_id}`,
    },
    {
      id: 'source',
      header: t('qy_aff_source'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactCell,
      cell: (row) => t(`qy_aff_src_${row.source_type}`, row.source_type),
    },
    {
      id: 'base',
      header: t('qy_aff_base_quota'),
      className: staticDataTableClassNames.compactHeaderCellRight,
      cellClassName: staticDataTableClassNames.compactNumericCell,
      cell: (row) => <QyAmountText quota={row.base_quota} />,
    },
    {
      id: 'gross',
      header: t('qy_aff_gross'),
      className: staticDataTableClassNames.compactHeaderCellRight,
      cellClassName: staticDataTableClassNames.compactNumericCell,
      // decimal(30,10) 字符串原样展示：转 number 会在长尾小数上丢位。
      cell: (row) => row.gross_amount,
    },
    {
      id: 'settled',
      header: t('qy_cm_settled_amount'),
      className: staticDataTableClassNames.compactHeaderCellRight,
      cellClassName: staticDataTableClassNames.compactMutedNumericCell,
      cell: (row) => row.settled_amount,
    },
    {
      id: 'status',
      header: t('qy_common_status'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactCell,
      cell: (row) => (
        <span className='inline-flex items-center gap-1.5'>
          <Badge
            variant={row.status === 'risk_hold' ? 'destructive' : 'secondary'}
          >
            {t(`qy_aff_st_${row.status}`, row.status)}
          </Badge>
          {row.risk_flags !== '' && (
            <Badge variant='outline'>{row.risk_flags}</Badge>
          )}
        </span>
      ),
    },
    {
      id: 'actions',
      header: t('qy_common_actions'),
      className: staticDataTableClassNames.actionHeaderCell,
      cellClassName: staticDataTableClassNames.actionCell,
      cell: (row) => (
        <div className='flex justify-end gap-1'>
          <Button
            variant='ghost'
            size='sm'
            disabled={settleMutation.isPending}
            onClick={() => settleMutation.mutate(row.inviter_id)}
          >
            {t('qy_cm_settle')}
          </Button>
          <Button
            variant='ghost'
            size='sm'
            disabled={blockMutation.isPending}
            onClick={() =>
              blockMutation.mutate({
                invitee_id: row.invitee_id,
                blocked: true,
                reason: t('qy_cm_block_default_reason'),
              })
            }
          >
            {t('qy_cm_block')}
          </Button>
          <Button
            variant='ghost'
            size='sm'
            onClick={() => setClawbackTarget(row)}
          >
            {t('qy_cm_clawback')}
          </Button>
        </div>
      ),
    },
  ]

  const resetPage = () => setPage(1)

  return (
    <QySectionPageLayout>
      <QySectionPageLayout.Title>
        {t('qy_nav_a_commission_records')}
      </QySectionPageLayout.Title>
      <QySectionPageLayout.Actions>
        {/* 佣金余额与 AFF 关系现在**各有一个侧栏入口**（「结算」组，本轮补上）。
            这两个按钮不是重复：侧栏回答"从零开始去哪找"，这里回答"我正看着这
            一笔，另外两张表怎么开"。删掉它们运营就得绕回侧栏重新找一遍。 */}
        <Button
          variant='outline'
          size='sm'
          render={<Link to='/qy/admin/commission-records/balances' />}
        >
          <Wallet aria-hidden='true' />
          {t('qy_cb_title')}
        </Button>
        <Button
          variant='outline'
          size='sm'
          render={<Link to='/qy/admin/commission-records/relations' />}
        >
          <Link2 aria-hidden='true' />
          {t('qy_rel_title')}
        </Button>
        <Button
          variant='outline'
          size='sm'
          render={<Link to='/qy/admin/commission' />}
        >
          <Settings2 aria-hidden='true' />
          {t('qy_nav_a_commission')}
        </Button>
      </QySectionPageLayout.Actions>
      <QySectionPageLayout.Content>
        <div className='space-y-3'>
          <div className='flex flex-wrap items-center gap-2'>
            <NativeSelect
              size='sm'
              aria-label={t('qy_common_status')}
              value={status}
              onChange={(event) => {
                resetPage()
                setStatus(event.target.value)
              }}
            >
              <NativeSelectOption value=''>
                {t('qy_common_all')}
              </NativeSelectOption>
              {STATUS_OPTIONS.map((value) => (
                <NativeSelectOption key={value} value={value}>
                  {t(`qy_aff_st_${value}`)}
                </NativeSelectOption>
              ))}
            </NativeSelect>

            <NativeSelect
              size='sm'
              aria-label={t('qy_aff_source')}
              value={sourceType}
              onChange={(event) => {
                resetPage()
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

            <Input
              className='h-8 w-44'
              inputMode='numeric'
              value={inviterId}
              placeholder={t('qy_cm_inviter_id_ph')}
              onChange={(event) => {
                resetPage()
                setInviterId(event.target.value)
              }}
            />
          </div>

          <QyPageBoundary
            query={query}
            isEmpty={items.length === 0}
            emptyIcon={ScrollText}
            emptyTitle={t('qy_cm_empty_title')}
            emptyDescription={t('qy_cm_empty_desc')}
          >
            <div className='w-full overflow-x-auto'>
              <StaticDataTable
                columns={columns}
                data={items}
                getRowKey={(row) => row.accrual_no}
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
      </QySectionPageLayout.Content>

      <ClawbackDialog
        accrual={clawbackTarget}
        onClose={() => setClawbackTarget(null)}
      />
    </QySectionPageLayout>
  )
}
