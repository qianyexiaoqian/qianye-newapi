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
import type { QueryClient } from '@tanstack/react-query'
import { isRedirect, redirect } from '@tanstack/react-router'

import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

import { qyConfigQueryOptions } from './config-query'

/**
 * qy 的路由守卫。
 *
 * 三层判定各写一次，叶子路由永远不写守卫：
 *   - 登录     → 上游 `_authenticated/route.tsx`（qy 全部挂在它下面）
 *   - 扩展启用 → `routes/_authenticated/qy/route.tsx` 调 {@link requireQyEnabled}
 *   - 管理员   → `routes/_authenticated/qy/admin/route.tsx` 调 {@link requireQyAdmin}
 *
 * 注意：前端守卫只是 UX，不是安全边界（改 localStorage 里的 role 即可绕过）。
 * 所有 `/api/qy/admin/*` 必须在后端独立鉴权。
 */

/**
 * 扩展启用守卫。
 *
 * 只在"确定性地关闭"时拦截。503 / 网络错误一律放行，由页面内的
 * `QyPageBoundary` 承担错误态 —— 后端抖一下就把用户踢到 404 页，
 * 用户会以为功能被删了。
 */
export async function requireQyEnabled(context: {
  queryClient: QueryClient
}): Promise<void> {
  try {
    const config = await context.queryClient.ensureQueryData(
      qyConfigQueryOptions()
    )
    if (!config.enabled) throw redirect({ to: '/404' })
  } catch (error) {
    // TanStack 的经典坑：redirect 是靠抛异常实现的，不重新抛出会被这个
    // catch 悄悄吞掉，守卫直接失效。
    if (isRedirect(error)) throw error
  }
}

/** 管理员守卫。不去重构原项目已有的 8 处 inline 守卫，避免制造无谓冲突。 */
export function requireQyAdmin(): void {
  const { auth } = useAuthStore.getState()
  if (!auth.user || auth.user.role < ROLE.ADMIN) {
    throw redirect({ to: '/403' })
  }
}

/** 超管守卫，留给佣金费率这类"直接决定平台出血速度"的配置页。 */
export function requireQySuperAdmin(): void {
  const { auth } = useAuthStore.getState()
  if (auth.user?.role !== ROLE.SUPER_ADMIN) {
    throw redirect({ to: '/403' })
  }
}
