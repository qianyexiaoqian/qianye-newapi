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
import { LifeBuoy, Lock, ShieldAlert } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'

import { useQyConfig } from '../hooks/use-qy-config'

/**
 * 受限账号的两块说明：常驻横幅 + 落地页。
 *
 * 放在同一个文件里，因为它们说的是**同一件事**，只是详略不同：横幅是"你现在
 * 的处境"，落地页是"你刚才点的那个地方为什么打不开、该去哪"。拆成两个文件的
 * 直接后果是两段文案各自演化，用户在两处读到不一样的规则。
 *
 * 文案必须具体到"哪些事不能做"。"您的账号异常"这种说法会把每一个受限用户都
 * 变成一张工单，而工单正是他此刻唯一能用的东西。
 */

/** 工单入口是否真的存在（扩展开着且 ticket 功能开着）。 */
function useQyTicketEntryAvailable(): boolean {
  const config = useQyConfig()
  return config.enabled && config.features.ticket
}

/**
 * 常驻横幅。**不可关闭**：受限是一个持续状态，不是一次性通知；给它一个关闭
 * 按钮等于让用户把自己唯一的解释关掉，然后在别处反复撞 403。
 */
export function QyRestrictedBanner() {
  const { t } = useTranslation()
  const hasTickets = useQyTicketEntryAvailable()

  return (
    <div className='px-4 pt-4'>
      <Alert variant='destructive'>
        <Lock />
        <AlertTitle>{t('qy_restricted_banner_title')}</AlertTitle>
        <AlertDescription>
          {t('qy_restricted_blocked')}
          <br />
          {hasTickets
            ? t('qy_restricted_appeal_hint')
            : t('qy_restricted_no_channel')}
        </AlertDescription>
      </Alert>
    </div>
  )
}

/**
 * 落地页：受限账号直接输 URL 到白名单之外时看到的东西。
 *
 * 刻意**不是** 403 报错页，也不是空白：用户没有做错什么，他只是点到了一个
 * 现在对他关闭的地方。所以这里给的是"发生了什么 + 现在能做什么"，
 * 并且把仅有的那两个出口摆在按钮上。
 */
export function QyRestrictedLanding() {
  const { t } = useTranslation()
  const config = useQyConfig()
  const hasTickets = config.enabled && config.features.ticket
  const hasViolations = config.enabled && config.features.violation

  return (
    <div className='flex min-h-0 flex-1 items-center justify-center overflow-auto p-6'>
      <div className='w-full max-w-xl space-y-5 text-center'>
        <div className='bg-muted text-muted-foreground mx-auto flex size-12 items-center justify-center rounded-full'>
          <Lock className='size-6' />
        </div>

        <div className='space-y-2'>
          <h1 className='text-xl font-semibold'>
            {t('qy_restricted_landing_title')}
          </h1>
          <p className='text-muted-foreground text-sm'>
            {t('qy_restricted_landing_desc')}
          </p>
        </div>

        {/*
          这里只说横幅**没有**说过的那件事 ——「你的东西还在」。
          「哪些事不能做」与「怎么申诉」由常驻横幅承担,而横幅与落地页永远同时
          在场(见 QyRestrictedGate),把它们再抄一遍的结果是同一屏上同样两段话
          出现两次,真正的增量信息反而被埋掉。已在浏览器里实测过那个形状。
        */}
        <div className='text-muted-foreground space-y-2 rounded-lg border p-4 text-start text-sm'>
          <p>{t('qy_restricted_kept')}</p>
        </div>

        <div className='flex flex-wrap items-center justify-center gap-3'>
          {hasTickets && (
            <Button render={<Link to='/qy/tickets' />}>
              <LifeBuoy className='size-4' />
              {t('qy_restricted_go_tickets')}
            </Button>
          )}
          {hasViolations && (
            <Button variant='outline' render={<Link to='/qy/violations' />}>
              <ShieldAlert className='size-4' />
              {t('qy_restricted_go_violations')}
            </Button>
          )}
        </div>
      </div>
    </div>
  )
}
