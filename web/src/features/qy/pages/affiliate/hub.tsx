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
import { QyInviteesBody } from '../invitees'
import { QyWithdrawBody } from '../withdraw'
import { QyWithdrawalsBody } from '../withdrawals'
import { QyAffiliateOverviewBody } from './index'

/**
 * 「推广佣金」选择夹（需求 3）。
 *
 * 项目方原话：「提现申请移动到推广板块下面。推广佣金：选择夹，我的邀请概览、
 * 已邀请用户、佣金提现、佣金提现记录。」
 *
 * 四张标签的顺序与可见性来自 `lib/pages.ts` 的 `QY_TAB_GROUPS`，本文件只提供
 * 正文 —— 顺序若在两处各写一份，迟早出现"侧栏说有四张、页面上只有三张"。
 *
 * 提现相关的两张标签受 `features.withdraw` 控制（由 `QyPageTabs` 统一按
 * `isQyPageVisible` 过滤）：提现功能关掉时选择夹自动缩成两张，而不是留下两张
 * 点进去报 503 的空标签。
 */
export function QyCommissionHub() {
  const { t } = useTranslation()

  return (
    <QySectionPageLayout>
      <QySectionPageLayout.Title>
        {t('qy_nav_commission_hub')}
      </QySectionPageLayout.Title>
      <QySectionPageLayout.Content>
        <QyPageTabs
          host='/qy/affiliate'
          bodies={{
            '/qy/affiliate': <QyAffiliateOverviewBody />,
            '/qy/invitees': <QyInviteesBody />,
            '/qy/withdraw': <QyWithdrawBody />,
            '/qy/withdrawals': <QyWithdrawalsBody />,
          }}
        />
      </QySectionPageLayout.Content>
    </QySectionPageLayout>
  )
}
