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
import { QyFundAuditTab } from './fund-audit-tab'
import { QyPiiAuditTab } from './pii-audit-tab'
import { QyRequestAuditTab } from './request-audit-tab'

/**
 * 审计中心：三张互补的表，一个页面。
 *
 *  1. **资金审计**（`qy_audit_logs`）—— 记「判定」。谁按什么费率动了谁的钱、
 *     前后快照是什么、当时的汇率冻结在多少。靠调用点手写埋点，一天几十行，
 *     每一行都要能被逐字辩论。
 *  2. **请求台账**（`qy_request_audits`）—— 记「调用」。谁在什么时候调了哪个
 *     写接口、成没成功。由中间件兜底，新接口天然有痕，一天几千行。
 *  3. **明文访问**（`qy_pii_audits`）—— 记「谁看了谁的收款信息」。合规专用，
 *     强制事由、保留期更长。
 *
 * 三者刻意不合表：合并会让资金台账被几千行低价值读取稀释，也会让合规导出
 * 连带带出大量无关记录。放在同一个页面则是因为排查一件事往往要横跨三张表 ——
 * 用 request_id 把「他调了哪个接口」和「那次调用改了多少钱」串起来。
 */
export function QyAdminAuditLogs() {
  const { t } = useTranslation()

  return (
    <QySectionPageLayout>
      <QySectionPageLayout.Title>
        {t('qy_cfg_audit_title')}
      </QySectionPageLayout.Title>
      <QySectionPageLayout.Content>
        <Tabs defaultValue='fund'>
          <TabsList>
            <TabsTrigger value='fund'>{t('qy_cfg_audit_tab_fund')}</TabsTrigger>
            <TabsTrigger value='request'>
              {t('qy_cfg_audit_tab_request')}
            </TabsTrigger>
            <TabsTrigger value='pii'>{t('qy_cfg_audit_tab_pii')}</TabsTrigger>
          </TabsList>

          {/* 三个 tab 各自持有筛选状态与查询。切回来时状态还在，
              而未挂载的 tab 不会发请求 —— 请求台账一天几千行，
              预取它只是白白给扩展库加读压力。 */}
          <TabsContent value='fund'>
            <QyFundAuditTab />
          </TabsContent>
          <TabsContent value='request'>
            <QyRequestAuditTab />
          </TabsContent>
          <TabsContent value='pii'>
            <QyPiiAuditTab />
          </TabsContent>
        </Tabs>
      </QySectionPageLayout.Content>
    </QySectionPageLayout>
  )
}
