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
import { useEffect, useId, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { cn } from '@/lib/utils'

import {
  QY_LOT_PPM_DEN,
  qyLotTiers,
  type QyLotSpecItem,
} from '../../lottery/types'
import { QyMallKindBadge } from '../../mall/components/mall-kind-badge'
import {
  qyWheelRotationFor,
  qyWheelSectorAt,
  qyWheelSectorColor,
  qyWheelSectors,
  type QyWheelSector,
} from '../lib/spin'

/** 旋转动效的时长（毫秒）。design-15 §11：不超过 2 秒、用 CSS transform。 */
export const QY_WHEEL_SPIN_MS = 2000

const CENTER = 100
const RADIUS = 96

/**
 * 转盘面。
 *
 * ## 主题约束（design-14 §4 / design-15 §11）
 *
 * Midnight Signal 是"一支色相 / 辉光 ≤2 / 零投影"，多色扇区与它正面相撞。让步的
 * 口径写在设计稿里：扇区**只用四支语义色 + 两档图表色**（族外允许项），不加辉光、
 * 不加投影，扇区之间只用一条背景色的发丝线分隔；`prefers-reduced-motion` 下不旋转，
 * 直接把盘面停在结果上。颜色全部走 CSS 变量，这里没有一个十六进制。
 *
 * ## 指针固定、盘面转
 *
 * 指针画在旋转元素之外，盘面（整个 `<svg>`）用 `transform: rotate()` 转到
 * {@link qyWheelRotationFor} 算出的角度。动效结束由一个与过渡时长相同的定时器
 * 宣告（而不是 `transitionend`：标签页在后台时那个事件可能永远不来，而结果文字
 * 必须出现）。缩减动效时没有过渡，`onSettled` 在同一拍内触发。
 *
 * ## 落定之后盘面自己会说话
 *
 * 停下那一拍：指针点一下头（`.qy-fx-pointer-settle`）、落档扇区描一圈前景色、
 * 其余扇区退到半透明（`.qy-fx-sector`）—— 不看文字也知道停在了哪一格。
 * 已发完的档在盘面上打斜纹并压暗：它仍占着公示的那一段区间（摇中它就是
 * "摇中但已发完"），所以不能从盘面上抹掉，但必须一眼看出"这一格已经没货"。
 */
export function QyWheelFace(props: {
  spec: QyLotSpecItem[]
  /** 落定的摇号量；`null` = 静止（还没转 / 只是展示盘面）。 */
  targetPpm: number | null
  reducedMotion: boolean
  /** 盘面停下（结果可以说出来了）。 */
  onSettled?: () => void
  className?: string
}) {
  const { t } = useTranslation()
  const hatchId = useId()
  const sectors = qyWheelSectors(props.spec)
  const [rotation, setRotation] = useState(0)
  const [settled, setSettled] = useState(false)

  const { targetPpm, reducedMotion, onSettled } = props
  useEffect(() => {
    if (targetPpm == null) {
      setRotation(0)
      setSettled(false)
      return
    }
    setRotation(qyWheelRotationFor(targetPpm))
    if (reducedMotion) {
      setSettled(true)
      onSettled?.()
      return
    }
    setSettled(false)
    const timer = setTimeout(() => {
      setSettled(true)
      onSettled?.()
    }, QY_WHEEL_SPIN_MS)
    return () => clearTimeout(timer)
  }, [targetPpm, reducedMotion, onSettled])

  if (sectors.length === 0) return null

  // 各档剩余库存。老后端不下发 stock_left,那时没有一格算"发完"。
  const stockByTier = new Map(
    qyLotTiers(props.spec).map((tier) => [tier.tier, tier.stock_left])
  )
  const soldOut = (sector: QyWheelSector) =>
    !sector.isNone && (stockByTier.get(sector.tier) ?? 1) <= 0

  const landed =
    settled && targetPpm != null
      ? qyWheelSectorAt(sectors, targetPpm)
      : undefined
  const faceLabel =
    landed == null
      ? t('qy_lot_wheel_face_aria')
      : t('qy_lot_wheel_face_landed_aria', { name: landed.name })

  return (
    <div className={cn('flex flex-col items-center gap-3', props.className)}>
      <div
        className='relative w-full max-w-64'
        data-testid='qy-wheel-face'
        data-rotation={Math.round(rotation)}
        data-settled={settled ? 'true' : undefined}
        data-landed-tier={landed?.tier}
      >
        {/* 指针：固定在 12 点钟，不随盘面转。落定那一拍点一下头。 */}
        <svg
          aria-hidden='true'
          viewBox='0 0 20 14'
          key={settled ? 'settled' : 'idle'}
          className={cn(
            'absolute top-0 left-1/2 z-10 w-5 -translate-x-1/2 -translate-y-1/3',
            settled && 'qy-fx-pointer-settle'
          )}
        >
          <polygon points='0,0 20,0 10,14' fill='var(--foreground)' />
        </svg>
        <svg
          role='img'
          aria-label={faceLabel}
          viewBox='0 0 200 200'
          className='block w-full'
          style={{
            transform: `rotate(${rotation}deg)`,
            transition: reducedMotion
              ? 'none'
              : `transform ${QY_WHEEL_SPIN_MS}ms cubic-bezier(0.2, 0.7, 0.1, 1)`,
          }}
        >
          <defs>
            {/* 斜纹用背景色画在扇区色之上:任何主题下都与扇区色对比得出来。 */}
            <pattern
              id={hatchId}
              patternUnits='userSpaceOnUse'
              width='6'
              height='6'
              patternTransform='rotate(45)'
            >
              <line
                x1='0'
                y1='0'
                x2='0'
                y2='6'
                stroke='var(--background)'
                strokeWidth='2.5'
              />
            </pattern>
          </defs>
          {sectors.map((sector) => {
            const isLanded = landed?.tier === sector.tier
            const dim = landed != null && !isLanded
            const exhausted = soldOut(sector)
            return (
              <g
                key={sector.tier}
                className='qy-fx-sector'
                style={{ opacity: dim ? 0.4 : 1 }}
                data-sold-out={exhausted ? 'true' : undefined}
              >
                <path
                  d={sectorPath(sector)}
                  fill={qyWheelSectorColor(sector.colorIndex)}
                  fillOpacity={exhausted ? 0.45 : 1}
                  stroke='var(--background)'
                  strokeWidth='1'
                />
                {exhausted && (
                  <path d={sectorPath(sector)} fill={`url(#${hatchId})`} />
                )}
                {isLanded && (
                  <path
                    d={sectorPath(sector)}
                    fill='none'
                    stroke='var(--foreground)'
                    strokeWidth='2.5'
                    strokeLinejoin='round'
                  />
                )}
              </g>
            )
          })}
          <circle
            cx={CENTER}
            cy={CENTER}
            r='10'
            fill='var(--background)'
            stroke='var(--border)'
            strokeWidth='1'
          />
        </svg>
      </div>
      {/* 图例。扇区里不写字：小于百分之几的扇区连一个字都放不下，而图例把
          "哪一色是哪一档、占多少"讲全。已发完的档在这里也挂一枚「售罄」。 */}
      <ul className='flex flex-wrap justify-center gap-x-3 gap-y-1 text-xs'>
        {sectors.map((sector) => {
          const exhausted = soldOut(sector)
          return (
            <li
              key={sector.tier}
              className={cn(
                'inline-flex items-center gap-1.5',
                landed != null && landed.tier !== sector.tier && 'opacity-60'
              )}
            >
              <span
                aria-hidden='true'
                className={cn(
                  'inline-block size-2.5 rounded-full',
                  exhausted && 'qy-art-hatch opacity-60'
                )}
                style={{ background: qyWheelSectorColor(sector.colorIndex) }}
              />
              <span
                className={cn(
                  'break-words',
                  exhausted && 'text-muted-foreground line-through'
                )}
              >
                {sector.name}
              </span>
              {/* 商品奖：奖档名之外把商品本身印出来（形态图标 + 商品名）。
                  "头奖"三个字说不出中的是一张月卡还是一件周边。 */}
              {sector.productTitle != null && (
                <span className='text-muted-foreground inline-flex items-center gap-1'>
                  <QyMallKindBadge
                    kind={sector.productKind ?? ''}
                    className='px-1 text-[10px] leading-4'
                  />
                  <span className='break-words'>{sector.productTitle}</span>
                </span>
              )}
              <span className='text-muted-foreground tabular-nums'>
                {(
                  ((sector.hiPpm - sector.loPpm) / QY_LOT_PPM_DEN) *
                  100
                ).toFixed(2)}
                %
              </span>
              {exhausted && (
                <span className='text-muted-foreground rounded border px-1 text-[10px] leading-4'>
                  {t('qy_lot_wheel_sold_out_badge')}
                </span>
              )}
            </li>
          )
        })}
      </ul>
    </div>
  )
}

/** 一个扇区的路径。0 度在 12 点钟、顺时针为正；整圆（单档 100%）另画。 */
function sectorPath(sector: QyWheelSector): string {
  const span = sector.endDeg - sector.startDeg
  if (span >= 360) {
    return [
      `M ${CENTER} ${CENTER - RADIUS}`,
      `A ${RADIUS} ${RADIUS} 0 1 1 ${CENTER} ${CENTER + RADIUS}`,
      `A ${RADIUS} ${RADIUS} 0 1 1 ${CENTER} ${CENTER - RADIUS}`,
      'Z',
    ].join(' ')
  }
  const from = polar(sector.startDeg)
  const to = polar(sector.endDeg)
  const largeArc = span > 180 ? 1 : 0
  return [
    `M ${CENTER} ${CENTER}`,
    `L ${from.x} ${from.y}`,
    `A ${RADIUS} ${RADIUS} 0 ${largeArc} 1 ${to.x} ${to.y}`,
    'Z',
  ].join(' ')
}

function polar(deg: number): { x: number; y: number } {
  const rad = (deg * Math.PI) / 180
  return {
    x: round(CENTER + RADIUS * Math.sin(rad)),
    y: round(CENTER - RADIUS * Math.cos(rad)),
  }
}

function round(value: number): number {
  return Math.round(value * 1000) / 1000
}
