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
import { useTranslation } from 'react-i18next'

import { StaticDataTable } from '@/components/data-table'
import { Badge } from '@/components/ui/badge'

import { QyPageBoundary } from '../../../components/qy-page-boundary'
import { qyArray } from '../../../lib/array'
import { QY_EMPTY_TEXT, formatQyTs } from '../../ops/format'
import { qyAdminLotEventsQuery, qyAdminLotFlagsQuery } from '../api'
import type { QyLotAdminEvent, QyLotAdminFlag } from '../types'

/**
 * 事件流与对账异常。
 *
 * ## 事件流不是审计日志
 *
 * 它对**用户可见**、属于证据链，而且永不清理。管理审计（`qy_audit_logs`）是另
 * 一张表，有保留期、只给管理员看。两边都要写 —— 证据链绝不能依赖一张会被
 * Prune 的表。
 *
 * ## 异常清单是这一页真正的工作项
 *
 * `pool_mismatch` / `count_drift` / `roster_drift` / `payout_stuck` /
 * `orphan_order` 全部来自对账任务的重算比对。`roster_drift` 尤其要紧：
 * 它是"名单被事后改动"的唯一自动检出手段。
 */
export function QyLotEventsTab(props: { actNo: string }) {
  const { t } = useTranslation()
  const events = useQuery(qyAdminLotEventsQuery(props.actNo))
  const flags = useQuery(qyAdminLotFlagsQuery(props.actNo))

  const eventItems = qyArray(events.data?.items)
  const flagItems = qyArray(flags.data?.items)

  return (
    <div className='space-y-4'>
      {flagItems.length > 0 && (
        <div className='space-y-2'>
          <h4 className='text-sm font-medium'>{t('qy_lot_a_flags_title')}</h4>
          <StaticDataTable
            data={flagItems}
            getRowKey={(row: QyLotAdminFlag) => row.id}
            columns={[
              {
                id: 'code',
                header: t('qy_lot_flag_code'),
                cell: (row: QyLotAdminFlag) => (
                  <Badge variant={row.resolved ? 'secondary' : 'destructive'}>
                    {t(`qy_lot_flag_${row.code}`, { defaultValue: row.code })}
                  </Badge>
                ),
              },
              {
                id: 'detail',
                header: t('qy_common_detail'),
                cell: (row: QyLotAdminFlag) => row.detail,
              },
              {
                id: 'created_at',
                header: t('qy_common_time'),
                cell: (row: QyLotAdminFlag) => formatQyTs(row.created_at),
              },
              {
                id: 'resolved_at',
                header: t('qy_lot_flag_resolved_at'),
                cell: (row: QyLotAdminFlag) =>
                  row.resolved_at === 0
                    ? QY_EMPTY_TEXT
                    : formatQyTs(row.resolved_at),
              },
            ]}
          />
        </div>
      )}

      <QyPageBoundary
        query={events}
        isEmpty={events.data != null && eventItems.length === 0}
        emptyTitle={t('qy_lot_a_events_empty')}
      >
        <StaticDataTable
          data={eventItems}
          getRowKey={(row: QyLotAdminEvent) => row.id}
          columns={[
            {
              id: 'created_at',
              header: t('qy_common_time'),
              cell: (row: QyLotAdminEvent) => formatQyTs(row.created_at),
            },
            {
              id: 'action',
              header: t('qy_lot_event_action'),
              cell: (row: QyLotAdminEvent) =>
                t(`qy_lot_event_${row.action}`, { defaultValue: row.action }),
            },
            {
              id: 'transition',
              header: t('qy_lot_event_transition'),
              cellClassName: 'font-mono text-xs',
              cell: (row: QyLotAdminEvent) =>
                `${row.from_status || '-'} → ${row.to_status || '-'}`,
            },
            {
              id: 'actor',
              header: t('qy_common_operator'),
              cell: (row: QyLotAdminEvent) =>
                // 系统触发（封盘、开奖、结算、逾期作废）没有操作人 —— 这正是
                // 「管理员没有提前截止、也没有立即开奖的按钮」的直接证据。
                row.actor_user_id > 0
                  ? `#${row.actor_user_id}`
                  : t('qy_lot_event_actor_system'),
            },
            {
              id: 'detail',
              header: t('qy_common_detail'),
              cellClassName: 'font-mono text-xs break-all',
              cell: (row: QyLotAdminEvent) =>
                row.detail === '' ? QY_EMPTY_TEXT : row.detail,
            },
          ]}
        />
      </QyPageBoundary>
    </div>
  )
}
