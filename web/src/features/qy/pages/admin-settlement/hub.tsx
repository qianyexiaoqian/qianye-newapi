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
import { QyAdminCommissionRecordsBody } from '../admin-commission-records'
import { QyAdminCommissionUsersBody } from '../admin-commission-users'
import { QyAdminDailyConsumeBody } from '../admin-daily-consume'
import { QyPageTabs } from '../components/qy-page-tabs'

/**
 * 「结算台」选择夹（D-15 恢复）—— 日消费明细 / 佣金审核 / 佣金用户。
 *
 * 项目方原话（需求 3 时期）：「把日消费明细/佣金审核，提醒审核，这些管理页面
 * 弄成选择夹，放在一个页面上。」D-14 把它连同佣金账本整体删除；D-15 把账本
 * 请回来，记的是星辉、到期自动入账 —— 第三张标签从此不再是「提现审核」（提现
 * 模块永久删除），换成此前「用户佣金」那一行的正文：每个人账上挂着多少、已经
 * 自动入账了多少、入账记录。
 *
 * ── 三张标签为什么是这个顺序 ──
 * 它是钱在系统里流动的顺序，也是运营对账时的追问顺序：
 *
 *   谁花了多少（主库 logs）→ 这笔消费给上线记了多少星辉（计佣账本）→ 每个人
 *   账上还挂着多少、已经进星辉多少（余额 + 入账记录）。
 *
 * 第一张标签直接嵌现有的日消费明细组件（`admin-daily-consume`）。那一份正文在
 * 「邀请管理」里也有一张标签：两个宿主各嵌一次是刻意的 —— 日消费明细上的
 * 「未计佣」那一列，答案全在隔壁佣金审核那一张表里，两者必须同屏。
 *
 * ── 这一页刻意**没有** Actions 槽 ──
 * 三张标签各自的动作（导出 CSV、通往佣金配置）都留在自己的正文里。槽是三张
 * 标签共用的，把谁的按钮放上去，另外两张标签上就会出现一个与本屏无关的按钮。
 *
 * 标签顺序与可见性来自 `lib/pages.ts` 的 `QY_TAB_GROUPS`，本文件只提供正文。
 * `QyPageTabs` 不 keepMounted，所以**不可见的标签一个请求都不发**：日消费明细
 * 那一条是主库大表的聚合查询，一进页面就打三份查询是这次合并最需要避免的事。
 */
export function QyAdminSettlementHub() {
  const { t } = useTranslation()

  return (
    <QySectionPageLayout>
      <QySectionPageLayout.Title>
        {t('qy_nav_a_settlement')}
      </QySectionPageLayout.Title>
      <QySectionPageLayout.Content>
        <QyPageTabs
          host='/qy/admin/settlement'
          bodies={{
            '/qy/admin/settlement': <QyAdminDailyConsumeBody />,
            '/qy/admin/commission-records': <QyAdminCommissionRecordsBody />,
            '/qy/admin/commission-users': <QyAdminCommissionUsersBody />,
          }}
        />
      </QySectionPageLayout.Content>
    </QySectionPageLayout>
  )
}
