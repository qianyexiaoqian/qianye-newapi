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
import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { ArrowLeftRight } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'

import { QyPageBoundary } from '../../components/qy-page-boundary'
import { QySectionPageLayout } from '../../components/qy-section-page-layout'
import { qyPayPasswordStatusQuery } from '../../lib/pay-password'
import { PayPasswordFormCard } from './components/pay-password-form-card'
import { PayPasswordRecoverCard } from './components/pay-password-recover-card'
import { PayPasswordStatusCard } from './components/pay-password-status-card'

/**
 * 支付密码管理页。
 *
 * 保护范围只有**余额划转**一条路径（裁决 1）。提现、充值、兑换码本轮没有接入，
 * 这是一个已知缺口，不要在文案里把它说成"资金操作已受保护"。
 *
 * 页面本身只负责取状态与排版；设置/修改的校验、邮箱找回的红线(绝不代为绑定邮箱)
 * 各自收敛在对应的卡片里。
 */
export function QyPayPassword() {
  const { t } = useTranslation()
  const statusQuery = useQuery(qyPayPasswordStatusQuery())

  return (
    <QySectionPageLayout>
      <QySectionPageLayout.Title>
        {t('qy_nav_pay_password')}
      </QySectionPageLayout.Title>
      <QySectionPageLayout.Actions>
        <Button variant='outline' size='sm' render={<Link to='/qy/transfer' />}>
          <ArrowLeftRight aria-hidden='true' />
          {t('qy_nav_transfer')}
        </Button>
      </QySectionPageLayout.Actions>
      <QySectionPageLayout.Content>
        <QyPageBoundary query={statusQuery}>
          {statusQuery.data != null && (
            <div className='grid gap-4 lg:grid-cols-[minmax(0,1fr)_minmax(0,1.4fr)] lg:items-start'>
              <PayPasswordStatusCard status={statusQuery.data} />
              <div className='space-y-4'>
                <PayPasswordFormCard
                  isSet={statusQuery.data.is_set}
                  locked={statusQuery.data.locked}
                />
                {/* 找回入口常驻，不只在锁定时出现:用户忘了密码但还没输错到锁定,
                    同样需要这条路。 */}
                {statusQuery.data.is_set && <PayPasswordRecoverCard />}
              </div>
            </div>
          )}
        </QyPageBoundary>
      </QySectionPageLayout.Content>
    </QySectionPageLayout>
  )
}
