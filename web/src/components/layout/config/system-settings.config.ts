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
import { type TFunction } from 'i18next'
import {
  Box,
  CreditCard,
  Layout,
  Settings,
  Shield,
  ShieldAlert,
  Wrench,
} from 'lucide-react'

import {
  QY_SYSTEM_SETTINGS_PATH_PATTERN,
  withQySystemSettingsNavGroups,
} from '@/features/qy/system-settings'
import { getAuthSectionNavItems } from '@/features/system-settings/auth/section-registry.tsx'
import { getBillingSectionNavItems } from '@/features/system-settings/billing/section-registry.tsx'
import { getContentSectionNavItems } from '@/features/system-settings/content/section-registry.tsx'
import { getModelsSectionNavItems } from '@/features/system-settings/models/section-registry.tsx'
import { getOperationsSectionNavItems } from '@/features/system-settings/operations/section-registry.tsx'
import { getSecuritySectionNavItems } from '@/features/system-settings/security/section-registry.tsx'
import { getSiteSectionNavItems } from '@/features/system-settings/site/section-registry.tsx'

import type { NavGroup, SidebarView } from '../types'

/**
 * Sidebar nav groups for the System Settings nested view.
 *
 * Kept as a single group because the workspace title in the sidebar
 * header already provides top-level context — the inner group label
 * scopes the items as "administration" actions.
 */
function getSystemSettingsNavGroups(t: TFunction): NavGroup[] {
  // qy 扩展的配置类管理页并进同一组（需求 8）。扩展关掉、或当前用户不是管理员
  // 时原样返回入参，这个视图与改造前逐字节一致。
  return withQySystemSettingsNavGroups(
    [
      {
        id: 'system-administration',
        title: t('System Administration'),
        items: [
          {
            title: t('Site & Branding'),
            icon: Settings,
            items: getSiteSectionNavItems(t),
          },
          {
            title: t('Authentication'),
            icon: Shield,
            items: getAuthSectionNavItems(t),
          },
          {
            title: t('Billing & Payment'),
            icon: CreditCard,
            items: getBillingSectionNavItems(t),
          },
          {
            title: t('Models & Routing'),
            icon: Box,
            items: getModelsSectionNavItems(t),
          },
          {
            title: t('Security & Limits'),
            icon: ShieldAlert,
            items: getSecuritySectionNavItems(t),
          },
          {
            title: t('Console Content'),
            icon: Layout,
            items: getContentSectionNavItems(t),
          },
          {
            title: t('Operations'),
            icon: Wrench,
            items: getOperationsSectionNavItems(t),
          },
        ],
      },
    ],
    t
  )
}

/**
 * Nested sidebar view for `/system-settings/*`.
 *
 * Activates the Vercel / Cloudflare-style drill-in sidebar:
 * the root navigation is replaced by the system administration
 * groups, with a "Back to Dashboard" affordance in the header.
 */
export const SYSTEM_SETTINGS_VIEW: SidebarView = {
  id: 'system-settings',
  // qy 的配置页现在也是这个抽屉的成员，所以它们的 url 必须一起匹配 ——
  // 否则从抽屉里点一个 qy 页面，侧栏立刻掉回根导航。见
  // `features/qy/system-settings.ts` 里对边界（只含配置页、不含流水页）的说明。
  pathPattern: QY_SYSTEM_SETTINGS_PATH_PATTERN,
  parent: {
    to: '/dashboard/overview',
    label: 'Back to Dashboard',
  },
  getNavGroups: getSystemSettingsNavGroups,
}
