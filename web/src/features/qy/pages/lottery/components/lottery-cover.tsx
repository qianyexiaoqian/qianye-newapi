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
import { CircleDot, Dices, Target } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { cn } from '@/lib/utils'

import {
  qyLotCoverKind,
  qyLotCoverSrc,
  type QyLotCoverSource,
} from '../lib/cover'

/**
 * 活动卡片的背景图，**带兜底**。
 *
 * ## 三种状态，一个都不能省
 *
 *   有封面且加载成功  → 图
 *   没配封面          → 兜底图案
 *   配了但加载失败    → 兜底图案（`onError` 切过去）
 *
 * 第三种是这个组件存在的主要理由。封面的两种来源里，外链是管理员填的第三方
 * 地址：图床跑路、防盗链、https 页面里的 http 图被浏览器拦掉 —— 每一种都会在
 * 某天发生，而默认表现是一个碎裂的破图图标压在卡片顶部。上传的那种也不是永远
 * 安全：多节点部署时 A 节点收到的上传 B 节点取不到（这条限制写在后端配置注释里）。
 * 没有 `onError` 的话，"大厅第一屏全是破图"是一个必然到来的日子。
 *
 * ## 兜底为什么不是一块灰色方块
 *
 * 空白块与"图还在加载"长得一模一样，用户会一直等。所以兜底画的是**这场活动
 * 的玩法图标 + 一层与主题一致的渐变** —— 它是一个明确的终态，而且顺带把
 * "这是抽奖还是竞猜还是双色球"再说一遍。
 */
export function QyLotCover(props: {
  activity: QyLotCoverSource & { kind?: string; draw_mode?: string }
  /** 卡片头部用 `banner`（16:9 的窄条），详情页头图用 `hero`。 */
  variant?: 'banner' | 'hero'
  className?: string
}) {
  const { activity } = props
  const { t } = useTranslation()
  const src = qyLotCoverSrc(activity)
  const isLink = qyLotCoverKind(activity) === 'link'

  // 失败过的那个地址记下来：`src` 一变（管理员换了图）就重新试一次，
  // 否则换完图仍然只能看到兜底，而运营会以为新图也没生效。
  const [failedSrc, setFailedSrc] = useState<string | null>(null)
  useEffect(() => {
    setFailedSrc(null)
  }, [src])

  const shape =
    props.variant === 'hero' ? 'aspect-[3/1] rounded-lg' : 'aspect-[16/6]'
  const box = cn('bg-muted relative w-full overflow-hidden', shape, props.className)

  if (src == null || failedSrc === src) {
    const Icon =
      activity.draw_mode === 'ball'
        ? CircleDot
        : activity.kind === 'guess'
          ? Target
          : Dices
    return (
      <div
        className={cn(
          box,
          'from-muted via-muted/60 to-muted flex items-center justify-center bg-gradient-to-br'
        )}
        // 纯装饰：它不携带任何信息，读屏软件念一句"抽奖图标"只是噪音。
        aria-hidden='true'
      >
        <Icon className='text-muted-foreground/40 size-8' />
      </div>
    )
  }

  return (
    <div className={box}>
      <img
        src={src}
        alt={t('qy_lot_cover_alt')}
        loading='lazy'
        // 外链指向管理员随手填的第三方主机。不加这一条的话，每一位打开大厅的
        // 访客都会把本站地址(含路径)送给那台机器 —— 一个纯粹白送的信息泄漏。
        referrerPolicy={isLink ? 'no-referrer' : undefined}
        className='size-full object-cover'
        onError={() => setFailedSrc(src)}
      />
    </div>
  )
}
