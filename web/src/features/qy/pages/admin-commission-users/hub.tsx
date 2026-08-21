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
import { Link } from '@tanstack/react-router'
import { ScrollText } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'

import { QySectionPageLayout } from '../../components/qy-section-page-layout'
import { qyTabTarget } from '../../lib/pages'
import { QyAdminCommissionBalancesBody } from '../admin-commission-balances'
import { QyAdminCommissionRelationsBody } from '../admin-commission-relations'
import { QyPageTabs } from '../components/qy-page-tabs'
import { QyAdminCommissionUsersBody } from './index'

/**
 * 「用户佣金」选择夹 —— 佣金管理的**唯一**按人入口。
 *
 * ── 它取代了什么 ──
 * 在此之前侧栏「结算」组里有三个平级的佣金入口：计佣流水 / 佣金余额 /
 * AFF 关系。项目方要的是「一个用户佣金列表」，而不是第四张割裂的表。
 * 所以后两张被收进这里，侧栏上只剩两行：
 *   · 「用户佣金」= 按用户看（本页三张标签）；
 *   · 「计佣流水」= 按流水看（一行一笔计佣，账本本身，主键不是人）。
 *
 * 三张标签互不重复，各自回答一个不同的问题：
 *   1. 用户总览：这个人的上下线 / 五列额度 / 行内改佣金与改绑定；
 *   2. AFF 关系：跨用户列全部邀请关系，含**已解绑**的历史 —— 那部分在主库里
 *      已经一个字都不剩（`users.inviter_id` 被清零），只有扩展库快照答得了；
 *   3. 佣金余额：对账台，四列额度的恒等式与 `ledger_drift`，以及「已提现」
 *      额度的迁移登记。
 *
 * 标签顺序与可见性来自 `lib/pages.ts` 的 `QY_TAB_GROUPS`，本文件只提供正文。
 */
export function QyAdminCommissionUsersHub() {
  const { t } = useTranslation()

  return (
    <QySectionPageLayout>
      <QySectionPageLayout.Title>
        {t('qy_nav_commission_users_hub')}
      </QySectionPageLayout.Title>
      <QySectionPageLayout.Actions>
        {/* 通往另一半（按流水看）。侧栏上也有它，这里回答的是"我正看着这个人，
            他那几笔计佣怎么开" —— 删掉运营就得绕回侧栏重新找一遍。 */}
        <Button
          variant='outline'
          size='sm'
          render={<Link {...qyTabTarget('/qy/admin/commission-records')} />}
        >
          <ScrollText aria-hidden='true' />
          {t('qy_nav_a_commission_records')}
        </Button>
      </QySectionPageLayout.Actions>
      <QySectionPageLayout.Content>
        <QyPageTabs
          host='/qy/admin/commission-records/users'
          bodies={{
            '/qy/admin/commission-records/users': (
              <QyAdminCommissionUsersBody />
            ),
            '/qy/admin/commission-records/relations': (
              <QyAdminCommissionRelationsBody />
            ),
            '/qy/admin/commission-records/balances': (
              <QyAdminCommissionBalancesBody />
            ),
          }}
        />
      </QySectionPageLayout.Content>
    </QySectionPageLayout>
  )
}
