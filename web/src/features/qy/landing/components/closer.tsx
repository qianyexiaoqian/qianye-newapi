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
import { useStatus } from '@/hooks/use-status'

import { QyLandingReveal } from './reveal'

/**
 * 收尾。
 *
 * 参考稿的 coordinate stamp footer：一行 `+` 起头的等宽小字给整页签名
 * （design-14 §2⑥）。站名取后台配置的 `system_name`，读不到时不硬编码一个
 * 名字，只留代号 —— 这一页是任何人 fork 下来都会看到的第一屏。
 *
 * 已登录用户看到的是同一段收尾但换成「回控制台」：上游在这里整段 `return null`，
 * 于是登录用户的首页最后一屏是半截空白加页脚。
 */
export function QyLandingCloser(props: { isAuthenticated: boolean }) {
  const { t } = useTranslation()
  const { status } = useStatus()
  const stamp = [t('qy_home_closer_stamp'), status?.system_name]
    .filter(Boolean)
    .join(' · ')

  return (
    <section className='qy-lp-section qy-lp-hr px-6'>
      <div className='mx-auto flex max-w-3xl flex-col items-center text-center'>
        <QyLandingReveal>
          <h2 className='qy-lp-display text-[clamp(1.75rem,3.8vw,3rem)]'>
            {t('qy_home_closer_title_1')}
            <br />
            <span className='qy-lp-accent'>{t('qy_home_closer_title_2')}</span>
          </h2>
        </QyLandingReveal>

        <QyLandingReveal delay={80}>
          <p className='text-muted-foreground mt-6 max-w-lg text-sm leading-relaxed'>
            {t('qy_home_closer_lede')}
          </p>
        </QyLandingReveal>

        <QyLandingReveal
          delay={160}
          className='mt-9 flex flex-wrap items-center justify-center gap-3'
        >
          <Button
            className='group h-11 px-5 text-sm'
            render={
              <Link to={props.isAuthenticated ? '/dashboard' : '/sign-up'} />
            }
          >
            {props.isAuthenticated ? t('Go to Dashboard') : t('Get Started')}
            <ArrowRight className='ml-1.5 size-4 transition-transform duration-200 group-hover:translate-x-0.5' />
          </Button>
          <Button
            variant='outline'
            className='h-11 px-5 text-sm'
            render={<Link to='/pricing' />}
          >
            {t('View Pricing')}
          </Button>
        </QyLandingReveal>

        <QyLandingReveal delay={240} className='mt-14 w-full'>
          <span aria-hidden className='qy-lp-route block' />
          <p className='qy-lp-readout mt-6'>{stamp}</p>
        </QyLandingReveal>
      </div>
    </section>
  )
}
