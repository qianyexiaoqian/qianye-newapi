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
import { qyLotBands } from '../../lottery/lib/verify'
import {
  QY_LOT_PPM_DEN,
  qyLotTiers,
  type QyLotSpecItem,
  type QyLotTier,
} from '../../lottery/types'

/**
 * 转盘的纯逻辑：客户端种子、扇区几何、旋转角、结果分类。
 *
 * 全部是无副作用的函数（`qyWheelRandomClientSeed` 除外，它读 `crypto`），
 * 弹窗、卡片与转盘面共用同一份 —— 扇区的角度若在两处各算一份，
 * 指针停在的那一格与文字说的那一档迟早对不上。
 */

/** `client_seed` 的长度上限，与后端 `maxClientSeed`（列宽）同一个数。 */
export const QY_WHEEL_CLIENT_SEED_MAX = 64

/** 默认随机生成的种子长度。 */
export const QY_WHEEL_CLIENT_SEED_DEFAULT_LEN = 16

/**
 * 种子字符集。恰好 64 个：一个随机字节 `& 63` 就能无偏地落到一个字符上，
 * 不需要拒绝采样。它与后端 `validClientSeed` 认的集合 `[0-9a-zA-Z_-]` 逐字相同。
 */
const SEED_ALPHABET =
  'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-'

/**
 * 种子合规吗。与后端 `validClientSeed` 同一条判据：≤ 64、仅 `[0-9a-zA-Z_-]`；
 * 空串合法（按空分量进原像）。刻意不 trim —— 进原像的字节就是发出去的字节。
 */
export function isQyWheelClientSeedValid(seed: string): boolean {
  return (
    seed.length <= QY_WHEEL_CLIENT_SEED_MAX && /^[0-9A-Za-z_-]*$/.test(seed)
  )
}

/** 随机一份种子。它只是这一转里用户可改的那个分量，不承担任何公正性。 */
export function qyWheelRandomClientSeed(
  length = QY_WHEEL_CLIENT_SEED_DEFAULT_LEN
): string {
  const bytes = new Uint8Array(length)
  crypto.getRandomValues(bytes)
  return Array.from(bytes, (byte) => SEED_ALPHABET[byte & 63]).join('')
}

/** 转盘面上的一个扇区。角度以 12 点钟为 0、顺时针为正，单位度。 */
export type QyWheelSector = {
  tier: number
  name: string
  isNone: boolean
  loPpm: number
  hiPpm: number
  startDeg: number
  endDeg: number
  /** 扇区色在调色板里的下标（none 档不占下标，它固定用中性色）。 */
  colorIndex: number
  /**
   * 商品奖（`prize_type='product'`）的商品名与形态。图例上要把"中的是哪件东西"
   * 与奖档名一起印出来；其余档为 `undefined`。
   */
  productTitle?: string
  productKind?: string
}

/**
 * 从公示奖档算出扇区。顺序与 `qyLotBands` 一致（tier 升序，含派生的 none 行），
 * 因此摇号量 `ppm` 落在哪个扇区与后端 / 验证器落档逐字同一条。
 *
 * 各档概率之和超过 100% 时 `qyLotBands` 会抛错 —— 那说明公示的奖档表本身是错的，
 * 画一个"看起来没问题"的盘面就是替平台圆场，所以返回空数组、盘面不渲染。
 */
export function qyWheelSectors(spec: QyLotSpecItem[]): QyWheelSector[] {
  const tiers = qyLotTiers(spec)
  let bands: ReturnType<typeof qyLotBands>
  try {
    bands = qyLotBands(tiers)
  } catch {
    return []
  }
  const byTier = new Map(tiers.map((tier) => [tier.tier, tier]))
  let colorIndex = 0
  return bands
    .filter((band) => band.hiPpm > band.loPpm)
    .map((band) => {
      const tier = byTier.get(band.tier)
      const isNone = tier?.prize_type === 'none'
      const sector: QyWheelSector = {
        tier: band.tier,
        name: tier?.name ?? '',
        isNone,
        loPpm: band.loPpm,
        hiPpm: band.hiPpm,
        startDeg: (band.loPpm / QY_LOT_PPM_DEN) * 360,
        endDeg: (band.hiPpm / QY_LOT_PPM_DEN) * 360,
        colorIndex: isNone ? -1 : colorIndex,
      }
      if (tier?.prize_type === 'product') {
        sector.productTitle = tier.product_title || tier.product_no
        sector.productKind = tier.product_kind ?? ''
      }
      if (!isNone) colorIndex += 1
      return sector
    })
}

/**
 * 扇区色。**只用四支语义色 + 两档图表色**（design-15 §11 对 Midnight Signal 的
 * 让步：一支色相 / 辉光 ≤2 / 零投影，转盘的多色扇区只能从"族外允许项"里取）。
 * none 档恒为中性的 `--muted`。全部走 CSS 变量，昼夜与各主题预设下自动跟随。
 */
const SECTOR_PALETTE = [
  'var(--success)',
  'var(--info)',
  'var(--warning)',
  'var(--destructive)',
  'var(--chart-1)',
  'var(--chart-2)',
] as const

export function qyWheelSectorColor(colorIndex: number): string {
  if (colorIndex < 0) return 'var(--muted)'
  return SECTOR_PALETTE[colorIndex % SECTOR_PALETTE.length]
}

/**
 * 摇号量 `ppm` 落在哪个扇区(区间左闭右开,与 `qyLotBands` 的落档同一条)。
 * 落在空隙里(概率之和不足 100% 时的尾段)返回 `undefined`,盘面不高亮任何一格。
 */
export function qyWheelSectorAt(
  sectors: QyWheelSector[],
  ppm: number
): QyWheelSector | undefined {
  return sectors.find((sector) => ppm >= sector.loPpm && ppm < sector.hiPpm)
}

/**
 * 让指针（固定在 12 点钟）停在摇号量 `ppm` 那一点上，盘面要顺时针转多少度。
 *
 * 盘面上 `ppm` 那一点在 θ = ppm / 1e6 × 360 度；整体顺时针转 R 度后它落在
 * θ + R。要它回到 0（指针下），R = turns × 360 − θ。多转几整圈只是让动效
 * 看得出"转过了"，落点不变。
 */
export function qyWheelRotationFor(ppm: number, turns = 3): number {
  const theta =
    (Math.min(Math.max(ppm, 0), QY_LOT_PPM_DEN - 1) / QY_LOT_PPM_DEN) * 360
  return turns * 360 - theta
}

/**
 * 一转的三种结局。
 *
 *  - `won`：`result_tier > 0`，中了那一档（星屑当场到账，文本奖等管理员履行）；
 *  - `exhausted`：摇中了某一档但它已发完（`exhausted_tier > 0`），结果落空；
 *  - `none`：落在"谢谢参与"那一段。
 *
 * 三条要说成三句话：第二条尤其不能塌进第三条 —— "摇中了但没货"是转盘特有的
 * 结局，证据链里那一位（`exhausted_tier`）正是它为真的证据。
 */
export type QyWheelOutcome = 'exhausted' | 'none' | 'won'

export function qyWheelOutcomeOf(result: {
  result_tier: number
  exhausted_tier: number
}): QyWheelOutcome {
  if (result.result_tier > 0) return 'won'
  if (result.exhausted_tier > 0) return 'exhausted'
  return 'none'
}

/** 结局 → 说明文案的 i18n 键（查表，登记在 i18n-key-coverage 的动态键清单里）。 */
export const QY_WHEEL_OUTCOME_I18N: Record<QyWheelOutcome, string> = {
  won: 'qy_lot_wheel_result_won',
  exhausted: 'qy_lot_wheel_result_exhausted',
  none: 'qy_lot_wheel_result_none',
}

/** 真实奖档（去掉派生的 none 行），tier 升序。卡片与转动面板只关心这些。 */
export function qyWheelRealTiers(
  spec: QyLotSpecItem[] | undefined
): QyLotTier[] {
  return qyLotTiers(spec).filter((tier) => tier.prize_type !== 'none')
}

/** 派生的「谢谢参与」行；没有就是 `undefined`（老后端 / 非转盘活动）。 */
export function qyWheelNoneTier(
  spec: QyLotSpecItem[] | undefined
): QyLotTier | undefined {
  return qyLotTiers(spec).find((tier) => tier.prize_type === 'none')
}

/** 按 tier 找名字；找不到给空串，调用方自己决定回落成什么。 */
export function qyWheelTierName(
  spec: QyLotSpecItem[] | undefined,
  tierNo: number
): string {
  return qyLotTiers(spec).find((tier) => tier.tier === tierNo)?.name ?? ''
}

/** 全部真实档都发完了吗。发完即封盘，按钮该说"奖品已发完"而不是"不可转动"。 */
export function isQyWheelSoldOut(spec: QyLotSpecItem[] | undefined): boolean {
  const real = qyWheelRealTiers(spec)
  return real.length > 0 && real.every((tier) => (tier.stock_left ?? 0) <= 0)
}
