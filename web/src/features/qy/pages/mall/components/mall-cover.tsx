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
import { Crown, Package, Ticket } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { cn } from '@/lib/utils'

import type { QyMallProduct } from '../types'

/** 没配封面时画的兜底图标：一眼分得出套餐 / 兑换码 / 实物。 */
const KIND_ICON: Partial<Record<string, typeof Package>> = {
  plan: Crown,
  code: Ticket,
  physical: Package,
}

/**
 * 商品封面，**带兜底**（形状照 `pages/lottery/components/lottery-cover.tsx`）。
 *
 * 与抽奖封面的一处不同：后端已经把 `cover_ref` 解析成了可直接 `<img src>` 的
 * 站内地址（`/api/qy/mall/covers/:ref`，匿名端点，只回已绑到商品的那些），前端
 * 拿到的只有 `cover_url` 一个字段，没有第二种来源。这里仍然只放行站内 `/api/`
 * 路径与 http(s)：这一列可以被手工改坏，而 `<img src>` 之外这串字符串日后
 * 还可能被放进 `<a href>`。
 *
 * 三种状态一个都不能省：有图且加载成功 → 图；没配 → 兜底图标；配了但加载
 * 失败（多节点部署时 A 节点收到的上传 B 节点取不到）→ 兜底图标。空白块与
 * "还在加载"长得一样，用户会一直等。
 */
export function QyMallCover(props: {
  product: Pick<QyMallProduct, 'cover_url' | 'kind'>
  /** 卡片顶部用 `banner`（16:6 的窄条），详情弹窗头图用 `hero`。 */
  variant?: 'banner' | 'hero'
  className?: string
}) {
  const { t } = useTranslation()
  const url = props.product.cover_url.trim()
  const src = url.startsWith('/api/') || /^https?:\/\//i.test(url) ? url : null

  // 失败过的那个地址记下来：`src` 一变（运营换了图）就重新试一次。
  const [failedSrc, setFailedSrc] = useState<string | null>(null)
  useEffect(() => {
    setFailedSrc(null)
  }, [src])
  // 兜底插画按商品类型选(/qy/art/{plan,code,physical}.jpg);取不到再退到图标。
  const [artFailed, setArtFailed] = useState(false)

  const shape =
    props.variant === 'hero' ? 'aspect-[3/1] rounded-lg' : 'aspect-[16/6]'
  const box = cn(
    'bg-muted relative w-full overflow-hidden',
    shape,
    props.className
  )

  if (src == null || failedSrc === src) {
    const Icon = KIND_ICON[props.product.kind] ?? Package
    return (
      <div
        className={cn(
          box,
          'from-muted via-muted/60 to-muted flex items-center justify-center bg-gradient-to-br'
        )}
        // 纯装饰，读屏念一句"礼包图标"只是噪音。
        aria-hidden='true'
      >
        {artFailed ? (
          <Icon className='text-muted-foreground/40 size-8' />
        ) : (
          <img
            src={`/qy/art/${props.product.kind}.jpg`}
            alt=''
            loading='lazy'
            className='size-full object-cover'
            onError={() => setArtFailed(true)}
          />
        )}
      </div>
    )
  }

  return (
    <div className={box}>
      <img
        src={src}
        alt={t('qy_ml_cover_alt')}
        loading='lazy'
        className='size-full object-cover'
        onError={() => setFailedSrc(src)}
      />
    </div>
  )
}
