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
import { Eye } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { StaticDataTable } from '@/components/data-table'
import { TableCell, TableRow } from '@/components/ui/table'

import { QyPageBoundary } from '../../components/qy-page-boundary'
import { qyKeys } from '../../lib/query-keys'
import { QyPager } from '../components/qy-pager'
import { formatQyTs } from '../ops/format'
import { QyFilterBar } from '../ops/qy-ops-ui'
import { listQyPiiAudits } from './api'
import { QyAuditNumberFilter } from './filters'
import { QY_AUDIT_PAGE_SIZE } from './shared'
import type { QyPiiAudit } from './types'

/**
 * 明文访问记录（`qy_pii_audits`）。
 *
 * # 为什么这个 tab 必须存在
 *
 * 后端一直在写这张表：每一次解密收款信息、每一次打开打款凭证原图，都会落一行，
 * 而且强制填写事由、保留期比资金审计更长。接口 `GET /admin/withdraw/pii-audits`
 * 也一直在，react-query 的 key 也早就定义好了 —— 唯独**没有任何页面消费它**。
 *
 * 也就是说,这套合规凭据事实上不可见:向用户承诺"谁看过你的银行卡都有记录",
 * 而平台自己没有任何界面能把这份记录调出来。写了却没人看的审计,
 * 与没写的差别只在事故之后请 DBA 手工查表。
 */
export function QyPiiAuditTab() {
  const { t } = useTranslation()

  const [page, setPage] = useState(1)
  const [adminId, setAdminId] = useState('')
  const [targetUserId, setTargetUserId] = useState('')

  const params = useMemo(
    () => ({
      p: page,
      page_size: QY_AUDIT_PAGE_SIZE,
      admin_id: adminId.trim() === '' ? undefined : Number(adminId),
      target_user_id:
        targetUserId.trim() === '' ? undefined : Number(targetUserId),
    }),
    [adminId, page, targetUserId]
  )

  const query = useQuery({
    queryKey: qyKeys.adminWithdrawPiiAudits(params),
    queryFn: () => listQyPiiAudits(params),
    staleTime: 15_000,
  })

  const rows = query.data?.items ?? []

  return (
    <div className='space-y-3'>
      <p className='text-muted-foreground text-xs'>{t('qy_cfg_pii_desc')}</p>

      <QyFilterBar>
        <QyAuditNumberFilter
          label={t('qy_cfg_pii_admin')}
          value={adminId}
          onChange={(value) => {
            setAdminId(value)
            setPage(1)
          }}
        />
        <QyAuditNumberFilter
          label={t('qy_cfg_audit_target')}
          value={targetUserId}
          onChange={(value) => {
            setTargetUserId(value)
            setPage(1)
          }}
        />
      </QyFilterBar>

      <QyPageBoundary
        query={query}
        isEmpty={query.data != null && rows.length === 0}
        emptyIcon={Eye}
        emptyTitle={t('qy_cfg_pii_empty')}
      >
        <div className='space-y-3'>
          <StaticDataTable
            data={rows}
            getRowKey={(row) => row.id}
            columns={[
              { id: 'created_at', header: t('qy_common_time') },
              { id: 'admin', header: t('qy_cfg_pii_admin') },
              { id: 'target', header: t('qy_cfg_audit_target') },
              { id: 'resource', header: t('qy_cfg_pii_resource') },
              { id: 'fields', header: t('qy_cfg_pii_fields') },
              { id: 'reason', header: t('qy_common_reason') },
              { id: 'ip', header: t('qy_cfg_audit_ip') },
            ]}
            renderRow={(row: QyPiiAudit) => (
              <TableRow key={row.id}>
                <TableCell>{formatQyTs(row.created_at)}</TableCell>
                <TableCell>
                  {row.admin_name === ''
                    ? `#${row.admin_id}`
                    : `${row.admin_name} (#${row.admin_id})`}
                </TableCell>
                <TableCell>
                  {row.target_user_id > 0 ? `#${row.target_user_id}` : '-'}
                </TableCell>
                <TableCell className='font-mono text-xs'>
                  {t(`qy_cfg_pii_res_${row.resource}`, {
                    defaultValue: row.resource,
                  })}
                  {row.resource_id > 0 ? ` #${row.resource_id}` : ''}
                </TableCell>
                <TableCell className='font-mono text-xs break-all'>
                  {row.fields === '' ? '-' : row.fields}
                </TableCell>
                <TableCell className='max-w-64 break-all'>
                  {row.reason === '' ? '-' : row.reason}
                </TableCell>
                <TableCell className='font-mono text-xs'>
                  {row.ip === '' ? '-' : row.ip}
                </TableCell>
              </TableRow>
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
