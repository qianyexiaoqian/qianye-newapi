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
import { useTranslation } from 'react-i18next'

import { QY_LANDING_APPS } from '../constants'
import { QyLandingReveal } from './reveal'

/**
 * 开箱即用的客户端。
 *
 * 上游把这一条塞在首屏左栏最下面，首屏因此挤成四段。这里单独成一条窄带：
 * 首屏只留「一句话 + 一个动作 + 一块板」，客户端条往下挪一屏。
 *
 * 图标用字母占位而不是各家的 favicon —— 上游那颗 `ccswitch.io/favicon.png`
 * 在没有外网出口的部署里要走完一整轮超时才落到 onError 兜底，那期间首屏一直缺块。
 */
export function QyLandingApps() {
  const { t } = useTranslation()

  return (
    <section className='qy-lp-section qy-lp-hr px-6'>
      <div className='mx-auto flex max-w-6xl flex-col gap-8 lg:flex-row lg:items-center lg:justify-between lg:gap-16'>
        <QyLandingReveal className='max-w-md'>
          <p className='qy-lp-label'>{t('qy_home_sec_apps_label')}</p>
          <h2 className='qy-lp-display mt-4 text-[clamp(1.375rem,2.4vw,1.875rem)]'>
            {t('qy_home_sec_apps_title')}
          </h2>
          <p className='text-muted-foreground mt-3 text-sm leading-relaxed'>
            {t('qy_home_apps_note')}
          </p>
        </QyLandingReveal>

        <QyLandingReveal
          delay={120}
          className='flex flex-wrap items-center gap-3'
        >
          {QY_LANDING_APPS.map((app) => (
            <a
              key={app.name}
              href={app.href}
              target='_blank'
              rel='noopener noreferrer'
              className='qy-lp-chip text-foreground/80 hover:text-foreground flex items-center gap-2.5 px-4 py-2.5 text-sm'
            >
              <span
                aria-hidden
                className='qy-lp-micro qy-lp-code qy-lp-mist flex size-6 shrink-0 items-center justify-center'
              >
                {app.short}
              </span>
              {app.name}
            </a>
          ))}
        </QyLandingReveal>
      </div>
    </section>
  )
}
