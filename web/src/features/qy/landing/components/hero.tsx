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
import { readQyGroupContact } from '@/features/qy/lib/group-contact'
import { useStatus } from '@/hooks/use-status'

import { QyLandingGroupQrCard } from './group-qr-card'
import { QyLandingReveal } from './reveal'
import { QyLandingSignalBoard } from './signal-board'

/**
 * 首屏。
 *
 * 与上游首屏的三处结构差异（不是配色差异）：
 *   1. 画布是**星野 + 一处辉光**（`.qy-lp-sky`），不是三层蓝紫 radial；
 *   2. 右侧是**信号板**（本站真实模型目录），不是 curl 示意图；
 *   3. 统计数字**并进首屏收尾的读数行**，不再单独占一条上下描边的横带 ——
 *      那条横带是上游首页最容易被一眼认出来的零件。
 */
export function QyLandingHero(props: { isAuthenticated: boolean }) {
  const { t } = useTranslation()
  const { status } = useStatus()
  // `docs_link` 在 SystemStatus 上是宽松类型，与上游首页取同一个窄化写法。
  const docsUrl =
    (status?.docs_link as string | undefined) || 'https://docs.newapi.pro'
  const docsIsExternal = docsUrl.startsWith('http')
  // 后台配了入群链接或群号就把右侧那一格换成群聊二维码卡，两项皆空（或值非法）
  // 时返回 null，回落到模型信号板 —— 全新装的站不会开天窗。这里读的是同一条
  // ['status'] 缓存，不新起请求。
  const groupContact = readQyGroupContact(status)

  const readout = [
    { value: '50+', label: t('upstream services integrated') },
    { value: '100+', label: t('model billing support') },
    { value: '50+', label: t('compatible API routes') },
    { value: '10+', label: t('scheduling controls') },
  ]

  return (
    <section className='qy-lp-sky relative overflow-hidden px-6 pt-28 pb-16 md:pt-36 md:pb-24'>
      <div className='mx-auto grid max-w-6xl grid-cols-1 items-center gap-12 lg:grid-cols-12 lg:gap-14'>
        <div className='flex flex-col items-start lg:col-span-7'>
          <QyLandingReveal className='qy-lp-micro qy-lp-code flex items-center gap-3'>
            <span>{t('qy_home_stamp_station')}</span>
            <span aria-hidden className='qy-lp-route qy-lp-stamp-route w-16' />
            <span>{t('qy_home_stamp_gate')}</span>
          </QyLandingReveal>

          <QyLandingReveal
            delay={60}
            className='qy-lp-chip mt-6 inline-flex items-center gap-2 px-3.5 py-1.5'
          >
            <span aria-hidden className='qy-lp-dot' />
            <span className='qy-lp-label'>{t('qy_home_badge')}</span>
          </QyLandingReveal>

          <QyLandingReveal delay={120}>
            <h1 className='qy-lp-display qy-lp-hero-title mt-6'>
              {t('qy_home_title_1')}
              <br />
              <span className='qy-lp-accent'>{t('qy_home_title_2')}</span>
            </h1>
          </QyLandingReveal>

          <QyLandingReveal delay={180}>
            <p className='text-muted-foreground mt-6 max-w-xl text-[15px] leading-relaxed'>
              {t('qy_home_lede')}
            </p>
          </QyLandingReveal>

          <QyLandingReveal
            delay={240}
            className='mt-9 flex flex-wrap items-center gap-3'
          >
            {props.isAuthenticated ? (
              <Button
                className='group h-11 px-5 text-sm'
                render={<Link to='/dashboard' />}
              >
                {t('Go to Dashboard')}
                <ArrowRight className='ml-1.5 size-4 transition-transform duration-200 group-hover:translate-x-0.5' />
              </Button>
            ) : (
              <Button
                className='group h-11 px-5 text-sm'
                render={<Link to='/sign-up' />}
              >
                {t('Get Started')}
                <ArrowRight className='ml-1.5 size-4 transition-transform duration-200 group-hover:translate-x-0.5' />
              </Button>
            )}
            <Button
              variant='outline'
              className='h-11 px-5 text-sm'
              render={<Link to='/pricing' />}
            >
              {t('View Pricing')}
            </Button>
            <Button
              variant='ghost'
              className='h-11 px-5 text-sm'
              render={
                docsIsExternal ? (
                  <a href={docsUrl} target='_blank' rel='noopener noreferrer' />
                ) : (
                  <Link to={docsUrl} />
                )
              }
            >
              {t('Docs')}
            </Button>
          </QyLandingReveal>

          <QyLandingReveal
            delay={300}
            className='qy-lp-hr mt-10 grid w-full max-w-xl grid-cols-2 gap-x-8 gap-y-5 pt-7 sm:grid-cols-4 sm:gap-x-4'
          >
            {readout.map((item) => (
              <div key={item.label} className='flex flex-col gap-1'>
                <span className='text-foreground text-xl font-light tabular-nums'>
                  {item.value}
                </span>
                <span className='qy-lp-muted text-xs leading-snug'>
                  {item.label}
                </span>
              </div>
            ))}
          </QyLandingReveal>
        </div>

        <QyLandingReveal delay={360} className='w-full lg:col-span-5'>
          {groupContact ? (
            <QyLandingGroupQrCard
              joinUrl={groupContact.joinUrl}
              number={groupContact.number}
            />
          ) : (
            <QyLandingSignalBoard />
          )}
        </QyLandingReveal>
      </div>
    </section>
  )
}
