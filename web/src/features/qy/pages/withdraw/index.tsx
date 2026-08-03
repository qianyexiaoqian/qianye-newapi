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

import { QyAmountText } from '../../components/qy-amount-text'
import { QyPageBoundary } from '../../components/qy-page-boundary'
import { useQyConfig } from '../../hooks/use-qy-config'
import { QyStatGrid, type QyStatItem } from '../components/qy-stat-grid'
import { qyWithdrawConfigQuery } from './api'
import { WithdrawForm } from './components/withdraw-form'

/**
 * 佣金提现（「推广佣金」选择夹的第三张标签，需求 3）。
 *
 * 门槛数字（可提额度、今日已提交、审核时效）全部先于表单展示：用户在填完
 * 一整张收款信息之后才被告知"低于最低提现额"，是这条链路上最常见的挫败点。
 */
export function QyWithdrawBody() {
  const { t } = useTranslation()
  const qyConfig = useQyConfig()
  const configQuery = useQuery(qyWithdrawConfigQuery())
  const config = configQuery.data

  const stats: QyStatItem[] =
    config == null
      ? []
      : [
          {
            key: 'withdrawable',
            label: t('qy_wd_withdrawable'),
            value: <QyAmountText quota={config.withdrawable_quota} />,
            emphasis: true,
          },
          {
            key: 'min',
            label: t('qy_wd_min_quota'),
            value: <QyAmountText quota={config.min_quota} />,
          },
          {
            key: 'today',
            label: t('qy_wd_used_today'),
            value:
              config.daily_max_count > 0
                ? t('qy_wd_used_today_value', {
                    used: config.used_today,
                    max: config.daily_max_count,
                  })
                : config.used_today,
          },
          {
            key: 'sla',
            label: t('qy_wd_review_sla'),
            value:
              config.review_sla_hours > 0
                ? t('qy_wd_sla_value', { hours: config.review_sla_hours })
                : t('qy_common_unlimited'),
            hint: config.auto_credit ? t('qy_wd_auto_credit_hint') : undefined,
          },
        ]

  return (
    <QyPageBoundary query={configQuery}>
      {config != null && (
        <div className='space-y-3'>
          <QyStatGrid items={stats} />
          <WithdrawForm
            config={config}
            degraded={qyConfig.status === 'enabled' && !qyConfig.available}
          />
        </div>
      )}
    </QyPageBoundary>
  )
}
