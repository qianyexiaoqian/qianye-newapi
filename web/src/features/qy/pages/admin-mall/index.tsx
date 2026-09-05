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

import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'

import { QySectionPageLayout } from '../../components/qy-section-page-layout'
import { QyMallAdminCodesTab } from './codes-tab'
import { QyMallAdminOrdersTab } from './orders-tab'
import { QyMallAdminProductsTab } from './products-tab'

/**
 * 商城管理：商品 / 码库存 / 订单，一个页面三张标签（形状照审计中心）。
 *
 * 三张标签各自持有筛选状态与查询；未挂载的标签不发请求 —— 订单表随时间增长，
 * 预取它只是白给扩展库加读压力。
 *
 * 两处敏感动作的出口在订单那一张：地址明文每次读都写审计（独立弹窗、两步），
 * 套餐订单裁决是超级管理员专属（role=10 看到的是一句"该找谁"，口径见
 * `__tests__/root-action-gates.test.tsx`）。
 */
export function QyAdminMall() {
  const { t } = useTranslation()

  return (
    <QySectionPageLayout>
      <QySectionPageLayout.Title>
        {t('qy_nav_a_mall')}
      </QySectionPageLayout.Title>
      <QySectionPageLayout.Content>
        <Tabs defaultValue='products'>
          <TabsList>
            <TabsTrigger value='products'>
              {t('qy_mladm_tab_products')}
            </TabsTrigger>
            <TabsTrigger value='codes'>{t('qy_mladm_tab_codes')}</TabsTrigger>
            <TabsTrigger value='orders'>{t('qy_mladm_tab_orders')}</TabsTrigger>
          </TabsList>
          <TabsContent value='products'>
            <QyMallAdminProductsTab />
          </TabsContent>
          <TabsContent value='codes'>
            <QyMallAdminCodesTab />
          </TabsContent>
          <TabsContent value='orders'>
            <QyMallAdminOrdersTab />
          </TabsContent>
        </Tabs>
      </QySectionPageLayout.Content>
    </QySectionPageLayout>
  )
}
