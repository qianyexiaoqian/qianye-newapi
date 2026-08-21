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
import { QyAdminDailyConsumeBody } from '../admin-daily-consume'
import { QyAdminWithdrawalsBody } from '../admin-withdrawals'
import { QyPageTabs } from '../components/qy-page-tabs'

/**
 * 「结算台」选择夹 —— 日消费明细 / 佣金审核 / 提现审核。
 *
 * 项目方原话：「把日消费明细/佣金审核，提醒审核，这些管理页面弄成选择夹，
 * 放在一个页面上。」（「提醒审核」站内不存在；侧栏「结算」组底下与前两页并列
 * 的第三页是**提现审核**，按上下文取它。）
 *
 * ── 三张标签为什么是这个顺序 ──
 * 它是钱在系统里流动的顺序，也是运营对账时的追问顺序：
 *
 *   谁花了多少（主库 logs）→ 这笔消费给上线记了多少（计佣账本）→ 把钱付出去。
 *
 * 所以第一张是日消费明细，而不是被点名最多的佣金审核。这三张表本来就要对着
 * 看：日消费明细上的「未计佣」那一列，答案全在佣金审核那一张表里。
 *
 * ── 为什么宿主是新开的一页 ──
 * 见 `lib/pages.ts` 的 `QY_TAB_GROUPS`。一句话：让三条旧地址一视同仁地重定向，
 * 而不是让其中一条变成"名字写着 A、打开是 B"。
 *
 * ── 这一页刻意**没有** Actions 槽 ──
 * 三张标签各自的动作（导出 CSV、通往佣金那几张表）都留在自己的正文里。槽是
 * 三张标签共用的，把谁的按钮放上去，另外两张标签上就会出现一个与本屏无关的
 * 按钮 —— 而"导出"这种按钮点下去还真会给出一份数据。
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
            '/qy/admin/daily-consume': <QyAdminDailyConsumeBody />,
            '/qy/admin/commission-records': <QyAdminCommissionRecordsBody />,
            '/qy/admin/withdrawals': <QyAdminWithdrawalsBody />,
          }}
        />
      </QySectionPageLayout.Content>
    </QySectionPageLayout>
  )
}
