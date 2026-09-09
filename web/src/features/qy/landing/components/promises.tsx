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

import { QyLandingReveal } from './reveal'

/**
 * 核心承诺。
 *
 * 上游这一段是 bento 宫格（2+1+1+2 的方块拼版）——「和原项目雷同」最集中的一处。
 * 这里换成参考稿的 Feature Row：**the card IS the row**，没有卡片、没有斑马纹，
 * 只有编号 + 一条上发丝线，hover 时整行右移。与后台工作区索引是同一套构图，
 * 落地页与产品内部因此读起来是同一个站。
 *
 * 七条文案的口径来自项目方，逐条对应一个真实承诺（号池、真伪、定价、补号、
 * 发票、客群、社群），**不是**通用 SaaS 的「快 / 稳 / 省」三件套。
 */
export function QyLandingPromises() {
  const { t } = useTranslation()

  const promises = [
    {
      title: t('qy_home_promise_pure_title'),
      desc: t('qy_home_promise_pure_desc'),
    },
    {
      title: t('qy_home_promise_genuine_title'),
      desc: t('qy_home_promise_genuine_desc'),
    },
    {
      title: t('qy_home_promise_price_title'),
      desc: t('qy_home_promise_price_desc'),
    },
    {
      title: t('qy_home_promise_resupply_title'),
      desc: t('qy_home_promise_resupply_desc'),
    },
    {
      title: t('qy_home_promise_invoice_title'),
      desc: t('qy_home_promise_invoice_desc'),
    },
    {
      title: t('qy_home_promise_clean_title'),
      desc: t('qy_home_promise_clean_desc'),
    },
    {
      title: t('qy_home_promise_group_title'),
      desc: t('qy_home_promise_group_desc'),
    },
  ]

  return (
    <section className='qy-lp-section px-6'>
      <div className='mx-auto max-w-6xl'>
        <QyLandingReveal className='mb-12 max-w-2xl md:mb-16'>
          <p className='qy-lp-label'>{t('qy_home_sec_promise_label')}</p>
          <h2 className='qy-lp-display mt-4 text-[clamp(1.75rem,3.4vw,2.75rem)]'>
            {t('qy_home_sec_promise_title')}
          </h2>
        </QyLandingReveal>

        <ul className='grid grid-cols-1 gap-x-14 md:grid-cols-2'>
          {promises.map((promise, index) => (
            <QyLandingReveal
              as='li'
              key={promise.title}
              delay={index * 60}
              className='qy-lp-row'
            >
              <span className='qy-lp-idx'>
                / {String(index + 1).padStart(2, '0')}
              </span>
              <div>
                <h3 className='text-foreground text-lg font-normal'>
                  {promise.title}
                </h3>
                <p className='text-muted-foreground mt-2 text-sm leading-relaxed'>
                  {promise.desc}
                </p>
              </div>
            </QyLandingReveal>
          ))}
        </ul>
      </div>
    </section>
  )
}
