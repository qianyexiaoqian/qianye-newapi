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
import { CloudOff, LifeBuoy, Plus } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { StaticDataTable } from '@/components/data-table'
import { EmptyState } from '@/components/empty-state'
import { StatusBadge } from '@/components/status-badge'
import { Button } from '@/components/ui/button'

import { QyPageBoundary } from '../../components/qy-page-boundary'
import { QySectionPageLayout } from '../../components/qy-section-page-layout'
import { QyStatusBadge } from '../../components/qy-status-badge'
import { useQyConfig } from '../../hooks/use-qy-config'
import { qyKeys } from '../../lib/query-keys'
import { QyPager } from '../components/qy-pager'
import { formatQyTs } from '../ops/format'
import { getQyTicketConfig, listQyTickets } from './api'
import { QyTicketComposeDialog } from './components/ticket-compose-dialog'
import { QyTicketDetailDialog } from './components/ticket-detail-dialog'
import { getQyTicketPriorityStyle } from './lib/priority'
import type { QyTicket } from './types'

const PAGE_SIZE = 20

/**
 * 我的工单。
 *
 * 一页搞定"列表 + 新建 + 详情/追加/关闭"：工单的全部交互都围绕一条对话，
 * 拆成两个路由只会让用户在"提交完跳回列表、再点进去看回复"之间来回跳。
 *
 * 「还能开几张」显示在新建按钮旁边，而不是等提交失败才说 —— 后端拦得住不代表
 * 用户知道为什么被拦，而他此时已经把整篇正文写完了。
 */
export function QyTickets() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const config = useQyConfig()

  const [page, setPage] = useState(1)
  const [composing, setComposing] = useState(false)
  // 用业务单号寻址：用户端视图不下发自增 id（列表里那一列恒为 0）。
  const [detailNo, setDetailNo] = useState<string | null>(null)

  const featureOff = config.status === 'enabled' && !config.features.ticket

  const configQuery = useQuery({
    queryKey: qyKeys.ticketConfig(),
    queryFn: getQyTicketConfig,
    enabled: !featureOff,
    staleTime: 60_000,
  })

  const listParams = { p: page, page_size: PAGE_SIZE }
  const listQuery = useQuery({
    queryKey: qyKeys.ticketList(listParams),
    queryFn: () => listQyTickets(listParams),
    enabled: !featureOff,
    staleTime: 15_000,
  })

  if (featureOff) {
    return (
      <QySectionPageLayout>
        <QySectionPageLayout.Title>
          {t('qy_tk_my_title')}
        </QySectionPageLayout.Title>
        <QySectionPageLayout.Content>
          <EmptyState
            icon={CloudOff}
            title={t('qy_err_feature_off')}
            description={t('qy_cfg_disabled_desc')}
          />
        </QySectionPageLayout.Content>
      </QySectionPageLayout>
    )
  }

  const ticketConfig = configQuery.data
  const rows = listQuery.data?.items ?? []
  const remaining =
    ticketConfig == null || ticketConfig.max_open_per_user <= 0
      ? null
      : Math.max(0, ticketConfig.max_open_per_user - ticketConfig.open_count)

  const invalidate = () => {
    void queryClient.invalidateQueries({ queryKey: qyKeys.all })
  }

  return (
    <QySectionPageLayout>
      <QySectionPageLayout.Title>
        {t('qy_tk_my_title')}
      </QySectionPageLayout.Title>
      <QySectionPageLayout.Actions>
        <div className='flex items-center gap-2'>
          {remaining != null && (
            <span className='text-muted-foreground text-xs'>
              {t('qy_tk_open_remaining', { n: remaining })}
            </span>
          )}
          <Button
            type='button'
            size='sm'
            disabled={ticketConfig == null || remaining === 0}
            onClick={() => setComposing(true)}
          >
            <Plus aria-hidden='true' />
            {t('qy_tk_new_action')}
          </Button>
        </div>
      </QySectionPageLayout.Actions>
      <QySectionPageLayout.Content>
        <QyPageBoundary
          query={listQuery}
          isEmpty={listQuery.data != null && rows.length === 0}
          emptyIcon={LifeBuoy}
          emptyTitle={t('qy_tk_my_empty')}
          emptyDescription={t('qy_tk_my_empty_desc')}
        >
          <div className='space-y-3'>
            <StaticDataTable
              data={rows}
              getRowKey={(row) => row.ticket_no}
              columns={[
                {
                  id: 'title',
                  header: t('qy_tk_col_title'),
                  cell: (row: QyTicket) => (
                    <button
                      type='button'
                      className='text-primary max-w-[22rem] truncate text-left hover:underline'
                      onClick={() => setDetailNo(row.ticket_no)}
                    >
                      {row.title}
                    </button>
                  ),
                },
                {
                  id: 'priority',
                  header: t('qy_tk_col_priority'),
                  cell: (row: QyTicket) => (
                    <StatusBadge
                      label={t(getQyTicketPriorityStyle(row.priority).labelKey)}
                      variant={getQyTicketPriorityStyle(row.priority).variant}
                      copyable={false}
                      size='sm'
                    />
                  ),
                },
                {
                  id: 'status',
                  header: t('qy_common_status'),
                  cell: (row: QyTicket) => (
                    <QyStatusBadge status={row.status} />
                  ),
                },
                {
                  id: 'messages',
                  header: t('qy_tk_col_messages'),
                  cell: (row: QyTicket) => row.message_count,
                },
                {
                  id: 'last_reply_at',
                  header: t('qy_tk_col_last_reply'),
                  cell: (row: QyTicket) => formatQyTs(row.last_reply_at),
                },
              ]}
            />

            <QyPager
              page={page}
              pageSize={PAGE_SIZE}
              total={listQuery.data?.total ?? 0}
              onPageChange={setPage}
            />
          </div>
        </QyPageBoundary>

        {ticketConfig != null && (
          <>
            <QyTicketComposeDialog
              open={composing}
              config={ticketConfig}
              onOpenChange={setComposing}
              onCreated={invalidate}
            />
            <QyTicketDetailDialog
              ticketNo={detailNo}
              config={ticketConfig}
              onClose={() => setDetailNo(null)}
              onChanged={invalidate}
            />
          </>
        )}
      </QySectionPageLayout.Content>
    </QySectionPageLayout>
  )
}
