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
import { Pencil, Trash2, TriangleAlert } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { StaticDataTable } from '@/components/data-table'
import { StatusBadge } from '@/components/status-badge'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'

import { formatQyTs, QY_EMPTY_TEXT } from '../../ops/format'
import type { QyGpRule } from '../types'
import { QyGpEffectiveCell } from './effective-price'

type QyGpRulesTableProps = {
  rules: QyGpRule[]
  shadowMode: boolean
  onEdit: (rule: QyGpRule) => void
  onDelete: (rule: QyGpRule) => void
}

/**
 * 规则表。
 *
 * 列的顺序刻意让人从左往右读成一句话：
 * 「default 分组的 gpt-4o，按次固定价 0.5，乘以分组倍率 1.2，实际扣 0.6」。
 * 折算结果直接用后端下发的 `effective`，与录入面板同源。
 *
 * 未启用的规则整行压暗但**不隐藏折算结果**：管理员常常是先建好一批规则、
 * 核对完折算再统一打开，那个阶段恰恰最需要看见这些数字。
 */
export function QyGpRulesTable(props: QyGpRulesTableProps) {
  const { t } = useTranslation()

  return (
    <div className='w-full overflow-x-auto'>
      <StaticDataTable
        data={props.rules}
        getRowKey={(row) => row.id}
        getRowClassName={(row) => (row.enabled ? undefined : 'opacity-60')}
        tableClassName='min-w-[1080px]'
        columns={[
          {
            id: 'group',
            header: t('qy_gp_col_group'),
            cell: (row: QyGpRule) => (
              <span className='font-medium'>{row.group_name}</span>
            ),
          },
          {
            id: 'model',
            header: t('qy_gp_col_model'),
            cell: (row: QyGpRule) => (
              <span className='font-mono text-xs'>{row.model_name}</span>
            ),
          },
          {
            id: 'mode',
            header: t('qy_gp_col_mode'),
            cell: (row: QyGpRule) => (
              <Badge variant='outline'>{t(`qy_gp_mode_${row.mode}`)}</Badge>
            ),
          },
          {
            id: 'value',
            header: t('qy_gp_col_base'),
            cell: (row: QyGpRule) => (
              <span className='tabular-nums'>{row.value}</span>
            ),
          },
          {
            id: 'group_ratio',
            header: t('qy_gp_col_group_ratio'),
            cell: (row: QyGpRule) => (
              <span className='tabular-nums'>
                ×&nbsp;{row.effective.group_ratio}
              </span>
            ),
          },
          {
            id: 'effective',
            header: t('qy_gp_col_effective'),
            cell: (row: QyGpRule) => (
              <QyGpEffectiveCell
                effective={row.effective}
                shadowMode={props.shadowMode}
                enabled={row.enabled}
              />
            ),
          },
          {
            id: 'warning',
            header: t('qy_gp_col_warning'),
            cell: (row: QyGpRule) => <QyGpWarningCell rule={row} />,
          },
          {
            id: 'enabled',
            header: t('qy_common_status'),
            cell: (row: QyGpRule) => (
              <StatusBadge
                label={t(row.enabled ? 'qy_gp_enabled' : 'qy_gp_disabled')}
                variant={row.enabled ? 'success' : 'neutral'}
                copyable={false}
              />
            ),
          },
          {
            id: 'remark',
            header: t('qy_common_remark'),
            cell: (row: QyGpRule) =>
              row.remark === '' ? QY_EMPTY_TEXT : row.remark,
          },
          {
            id: 'updated_at',
            header: t('qy_common_updated_at'),
            cell: (row: QyGpRule) =>
              row.updated_at === 0 ? QY_EMPTY_TEXT : formatQyTs(row.updated_at),
          },
          {
            id: 'actions',
            header: t('qy_common_actions'),
            cell: (row: QyGpRule) => (
              <span className='flex items-center gap-1'>
                <Button
                  type='button'
                  variant='ghost'
                  size='icon-sm'
                  aria-label={t('qy_gp_edit')}
                  onClick={() => props.onEdit(row)}
                >
                  <Pencil aria-hidden='true' />
                </Button>
                <Button
                  type='button'
                  variant='ghost'
                  size='icon-sm'
                  aria-label={t('qy_gp_delete')}
                  onClick={() => props.onDelete(row)}
                >
                  <Trash2 aria-hidden='true' />
                </Button>
              </span>
            ),
          },
        ]}
      />
    </div>
  )
}

/**
 * 「配了也不会生效」告警。
 *
 * 独立成一列而不是塞进折算单元格：一条静默不生效的价格规则与一个定义了却
 * 没有消费方的配置项是同一种缺陷，它必须在列表上一眼可见，而不是要点开
 * 编辑才发现。文案是后端原文（它读得到模型当前的全局计费口径，前端读不到）。
 */
function QyGpWarningCell(props: { rule: QyGpRule }) {
  const warning = props.rule.effective.warning
  if (warning == null || warning === '') {
    return <span className='text-muted-foreground'>{QY_EMPTY_TEXT}</span>
  }
  // 用原生 title 而不是 Tooltip 组件：这一列只需要「悬停能看全文」，
  // 而后端的告警原文本身已经是完整可读的一句话，不值得为它引入一层浮层。
  return (
    <span
      title={warning}
      className='text-warning inline-flex max-w-[16rem] items-start gap-1 text-xs'
    >
      <TriangleAlert className='mt-0.5 size-3.5 shrink-0' aria-hidden='true' />
      <span className='line-clamp-2'>{warning}</span>
    </span>
  )
}
