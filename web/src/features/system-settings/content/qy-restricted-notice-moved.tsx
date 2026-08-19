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
import { ArrowRight } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'

import { SettingsSection } from '../components/settings-section'

/**
 * 受限账号公告在「内容管理」里留下的**路牌**。
 *
 * ── 为什么留、而不是把这一段整个删掉 ──
 *
 * 这一页的 `$section` 路由对不认识的 section id 一律**静默重定向**回默认段
 * （`content/$section.tsx` 的 beforeLoad）。删掉 id 的直接后果是：任何一条
 * `/system-settings/content/qy-restricted-notice` 的深链接（书签、工单里贴出去
 * 的地址、浏览器历史）会把人送到「数据看板」，既没有报错也没有解释 ——
 * 管理员只会以为自己记错了地址。本仓已经为同一个形状写过一次判断
 * （`features/qy/system-settings.ts` 里 `QY_BILLING_SECTION_URLS` 的注释：
 * section 恒在，只有入口跟着走），这里照同一条办。
 *
 * ── 为什么它不是"改了不生效的孤儿表单" ──
 *
 * 这里**一个输入控件都没有**。表单连同它的取数、保存、预览整体搬到了
 * `features/qy/pages/admin-restricted-accounts/components/notice-card.tsx`，
 * 这一段只剩一句话和一个链接。留下一个能填能存、但存到别处去的副本才是那种
 * 孤儿，而那正是这个组件刻意不做的事。
 *
 * ── 它不出现在左侧菜单里 ──
 *
 * 见 `section-registry.tsx` 的 `getContentSectionNavItems`：菜单项被滤掉，
 * 只有深链接还落得到这里。一个"点进去只告诉你去别处"的常驻菜单项是纯噪声，
 * 而深链接的接住是一次性的。
 */
export function QyRestrictedNoticeMovedSection() {
  const { t } = useTranslation()
  return (
    <SettingsSection title={t('qy_restricted_notice_title')}>
      <p className='text-muted-foreground text-sm'>{t('qy_ra_moved_hint')}</p>
      <div>
        <Button
          variant='outline'
          render={<Link to='/qy/admin/restricted-accounts' />}
        >
          {t('qy_ra_moved_link')}
          <ArrowRight className='size-4' />
        </Button>
      </div>
    </SettingsSection>
  )
}
