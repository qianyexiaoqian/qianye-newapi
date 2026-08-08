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
import { createFileRoute } from '@tanstack/react-router'

import { QyAdminUserGroups } from '@/features/qy/pages/admin-user-groups'

/**
 * 「分组矩阵 / 诊断视图」——`/qy/admin/group-matrix`。
 *
 * ── 本轮之后它是这一页唯一的入口 ──
 *
 * 这条路由此前对超管重定向到「系统设置 → 计费与支付 → 用户分组可用的模型分组
 * 配置」。那个 section 已经下线（见 `features/system-settings/billing/
 * section-registry.tsx`）：项目方要的是「用户分组」与「模型分组」两块，而配置
 * 动作已经全部搬进那两张表与行内弹窗。
 *
 * 留在这里的是三件**排查**能力，它们在"一次只看一档"的弹窗里结构性表达不出来：
 * 整列批量、跨档对比「哪几档人能到达某个模型分组」、以及孤儿令牌基线。
 *
 * 重定向必须一起删掉：目标 section 不在白名单里之后，`$section` 路由会把它弹回
 * 「额度设置」—— 超管点开这条书签会掉进一个与分组无关的页面，而普通管理员
 * （role=10，本页后端一直是 `AdminAuth`）看到的是正确的页面。同一条地址对两种
 * 角色给出两种结果，且其中一种是静默跳走。
 */
export const Route = createFileRoute('/_authenticated/qy/admin/group-matrix/')({
  component: QyAdminUserGroups,
})
