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

import { Badge } from '@/components/ui/badge'
import { Progress } from '@/components/ui/progress'

import type { QyMyViolationCategories } from '../types'

/**
 * 违规类型公示卡片。
 *
 * ── 它公示什么 ──
 * 有哪些违规类型、每一类累计多少次会被处置、以及**自己当前每一类的计数**。
 * 加上账号总量线那一条，用户就能自己回答"我还剩几次"。
 *
 * ── 它绝不公示什么 ──
 * 类型的内部名与内部说明。后端的 `userCategoryView` 白名单根本不下发它们
 * （内部说明写的就是匹配判据，公示它等于把绕过方法印给用户），所以这里也
 * **没有任何字段可以渲染它们** —— 前端不要试图从别的接口把这些补回来。
 *
 * ── 为什么两条线都要摆出来 ──
 * 用户会撞的线有两条，任一越过即触发处置。只显示其中一条会让"到底几次"在另一
 * 条线上失真，而失真的方向是"我以为还剩 5 次，结果第 3 次就被限制了"——
 * 那比不给数字更糟。
 */
export function QyMyViolationCategoriesCard(props: {
  data: QyMyViolationCategories | undefined
}) {
  const { t } = useTranslation()
  const data = props.data
  // 站点一个类型都没公示时整块收起：一张空卡片只会让人以为页面坏了。
  if (data == null || data.items.length === 0) return null

  return (
    <section className='space-y-3 rounded-lg border p-4'>
      <div className='space-y-1'>
        <h2 className='text-sm font-medium'>{t('qy_vio_cat_title')}</h2>
        <p className='text-muted-foreground text-xs'>
          {t('qy_vio_cat_desc')}
        </p>
      </div>

      {/* 账号总量线。它跨全部类型，与下面每一类各自的线是并列关系。 */}
      <div className='bg-muted/40 rounded-md px-3 py-2 text-xs'>
        {data.account_threshold > 0
          ? t('qy_vio_cat_account_line', {
              hit: data.account_hit_count,
              threshold: data.account_threshold,
              hours: data.account_window_hours,
            })
          : t('qy_vio_cat_account_line_off', {
              hit: data.account_hit_count,
              hours: data.account_window_hours,
            })}
      </div>

      <ul className='space-y-3'>
        {data.items.map((item) => {
          const progress =
            item.threshold > 0
              ? Math.min(100, (item.hit_count / item.threshold) * 100)
              : 0
          return (
            <li key={item.id} className='space-y-1'>
              <div className='flex flex-wrap items-center justify-between gap-2'>
                <span className='flex flex-wrap items-center gap-2 text-sm'>
                  {item.title}
                  {item.threshold > 0 ? (
                    <Badge variant='outline'>
                      {t('qy_vio_cat_threshold', {
                        count: item.threshold,
                        hours: item.window_hours,
                      })}
                    </Badge>
                  ) : (
                    <Badge variant='outline'>
                      {t('qy_vio_cat_threshold_off')}
                    </Badge>
                  )}
                </span>
                <span
                  className={
                    // 剩余 1 次时标红：这是用户主动收敛行为的最后提醒，
                    // 与页面顶部的账号总量线同口径。
                    item.threshold > 0 && item.remaining <= 1
                      ? 'text-destructive text-xs'
                      : 'text-muted-foreground text-xs'
                  }
                >
                  {item.threshold > 0
                    ? t('qy_vio_cat_progress', {
                        hit: item.hit_count,
                        threshold: item.threshold,
                        remaining: item.remaining,
                      })
                    : t('qy_vio_cat_progress_off', { hit: item.hit_count })}
                </span>
              </div>
              {item.description !== '' && (
                <p className='text-muted-foreground text-xs'>
                  {item.description}
                </p>
              )}
              {item.threshold > 0 && (
                <Progress value={progress} aria-label={item.title} />
              )}
            </li>
          )
        })}
      </ul>

      <p className='text-muted-foreground text-xs'>
        {t('qy_vio_cat_any_line_note')}
      </p>
    </section>
  )
}
