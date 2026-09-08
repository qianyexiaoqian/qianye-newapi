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
  // 三份渐变/图案各要一个文档内唯一的 id：同一页上可以同时挂着面板那只静止的
  // 盘与弹窗里那只在转的盘，写死的 id 会让后挂载的那只引用到前一只的定义。
  const hatchId = useId()
  const domeId = useId()
  const sheenId = useId()
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
  // 「正在转」= 已经有目标角度、盘面还没落定，且这一次真的走了过渡。缩减动效下
  // `settled` 与 `targetPpm` 在同一拍内一起就位，这里恒为 false。
  const spinning = targetPpm != null && !settled && !reducedMotion
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
        {/*
          转动中的一道扫光。它画在**盘面之外**、不随盘面那 2 秒过渡走：盘面自己
          转到目标角度是一条 ease-out 曲线，最后一秒几乎不动，而"还在转"这件事
          必须在那一秒里仍然看得见（否则用户以为卡住了）。扫光匀速自转，转完即
          卸载。缩减动效下 CSS 把它置为透明。
        */}
        {spinning && (
          <svg
            aria-hidden='true'
            viewBox='0 0 200 200'
            className='pointer-events-none absolute inset-0 z-[5] w-full'
          >
            <g className='qy-fx-spin-sheen'>
              <path
                d={sectorPath({ startDeg: 0, endDeg: 70 })}
                fill={`url(#${sheenId})`}
              />
            </g>
          </svg>
        )}
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
            {/*
              盘面的球面光：左上一层高光、右下一层暗面，盖在全部扇区之上。
              与号码球同一条口径（`styles/qy-visual.css`）——白与黑的低透明度是
              **光**而不是色相，叠在任何一支扇区色上都成立，所以换主题预设时
              这一层不用跟着改。它不是投影（design-14 的零投影管的是 box-shadow），
              也不是辉光（那两处配额分给了后台页头与落地页首屏）。
            */}
            <radialGradient id={domeId} cx='38%' cy='30%' r='78%'>
              <stop offset='0%' stopColor='#fff' stopOpacity='0.22' />
              <stop offset='52%' stopColor='#fff' stopOpacity='0' />
              <stop offset='100%' stopColor='#000' stopOpacity='0.14' />
            </radialGradient>
            <linearGradient id={sheenId} x1='0' y1='0' x2='0' y2='1'>
              <stop
                offset='0%'
                stopColor='var(--foreground)'
                stopOpacity='0.18'
              />
              <stop
                offset='55%'
                stopColor='var(--foreground)'
                stopOpacity='0'
              />
            </linearGradient>
          </defs>
          {/* 外圈：盘子的边。发丝线一条，不是投影。 */}
          <circle
            cx={CENTER}
            cy={CENTER}
            r={RADIUS + 3}
            fill='none'
            stroke='var(--border)'
            strokeWidth='2'
          />
          {sectors.map((sector) => {
            const isLanded = landed?.tier === sector.tier
            const dim = landed != null && !isLanded
            const exhausted = soldOut(sector)
            // 落档描边的作用是"把这一格从别的格里挑出来"。当这一格几乎就是整个
            // 盘时（实测演示站有一场是 0.5% / 99.5%，落在后者），它退化成绕盘一圈
            // 的粗黑环外加一条从圆心拉到边缘的竖线 —— 没有对照物，只剩噪音，而且
            // 它强调的恰好是"你没中"那一格。这种场次里"停在哪"由指针与其余格的
            // 变暗回答，描边不必再说一遍。
            const outlineLanded = isLanded && !isNearFullCircle(sector)
            return (
              <g
                key={sector.tier}
                className={cn('qy-fx-sector', isLanded && 'qy-fx-land')}
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
                {outlineLanded && (
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
          {/* 每个扇区起点一道内向刻度：扇区之间那条 1px 发丝线在窄屏上几乎看不见，
              刻度把"这里是一档的边界"补回来，也让盘面读起来像一只转盘而不是饼图。 */}
          {sectors.map((sector) => {
            const outer = polar(sector.startDeg)
            const inner = polar(sector.startDeg, RADIUS - 9)
            return (
              <line
                key={`tick-${sector.tier}`}
                x1={outer.x}
                y1={outer.y}
                x2={inner.x}
                y2={inner.y}
                stroke='var(--background)'
                strokeWidth='1.5'
                strokeLinecap='round'
                opacity='0.7'
              />
            )
          })}
          {/* 球面光盖在全部扇区与刻度之上，盘心那颗轮毂再盖在它之上。 */}
          <circle
            cx={CENTER}
            cy={CENTER}
            r={RADIUS}
            fill={`url(#${domeId})`}
            pointerEvents='none'
          />
          {/* 轮毂。同一层球面光让它也鼓起来，中间一颗星就是"抽"这个动作的符号。 */}
          <circle
            cx={CENTER}
            cy={CENTER}
            r='15'
            fill='var(--card)'
            stroke='var(--border)'
            strokeWidth='1'
          />
          <circle cx={CENTER} cy={CENTER} r='15' fill={`url(#${domeId})`} />
          <path
            d='M100 92 l2.2 5.3 5.8 .5 -4.4 3.8 1.3 5.6 -4.9 -3 -4.9 3 1.3 -5.6 -4.4 -3.8 5.8 -.5 z'
            fill='var(--primary)'
          />
        </svg>
      </div>
      {/*
        图例。扇区里不写字：小于百分之几的扇区连一个字都放不下，而图例把
        "哪一色是哪一档、占多少"讲全。已发完的档在这里也挂一枚「售罄」。

        排成**每档一行的网格**而不是一条会折行的横排：横排里概率跟在奖档名
        后面随着名字长短各自漂到不同的位置，六七档挤成一团时读者要在文字里
        找那个百分号。一行一档、概率右对齐之后，"哪一档最容易中"是扫一眼
        列就能比出来的，而不需要逐个读。
      */}
      <ul className='grid w-full gap-y-1 text-xs sm:grid-cols-2 sm:gap-x-4'>
        {sectors.map((sector) => {
          const exhausted = soldOut(sector)
          return (
            <li
              key={sector.tier}
              className={cn(
                'flex items-baseline gap-1.5',
                landed != null && landed.tier !== sector.tier && 'opacity-60'
              )}
            >
              <span
                aria-hidden='true'
                className={cn(
                  'mt-1 inline-block size-2.5 shrink-0 self-start rounded-full',
                  exhausted && 'qy-art-hatch opacity-60'
                )}
                style={{ background: qyWheelSectorColor(sector.colorIndex) }}
              />
              <span className='min-w-0 flex-1'>
                <span
                  className={cn(
                    'break-words',
                    exhausted && 'text-muted-foreground line-through'
                  )}
                >
                  {sector.name}
                </span>
                {exhausted && (
                  <span className='text-muted-foreground ms-1.5 rounded border px-1 text-[10px] leading-4'>
                    {t('qy_lot_wheel_sold_out_badge')}
                  </span>
                )}
                {/* 商品奖：奖档名之外把商品本身印出来（形态图标 + 商品名）。
                    "头奖"三个字说不出中的是一张月卡还是一件周边。自己一行，
                    否则商品名一长就把概率挤到下一行去。 */}
                {sector.productTitle != null && (
                  <span className='text-muted-foreground flex items-center gap-1'>
                    <QyMallKindBadge
                      kind={sector.productKind ?? ''}
                      className='px-1 text-[10px] leading-4'
                    />
                    <span className='break-words'>{sector.productTitle}</span>
                  </span>
                )}
              </span>
              <span className='text-muted-foreground shrink-0 tabular-nums'>
                {(
                  ((sector.hiPpm - sector.loPpm) / QY_LOT_PPM_DEN) *
                  100
                ).toFixed(2)}
                %
              </span>
            </li>
          )
        })}
      </ul>
    </div>
  )
}

/**
 * 一个扇区的路径。0 度在 12 点钟、顺时针为正；整圆（单档 100%）另画。
 *
 * 只用得到起止角，所以形参收成这两个字段 —— 扫光那一片不是任何一档奖，
 * 它没有 tier / 名称 / 概率可填，硬造一个假的 `QyWheelSector` 去满足签名
 * 会让"扇区"这个类型开始容纳不是扇区的东西。
 */
function sectorPath(
  sector: Pick<QyWheelSector, 'endDeg' | 'startDeg'>
): string {
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

/**
 * 这一格是不是几乎占满整个盘。
 *
 * 阈值取 300 度：到这个跨度，落档描边的两条半径已经近乎重合，描出来的是盘子的
 * 外圈而不是某一格的边界。奖档表里出现这种比例并不罕见 —— 只配一档小奖、其余
 * 全是「谢谢参与」的转盘就是这个形状。
 */
function isNearFullCircle(sector: Pick<QyWheelSector, 'endDeg' | 'startDeg'>) {
  return sector.endDeg - sector.startDeg > 300
}

/** 极坐标转直角。`radius` 缺省是盘面外缘，刻度的内端传一个更小的半径。 */
function polar(deg: number, radius = RADIUS): { x: number; y: number } {
  const rad = (deg * Math.PI) / 180
  return {
    x: round(CENTER + radius * Math.sin(rad)),
    y: round(CENTER - radius * Math.cos(rad)),
  }
}

function round(value: number): number {
  return Math.round(value * 1000) / 1000
}
