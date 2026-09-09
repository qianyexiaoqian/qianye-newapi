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

import { QY_LANDING_FAMILIES } from '../constants'
import { QyLandingReveal } from './reveal'

/**
 * 模型阵容。
 *
 * 上游这一段是「三步上手」的圆角图标 + 序号徽章（每个 SaaS 首页都有的那一套）。
 * 这里改成三张**票面**（19.2px 圆角 + 发丝线 + 洗色底，零投影），族名走等宽轴的
 * 展示档 —— 纯拉丁字串，是 design-14 §2① 允许用等宽轴的少数位置之一。
 *
 * 族名单来自 `QY_LANDING_FAMILIES`，与信号板的排序权重共用一份，
 * 免得「首页说主推三家、板子上却排着别的」。
 */
export function QyLandingLineup() {
  const { t } = useTranslation()

  const descriptions: Record<string, string> = {
    claude: t('qy_home_lineup_claude_desc'),
    gemini: t('qy_home_lineup_gemini_desc'),
    gpt: t('qy_home_lineup_gpt_desc'),
  }

  return (
    <section className='qy-lp-section qy-lp-hr px-6'>
      <div className='mx-auto max-w-6xl'>
        <QyLandingReveal className='mb-12 flex flex-col gap-4 md:mb-16 md:flex-row md:items-end md:justify-between'>
          <div className='max-w-2xl'>
            <p className='qy-lp-label'>{t('qy_home_sec_lineup_label')}</p>
            <h2 className='qy-lp-display mt-4 text-[clamp(1.75rem,3.4vw,2.75rem)]'>
              {t('qy_home_sec_lineup_title')}
            </h2>
          </div>
          <Link
            to='/pricing'
            className='qy-lp-label qy-lp-mist group inline-flex shrink-0 items-center gap-1.5'
          >
            {t('qy_home_lineup_all')}
            <ArrowRight
              aria-hidden
              className='size-3.5 transition-transform duration-200 group-hover:translate-x-0.5'
            />
          </Link>
        </QyLandingReveal>

        <div className='grid grid-cols-1 gap-5 md:grid-cols-3'>
          {QY_LANDING_FAMILIES.map((family, index) => (
            <QyLandingReveal
              key={family.id}
              delay={index * 90}
              className='qy-lp-panel flex flex-col gap-6 p-7'
            >
              <span className='qy-lp-micro qy-lp-code'>
                {String(index + 1).padStart(2, '0')}
              </span>
              <span className='qy-lp-code text-foreground text-2xl font-light tracking-tight'>
                {family.label}
              </span>
              <p className='text-muted-foreground text-sm leading-relaxed'>
                {descriptions[family.id]}
              </p>
              <span aria-hidden className='qy-lp-route mt-auto' />
            </QyLandingReveal>
          ))}
        </div>
      </div>
    </section>
  )
}
