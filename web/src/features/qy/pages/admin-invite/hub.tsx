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
import { QyAdminDailyConsumeBody } from '../admin-daily-consume'
import { QyPageTabs } from '../components/qy-page-tabs'
import { QyAdminInviteAccrualsBody } from './components/accruals-body'
import { QyAdminInviteRelationsBody } from './index'

/**
 * 「邀请管理」选择夹（D-14）：邀请关系 / 下线日消费 / 日结明细。
 *
 * 它取代了「结算台」（日消费明细 / 佣金审核 / 提现审核）与「用户佣金」（用户
 * 总览 / AFF 关系 / 佣金余额）两个选择夹：佣金审核、提现审核、佣金余额、用户佣金
 * 四张表连同账本一起删除，剩下三张表回答的是同一件事的三个切面 ——
 *
 *   谁邀请了谁（主库 users.inviter_id）→ 下线昨天花了多少（主库 logs 的聚合）
 *   → 按邀请人分组档算出来该返多少星屑（qy_sd_invite_accrual）。
 *
 * 顺序 = 运营对账时的追问顺序。标签顺序与可见性来自 `lib/pages.ts` 的
 * `QY_TAB_GROUPS`，本文件只提供正文（`__tests__/qy-page-tabs.test.ts` 按源码扫
 * 这一条覆盖度）。`QyPageTabs` 不 keepMounted，所以**不可见的标签一个请求都不发**：
 * 日消费明细那一条是主库大表的聚合查询，一进页面就打三份查询是这次合并最需要
 * 避免的事。
 *
 * 这一页刻意**没有** Actions 槽：三张标签各自的动作（新增绑定、导出 CSV）都留在
 * 自己的正文里，槽是三张标签共用的，把谁的按钮放上去，另外两张上就会出现一个
 * 与本屏无关的按钮。
 */
export function QyAdminInviteHub() {
  const { t } = useTranslation()

  return (
    <QySectionPageLayout>
      <QySectionPageLayout.Title>
        {t('qy_nav_a_invite_hub')}
      </QySectionPageLayout.Title>
      <QySectionPageLayout.Content>
        <QyPageTabs
          host='/qy/admin/invite'
          bodies={{
            '/qy/admin/invite': <QyAdminInviteRelationsBody />,
            '/qy/admin/daily-consume': <QyAdminDailyConsumeBody />,
            '/qy/admin/invite-accruals': <QyAdminInviteAccrualsBody />,
          }}
        />
      </QySectionPageLayout.Content>
    </QySectionPageLayout>
  )
}
