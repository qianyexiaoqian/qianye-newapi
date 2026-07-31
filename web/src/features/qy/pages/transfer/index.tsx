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
import { Link } from '@tanstack/react-router'
import { History } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { SectionPageLayout } from '@/components/layout'
import { Button } from '@/components/ui/button'

import { QyPageBoundary } from '../../components/qy-page-boundary'
import { useQyConfig } from '../../hooks/use-qy-config'
import { qyTransferLimitsQuery } from './api'
import { TransferForm } from './components/transfer-form'
import { TransferLimitsCard } from './components/transfer-limits-card'

/**
 * 余额划转页。
 *
 * 页面本身只负责取限额与排版；不可逆确认、幂等键、错误分流全部收敛在
 * `TransferForm` 里，因为那三件事必须一起看才说得清。
 */
export function QyTransfer() {
  const { t } = useTranslation()
  const config = useQyConfig()
  const limitsQuery = useQuery(qyTransferLimitsQuery())

  return (
    <SectionPageLayout>
      <SectionPageLayout.Title>{t('qy_nav_transfer')}</SectionPageLayout.Title>
      <SectionPageLayout.Actions>
        <Button
          variant='outline'
          size='sm'
          render={<Link to='/qy/transfer-logs' />}
        >
          <History aria-hidden='true' />
          {t('qy_nav_transfer_logs')}
        </Button>
      </SectionPageLayout.Actions>
      <SectionPageLayout.Content>
        <QyPageBoundary query={limitsQuery}>
          {limitsQuery.data != null && (
            <div className='grid gap-4 lg:grid-cols-[minmax(0,1.5fr)_minmax(0,1fr)] lg:items-start'>
              <TransferForm
                limits={limitsQuery.data}
                degraded={config.status === 'enabled' && !config.available}
              />
              <TransferLimitsCard limits={limitsQuery.data} />
            </div>
          )}
        </QyPageBoundary>
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}
