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

import { QySectionPageLayout } from '../../components/qy-section-page-layout'
import { QyPageTabs } from '../components/qy-page-tabs'
import { QyStardustAccrualsBody } from './components/accruals-body'
import { QyStardustBalanceBody } from './components/balance-body'
import { QyStardustLedgerBody } from './components/ledger-body'

/**
 * 「星屑」选择夹：余额 / 流水 / 待结算。
 *
 * 三张表回答的是同一件事的三个切面 —— 我有多少、怎么来的、明天会到多少。
 * 标签顺序与可见性来自 `lib/pages.ts` 的 `QY_TAB_GROUPS`，本文件只提供正文
 * （`__tests__/qy-page-tabs.test.ts` 按源码扫这一条覆盖度）。
 *
 * `QyPageTabs` 不 keepMounted：三张标签各打各的请求，只有当前这一张会取数。
 */
export function QyStardustHub() {
  const { t } = useTranslation()

  return (
    <QySectionPageLayout>
      <QySectionPageLayout.Title>
        {t('qy_nav_stardust_hub')}
      </QySectionPageLayout.Title>
      <QySectionPageLayout.Content>
        <QyPageTabs
          host='/qy/stardust'
          bodies={{
            '/qy/stardust': <QyStardustBalanceBody />,
            '/qy/stardust-ledger': <QyStardustLedgerBody />,
            '/qy/stardust-accruals': <QyStardustAccrualsBody />,
          }}
        />
      </QySectionPageLayout.Content>
    </QySectionPageLayout>
  )
}
