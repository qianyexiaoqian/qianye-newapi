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
import { Megaphone, ShieldCheck } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { EmptyState } from '@/components/empty-state'
import { SectionPageLayout } from '@/components/layout'

/**
 * 工作区首页占位。
 *
 * 刻意**不**做 `redirect` 到 `/qy/affiliate`：那个路由由返佣模块负责，
 * 在它落地之前重定向会把用户送进 404。等返佣页合入后，把本组件换成
 * `beforeLoad: () => { throw redirect({ to: '/qy/affiliate' }) }` 即可，
 * 改动范围就是路由文件那一行。
 */
export function QyWorkspaceHome() {
  const { t } = useTranslation()

  return (
    <SectionPageLayout>
      <SectionPageLayout.Title>{t('qy_nav_workspace')}</SectionPageLayout.Title>
      <SectionPageLayout.Content>
        <EmptyState
          icon={Megaphone}
          title={t('qy_common_workspace_title')}
          description={t('qy_common_pick_from_sidebar')}
        />
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}

/** 管理区首页占位。理由同上，等首个管理页落地后改成 redirect。 */
export function QyAdminHome() {
  const { t } = useTranslation()

  return (
    <SectionPageLayout>
      <SectionPageLayout.Title>
        {t('qy_nav_admin_workspace')}
      </SectionPageLayout.Title>
      <SectionPageLayout.Content>
        <EmptyState
          icon={ShieldCheck}
          title={t('qy_common_admin_workspace_title')}
          description={t('qy_common_pick_from_sidebar')}
        />
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}
