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
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { FileSearch, ShieldAlert, Undo2 } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { StaticDataTable } from '@/components/data-table'
import { StatusBadge } from '@/components/status-badge'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

import { QyAmountText } from '../../../components/qy-amount-text'
import { QyPageBoundary } from '../../../components/qy-page-boundary'
import { qyKeys } from '../../../lib/query-keys'
import { QyPager } from '../../components/qy-pager'
import { formatQyTs, qySinceHours } from '../../ops/format'
import { QyFilterBar, QyFilterField } from '../../ops/qy-ops-ui'
import { listQyViolationRecords } from '../api'
import type { QyViolationRecord } from '../types'
import { QyViolationEvidenceDialog } from './violation-evidence-dialog'
import { QyViolationRevokeDialog } from './violation-revoke-dialog'

const PAGE_SIZE = 20
const ALL = 'all'

const RECORD_STATUS_VARIANT: Record<string, 'danger' | 'neutral' | 'warning'> =
  {
    active: 'danger',
    appealed: 'warning',
    revoked: 'neutral',
  }

export function QyViolationRecordsTab() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()

  const [page, setPage] = useState(1)
  const [status, setStatus] = useState(ALL)
  const [hours, setHours] = useState(168)
  const [userId, setUserId] = useState('')
  const [model, setModel] = useState('')
  const [requestId, setRequestId] = useState('')
  const [evidenceRecord, setEvidenceRecord] =
    useState<QyViolationRecord | null>(null)
  const [revokeRecord, setRevokeRecord] = useState<QyViolationRecord | null>(
    null
  )

  const params = useMemo(
    () => ({
      p: page,
      page_size: PAGE_SIZE,
      status: status === ALL ? undefined : status,
      user_id: userId.trim() === '' ? undefined : Number(userId),
      model: model.trim() === '' ? undefined : model.trim(),
      request_id: requestId.trim() === '' ? undefined : requestId.trim(),
      start_ts: hours > 0 ? qySinceHours(hours) : undefined,
    }),
    [hours, model, page, requestId, status, userId]
  )

  const query = useQuery({
    queryKey: qyKeys.adminViolationRecords(params),
    queryFn: () => listQyViolationRecords(params),
    staleTime: 15_000,
  })

  const records = query.data?.items ?? []
  const resetPage = () => setPage(1)

  return (
    <div className='space-y-3'>
      <QyFilterBar>
        <QyFilterField label={t('qy_common_status')}>
          <Select
            value={status}
            onValueChange={(value) => {
              setStatus(value ?? ALL)
              resetPage()
            }}
          >
            <SelectTrigger className='w-32'>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL}>{t('qy_common_all')}</SelectItem>
              <SelectItem value='active'>
                {t('qy_vio_record_active')}
              </SelectItem>
              <SelectItem value='appealed'>
                {t('qy_vio_record_appealed')}
              </SelectItem>
              <SelectItem value='revoked'>
                {t('qy_vio_record_revoked')}
              </SelectItem>
            </SelectContent>
          </Select>
        </QyFilterField>

        <QyFilterField label={t('qy_avl_range')}>
          <Select
            value={String(hours)}
            onValueChange={(value) => {
              setHours(Number(value ?? hours))
              resetPage()
            }}
          >
            <SelectTrigger className='w-28'>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value='24'>{t('qy_avl_range_24h')}</SelectItem>
              <SelectItem value='168'>{t('qy_avl_range_7d')}</SelectItem>
              <SelectItem value='720'>{t('qy_avl_range_30d')}</SelectItem>
              <SelectItem value='0'>{t('qy_common_all')}</SelectItem>
            </SelectContent>
          </Select>
        </QyFilterField>

        <QyFilterField label={t('qy_vio_filter_user_id')}>
          <Input
            className='w-28'
            inputMode='numeric'
            value={userId}
            onChange={(event) => {
              setUserId(event.target.value.replaceAll(/\D/g, ''))
              resetPage()
            }}
          />
        </QyFilterField>

        <QyFilterField label={t('qy_avl_model')}>
          <Input
            className='w-40'
            value={model}
            onChange={(event) => {
              setModel(event.target.value)
              resetPage()
            }}
          />
        </QyFilterField>

        <QyFilterField label={t('qy_vio_filter_request_id')}>
          <Input
            className='w-48'
            value={requestId}
            onChange={(event) => {
              setRequestId(event.target.value)
              resetPage()
            }}
          />
        </QyFilterField>
      </QyFilterBar>

      <QyPageBoundary
        query={query}
        isEmpty={query.data != null && records.length === 0}
        emptyIcon={ShieldAlert}
        emptyTitle={t('qy_vio_records_empty')}
      >
        <div className='space-y-3'>
          <StaticDataTable
            data={records}
            getRowKey={(row) => row.id}
            columns={[
              {
                id: 'created_at',
                header: t('qy_common_time'),
                cell: (row: QyViolationRecord) => formatQyTs(row.created_at),
              },
              {
                id: 'user',
                header: t('qy_common_user'),
                cell: (row: QyViolationRecord) =>
                  `${row.username} (#${row.user_id})`,
              },
              {
                id: 'model',
                header: t('qy_avl_model'),
                cell: (row: QyViolationRecord) => row.model_name,
              },
              {
                id: 'rule',
                header: t('qy_vio_col_rule'),
                cell: (row: QyViolationRecord) => row.rule_name,
              },
              {
                id: 'flags',
                header: t('qy_vio_col_flags'),
                cell: (row: QyViolationRecord) => (
                  <span className='flex flex-wrap gap-1'>
                    {/* 影子命中意味着用户并没有被扣钱，与真实命中必须分得开 */}
                    {row.shadow && (
                      <Badge variant='outline'>{t('qy_vio_flag_shadow')}</Badge>
                    )}
                    {row.blocked && (
                      <Badge variant='destructive'>
                        {t('qy_vio_flag_blocked')}
                      </Badge>
                    )}
                    {row.quota_clamp !== '' && (
                      <Badge variant='destructive'>
                        {t('qy_vio_flag_clamp')}
                      </Badge>
                    )}
                  </span>
                ),
              },
              {
                id: 'fee',
                header: t('qy_vio_col_fee'),
                cell: (row: QyViolationRecord) => (
                  <span className='flex flex-col'>
                    <QyAmountText quota={row.fee_quota} />
                    <span className='text-muted-foreground text-xs'>
                      {t(`qy_vio_fee_status_${row.fee_status}`, {
                        defaultValue: row.fee_status,
                      })}
                    </span>
                  </span>
                ),
              },
              {
                id: 'counter',
                header: t('qy_vio_col_counter'),
                cellClassName: 'tabular-nums',
                cell: (row: QyViolationRecord) =>
                  row.counted ? row.counter_after : '-',
              },
              {
                id: 'status',
                header: t('qy_common_status'),
                cell: (row: QyViolationRecord) => (
                  <StatusBadge
                    label={t(`qy_vio_record_${row.status}`)}
                    variant={RECORD_STATUS_VARIANT[row.status] ?? 'neutral'}
                    copyable={false}
                  />
                ),
              },
              {
                id: 'actions',
                header: t('qy_common_actions'),
                cell: (row: QyViolationRecord) => (
                  <span className='flex items-center gap-1'>
                    <Button
                      type='button'
                      variant='ghost'
                      size='icon-sm'
                      aria-label={t('qy_vio_evidence_title')}
                      disabled={!row.has_payload}
                      onClick={() => setEvidenceRecord(row)}
                    >
                      <FileSearch aria-hidden='true' />
                    </Button>
                    <Button
                      type='button'
                      variant='ghost'
                      size='icon-sm'
                      aria-label={t('qy_vio_revoke_title')}
                      disabled={row.status === 'revoked'}
                      onClick={() => setRevokeRecord(row)}
                    >
                      <Undo2 aria-hidden='true' />
                    </Button>
                  </span>
                ),
              },
            ]}
          />

          <QyPager
            page={page}
            pageSize={PAGE_SIZE}
            total={query.data?.total ?? 0}
            onPageChange={setPage}
          />
        </div>
      </QyPageBoundary>

      <QyViolationEvidenceDialog
        record={evidenceRecord}
        onClose={() => setEvidenceRecord(null)}
      />
      <QyViolationRevokeDialog
        record={revokeRecord}
        onClose={() => setRevokeRecord(null)}
        onDone={() => {
          void queryClient.invalidateQueries({ queryKey: qyKeys.all })
        }}
      />
    </div>
  )
}
