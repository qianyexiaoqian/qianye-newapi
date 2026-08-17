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
import { Megaphone, ShieldCheck } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { EmptyState } from '@/components/empty-state'
import { useAuthStore } from '@/stores/auth-store'

import { QySectionPageLayout } from '../components/qy-section-page-layout'
import { useQyConfig } from '../hooks/use-qy-config'
import { useQyIsSteinsGate } from '../hooks/use-qy-theme-preset'
import { qyPageMeta } from '../lib/page-meta'
import { getQyWorkspaceNavGroups } from '../nav'

/**
 * 工作区索引。
 *
 * 两个首页原本各放一个 `EmptyState`「请从左侧选择要进入的功能」—— 一个死胡同:
 * 移动端侧栏是收起的,用户在这一屏拿不到任何入口。Midnight Signal 主题下换成参考稿
 * 的**编辑式编号行**(`.feat-row`,原稿标题正是「未来道具 · 機能一覧」),
 * 每行是一个真正可点的链接。这既是该主题最有辨识度的构图之一,也顺手把死胡同
 * 补成了导航。其他预设保持原样的 `EmptyState`,零影响。
 *
 * 分组数据直接取侧边栏的定义(`getQyWorkspaceNavGroups`),不另建一张表 ——
 * 两处各写一遍,加页面时必然漏掉一处。该函数内部读的是 store/config 快照,
 * 所以这里显式订阅 `useQyConfig()` 与 `useAuthStore`,让开关或角色变化时能重渲。
 */
function QyWorkspaceIndex(props: { groupId: string }) {
  const { t } = useTranslation()
  // 订阅本身就是目的:两个值都只用于触发重渲染,真正的读取在下面那个函数里。
  useQyConfig()
  useAuthStore((state) => state.auth.user?.role)

  const group = getQyWorkspaceNavGroups(t).find(
    (item) => item.id === props.groupId
  )
  if (group == null || group.items.length === 0) return null

  return (
    <div className='qy-sg-index-group'>
      <span className='qy-sg-index-label'>{group.title}</span>
      <div className='qy-sg-rows qy-sg-index'>
        {group.items.map((item) => {
          // NavItem.url 的类型是路由联合类型（可为 undefined）；qy 的每一项都写了
          // 字面量路径，取不到时跳过而不是渲染一个点不动的行。
          const url = typeof item.url === 'string' ? item.url : null
          if (url == null) return null
          return (
            <Link key={url} to={item.url} className='qy-sg-row'>
              <span className='qy-sg-idx'>/ {qyPageMeta(url).no}</span>
              <span className='qy-sg-row-title'>
                {item.title}
                {/* 路径代号。参考稿 .feat-row h3 small 的位置，原稿放的是
                    「WORLD LINE ROUTER」这类英文代号；这里放真实路由路径，
                    既符合"等宽英文代号"的排版，也是一条有用的信息。 */}
                <small>
                  {url.replace(/^\/qy\/?/, '').replaceAll('/', ' · ')}
                </small>
              </span>
            </Link>
          )
        })}
      </div>
    </div>
  )
}

export function QyWorkspaceHome() {
  const { t } = useTranslation()
  const steinsGate = useQyIsSteinsGate()

  return (
    <QySectionPageLayout>
      <QySectionPageLayout.Title>
        {t('qy_nav_workspace')}
      </QySectionPageLayout.Title>
      <QySectionPageLayout.Content>
        {steinsGate ? (
          <QyWorkspaceIndex groupId='qy-personal' />
        ) : (
          <EmptyState
            icon={Megaphone}
            title={t('qy_common_workspace_title')}
            description={t('qy_common_pick_from_sidebar')}
          />
        )}
      </QySectionPageLayout.Content>
    </QySectionPageLayout>
  )
}

export function QyAdminHome() {
  const { t } = useTranslation()
  const steinsGate = useQyIsSteinsGate()

  return (
    <QySectionPageLayout>
      <QySectionPageLayout.Title>
        {t('qy_nav_admin_workspace')}
      </QySectionPageLayout.Title>
      <QySectionPageLayout.Content>
        {steinsGate ? (
          <QyWorkspaceIndex groupId='qy-admin' />
        ) : (
          <EmptyState
            icon={ShieldCheck}
            title={t('qy_common_admin_workspace_title')}
            description={t('qy_common_pick_from_sidebar')}
          />
        )}
      </QySectionPageLayout.Content>
    </QySectionPageLayout>
  )
}
