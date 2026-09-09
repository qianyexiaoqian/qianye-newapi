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
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import { getPricing } from '@/features/pricing/api'
import { cn } from '@/lib/utils'

import { QY_LANDING_BOARD_ROWS, QY_LANDING_FAMILIES } from '../constants'
import { pickBoardModels } from '../lib/board-models'

/**
 * 首屏右侧的信号板。
 *
 * 上游首页这个位置是一段 curl 演示（示意图，与本站卖什么无关）。这里换成参考稿
 * 的航班信息板：**无外框、无斑马纹、无单元格盒，只有横发丝线**（design-14 §2③），
 * 装的是本站模型目录里真实存在的模型名。
 *
 * queryKey 与定价页共用 `['pricing']` —— 用户从首页点进定价页时不会再请求一次。
 * 取不到（未开放游客预览 / 目录为空 / 请求失败）时落到三支主推族，板子不会空着，
 * 也不会编造站点没上架的模型名。
 */
export function QyLandingSignalBoard(props: { className?: string }) {
  const { t } = useTranslation()
  const { data } = useQuery({
    queryKey: ['pricing'],
    queryFn: getPricing,
    staleTime: 5 * 60 * 1000,
    retry: false,
  })

  const live = pickBoardModels(data?.data, QY_LANDING_BOARD_ROWS)
  const rows =
    live.length > 0 ? live : QY_LANDING_FAMILIES.map((family) => family.label)

  return (
    <div className={cn('qy-lp-panel overflow-hidden', props.className)}>
      <div className='qy-lp-panel-head flex items-center justify-between gap-3 px-5 py-3.5 sm:px-6'>
        <span className='qy-lp-micro qy-lp-muted'>
          {t('qy_home_board_title')}
        </span>
        <span className='qy-lp-micro qy-lp-code qy-lp-mist'>
          {t('qy_home_board_official')}
        </span>
      </div>

      <ul className='px-5 py-1 sm:px-6'>
        {rows.map((name, index) => (
          <li key={name} className='qy-lp-board-row'>
            <span className='qy-lp-code qy-lp-faint'>
              {String(index + 1).padStart(2, '0')}
            </span>
            <span className='qy-lp-code text-foreground truncate'>{name}</span>
            <span className='text-muted-foreground flex items-center gap-2 whitespace-nowrap'>
              <span aria-hidden className='qy-lp-dot' />
              {t('qy_home_board_relay')}
            </span>
          </li>
        ))}
      </ul>

      <p className='qy-lp-readout qy-lp-panel-foot px-5 py-3.5 sm:px-6'>
        {live.length > 0
          ? t('qy_home_board_caption_live')
          : t('qy_home_board_caption_fallback')}
      </p>
    </div>
  )
}
