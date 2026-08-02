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
import { ChevronDown, ChevronRight, Network } from 'lucide-react'
import { useMemo, useState } from 'react'
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
import { TableCell, TableRow } from '@/components/ui/table'

import { QyPageBoundary } from '../../components/qy-page-boundary'
import { qyKeys } from '../../lib/query-keys'
import { QyPager } from '../components/qy-pager'
import { formatQySnapshot, formatQyTs, qySinceHours } from '../ops/format'
import { QyFilterBar, QyFilterField } from '../ops/qy-ops-ui'
import { listQyRequestAudits } from './api'
import {
  QyAuditDetailLine,
  QyAuditNumberFilter,
  QyAuditRangeFilter,
} from './filters'
import { QY_AUDIT_ALL, QY_AUDIT_PAGE_SIZE, qyAuditTrimmed } from './shared'
import type { QyRequestAudit } from './types'

const COLUMN_COUNT = 8
const METHODS = ['POST', 'PUT', 'PATCH', 'DELETE', 'GET']

/**
 * HTTP 请求台账（`qy_request_audits`）。
 *
 * 与资金审计分成两个 tab 而不是一张表：那张表回答「按什么费率、前后快照是
 * 什么」，一天几十行、每行都要能被逐字辩论；这张表回答「谁调了哪个写接口、
 * 成没成功」，一天几千行，价值在覆盖率。混在一起，资金台账会被稀释到
 * 无法扫读。
 *
 * 默认只看失败：越权探测与暴力枚举全是失败请求，而成功的写请求绝大多数是
 * 管理员的日常操作。把默认值放在失败这一侧，等于每次打开这个 tab 都做了
 * 一次「有没有人在试探」的检查。
 */
export function QyRequestAuditTab() {
  const { t } = useTranslation()

  const [page, setPage] = useState(1)
  const [action, setAction] = useState('')
  const [method, setMethod] = useState(QY_AUDIT_ALL)
  const [success, setSuccess] = useState<'all' | 'false' | 'true'>('all')
  const [ip, setIp] = useState('')
  const [requestId, setRequestId] = useState('')
  const [actorUserId, setActorUserId] = useState('')
  const [targetUserId, setTargetUserId] = useState('')
  const [hours, setHours] = useState(168)
  const [expanded, setExpanded] = useState<number | null>(null)

  const params = useMemo(
    () => ({
      p: page,
      page_size: QY_AUDIT_PAGE_SIZE,
      action: qyAuditTrimmed(action),
      method: method === QY_AUDIT_ALL ? undefined : method,
      success: success === 'all' ? undefined : success,
      ip: qyAuditTrimmed(ip),
      request_id: qyAuditTrimmed(requestId),
      actor_user_id:
        actorUserId.trim() === '' ? undefined : Number(actorUserId),
      target_user_id:
        targetUserId.trim() === '' ? undefined : Number(targetUserId),
      start_ts: hours > 0 ? qySinceHours(hours) : undefined,
    }),
    [
      action,
      actorUserId,
      hours,
      ip,
      method,
      page,
      requestId,
      success,
      targetUserId,
    ]
  )

  const query = useQuery({
    queryKey: qyKeys.adminRequestAudits(params),
    queryFn: () => listQyRequestAudits(params),
    staleTime: 15_000,
  })

  const rows = query.data?.items ?? []
  const resetPage = () => setPage(1)

  return (
    <div className='space-y-3'>
      <p className='text-muted-foreground text-xs'>
        {t('qy_cfg_req_audit_desc')}
      </p>

      <QyFilterBar>
        <QyFilterField label={t('qy_cfg_audit_action')}>
          <Input
            className='w-48'
            value={action}
            onChange={(event) => {
              setAction(event.target.value)
              resetPage()
            }}
            placeholder='admin.withdraw.'
          />
        </QyFilterField>

        <QyFilterField label={t('qy_cfg_req_audit_method')}>
          <Select
            value={method}
            onValueChange={(value) => {
              setMethod(value ?? QY_AUDIT_ALL)
              resetPage()
            }}
          >
            <SelectTrigger className='w-28'>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={QY_AUDIT_ALL}>{t('qy_common_all')}</SelectItem>
              {METHODS.map((item) => (
                <SelectItem key={item} value={item}>
                  {item}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </QyFilterField>

        <QyFilterField label={t('qy_common_status')}>
          <Select
            value={success}
            onValueChange={(value) => {
              setSuccess((value ?? 'all') as 'all' | 'false' | 'true')
              resetPage()
            }}
          >
            <SelectTrigger className='w-32'>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value='all'>{t('qy_common_all')}</SelectItem>
              <SelectItem value='false'>
                {t('qy_cfg_req_audit_failed')}
              </SelectItem>
              <SelectItem value='true'>{t('qy_cfg_req_audit_ok')}</SelectItem>
            </SelectContent>
          </Select>
        </QyFilterField>

        <QyFilterField label={t('qy_cfg_audit_ip')}>
          <Input
            className='w-36'
            value={ip}
            onChange={(event) => {
              setIp(event.target.value)
              resetPage()
            }}
          />
        </QyFilterField>

        <QyFilterField label={t('qy_cfg_audit_request_id')}>
          <Input
            className='w-44'
            value={requestId}
            onChange={(event) => {
              setRequestId(event.target.value)
              resetPage()
            }}
          />
        </QyFilterField>

        <QyAuditNumberFilter
          label={t('qy_cfg_audit_actor_id')}
          value={actorUserId}
          onChange={(value) => {
            setActorUserId(value)
            resetPage()
          }}
        />
        <QyAuditNumberFilter
          label={t('qy_cfg_audit_target')}
          value={targetUserId}
          onChange={(value) => {
            setTargetUserId(value)
            resetPage()
          }}
        />

        <QyAuditRangeFilter
          hours={hours}
          onChange={(value) => {
            setHours(value)
            resetPage()
          }}
        />
      </QyFilterBar>

      <QyPageBoundary
        query={query}
        isEmpty={query.data != null && rows.length === 0}
        emptyIcon={Network}
        emptyTitle={t('qy_cfg_req_audit_empty')}
      >
        <div className='space-y-3'>
          <StaticDataTable
            data={rows}
            getRowKey={(row) => row.id}
            columns={[
              { id: 'expand', header: '', className: 'w-8' },
              { id: 'created_at', header: t('qy_common_time') },
              { id: 'method', header: t('qy_cfg_req_audit_method') },
              { id: 'action', header: t('qy_cfg_audit_action') },
              { id: 'actor', header: t('qy_common_operator') },
              { id: 'target', header: t('qy_cfg_audit_target') },
              { id: 'latency', header: t('qy_cfg_req_audit_latency') },
              { id: 'status', header: t('qy_common_status') },
            ]}
            renderRow={(row: QyRequestAudit) => (
              <QyRequestAuditRow
                key={row.id}
                row={row}
                expanded={expanded === row.id}
                onToggle={() =>
                  setExpanded(expanded === row.id ? null : row.id)
                }
              />
            )}
          />

          <QyPager
            page={page}
            pageSize={QY_AUDIT_PAGE_SIZE}
            total={query.data?.total ?? 0}
            onPageChange={setPage}
          />
        </div>
      </QyPageBoundary>
    </div>
  )
}

function QyRequestAuditRow(props: {
  row: QyRequestAudit
  expanded: boolean
  onToggle: () => void
}) {
  const { t } = useTranslation()
  const row = props.row

  return (
    <>
      <TableRow>
        <TableCell className='w-8'>
          <Button
            type='button'
            variant='ghost'
            size='icon-sm'
            aria-label={t('qy_cfg_audit_toggle')}
            aria-expanded={props.expanded}
            onClick={props.onToggle}
          >
            {props.expanded ? (
              <ChevronDown aria-hidden='true' />
            ) : (
              <ChevronRight aria-hidden='true' />
            )}
          </Button>
        </TableCell>
        <TableCell>{formatQyTs(row.created_at)}</TableCell>
        <TableCell className='font-mono text-xs'>{row.method}</TableCell>
        <TableCell className='font-mono text-xs'>{row.action}</TableCell>
        <TableCell>
          {/* 匿名请求（被 401 挡掉的探测）的 actor_type 为空，这里显示成
              「匿名」而不是留白 —— 留白看起来像渲染坏了。 */}
          {row.actor_user_id > 0
            ? `${row.actor_name === '' ? `#${row.actor_user_id}` : row.actor_name} (#${row.actor_user_id})`
            : t('qy_cfg_req_audit_anonymous')}
        </TableCell>
        <TableCell>
          {row.target_user_id > 0 ? `#${row.target_user_id}` : '-'}
        </TableCell>
        <TableCell className='tabular-nums'>{row.latency_ms} ms</TableCell>
        <TableCell>
          <StatusBadge
            label={String(row.status_code)}
            variant={row.success ? 'success' : 'danger'}
            copyable={false}
          />
        </TableCell>
      </TableRow>

      {props.expanded && (
        <TableRow>
          <TableCell colSpan={COLUMN_COUNT} className='bg-muted/30'>
            <div className='space-y-2 text-xs'>
              <QyAuditDetailLine
                label={t('qy_cfg_req_audit_path')}
                value={row.path}
                mono
              />
              <QyAuditDetailLine
                label={t('qy_cfg_req_audit_params')}
                value={row.params}
                mono
              />
              <QyAuditDetailLine
                label={t('qy_cfg_req_audit_query')}
                value={row.query}
                mono
              />
              {row.body !== '' && (
                <div>
                  <p className='text-muted-foreground mb-1'>
                    {t('qy_cfg_req_audit_body')}
                  </p>
                  <pre className='bg-background max-h-56 overflow-auto rounded-md border p-2 break-words whitespace-pre-wrap'>
                    {formatQySnapshot(row.body)}
                  </pre>
                </div>
              )}
              <QyAuditDetailLine
                label={t('qy_cfg_req_audit_auth')}
                value={
                  row.auth_method === ''
                    ? ''
                    : `${row.auth_method} · role ${row.actor_role}`
                }
              />
              <QyAuditDetailLine
                label={t('qy_cfg_audit_request_id')}
                value={row.request_id}
                mono
              />
              <QyAuditDetailLine
                label={t('qy_cfg_audit_user_agent')}
                value={row.user_agent}
              />
              <p className='text-muted-foreground'>
                {t('qy_cfg_audit_meta', {
                  ip: row.ip === '' ? '-' : row.ip,
                  node: row.node_name === '' ? '-' : row.node_name,
                })}
              </p>
            </div>
          </TableCell>
        </TableRow>
      )}
    </>
  )
}
