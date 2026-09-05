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
 * 「API信息」在「内容管理」里留下的**路牌**，形状与 `qy-restricted-notice-moved`
 * 逐字相同，理由见那个文件：section id 一旦删掉，旧深链接会被 `$section` 路由
 * 静默重定向回默认段，管理员只会以为自己记错了地址。
 *
 * ── 为什么整块表单搬去了 qy「API 地址」 ──
 *
 * 站内一度有**两张** API 地址表：这里的 `console_setting.api_info`（控制台
 * 「API信息」卡片的数据源，对所有访客一视同仁）和 qy 地址簿（密钥页「复制链接
 * 信息」的数据源，按用户分组过滤）。同一个概念的两份拷贝意味着运营要配两遍、
 * 而且只有其中一份认得分组。并表后控制台卡片改读地址簿（`api-info-panel.tsx`），
 * 颜色字段随之搬进地址簿，这里一个输入控件都不剩 —— 不是"改了不生效的孤儿
 * 表单"，是表单整体走了。
 */
export function QyApiInfoMovedSection() {
  const { t } = useTranslation()
  return (
    <SettingsSection title={t('API Addresses')}>
      <p className='text-muted-foreground text-sm'>{t('qy_aa_moved_hint')}</p>
      <div>
        <Button variant='outline' render={<Link to='/qy/admin/api-address' />}>
          {t('qy_aa_moved_link')}
          <ArrowRight className='size-4' />
        </Button>
      </div>
    </SettingsSection>
  )
}
