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
/*
 * 主题层不得改写浮层的定位方式。
 *
 * ─────────────────────── 这个测试是为什么存在的 ───────────────────────
 *
 * qy-sg-apply.css §4 曾经把七个 slot 写进同一条规则一起吃 `position: relative`,
 * 目的只是给 ::before / ::after 的边框层让出一个定位上下文。但其中四个
 * (dialog / alert-dialog / sheet / drawer)上游给的是 Tailwind 的 `.fixed`,
 * 特异度 (0,1,0);主题那条规则带 body 前缀,是 (0,2,1)。**主题赢**。
 *
 * 后果不是"样式差一点":浮层被打回文档流。portal 挂在 <body> 末尾,弹窗于是
 * 落到整页内容的最下方,`top: 50%` 再把它往下推半个页高。项目方连续几轮反馈
 * 「弹窗从下面伸出来」,而每一轮都在组件侧找原因 —— 组件侧改多少次居中都无效,
 * 因为定位压根轮不到组件说话。这正是本仓最贵的那种缺陷:**症状在 A 层,
 * 原因在 B 层,而两层各自看都是对的**。
 *
 * ─────────────────────── 判据 ───────────────────────
 *
 * 反向交叉比对,不写死名单(写死名单只会在上游改动时静默失效):
 *
 *   主题 CSS 里每一条设了 `position` 的规则 → 取出它选中的所有 data-slot
 *   → 到 components/ui 里查这个 slot 自带什么定位
 *   → 自带 fixed / absolute / sticky 的,主题一个字都不能碰。
 *
 * 于是不管以后是主题把某个 slot 加进 position 规则,还是上游把某个静态 slot
 * 改成 fixed,任何一边动了都会在这里失败。
 */
import assert from 'node:assert/strict'
import { readFileSync, readdirSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { describe, test } from 'node:test'
import { fileURLToPath } from 'node:url'

const stylesDir = join(dirname(fileURLToPath(import.meta.url)), '..')
const uiDir = join(stylesDir, '..', 'components', 'ui')

/** 主题挂载层。令牌层与形状层只导出变量,不选 slot,不参与本测试。 */
const THEME_CSS = 'qy-sg-apply.css'

/**
 * 自带定位的工具类。`static` 不在内 —— 它与主题补 `relative` 不冲突。
 *
 * 用词边界匹配:`inset-x-0` 里没有独立的 `absolute`,而
 * `data-[vaul-drawer-direction=bottom]:bottom-0` 里也不该匹配上 `fixed`。
 */
const POSITIONED = /(?:^|[\s:'"])(fixed|absolute|sticky)(?:$|[\s'"])/

/**
 * 逐个 slot 记录它在上游组件里自带的定位。
 *
 * 只看 `data-slot='X'` 之后**第一个**长类名字符串 —— 那就是该元素自己的基础
 * 类。不能整块扫:dialog.tsx 在 dialog-content 之后还有一个
 * `className='absolute top-2 right-2'` 的关闭按钮,整块扫会把子元素的定位
 * 算到父元素头上。
 */
function collectNativePositions() {
  const positioned = new Map<string, string>()
  const seen = new Set<string>()

  for (const file of readdirSync(uiDir).filter((f) => f.endsWith('.tsx'))) {
    const src = readFileSync(join(uiDir, file), 'utf8')
    const slotRe = /data-slot='([a-z0-9-]+)'/g
    let m: RegExpExecArray | null

    while ((m = slotRe.exec(src)) !== null) {
      const slot = m[1]
      seen.add(slot)
      // 从本次 data-slot 到下一个 data-slot 之间,取第一个长度够的字符串字面量。
      const nextSlot = src.indexOf('data-slot=', m.index + 1)
      const window = src.slice(m.index, nextSlot === -1 ? undefined : nextSlot)
      const classIdx = window.indexOf('className')
      if (classIdx === -1) continue
      const literal = /'([^']{20,})'/.exec(window.slice(classIdx))
      if (!literal) continue
      const hit = POSITIONED.exec(literal[1])
      if (hit) positioned.set(slot, hit[1])
    }
  }
  return { positioned, seen }
}

/** 拆成 `{ selector, body }`,只要顶层规则;@media / @supports 里的块同样会被拆出来。 */
function ruleBlocks(css: string) {
  const out: Array<{ selector: string; body: string }> = []
  const re = /([^{}]+)\{([^{}]*)\}/g
  let m: RegExpExecArray | null
  while ((m = re.exec(css)) !== null) {
    out.push({ selector: m[1].trim(), body: m[2] })
  }
  return out
}

/** `position:` 本身,而不是 background-position / mask-position / object-position。 */
const POSITION_DECL = /(?:^|[;{\s])position\s*:/

/**
 * 按**顶层**逗号拆选择器列表。
 *
 * 不能直接 `split(',')`:本文件的选择器几乎都长成
 * `body[...] :is([data-slot='a'], [data-slot='b'])::before`,
 * 逗号在 `:is()` 里面,裸拆会把一条规则拆成两个残句。
 */
function splitSelectorList(selector: string) {
  const out: string[] = []
  let depth = 0
  let start = 0
  for (let i = 0; i < selector.length; i++) {
    const ch = selector[i]
    if (ch === '(') depth++
    else if (ch === ')') depth--
    else if (ch === ',' && depth === 0) {
      out.push(selector.slice(start, i))
      start = i + 1
    }
  }
  out.push(selector.slice(start))
  return out
}

describe('Steins Gate 挂载层 · 浮层定位', () => {
  test('主题不得给自带 fixed/absolute/sticky 的 slot 写 position', () => {
    const { positioned, seen } = collectNativePositions()
    // 前置条件:扫描本身必须有效。一个都没扫到说明正则跟不上组件写法的变化,
    // 此时主断言会空转通过 —— 那是这类测试最典型的假回归。
    assert.ok(seen.size > 20, `data-slot 扫描失效,只找到 ${seen.size} 个`)
    assert.ok(
      positioned.has('dialog-content'),
      'dialog-content 自带 fixed,却没被识别出来 —— 扫描逻辑已失效'
    )

    const css = readFileSync(join(stylesDir, THEME_CSS), 'utf8')
    const offenders: string[] = []

    for (const { selector, body } of ruleBlocks(css)) {
      if (!POSITION_DECL.test(body)) continue
      for (const part of splitSelectorList(selector)) {
        // 伪元素不参与:::before / ::after 正是靠 `position: absolute` +
        // `inset: 0` 铺出边框层的,那是本主题的核心手法,不是缺陷。
        // 被改写定位的是**宿主元素**,所以只看不带 :: 的选择器。
        if (part.includes('::')) continue
        for (const m of part.matchAll(/\[data-slot='([a-z0-9-]+)'\]/g)) {
          const native = positioned.get(m[1])
          if (native) offenders.push(`${m[1]}(自带 ${native})`)
        }
      }
    }

    assert.deepEqual(
      offenders,
      [],
      `${THEME_CSS} 改写了这些浮层的定位:${offenders.join('、')}。\n` +
        '主题规则带 body 前缀是 (0,2,1),会压过上游 (0,1,0) 的 .fixed,\n' +
        '浮层被打回文档流后会掉到页面底部而不是悬浮居中。\n' +
        '边框层需要定位上下文时,只给原本 static 的 slot 补 position:relative。'
    )
  })
})
