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
import { useTranslation } from 'react-i18next'

import { StaticDataTable } from '@/components/data-table'
import { Badge } from '@/components/ui/badge'

import { QyAmountText } from '../../../components/qy-amount-text'
import { formatQyCount, QY_EMPTY_TEXT } from '../../ops/format'
import type { QyGpShadowSegment } from '../types'
import { QyGpDeltaAmount } from './shadow-amount'

/**
 * 差额明细。一行 = 一个 (分组, 模型) 在一段「规则值没变过」的区间上的汇总。
 *
 * 两件事必须在行内说清楚，否则这张表会用一个精确的外观掩盖它的不确定性：
 *   1. **不可折算的行**（旧值为 0 / 计价口径切换）没有金额，只有请求数，
 *      并显示后端给的原因。它们的差额不是 0，是「算不出来」。
 *   2. **分摊过的行**（同一 (分组,模型) 区间内换过规则值）金额是按请求数占比
 *      分摊的，`share_is_exact=false` 时必须标出来。
 */
export function QyGpShadowSegmentsTable(props: {
  segments: QyGpShadowSegment[]
}) {
  const { t } = useTranslation()

  return (
    <div className='w-full overflow-x-auto'>
      <StaticDataTable
        data={props.segments}
        getRowKey={(row) =>
          `${row.group_name}|${row.model_name}|${row.mode}|${row.old_value}|${row.new_value}|${String(row.exact)}`
        }
        getRowClassName={(row) => (row.exact ? undefined : 'opacity-70')}
        tableClassName='min-w-[980px]'
        columns={[
          {
            id: 'group',
            header: t('qy_gp_col_group'),
            cell: (row: QyGpShadowSegment) => (
              <span className='font-medium'>{row.group_name}</span>
            ),
          },
          {
            id: 'model',
            header: t('qy_gp_col_model'),
            cell: (row: QyGpShadowSegment) => (
              <span className='font-mono text-xs'>{row.model_name}</span>
            ),
          },
          {
            id: 'change',
            header: t('qy_gp_sh_col_change'),
            cell: (row: QyGpShadowSegment) => (
              <span className='flex items-center gap-1.5 text-xs'>
                <Badge variant='outline'>{t(`qy_gp_mode_${row.mode}`)}</Badge>
                <span className='tabular-nums'>
                  {row.old_value} → {row.new_value}
                </span>
              </span>
            ),
          },
          {
            id: 'requests',
            header: t('qy_gp_sh_stat_requests'),
            cell: (row: QyGpShadowSegment) => formatQyCount(row.requests),
          },
          {
            id: 'actual',
            header: t('qy_gp_sh_col_attributed'),
            cell: (row: QyGpShadowSegment) => {
              if (!row.exact) {
                return (
                  <span className='text-muted-foreground'>{QY_EMPTY_TEXT}</span>
                )
              }
              return (
                <span className='flex flex-col gap-0.5'>
                  <QyAmountText quota={row.attributed_quota} />
                  {!row.share_is_exact && (
                    <span className='text-muted-foreground text-[11px]'>
                      {t('qy_gp_sh_shared', { share: row.request_share })}
                    </span>
                  )}
                </span>
              )
            },
          },
          {
            id: 'delta',
            header: t('qy_gp_sh_stat_delta'),
            cell: (row: QyGpShadowSegment) => {
              if (!row.exact) {
                return (
                  <span
                    className='text-warning text-xs'
                    title={row.inexact_reason}
                  >
                    {t('qy_gp_sh_inexact')}
                  </span>
                )
              }
              return <QyGpDeltaAmount quota={row.delta_quota} />
            },
          },
          {
            id: 'sample',
            header: t('qy_gp_sh_col_sample'),
            cell: (row: QyGpShadowSegment) =>
              row.sample_request_id == null || row.sample_request_id === '' ? (
                QY_EMPTY_TEXT
              ) : (
                <span className='font-mono text-[11px]'>
                  {row.sample_request_id}
                </span>
              ),
          },
        ]}
      />
    </div>
  )
}
