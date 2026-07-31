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

import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'

import { QySectionPageLayout } from '../../components/qy-section-page-layout'
import { qyKeys } from '../../lib/query-keys'
import { getQyViolationStats } from '../admin-violation-rules/api'
import { QyViolationShadowBanner } from '../admin-violation-rules/components/violation-shadow-banner'
import { QyViolationAppealsTab } from './components/violation-appeals-tab'
import { QyViolationBansTab } from './components/violation-bans-tab'
import { QyViolationRecordsTab } from './components/violation-records-tab'

/**
 * 违规记录 / 封禁 / 申诉。
 *
 * 三块收敛在一页的 Tab 里而不是三条路由：它们共享同一份「当前是不是影子模式」
 * 的前提 —— 影子模式下这一页看到的记录全都没有真正扣钱，不把这件事顶在
 * 页面最上方，管理员会对着一堆「已扣费」的记录做出错误处置。
 */
export function QyAdminViolations() {
  const { t } = useTranslation()

  const statsQuery = useQuery({
    queryKey: qyKeys.adminViolationStats(),
    queryFn: () => getQyViolationStats({ hours: 24 }),
    staleTime: 30_000,
  })

  return (
    <QySectionPageLayout>
      <QySectionPageLayout.Title>
        {t('qy_vio_records_title')}
      </QySectionPageLayout.Title>
      <QySectionPageLayout.Content>
        <div className='space-y-3'>
          <QyViolationShadowBanner stats={statsQuery.data} />

          <Tabs defaultValue='records'>
            <TabsList>
              <TabsTrigger value='records'>
                {t('qy_vio_tab_records')}
              </TabsTrigger>
              <TabsTrigger value='bans'>{t('qy_vio_tab_bans')}</TabsTrigger>
              <TabsTrigger value='appeals'>
                {t('qy_vio_tab_appeals')}
              </TabsTrigger>
            </TabsList>
            <TabsContent value='records'>
              <QyViolationRecordsTab />
            </TabsContent>
            <TabsContent value='bans'>
              <QyViolationBansTab />
            </TabsContent>
            <TabsContent value='appeals'>
              <QyViolationAppealsTab />
            </TabsContent>
          </Tabs>
        </div>
      </QySectionPageLayout.Content>
    </QySectionPageLayout>
  )
}
