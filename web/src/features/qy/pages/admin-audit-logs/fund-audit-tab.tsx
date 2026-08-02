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
import { ChevronDown, ChevronRight, ScrollText } from 'lucide-react'
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

import { QyAmountText } from '../../components/qy-amount-text'
import { QyPageBoundary } from '../../components/qy-page-boundary'
import { qyKeys } from '../../lib/query-keys'
import { QyPager } from '../components/qy-pager'
import { formatQySnapshot, formatQyTs, qySinceHours } from '../ops/format'
import { QyFilterBar, QyFilterField } from '../ops/qy-ops-ui'
import { listQyAuditLogs } from './api'
import {
  QyAuditDetailLine,
  QyAuditNumberFilter,
  QyAuditRangeFilter,
} from './filters'
import { QY_AUDIT_ALL, QY_AUDIT_PAGE_SIZE, qyAuditTrimmed } from './shared'
import type { QyAuditLog } from './types'

const COLUMN_COUNT = 8

const CATEGORIES = [
  'fund',
  'transfer',
  'commission',
  'withdraw',
  'violation',
  'config',
  'admin',
]

const RESULTS = ['ok', 'fail', 'pending']
const ACTOR_TYPES = ['user', 'admin', 'system']

const RESULT_VARIANT: Record<string, 'danger' | 'success' | 'warning'> = {
  ok: 'success',
  fail: 'danger',
  pending: 'warning',
}

/**
 * 资金决策台账（`qy_audit_logs`）。
 *
 * 每一行都回答「谁、什么时候、对谁、做了什么、之前是什么样、之后是什么样」。
 * 前后快照默认折叠：它们是 JSON 文本，全部展开会让表格失去可扫读性，
 * 而排障时需要的往往只是其中一两行。
 */
export function QyFundAuditTab() {
  const { t } = useTranslation()

  const [page, setPage] = useState(1)
  const [category, setCategory] = useState(QY_AUDIT_ALL)
  const [result, setResult] = useState(QY_AUDIT_ALL)
  const [actorType, setActorType] = useState(QY_AUDIT_ALL)
  const [action, setAction] = useState('')
  const [traceNo, setTraceNo] = useState('')
  const [ip, setIp] = useState('')
  const [targetUserId, setTargetUserId] = useState('')
  const [actorUserId, setActorUserId] = useState('')
  const [hours, setHours] = useState(168)
  const [expanded, setExpanded] = useState<number | null>(null)

  const params = useMemo(
    () => ({
      p: page,
      page_size: QY_AUDIT_PAGE_SIZE,
      category: category === QY_AUDIT_ALL ? undefined : category,
      result: result === QY_AUDIT_ALL ? undefined : result,
      actor_type: actorType === QY_AUDIT_ALL ? undefined : actorType,
      // 后端按前缀匹配，因此 `withdraw.` 会命中整个提现子树。
      action: qyAuditTrimmed(action),
      trace_no: qyAuditTrimmed(traceNo),
      ip: qyAuditTrimmed(ip),
      target_user_id:
        targetUserId.trim() === '' ? undefined : Number(targetUserId),
      actor_user_id:
        actorUserId.trim() === '' ? undefined : Number(actorUserId),
      start_ts: hours > 0 ? qySinceHours(hours) : undefined,
    }),
    [
      action,
      actorType,
      actorUserId,
      category,
      hours,
      ip,
      page,
      result,
      targetUserId,
      traceNo,
    ]
  )

  const query = useQuery({
    queryKey: qyKeys.adminAuditLogs(params),
    queryFn: () => listQyAuditLogs(params),
    staleTime: 15_000,
  })

  const logs = query.data?.items ?? []
  const resetPage = () => setPage(1)

  return (
    <div className='space-y-3'>
      <QyFilterBar>
        <QyFilterField label={t('qy_cfg_audit_category')}>
          <Select
            value={category}
            onValueChange={(value) => {
              setCategory(value ?? QY_AUDIT_ALL)
              resetPage()
            }}
          >
            <SelectTrigger className='w-36'>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={QY_AUDIT_ALL}>{t('qy_common_all')}</SelectItem>
              {CATEGORIES.map((item) => (
                <SelectItem key={item} value={item}>
                  {t(`qy_cfg_audit_cat_${item}`, { defaultValue: item })}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </QyFilterField>

        <QyFilterField label={t('qy_cfg_audit_action')}>
          <Input
            className='w-44'
            value={action}
            onChange={(event) => {
              setAction(event.target.value)
              resetPage()
            }}
            // 前缀匹配，因此 placeholder 直接给出前缀写法：
            // 之前这里写的是完整动作名，而后端做的是精确匹配 ——
            // 管理员照着「像前缀」的样子输 `withdraw.` 只会得到空列表。
            placeholder='withdraw.'
          />
        </QyFilterField>
        <p className='text-muted-foreground w-full text-xs'>
          {t('qy_cfg_audit_action_prefix_hint')}
        </p>

        <QyFilterField label={t('qy_common_status')}>
          <Select
            value={result}
            onValueChange={(value) => {
              setResult(value ?? QY_AUDIT_ALL)
              resetPage()
            }}
          >
            <SelectTrigger className='w-28'>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={QY_AUDIT_ALL}>{t('qy_common_all')}</SelectItem>
              {RESULTS.map((item) => (
                <SelectItem key={item} value={item}>
                  {t(`qy_cfg_audit_result_${item}`, { defaultValue: item })}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </QyFilterField>

        <QyFilterField label={t('qy_common_operator')}>
          <Select
            value={actorType}
            onValueChange={(value) => {
              setActorType(value ?? QY_AUDIT_ALL)
              resetPage()
            }}
          >
            <SelectTrigger className='w-28'>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={QY_AUDIT_ALL}>{t('qy_common_all')}</SelectItem>
              {ACTOR_TYPES.map((item) => (
                <SelectItem key={item} value={item}>
                  {t(`qy_cfg_audit_actor_${item}`, { defaultValue: item })}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </QyFilterField>

        <QyFilterField label={t('qy_cfg_audit_trace_no')}>
          <Input
            className='w-56'
            value={traceNo}
            onChange={(event) => {
              setTraceNo(event.target.value)
              resetPage()
            }}
          />
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
        isEmpty={query.data != null && logs.length === 0}
        emptyIcon={ScrollText}
        emptyTitle={t('qy_cfg_audit_empty')}
      >
        <div className='space-y-3'>
          <StaticDataTable
            data={logs}
            getRowKey={(row) => row.id}
            columns={[
              { id: 'expand', header: '', className: 'w-8' },
              { id: 'created_at', header: t('qy_common_time') },
              { id: 'category', header: t('qy_cfg_audit_category') },
              { id: 'action', header: t('qy_cfg_audit_action') },
              { id: 'actor', header: t('qy_common_operator') },
              { id: 'target', header: t('qy_cfg_audit_target') },
              { id: 'amount', header: t('qy_common_amount') },
              { id: 'result', header: t('qy_common_status') },
            ]}
            renderRow={(row: QyAuditLog) => (
              <QyAuditLogRow
                key={row.id}
                log={row}
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

/** 一条审计：主行 + 可展开的前后快照行。 */
function QyAuditLogRow(props: {
  log: QyAuditLog
  expanded: boolean
  onToggle: () => void
}) {
  const { t } = useTranslation()
  const log = props.log
  const before = formatQySnapshot(log.before_snap)
  const after = formatQySnapshot(log.after_snap)

  return (
    <>
      <TableRow>
        <TableCell className='w-8'>
          {/* 展开键无条件渲染：之前只在「有快照或有理由」时才画，于是
              没有快照的那些行连 request_id / User-Agent 都看不到，
              而追一次越权恰恰要靠 request_id 把两张表串起来。 */}
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
        <TableCell>{formatQyTs(log.created_at)}</TableCell>
        <TableCell>
          {t(`qy_cfg_audit_cat_${log.category}`, {
            defaultValue: log.category,
          })}
        </TableCell>
        <TableCell className='font-mono text-xs'>{log.action}</TableCell>
        <TableCell>
          {log.actor_name === ''
            ? t(`qy_cfg_audit_actor_${log.actor_type}`, {
                defaultValue: log.actor_type,
              })
            : `${log.actor_name} (#${log.actor_user_id})`}
        </TableCell>
        <TableCell>
          {log.target_user_id > 0 ? `#${log.target_user_id}` : '-'}
        </TableCell>
        <TableCell>
          {log.amount_quota === 0 ? (
            '-'
          ) : (
            <QyAmountText quota={log.amount_quota} />
          )}
        </TableCell>
        <TableCell>
          <StatusBadge
            label={t(`qy_cfg_audit_result_${log.result}`, {
              defaultValue: log.result,
            })}
            variant={RESULT_VARIANT[log.result] ?? 'neutral'}
            copyable={false}
          />
        </TableCell>
      </TableRow>

      {props.expanded && (
        <TableRow>
          <TableCell colSpan={COLUMN_COUNT} className='bg-muted/30'>
            <div className='space-y-2 text-xs'>
              <QyAuditDetailLine
                label={t('qy_common_reason')}
                value={log.reason}
              />
              <QyAuditDetailLine
                label={t('qy_cfg_audit_trace_no')}
                value={log.trace_no}
                mono
              />
              <div className='grid gap-2 md:grid-cols-2'>
                <div>
                  <p className='text-muted-foreground mb-1'>
                    {t('qy_cfg_audit_before')}
                  </p>
                  <pre className='bg-background max-h-56 overflow-auto rounded-md border p-2 break-words whitespace-pre-wrap'>
                    {before === '' ? '-' : before}
                  </pre>
                </div>
                <div>
                  <p className='text-muted-foreground mb-1'>
                    {t('qy_cfg_audit_after')}
                  </p>
                  <pre className='bg-background max-h-56 overflow-auto rounded-md border p-2 break-words whitespace-pre-wrap'>
                    {after === '' ? '-' : after}
                  </pre>
                </div>
              </div>
              <QyAuditDetailLine
                label={t('qy_cfg_audit_request_id')}
                value={log.request_id}
                mono
              />
              <QyAuditDetailLine
                label={t('qy_cfg_audit_user_agent')}
                value={log.user_agent}
              />
              <p className='text-muted-foreground'>
                {t('qy_cfg_audit_meta', {
                  ip: log.ip === '' ? '-' : log.ip,
                  node: log.node_name === '' ? '-' : log.node_name,
                })}
              </p>
            </div>
          </TableCell>
        </TableRow>
      )}
    </>
  )
}
