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
import { createFileRoute, redirect } from '@tanstack/react-router'

import { QyAdminUserGroups } from '@/features/qy/pages/admin-user-groups'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

/**
 * 旧路由 —— 分组矩阵已搬进「系统设置 → 计费与支付 → 用户分组」。
 *
 * 保留成重定向而不是删掉：管理员的书签与浏览器历史里还留着这个地址，审计记录
 * 与工单里也会引用它。两个页面读写的是同一组数据、同一组端点，并存两个入口就
 * 意味着两份互不知情的草稿与两道各自为政的保存闸门 —— 而写入是两库不原子的，
 * 一个屏幕显示绿色「已保存」、另一个显示线上真实的半成状态，是这套设计最坏的
 * 失败方式。
 *
 * `replace`：旧地址不该留在历史栈里，否则按返回键会被立刻再弹回来。
 *
 * ────────────────── 为什么重定向要看角色 ──────────────────
 *
 * 搬家把这一页的准入从 **ROLE.ADMIN(10)** 悄悄提到了 **ROLE.SUPER_ADMIN(100)**：
 * 新家在 `/system-settings` 下，那条父路由要求超管
 * (`routes/_authenticated/system-settings/route.tsx`)，而分组矩阵的后端这组接口
 * 一直是 `middleware.AdminAuth()`，也就是 role>=10 就能调
 * (`qianye/router.go` 的 admin 组)。
 *
 * 无条件重定向的后果不是「换了个地址」，而是**普通管理员从此在前端完全没有任何
 * 入口配置用户分组的可用范围与倍率**：父守卫放行 → 这里重定向 → 新家判超管 →
 * 403。他看到的是一个没有解释的 403，而不是「需要超管」。这是一次纯由前端搬家
 * 造成的能力回退，后端从头到尾没有要求过超管。
 *
 * 所以按角色分叉：够得着新家的人被送过去（保证只有一个入口）；够不着的人
 * 原地渲染旧页面（保证不丢能力）。两者对同一个人不会同时出现，
 * 「两份互不知情的草稿」那个顾虑不成立。
 */
export const Route = createFileRoute('/_authenticated/qy/admin/group-matrix/')({
  beforeLoad: () => {
    const { auth } = useAuthStore.getState()
    if (auth.user?.role === ROLE.SUPER_ADMIN) {
      throw redirect({
        to: '/system-settings/billing/$section',
        params: { section: 'user-groups' },
        replace: true,
      })
    }
  },
  component: QyAdminUserGroups,
})
