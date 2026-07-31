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
import { Info } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Popover,
  PopoverContent,
  PopoverHeader,
  PopoverTitle,
  PopoverTrigger,
} from '@/components/ui/popover'

import { qyAvailOutcomeKey } from '../constants'
import type { QyAvailDefinition } from '../types'

/**
 * 口径说明浮层。
 *
 * 可用率是最容易引发争议的指标：页面必须能当场回答「99.4% 是怎么算出来的」。
 * 分子 / 分母 / 排除项全部由后端随查询下发（`definition` 字段），前端只渲染，
 * **不在两侧各写一遍规则** —— 否则改一次开关就会出现两套说法。
 */
export function QyAvailabilityDefinition(props: {
  definition: QyAvailDefinition
}) {
  const { t } = useTranslation()

  return (
    <Popover>
      <PopoverTrigger
        render={
          <Button type='button' variant='outline' size='sm'>
            <Info aria-hidden='true' />
            {t('qy_avl_definition_title')}
          </Button>
        }
      />
      <PopoverContent
        align='end'
        side='bottom'
        sideOffset={8}
        className='w-[min(26rem,calc(100vw-2rem))] gap-3 p-3'
      >
        <PopoverHeader className='gap-1'>
          <PopoverTitle>{t('qy_avl_definition_title')}</PopoverTitle>
        </PopoverHeader>

        <div className='space-y-3 text-xs'>
          <div>
            <p className='text-muted-foreground mb-1'>
              {t('qy_avl_definition_numerator')}
            </p>
            <Badge variant='outline'>
              {t(qyAvailOutcomeKey(props.definition.numerator))}
            </Badge>
          </div>

          <div>
            <p className='text-muted-foreground mb-1'>
              {t('qy_avl_definition_denominator')}
            </p>
            <div className='flex flex-wrap gap-1'>
              {props.definition.denominator.map((item) => (
                <Badge key={item} variant='outline'>
                  {t(qyAvailOutcomeKey(item))}
                </Badge>
              ))}
            </div>
          </div>

          <div>
            <p className='text-muted-foreground mb-1'>
              {t('qy_avl_definition_excluded')}
            </p>
            <div className='flex flex-wrap gap-1'>
              {props.definition.excluded.map((item) => (
                <Badge key={item} variant='secondary'>
                  {t(qyAvailOutcomeKey(item))}
                </Badge>
              ))}
            </div>
          </div>

          <div className='text-muted-foreground space-y-1 border-t pt-2 leading-relaxed'>
            <p>
              {t('qy_avl_definition_thresholds', {
                ok: props.definition.ok_threshold,
                degraded: props.definition.degraded_threshold,
                min: props.definition.min_samples,
              })}
            </p>
            {/* 性能维度自己的样本下限：可用率有 1000 条样本不代表首字延迟也有，
                不写清楚，用户看到延迟列一片横杠只会以为页面坏了。 */}
            <p>
              {t('qy_avl_definition_perf', {
                min: props.definition.perf_min_samples,
              })}
            </p>
            {/* 覆盖面的局限必须在 UI 上声明：异步任务路径不产生样本，
                那些模型显示「无数据」而不是 0%，不写清楚就会被当成故障。 */}
            <p>{props.definition.note}</p>
            <p>{t('qy_avl_definition_coverage')}</p>
          </div>
        </div>
      </PopoverContent>
    </Popover>
  )
}
