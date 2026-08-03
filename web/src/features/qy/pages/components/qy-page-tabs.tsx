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
import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

import { useQyConfig } from '../../hooks/use-qy-config'
import {
  QY_PAGES,
  QY_TAB_GROUPS,
  isQyPageVisible,
  qyTabHash,
} from '../../lib/pages'
import { useQyTabHash } from '../lib/tabs'

type QyPageTabsProps = {
  /** 宿主页 url，必须在 {@link QY_TAB_GROUPS} 里登记过。 */
  host: string
  /**
   * 每张标签的正文，按页面 url 索引。
   *
   * **只传正文，不要传 `QySectionPageLayout`**：标签页里再套一层区段头
   * （`LAB MEMO — NN` + 大标题）会得到两级标题，而且序号讲的是"独立页面"，
   * 收进选择夹之后它已经不是一个独立页面了。
   *
   * 少给一张 = 那张标签不渲染（不是崩），多给一张 = 被忽略；两种情况
   * 都由 `__tests__/qy-page-tabs.test.ts` 的覆盖度断言在构建期拦掉。
   */
  bodies: Record<string, ReactNode>
}

/**
 * 选项卡组的通用外壳（需求 2 / 3 的「选择夹」）。
 *
 * 顺序、标题、可见性全部从 {@link QY_TAB_GROUPS} × {@link QY_PAGES} 派生，
 * 宿主页只负责把正文塞进来 —— 两个宿主各写一份标签清单就会漂移成
 * "侧栏删了这一页、宿主页却还有这张标签"。
 *
 * 刻意**不加 `keepMounted`**：这三/四张标签各自会打自己的请求（限额、记录、
 * 支付密码状态、提现配置），全部挂载等于一进钱包页就同时打四个接口。
 * Base UI 的 `Tabs.Panel` 在没有 `keepMounted` 时隐藏即返回 null，只有当前
 * 这张会跑查询；切回去由 react-query 的缓存兜住。
 */
export function QyPageTabs(props: QyPageTabsProps) {
  const { t } = useTranslation()
  const config = useQyConfig()
  const role = useAuthStore((state) => state.auth.user?.role) ?? ROLE.GUEST

  const group = QY_TAB_GROUPS.find((item) => item.host === props.host)
  const tabs = (group?.pages ?? []).flatMap((url) => {
    const page = QY_PAGES.find((item) => item.url === url)
    if (page == null) return []
    if (!isQyPageVisible(page, config.features, role >= ROLE.ADMIN)) return []
    if (props.bodies[url] == null) return []
    return [page]
  })

  const { active, select } = useQyTabHash(tabs.map((page) => page.url))
  if (active == null) return null

  return (
    <Tabs
      value={active}
      onValueChange={(value) => {
        // Base UI 把 tab value 标成 any，自动回落时还可能给 null。
        const next = tabs.find((page) => page.url === value)
        if (next != null) select(next.url)
      }}
      className='gap-3'
    >
      <TabsList className='flex w-full flex-wrap sm:w-auto'>
        {tabs.map((page) => (
          <TabsTrigger key={page.url} value={page.url} className='px-3'>
            {t(page.titleKey)}
          </TabsTrigger>
        ))}
      </TabsList>
      {tabs.map((page) => (
        <TabsContent key={page.url} value={page.url}>
          {/* 锚点 id 挂在内层 div 上，不挂在 Tabs.Panel 上：Base UI 用 panel 自己的
              id 与触发器做 aria-controls / aria-labelledby 配对，从外面覆写它有
              把这条关系拆掉的风险。这里只是让浏览器在带 hash 打开时把这一段滚
              进视野。 */}
          <div id={qyTabHash(page.url)} className='scroll-mt-4'>
            {props.bodies[page.url]}
          </div>
        </TabsContent>
      ))}
    </Tabs>
  )
}
