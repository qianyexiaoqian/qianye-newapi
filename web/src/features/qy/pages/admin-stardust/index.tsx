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
import { Settings2 } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

import { QySectionPageLayout } from '../../components/qy-section-page-layout'
import { QySdAdminAccrualsTab } from './components/accruals-tab'
import {
  AdjustStardustDialog,
  type QySdAdjustTarget,
} from './components/adjust-stardust-dialog'
import { QySdAdminBalancesTab } from './components/balances-tab'
import { QySdAdminLedgerTab } from './components/ledger-tab'
import { QySdAdminSettleTab } from './components/settle-tab'

type AdminTab = 'accruals' | 'balances' | 'ledger' | 'settle'

/**
 * 星屑账本（管理端）：余额 / 流水 / 日桶 / 结算，四张标签。
 *
 * 用上游 `Tabs` 而不是 `QyPageTabs`：这四张不是四个登记在案的页面（侧栏只有
 * 「星屑账本」一行），没有各自的 url 与 hash 可写。四张标签各打各的请求，
 * Base UI 的面板不 keepMounted，只有当前这一张会取数。
 *
 * ## 手调是超级管理员专属
 *
 * 后端 `RootActionStardustAdjust`：role=10 点了就是 403。所以 role<100 时渲染的
 * 是**一句话**（"该找谁"），不是一颗点了吃 403 的按钮，也不是直接抹掉 ——
 * 抹掉之后这一页对普通管理员就没有任何解释，口径见
 * `__tests__/root-action-gates.test.tsx`。其余动作（看流水、重跑结算、体检）
 * 一个都不连坐。
 */
export function QyAdminStardust() {
  const { t } = useTranslation()
  const isRoot =
    useAuthStore((state) => state.auth.user?.role) === ROLE.SUPER_ADMIN

  const [tab, setTab] = useState<AdminTab>('balances')
  // 从余额表点「流水」带过来的用户：换成 key 让流水标签整个重建，筛选框回到那个人。
  const [ledgerUserId, setLedgerUserId] = useState(0)
  const [adjustTarget, setAdjustTarget] = useState<QySdAdjustTarget | null>(
    null
  )

  const showLedger = (userId: number) => {
    setLedgerUserId(userId)
    setTab('ledger')
  }

  return (
    <QySectionPageLayout>
      <QySectionPageLayout.Title>
        {t('qy_nav_a_stardust')}
      </QySectionPageLayout.Title>
      <QySectionPageLayout.Actions>
        <Button
          size='sm'
          variant='outline'
          render={<Link to='/qy/admin/stardust-config' />}
        >
          <Settings2 aria-hidden='true' />
          {t('qy_nav_a_stardust_config')}
        </Button>
        {isRoot ? (
          <Button
            size='sm'
            onClick={() =>
              setAdjustTarget({ user_id: 0, username: '', available: null })
            }
          >
            {t('qy_sdadm_adjust_action')}
          </Button>
        ) : (
          <span className='text-muted-foreground self-center text-xs'>
            {t('qy_sdadm_adjust_root_only')}
          </span>
        )}
      </QySectionPageLayout.Actions>
      <QySectionPageLayout.Content>
        <Tabs
          value={tab}
          onValueChange={(value) => {
            // Base UI 把 tab value 标成 any，自动回落时还可能给 null。
            if (
              value === 'balances' ||
              value === 'ledger' ||
              value === 'accruals' ||
              value === 'settle'
            ) {
              setTab(value)
            }
          }}
          className='gap-3'
        >
          <TabsList className='flex w-full flex-wrap sm:w-auto'>
            <TabsTrigger value='balances' className='px-3'>
              {t('qy_sdadm_tab_balances')}
            </TabsTrigger>
            <TabsTrigger value='ledger' className='px-3'>
              {t('qy_sdadm_tab_ledger')}
            </TabsTrigger>
            <TabsTrigger value='accruals' className='px-3'>
              {t('qy_sdadm_tab_accruals')}
            </TabsTrigger>
            <TabsTrigger value='settle' className='px-3'>
              {t('qy_sdadm_tab_settle')}
            </TabsTrigger>
          </TabsList>
          <TabsContent value='balances'>
            <QySdAdminBalancesTab
              onAdjust={isRoot ? setAdjustTarget : undefined}
              onShowLedger={showLedger}
            />
          </TabsContent>
          <TabsContent value='ledger'>
            <QySdAdminLedgerTab
              key={ledgerUserId}
              initialUserId={ledgerUserId}
            />
          </TabsContent>
          <TabsContent value='accruals'>
            <QySdAdminAccrualsTab />
          </TabsContent>
          <TabsContent value='settle'>
            <QySdAdminSettleTab />
          </TabsContent>
        </Tabs>

        <AdjustStardustDialog
          target={adjustTarget}
          onClose={() => setAdjustTarget(null)}
        />
      </QySectionPageLayout.Content>
    </QySectionPageLayout>
  )
}
