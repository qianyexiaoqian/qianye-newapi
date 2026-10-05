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
import { Megaphone } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Alert, AlertDescription } from '@/components/ui/alert'
import { useStatus } from '@/hooks/use-status'

import { qyViolationNotices } from '../../../lib/violation-notice'

/**
 * 「我的违规记录」页顶部的横幅公告(最多三条,超级管理员在系统设置里填写)。
 *
 * 一条都没配时整块不渲染。正文按纯文本展示、保留换行:不走 Markdown / HTML,
 * 这样运营写什么用户就看到什么,也不存在被注入链接或脚本的可能。
 */
export function QyViolationNoticeBanners() {
  const { t } = useTranslation()
  const { status } = useStatus()
  const notices = qyViolationNotices(status as Record<string, unknown> | null)

  if (notices.length === 0) return null

  return (
    <section aria-label={t('qy_vio_notice_region')} className='space-y-2'>
      {notices.map((notice, index) => (
        <Alert key={`${index}-${notice}`}>
          <Megaphone className='h-4 w-4' aria-hidden='true' />
          <AlertDescription className='break-words whitespace-pre-wrap'>
            {notice}
          </AlertDescription>
        </Alert>
      ))}
    </section>
  )
}
