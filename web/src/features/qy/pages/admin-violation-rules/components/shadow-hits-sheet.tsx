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
import { Download } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { StaticDataTable } from '@/components/data-table'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'

import { QyResponsiveDialog } from '../../../components/qy-responsive-dialog'
import { qyKeys } from '../../../lib/query-keys'
import { QyPager } from '../../components/qy-pager'
import { qyOpsErrorMessage } from '../../ops/errors'
import { formatQyTs, QY_EMPTY_TEXT } from '../../ops/format'
import { exportQyViolationRecords, listQyViolationRecords } from '../api'
import type { QyViolationRecord, QyViolationRule } from '../types'

const PAGE_SIZE = 20

type QyShadowHitsSheetProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** `null` 表示未选中任何规则（抽屉不渲染内容）。 */
  rule: QyViolationRule | null
}

/**
 * 单条规则的影子命中。
 *
 * 项目方对影子模式的原话是：「我把这个规则设置成影子模式就是只记录不处罚，
 * 用于抓取涉嫌违规用户的日志、上下文，我要进行分析。」这个抽屉就是「分析」
 * 那一步的入口，所以它刻意做在**规则行上**而不是违规记录页的一个筛选框里 ——
 * 从「我改了这条规则」到「我看这条规则抓到了什么」必须是一次点击。
 *
 * 表格里每一列都对应一个分析时立刻会问的问题。尤其是「若真实执行会扣多少」：
 * 那是切模式之前唯一能算出成本的数字，也是影子模式的全部价值。
 */
export function QyShadowHitsSheet(props: QyShadowHitsSheetProps) {
  const { t } = useTranslation()
  const [page, setPage] = useState(1)
  const ruleId = props.rule?.id ?? 0

  const params = {
    p: page,
    page_size: PAGE_SIZE,
    rule_id: ruleId,
    shadow: '1' as const,
  }

  const recordsQuery = useQuery({
    queryKey: qyKeys.adminViolationRecords(params),
    queryFn: () => listQyViolationRecords(params),
    enabled: props.open && ruleId > 0,
    staleTime: 10_000,
  })

  const exportMutation = useMutation({
    mutationFn: () =>
      exportQyViolationRecords({ rule_id: ruleId, shadow: '1' }),
    onSuccess: (blob) => {
      // 走 Blob + createObjectURL 而不是 <a href> 直链：导出路由要管理员身份，
      // 直链缺 Bearer 会直接 401，而浏览器下载失败不会有任何可见提示。
      const url = URL.createObjectURL(blob)
      const link = document.createElement('a')
      link.href = url
      link.download = `violation-shadow-${ruleId}.csv`
      link.click()
      URL.revokeObjectURL(url)
    },
    onError: (error) => toast.error(qyOpsErrorMessage(error, t)),
  })

  const rows = recordsQuery.data?.items ?? []

  return (
    <QyResponsiveDialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={t('qy_vio_shadow_hits_title', { name: props.rule?.name ?? '' })}
      description={t('qy_vio_shadow_hits_desc')}
      contentClassName='sm:max-w-5xl'
      footer={
        <Button
          type='button'
          variant='outline'
          onClick={() => props.onOpenChange(false)}
        >
          {t('qy_common_close')}
        </Button>
      }
    >
      <div className='space-y-3'>
        <div className='flex flex-wrap items-center justify-between gap-2'>
          <span className='text-muted-foreground text-xs'>
            {t('qy_vio_shadow_hits_total', {
              total: recordsQuery.data?.total ?? 0,
            })}
          </span>
          <Button
            type='button'
            variant='outline'
            size='sm'
            disabled={exportMutation.isPending || rows.length === 0}
            onClick={() => exportMutation.mutate()}
          >
            <Download aria-hidden='true' />
            {t('qy_vio_shadow_hits_export')}
          </Button>
        </div>

        <StaticDataTable
          data={rows}
          getRowKey={(row) => row.id}
          columns={[
            {
              id: 'created_at',
              header: t('qy_common_created_at'),
              cell: (row: QyViolationRecord) => formatQyTs(row.created_at),
            },
            {
              id: 'user',
              header: t('qy_vio_field_user'),
              cell: (row: QyViolationRecord) =>
                row.username === ''
                  ? String(row.user_id)
                  : `${row.username} (${row.user_id})`,
            },
            {
              id: 'model',
              header: t('qy_vio_field_model'),
              cell: (row: QyViolationRecord) =>
                row.model_name === '' ? QY_EMPTY_TEXT : row.model_name,
            },
            {
              id: 'group',
              header: t('qy_vio_field_group'),
              cell: (row: QyViolationRecord) =>
                row.using_group === '' ? QY_EMPTY_TEXT : row.using_group,
            },
            {
              id: 'token',
              header: t('qy_vio_field_token'),
              cell: (row: QyViolationRecord) =>
                row.token_name === '' ? QY_EMPTY_TEXT : row.token_name,
            },
            {
              id: 'terms',
              header: t('qy_vio_field_matched_terms'),
              cellClassName: 'max-w-64 truncate',
              cell: (row: QyViolationRecord) =>
                row.matched_terms === '' ? QY_EMPTY_TEXT : row.matched_terms,
            },
            {
              id: 'snippet',
              header: t('qy_vio_field_snippet'),
              cellClassName: 'max-w-80 truncate',
              cell: (row: QyViolationRecord) =>
                row.match_snippet === '' ? QY_EMPTY_TEXT : row.match_snippet,
            },
            {
              // 影子模式的全部价值：若真实执行会扣多少。fee_quota 恒为 0，
              // 只看那一列会得出「这条规则不花钱」的错误结论。
              id: 'fee_want',
              header: t('qy_vio_field_fee_quota_want'),
              cellClassName: 'tabular-nums',
              cell: (row: QyViolationRecord) => row.fee_quota_want,
            },
            {
              id: 'reason',
              header: t('qy_vio_field_shadow_reason'),
              cell: (row: QyViolationRecord) => (
                <Badge variant='outline'>
                  {t(
                    `qy_vio_shadow_reason_${row.shadow_reason === '' ? 'rule_mode' : row.shadow_reason}`
                  )}
                </Badge>
              ),
            },
            {
              id: 'payload',
              header: t('qy_vio_field_has_payload'),
              cell: (row: QyViolationRecord) =>
                row.has_payload
                  ? t('qy_common_yes')
                  : t('qy_vio_no_payload_hint'),
            },
          ]}
        />

        <QyPager
          page={page}
          pageSize={PAGE_SIZE}
          total={recordsQuery.data?.total ?? 0}
          onPageChange={setPage}
        />
      </div>
    </QyResponsiveDialog>
  )
}
