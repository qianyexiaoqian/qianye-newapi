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
import { useLocation } from '@tanstack/react-router'
import type { ReactNode } from 'react'

import {
  isQyRestrictedPathAllowed,
  useQyIsRestricted,
} from '../lib/account-status'
import { QyRestrictedBanner, QyRestrictedLanding } from './qy-restricted-notice'

/**
 * 登录后主内容区的受限闸。
 *
 * ── 为什么是渲染期判定而不是路由 `beforeLoad` 守卫 ──
 * `beforeLoad` 只在路由匹配时跑一次，而"被中途禁用"恰恰是**不发生导航**的那
 * 一刻：用户停在某个页面上，管理员在后台把他禁了，下一次接口调用返回 401。
 * 渲染期判定订阅的是 store 里的 `status`，任何一次刷新到新用户信息（登录、
 * refresh、`GET /api/user/self`）都会立刻把界面切过来，不需要用户先点一下。
 *
 * 顺带也不用担心 TanStack 的守卫缓存语义：一个 `if` 永远不会被跳过。
 *
 * ── 为什么放在这里而不是每个页面自己判 ──
 * 这是登录后主内容区的唯一出口（`AuthenticatedLayout`），一处即全站。
 * 逐页判定要改 40+ 个文件，而且新页面照抄现有页面时会漏 —— 那正是这个仓库
 * 反复出现的"漏一处就是一条静默死路"。
 *
 * **这不是安全边界**，是降级展示：真正的拦截在后端鉴权层的白名单上。
 */
export function QyRestrictedGate(props: { children: ReactNode }) {
  const restricted = useQyIsRestricted()
  const pathname = useLocation({ select: (location) => location.pathname })

  if (!restricted) return props.children

  return (
    <>
      <QyRestrictedBanner />
      {isQyRestrictedPathAllowed(pathname) ? (
        props.children
      ) : (
        <QyRestrictedLanding />
      )}
    </>
  )
}
