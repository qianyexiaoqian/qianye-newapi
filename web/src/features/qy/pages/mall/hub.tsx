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

import { Alert, AlertDescription } from '@/components/ui/alert'

import { QySectionPageLayout } from '../../components/qy-section-page-layout'
import { useQyConfig } from '../../hooks/use-qy-config'
import { QyPageTabs } from '../components/qy-page-tabs'
import { QyMallProductsBody } from './index'
import { QyMallOrdersBody } from './orders'

/**
 * 「星屑商城」选择夹：商品 / 我的订单。
 *
 * 标签顺序与可见性来自 `lib/pages.ts` 的 `QY_TAB_GROUPS`，本文件只提供正文
 * （`__tests__/qy-page-tabs.test.ts` 按源码扫这一条覆盖度）。
 *
 * 「我的订单」永远在：运营把入口关掉（`mall.show_entry=false`）只影响侧栏与
 * 新的兑换，已经买过的人必须还能查到自己的单、还能把兑换码领出来 —— 这与
 * 抽奖大厅「我的参与」是同一条纪律。所以关掉入口时这里只在顶上给一句中性说明。
 */
export function QyMallHub() {
  const { t } = useTranslation()
  const config = useQyConfig()

  return (
    <QySectionPageLayout>
      <QySectionPageLayout.Title>
        {t('qy_nav_mall_hub')}
      </QySectionPageLayout.Title>
      <QySectionPageLayout.Content>
        <div className='space-y-3'>
          {config.status === 'enabled' && !config.mall.show_entry && (
            <Alert>
              <AlertDescription>{t('qy_ml_entry_hidden')}</AlertDescription>
            </Alert>
          )}
          <QyPageTabs
            host='/qy/mall'
            bodies={{
              '/qy/mall': <QyMallProductsBody />,
              '/qy/mall-orders': <QyMallOrdersBody />,
            }}
          />
        </div>
      </QySectionPageLayout.Content>
    </QySectionPageLayout>
  )
}
