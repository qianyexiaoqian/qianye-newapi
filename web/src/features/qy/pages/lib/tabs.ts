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
import { useLocation, useNavigate } from '@tanstack/react-router'
import { useCallback } from 'react'

import { qyTabHash } from '../../lib/pages'

/**
 * 选项卡组的"当前是哪一张"。
 *
 * ── 为什么是 hash 而不是 query 参数 ──
 * 其中一个宿主是**上游的钱包页**，它的路由用 `validateSearch` + zod object 校验
 * query；zod object 默认 strip 未声明的键，路由器会把 `?qy_tab=…` 直接从地址栏
 * 抹掉，标签状态活不过一次导航。hash 不经过 `validateSearch`。
 * 另一个宿主是 qy 自己的页面，本可以用 query，但那样就有了两套机制、两处要改 ——
 * 统一用 hash。
 *
 * ── 为什么把解析拆成纯函数 ──
 * `useLocation` 只能在路由树里跑，而"hash 认不出来时该落到哪张标签"才是这里
 * 真正会出错的地方（手改地址、功能开关把目标标签关掉、旧书签指向已下线的页面）。
 * 拆出来之后这条规则可以被直接测到，不用挂整棵路由。
 */

/**
 * 把 hash 解析成一个**一定可见**的标签 url。
 *
 * `visibleTabs` 已经过功能开关与角色过滤，所以指向被关掉功能的旧链接会落到
 * 第一张标签，而不是渲染一张空白页。列表为空时返回 `null`（宿主自己决定
 * 整块不渲染）。
 */
export function qyResolveTab(
  hash: string | undefined,
  visibleTabs: readonly string[]
): string | null {
  if (visibleTabs.length === 0) return null
  const wanted = (hash ?? '').replace(/^#/, '')
  const hit = visibleTabs.find((url) => qyTabHash(url) === wanted)
  return hit ?? visibleTabs[0]
}

/**
 * 选中的标签 + 切换函数。
 *
 * `replace: true`：切标签不该进历史栈 —— 用户按返回键的预期是"离开这个页面"，
 * 而不是"撤销一次标签切换"（上游钱包页的 tab 也是这么做的）。
 */
export function useQyTabHash(visibleTabs: readonly string[]) {
  const hash = useLocation({ select: (location) => location.hash })
  const navigate = useNavigate()
  const active = qyResolveTab(hash, visibleTabs)

  const select = useCallback(
    (url: string) => {
      void navigate({ to: '.', hash: qyTabHash(url), replace: true })
    },
    [navigate]
  )

  return { active, select }
}
