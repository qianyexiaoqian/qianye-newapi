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
import { LifeBuoy } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { StaticDataTable } from '@/components/data-table'
import { StatusBadge } from '@/components/status-badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

import { QyPageBoundary } from '../../components/qy-page-boundary'
import { QySectionPageLayout } from '../../components/qy-section-page-layout'
import { QyStatusBadge } from '../../components/qy-status-badge'
import { qyKeys } from '../../lib/query-keys'
import { QyPager } from '../components/qy-pager'
import { QyStatGrid } from '../components/qy-stat-grid'
import { formatQyTs } from '../ops/format'
import { getQyTicketConfig } from '../tickets/api'
import {
  QY_TICKET_STATUSES,
  getQyTicketPriorityStyle,
  QY_TICKET_PRIORITIES,
} from '../tickets/lib/priority'
import type { QyTicket } from '../tickets/types'
import { getQyAdminTicketStats, listQyAdminTickets } from './api'
import { QyAdminTicketDialog } from './components/admin-ticket-dialog'

const PAGE_SIZE = 20
const ALL = 'all'

/**
 * 工单队列（管理端）。
 *
 * 排序由后端定：**等得最久的在前**，而不是按等级。等级排前面听起来更合理，
 * 但那一列是**用户自己填的** —— 一批被标成"紧急"的低价值工单会永久压住正常
 * 队列。等级在这里的用处是筛选，不是全局排序权重。
 */
export function QyAdminTickets() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()

  const [page, setPage] = useState(1)
  const [status, setStatus] = useState<string>(ALL)
  const [priority, setPriority] = useState<string>(ALL)
  const [keyword, setKeyword] = useState('')
  const [keywordDraft, setKeywordDraft] = useState('')
  const [detailId, setDetailId] = useState<number | null>(null)

  const listParams = {
    p: page,
    page_size: PAGE_SIZE,
    status: status === ALL ? undefined : status,
    priority: priority === ALL ? undefined : priority,
    keyword: keyword === '' ? undefined : keyword,
  }

  const listQuery = useQuery({
    queryKey: qyKeys.adminTickets(listParams),
    queryFn: () => listQyAdminTickets(listParams),
    staleTime: 10_000,
  })
  const statsQuery = useQuery({
    queryKey: qyKeys.adminTicketStats(),
    queryFn: getQyAdminTicketStats,
    staleTime: 30_000,
  })
  // 图片上限口径从用户端 config 取:管理员本身也是登录用户,那条接口对他可用,
  // 而后端的图片上限是全站一份 —— 再给管理端复制一个回显接口就是第二份拷贝。
  const configQuery = useQuery({
    queryKey: qyKeys.ticketConfig(),
    queryFn: getQyTicketConfig,
    staleTime: 60_000,
  })

  const rows = listQuery.data?.items ?? []
  const buckets = statsQuery.data?.buckets ?? []
  const countOf = (name: string) =>
    buckets.find((bucket) => bucket.status === name)?.count ?? 0

  return (
    <QySectionPageLayout>
      <QySectionPageLayout.Title>
        {t('qy_tk_a_title')}
      </QySectionPageLayout.Title>
      <QySectionPageLayout.Content>
        <div className='space-y-3'>
          <QyStatGrid
            items={[
              {
                key: 'open',
                label: t('qy_common_st_open'),
                value: countOf('open'),
                emphasis: true,
              },
              {
                key: 'user_replied',
                label: t('qy_common_st_user_replied'),
                value: countOf('user_replied'),
                emphasis: true,
              },
              {
                key: 'replied',
                label: t('qy_common_st_replied'),
                value: countOf('replied'),
              },
              {
                key: 'closed',
                label: t('qy_common_st_closed'),
                value: countOf('closed'),
              },
            ]}
          />

          <div className='flex flex-wrap items-center gap-2'>
            <Select
              value={status}
              onValueChange={(value) => {
                // Base UI 的 Select 在"清空选中"时给 null。本页没有可清空的
                // 入口，但类型上它是合法值 —— 回落到 ALL 而不是断言非空。
                setStatus(value ?? ALL)
                setPage(1)
              }}
            >
              <SelectTrigger size='sm' className='w-40'>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={ALL}>{t('qy_tk_a_filter_all')}</SelectItem>
                {QY_TICKET_STATUSES.map((item) => (
                  <SelectItem key={item} value={item}>
                    {t(`qy_common_st_${item}`)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>

            <Select
              value={priority}
              onValueChange={(value) => {
                setPriority(value ?? ALL)
                setPage(1)
              }}
            >
              <SelectTrigger size='sm' className='w-36'>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={ALL}>{t('qy_tk_a_filter_all')}</SelectItem>
                {QY_TICKET_PRIORITIES.map((item) => (
                  <SelectItem key={item} value={item}>
                    {t(getQyTicketPriorityStyle(item).labelKey)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>

            {/* 关键词只匹配单号与标题（后端如此），不匹配正文 ——
                正文是无索引的 text 列，LIKE '%…%' 就是全表扫，而这一页是
                客服每分钟都在刷新的。占位文案必须如实说明，否则客服会
                以为搜不到就是没有。 */}
            <Input
              className='h-8 w-56'
              value={keywordDraft}
              placeholder={t('qy_tk_a_search_ph')}
              onChange={(event) => setKeywordDraft(event.target.value)}
              onKeyDown={(event) => {
                if (event.key !== 'Enter') return
                setKeyword(keywordDraft.trim())
                setPage(1)
              }}
            />
            <Button
              type='button'
              size='sm'
              variant='outline'
              onClick={() => {
                setKeyword(keywordDraft.trim())
                setPage(1)
              }}
            >
              {t('qy_common_search')}
            </Button>
          </div>

          <QyPageBoundary
            query={listQuery}
            isEmpty={listQuery.data != null && rows.length === 0}
            emptyIcon={LifeBuoy}
            emptyTitle={t('qy_tk_a_empty')}
            emptyDescription={t('qy_tk_a_empty_desc')}
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
                        className='text-primary max-w-[20rem] truncate text-left hover:underline'
                        onClick={() => setDetailId(row.id)}
                      >
                        {row.title}
                      </button>
                    ),
                  },
                  {
                    id: 'user',
                    header: t('qy_tk_col_user'),
                    cell: (row: QyTicket) => `${row.username} #${row.user_id}`,
                  },
                  {
                    id: 'priority',
                    header: t('qy_tk_col_priority'),
                    cell: (row: QyTicket) => (
                      <StatusBadge
                        label={t(
                          getQyTicketPriorityStyle(row.priority).labelKey
                        )}
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
                    id: 'assignee',
                    header: t('qy_tk_col_assignee'),
                    // 指派动作在处理台里（点标题进去）。这里只显示结果 ——
                    // 把一张单派给谁是需要先读完对话才能做的判断，
                    // 放进表格单元格只会鼓励不看内容就分派。
                    cell: (row: QyTicket) =>
                      row.assignee_id > 0 ? row.assignee_name : '-',
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

          <QyAdminTicketDialog
            ticketId={detailId}
            config={configQuery.data}
            onClose={() => setDetailId(null)}
            onChanged={() => {
              void queryClient.invalidateQueries({ queryKey: qyKeys.all })
            }}
          />
        </div>
      </QySectionPageLayout.Content>
    </QySectionPageLayout>
  )
}
