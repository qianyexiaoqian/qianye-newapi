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
import type { TFunction } from 'i18next'

import type { ContentSettings } from '../types'
import { createSectionRegistry } from '../utils/section-registry'
import { AnnouncementsSection } from './announcements-section'
import { ApiInfoSection } from './api-info-section'
import { ChatSettingsSection } from './chat-settings-section'
import { DashboardSection } from './dashboard-section'
import { DrawingSettingsSection } from './drawing-settings-section'
import { FAQSection } from './faq-section'
import { QyRestrictedNoticeMovedSection } from './qy-restricted-notice-moved'
import { UptimeKumaSection } from './uptime-kuma-section'

/**
 * Validate and coerce DataExportDefaultTime to a safe value
 */
function validateDataExportDefaultTime(value: string): 'week' | 'hour' | 'day' {
  if (value === 'week' || value === 'hour' || value === 'day') {
    return value
  }
  // Default to 'hour' if value is unexpected
  return 'hour'
}

const CONTENT_SECTIONS = [
  {
    id: 'dashboard',
    titleKey: 'Data Dashboard',
    build: (settings: ContentSettings) => (
      <DashboardSection
        defaultValues={{
          DataExportEnabled: settings.DataExportEnabled,
          DataExportInterval: settings.DataExportInterval,
          DataExportDefaultTime: validateDataExportDefaultTime(
            settings.DataExportDefaultTime
          ),
        }}
      />
    ),
  },
  {
    id: 'announcements',
    titleKey: 'Announcements',
    build: (settings: ContentSettings) => (
      <AnnouncementsSection
        enabled={settings['console_setting.announcements_enabled']}
        data={settings['console_setting.announcements']}
      />
    ),
  },
  {
    /*
      受限账号公告**已经不在这一页**。项目方原话：「受限制账号，在系统设置里面
      单独进行配置。」表单整体搬去了「系统设置 → 扩展设置 → 受限账号」
      （`/qy/admin/restricted-accounts`），与受限账号计数、受限账号可达面清单
      放在同一屏 —— 那三件事回答的是同一个问题，此前散在三处。

      这里保留的是一块**路牌**（`QyRestrictedNoticeMovedSection`，零输入控件），
      理由见该文件：section id 一旦删掉，旧深链接会被 `$section` 路由静默重定向
      回「数据看板」，既没有报错也没有解释。路牌同时被
      {@link MOVED_SECTION_IDS} 从左侧菜单里滤掉 —— 深链接接得住，菜单上不留噪声。
    */
    id: 'qy-restricted-notice',
    titleKey: 'qy_restricted_notice_title',
    build: () => <QyRestrictedNoticeMovedSection />,
  },
  {
    id: 'api-info',
    titleKey: 'API Addresses',
    build: (settings: ContentSettings) => (
      <ApiInfoSection
        enabled={settings['console_setting.api_info_enabled']}
        data={settings['console_setting.api_info']}
      />
    ),
  },
  {
    id: 'faq',
    titleKey: 'FAQ',
    build: (settings: ContentSettings) => (
      <FAQSection
        enabled={settings['console_setting.faq_enabled']}
        data={settings['console_setting.faq']}
      />
    ),
  },
  {
    id: 'uptime-kuma',
    titleKey: 'Uptime Kuma',
    build: (settings: ContentSettings) => (
      <UptimeKumaSection
        enabled={settings['console_setting.uptime_kuma_enabled']}
        data={settings['console_setting.uptime_kuma_groups']}
      />
    ),
  },
  {
    id: 'chat',
    titleKey: 'Chat Presets',
    build: (settings: ContentSettings) => (
      <ChatSettingsSection defaultValue={settings.Chats} />
    ),
  },
  {
    id: 'drawing',
    titleKey: 'Drawing',
    build: (settings: ContentSettings) => (
      <DrawingSettingsSection
        defaultValues={{
          DrawingEnabled: settings.DrawingEnabled,
          MjNotifyEnabled: settings.MjNotifyEnabled,
          MjAccountFilterEnabled: settings.MjAccountFilterEnabled,
          MjForwardUrlEnabled: settings.MjForwardUrlEnabled,
          MjModeClearEnabled: settings.MjModeClearEnabled,
          MjActionCheckSuccessEnabled: settings.MjActionCheckSuccessEnabled,
        }}
      />
    ),
  },
] as const

export type ContentSectionId = (typeof CONTENT_SECTIONS)[number]['id']

const contentRegistry = createSectionRegistry<
  ContentSectionId,
  ContentSettings
>({
  sections: CONTENT_SECTIONS,
  defaultSection: 'dashboard',
  basePath: '/system-settings/content',
  urlStyle: 'path',
})

export const CONTENT_SECTION_IDS = contentRegistry.sectionIds
export const CONTENT_DEFAULT_SECTION = contentRegistry.defaultSection

/**
 * 已搬走、只留路牌的 section。
 *
 * 它们仍然是**合法的 section id**（旧深链接因此落到路牌上，而不是被静默重定向
 * 回默认段），但不出现在左侧菜单里：一个"点进去只告诉你去别处"的常驻菜单项
 * 是纯噪声，而深链接的接住是一次性的。
 *
 * 过滤放在这里而不是共享的 `utils/section-registry.ts`：那是 7 个设置页共用的
 * 上游文件，为一次搬家给它加一个字段，等于让另外 6 页都长出一个没人用的概念。
 */
const MOVED_SECTION_IDS = new Set<string>(['qy-restricted-notice'])

export const getContentSectionNavItems = (t: TFunction) =>
  contentRegistry
    .getSectionNavItems(t)
    .filter(
      (item) =>
        !MOVED_SECTION_IDS.has(item.url.slice(item.url.lastIndexOf('/') + 1))
    )
export const getContentSectionContent = contentRegistry.getSectionContent
export const getContentSectionMeta = contentRegistry.getSectionMeta
