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
import { QyCommissionRecordsBody } from '../commission-records'
import { QyPageTabs } from '../components/qy-page-tabs'
import { QyInviteRecordsBody } from '../invite-records'
import { QyInviteesBody } from '../invitees'
import { QyAffiliateOverviewBody } from './index'

/**
 * 「我的推广」选择夹（D-15）：概览 / 下线 / 佣金明细 / 返星屑明细。
 *
 * D-14 曾把这里收成三张（佣金账本与提现整体删除）；D-15 把佣金账本请回来，
 * 记的是星辉、到期自动入账。第三张标签因此是佣金账本的逐笔与自动入账记录，
 * 第四张仍是星屑流水里邀请类那五种 kind 的行。「提现」两张标签**不**回来。
 *
 * 四张标签的顺序与可见性来自 `lib/pages.ts` 的 `QY_TAB_GROUPS`，本文件只提供
 * 正文（`__tests__/qy-page-tabs.test.ts` 按源码扫这一条覆盖度）。佣金明细挂
 * `features.commission`：佣金关掉时那一张自动消失，其余三张照旧。
 * `QyPageTabs` 不 keepMounted：四张标签各打各的请求，只有当前这一张会取数。
 */
export function QyInviteHub() {
  const { t } = useTranslation()

  return (
    <QySectionPageLayout>
      <QySectionPageLayout.Title>
        {t('qy_nav_invite_hub')}
      </QySectionPageLayout.Title>
      <QySectionPageLayout.Content>
        <QyPageTabs
          host='/qy/affiliate'
          bodies={{
            '/qy/affiliate': <QyAffiliateOverviewBody />,
            '/qy/invitees': <QyInviteesBody />,
            '/qy/commission-records': <QyCommissionRecordsBody />,
            '/qy/invite-records': <QyInviteRecordsBody />,
          }}
        />
      </QySectionPageLayout.Content>
    </QySectionPageLayout>
  )
}
