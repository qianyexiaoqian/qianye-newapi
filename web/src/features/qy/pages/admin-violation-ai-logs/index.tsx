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
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'

import { QySectionPageLayout } from '../../components/qy-section-page-layout'
import { QyAiLogCard } from './components/ai-log-card'

/**
 * AI 审核日志。
 *
 * ## 为什么它是**自己一页**,而不是 AI 审核配置页底下的一张卡
 *
 * 第一版把它挂在 `/qy/admin/violation-ai-review` 最底下 —— 那一页从上到下是
 * 设置(含一个 12 行的提示词编辑框与全文预览)、作用域、渠道、成本,日志排第五。
 * 结果是项目方打开那一页之后**根本没看见它**,直接问"日志表在哪"。
 *
 * 这不是排序问题,是分类问题。本仓侧栏的界线写在 `lib/pages.ts` 顶部:
 * **根侧栏放每天要开的流水页,设置抽屉放"改一次影响后续每一笔"的配置**。
 * AI 审核配置属于后者(渠道、密钥、抽样率),而这份日志属于前者 —— 它与
 * 「违规记录」「工单」「审计日志」是同一类东西,所以它归到「风控与审计」组里,
 * 紧挨着违规记录。
 *
 * 保留期与"要不要留内容"仍然在配置页上(它们跟 AI 审核设置是同一次保存),
 * 右上角那个按钮就是去那里的路。
 */
export function QyAdminViolationAiLogs() {
  const { t } = useTranslation()
  return (
    <QySectionPageLayout>
      <QySectionPageLayout.Title>
        {t('qy_nav_a_violation_ai_logs')}
      </QySectionPageLayout.Title>
      <QySectionPageLayout.Actions>
        {/* 保留期、内容留存开关都在配置页上 —— 看日志的人最常问的第二个问题
            就是"这些能留多久",给他一条路过去。 */}
        <Button
          size='sm'
          variant='outline'
          render={<Link to='/qy/admin/violation-ai-review' />}
        >
          {t('qy_ai_log_go_config')}
        </Button>
      </QySectionPageLayout.Actions>
      <QySectionPageLayout.Content>
        <QyAiLogCard />
      </QySectionPageLayout.Content>
    </QySectionPageLayout>
  )
}
