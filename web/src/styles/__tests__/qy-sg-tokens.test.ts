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
 * Steins Gate 令牌层的契约测试。
 *
 * 这里钉的是三件"改坏了肉眼看不出来、但用户会中招"的事:
 *
 * 1. 昼夜覆写完整性。夜块只覆写"公式本身不同"的量,漏掉一个,夜间就会静默
 *    继承昼间的值(例如 --qy-sg-ember 漏覆写,夜间表头会变成 Claude 的陶土色)。
 *    这类断链在渲染上不报错,只是"颜色有点怪",评审极易放过。
 * 2. 声明唯一性。--qy-sg-* 的颜色派生量只允许在 qy-sg-tokens.css 声明;
 *    别的 qy-sg-*.css 一旦也声明同名量,后加载的会静默覆盖,是最难查的一类样式 bug。
 * 3. 对比度下限。夜间底色是【中间调】混凝土灰,它把语义色两头都挤住;
 *    这套值的亮度是按"压在底色上 ≥4.5:1"反解出来的。有人为了"更红更醒目"
 *    把 destructive 调回饱和红,正文就会掉到 3:1 以下 —— 只有算出来才知道。
 *
 * 测试直接解析 CSS 源文件而不是跑浏览器:本仓库前端测试跑在 node:test 下,
 * 没有渲染引擎。oklch → sRGB 的换算在下面自带一份(公式来自 Björn Ottosson
 * 的 OKLab 定义),与浏览器实测值逐一核对过。
 */
import assert from 'node:assert/strict'
import { readFileSync, readdirSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { describe, test } from 'node:test'
import { fileURLToPath } from 'node:url'

const stylesDir = join(dirname(fileURLToPath(import.meta.url)), '..')
const tokensFile = join(stylesDir, 'qy-sg-tokens.css')
const source = readFileSync(tokensFile, 'utf8')

const LIGHT_SELECTOR = "[data-theme-preset='steins-gate'] {"
const DARK_SELECTOR = ".dark [data-theme-preset='steins-gate'] {"

/** 取出选择器后紧跟的那一对花括号里的内容(注释里的花括号本文件中不存在)。 */
function blockOf(selector: string): string {
  const start = source.indexOf(selector)
  assert.notEqual(start, -1, `找不到选择器 ${selector}`)
  let depth = 0
  for (let i = start + selector.length - 1; i < source.length; i += 1) {
    if (source[i] === '{') depth += 1
    else if (source[i] === '}') {
      depth -= 1
      if (depth === 0) return source.slice(start + selector.length, i)
    }
  }
  throw new Error(`选择器 ${selector} 的块没有闭合`)
}

/** 解析声明。先剥注释,再按顶层分号切分,以免 color-mix(...) 里的逗号被误切。 */
function declarationsOf(block: string): Map<string, string> {
  const stripped = block.replaceAll(/\/\*[\s\S]*?\*\//g, '')
  const out = new Map<string, string>()
  let depth = 0
  let buf = ''
  const flush = () => {
    const text = buf.trim()
    buf = ''
    if (!text) return
    const colon = text.indexOf(':')
    if (colon === -1) return
    out.set(text.slice(0, colon).trim(), text.slice(colon + 1).trim())
  }
  for (const ch of stripped) {
    if (ch === '(') depth += 1
    else if (ch === ')') depth -= 1
    if (ch === ';' && depth === 0) {
      flush()
      continue
    }
    buf += ch
  }
  flush()
  return out
}

const light = declarationsOf(blockOf(LIGHT_SELECTOR))
const dark = declarationsOf(blockOf(DARK_SELECTOR))

/* ── oklch → sRGB → WCAG 相对亮度 ────────────────────────────────────── */

/** 返回【线性光】三分量:relativeLuminance 直接吃它,不要再做一次传输函数。 */
function oklchToLinearRgb(
  l: number,
  c: number,
  hDeg: number
): [number, number, number] {
  const h = (hDeg * Math.PI) / 180
  const a = c * Math.cos(h)
  const b = c * Math.sin(h)
  const l_ = (l + 0.3963377774 * a + 0.2158037573 * b) ** 3
  const m_ = (l - 0.1055613458 * a - 0.0638541728 * b) ** 3
  const s_ = (l - 0.0894841775 * a - 1.291485548 * b) ** 3
  const lin = [
    4.0767416621 * l_ - 3.3077115913 * m_ + 0.2309699292 * s_,
    -1.2684380046 * l_ + 2.6097574011 * m_ - 0.3413193965 * s_,
    -0.0041960863 * l_ - 0.7034186147 * m_ + 1.707614701 * s_,
  ]
  return lin.map((v) => Math.min(1, Math.max(0, v))) as [number, number, number]
}

/** 解析一个可能是 oklch(...) 或 var(--other) 的取值,var 在同块内解一层。 */
function resolveColor(
  name: string,
  scope: Map<string, string>
): [number, number, number] {
  const value = scope.get(name) ?? light.get(name)
  assert.ok(value, `${name} 未声明`)
  const varMatch = /^var\(\s*(--[a-z0-9-]+)\s*\)$/i.exec(value)
  if (varMatch) return resolveColor(varMatch[1], scope)
  const m = /^oklch\(\s*([\d.]+)\s+([\d.]+)\s+([\d.]+)\s*\)$/i.exec(value)
  assert.ok(m, `${name} 的取值 ${value} 不是可解析的 oklch()`)
  return oklchToLinearRgb(Number(m[1]), Number(m[2]), Number(m[3]))
}

function relativeLuminance([r, g, b]: [number, number, number]): number {
  return 0.2126 * r + 0.7152 * g + 0.0722 * b
}

function contrast(a: string, b: string, scope: Map<string, string>): number {
  const la = relativeLuminance(resolveColor(a, scope))
  const lb = relativeLuminance(resolveColor(b, scope))
  return (Math.max(la, lb) + 0.05) / (Math.min(la, lb) + 0.05)
}

/* ── 1. 取材锚点 ─────────────────────────────────────────────────────── */

describe('qy-sg-tokens 取材锚点', () => {
  // 暗色的锚点全部是从游戏截图里逐像素量出来的;昼间的锚点是 Anthropic 官方品牌色。
  // 钉住它们是为了让"有人凭手感把颜色挪一挪"必须先解释清楚新的取材来源。
  const anchors: Array<[string, Map<string, string>, string]> = [
    ['--background', dark, 'oklch(0.435 0.005 197)'], // #4E5252 混凝土
    ['--foreground', dark, 'oklch(0.936 0.007 145.5)'], // #E7EBE7 字形峰值
    ['--primary', dark, 'oklch(0.857 0.175 88.5)'], // #FFC800 金
    ['--qy-sg-ember', dark, 'oklch(0.689 0.175 46.1)'], // #EF7129 橙
    ['--qy-sg-vignette', dark, 'oklch(0.247 0.006 179.1)'], // #1E2221 暗角
    ['--background', light, 'oklch(0.982 0.005 95.1)'], // #FAF9F5 Anthropic Light
    ['--foreground', light, 'oklch(0.191 0.002 106.6)'], // #141413 Anthropic Dark
    ['--primary', light, 'oklch(0.672 0.131 38.8)'], // #D97757 Anthropic Orange
  ]
  for (const [name, scope, expected] of anchors) {
    const theme = scope === dark ? '夜' : '昼'
    test(`${theme} ${name}`, () => {
      assert.equal(scope.get(name), expected)
    })
  }
})

/* ── 2. 昼夜覆写完整性 ───────────────────────────────────────────────── */

describe('qy-sg-tokens 昼夜覆写', () => {
  // 昼夜取值必须不同的私有派生量。漏一个,夜间就静默继承昼间的值。
  const mustOverride = [
    '--qy-sg-hair',
    '--qy-sg-hair-strong',
    '--qy-sg-accent-soft',
    '--qy-sg-glow',
    '--qy-sg-ember',
    '--qy-sg-vignette',
    '--qy-sg-band',
    '--qy-sg-band-fg',
    '--qy-sg-ink-btn-bg',
    '--qy-sg-ink-btn-fg',
    '--qy-sg-ink-btn-shadow',
    '--qy-sg-ink-btn-shadow-hover',
    '--qy-sg-lift-shadow',
    '--qy-sg-overlay',
    '--qy-sg-selection-bg',
    '--qy-sg-selection-fg',
    '--qy-sg-grain-opacity',
    '--qy-sg-gear-opacity',
  ]

  for (const name of mustOverride) {
    test(`夜块覆写 ${name}`, () => {
      assert.ok(light.has(name), `${name} 必须先在昼块声明`)
      assert.ok(dark.has(name), `${name} 昼夜取值不同,夜块必须覆写`)
      assert.notEqual(
        dark.get(name),
        light.get(name),
        `${name} 夜块的取值与昼块相同,覆写没有意义`
      )
    })
  }

  // 公式两边一致的量【不能】在夜块重复声明:同一个概念两处定义会各自漂移,
  // 这是本项目已经踩过六组的坑。它们 var() 引用的标准变量已经在夜块换了值。
  const sharedFormula = [
    '--qy-sg-serif',
    '--qy-sg-sans',
    '--qy-sg-mono',
    '--qy-sg-ease',
    '--qy-sg-pill',
    '--qy-sg-blur',
    '--qy-sg-sec-gap',
    '--qy-sg-head-gap',
    '--qy-sg-leading',
    '--qy-sg-faint',
    '--qy-sg-accent-deep',
    // hover 落点两边都是 accent-deep,夜间靠 accent-deep 自己跟着 --primary 翻转
    '--qy-sg-ink-btn-bg-hover',
  ]
  for (const name of sharedFormula) {
    test(`夜块不重复声明 ${name}`, () => {
      assert.ok(light.has(name), `${name} 必须在昼块声明`)
      assert.equal(
        dark.has(name),
        false,
        `${name} 公式昼夜一致,夜块不应重复声明`
      )
    })
  }
})

/* ── 3. 声明唯一性 ───────────────────────────────────────────────────── */

test('--qy-sg-* 的颜色派生量只在 qy-sg-tokens.css 声明', () => {
  const owned = new Set(
    [...light.keys(), ...dark.keys()].filter((k) => k.startsWith('--qy-sg-'))
  )
  const offenders: string[] = []
  for (const file of readdirSync(stylesDir)) {
    if (!file.startsWith('qy-sg-') || !file.endsWith('.css')) continue
    if (file === 'qy-sg-tokens.css') continue
    const text = readFileSync(join(stylesDir, file), 'utf8').replaceAll(
      /\/\*[\s\S]*?\*\//g,
      ''
    )
    for (const m of text.matchAll(/(--qy-sg-[a-z0-9-]+)\s*:/g)) {
      if (owned.has(m[1])) offenders.push(`${file}: ${m[1]}`)
    }
  }
  assert.deepEqual(offenders, [])
})

/* ── 4. 对比度下限 ───────────────────────────────────────────────────── */

describe('qy-sg-tokens 对比度', () => {
  const cases: Array<[string, Map<string, string>, string, string, number]> = [
    // 正文与次级正文压在各自底色上
    ['夜 正文', dark, '--foreground', '--background', 4.5],
    ['夜 次级正文', dark, '--muted-foreground', '--background', 4.5],
    ['昼 正文', light, '--foreground', '--background', 4.5],
    ['昼 次级正文', light, '--muted-foreground', '--background', 4.5],
    // 四个语义色会被当作正文色用(controls 里 destructive 幽灵按钮就是 color:)
    ['夜 destructive', dark, '--destructive', '--background', 4.5],
    ['夜 success', dark, '--success', '--background', 4.5],
    ['夜 warning', dark, '--warning', '--background', 4.5],
    ['夜 info', dark, '--info', '--background', 4.5],
    ['昼 destructive', light, '--destructive', '--background', 4.5],
    ['昼 success', light, '--success', '--background', 4.5],
    ['昼 warning', light, '--warning', '--background', 4.5],
    ['昼 info', light, '--info', '--background', 4.5],
    // 压在强调色上的字(shadcn 的 bg-primary text-primary-foreground)
    ['夜 强调色上的字', dark, '--primary-foreground', '--primary', 4.5],
    ['昼 强调色上的字', light, '--primary-foreground', '--primary', 4.5],
    // 表头横幅:夜间刻意不用游戏原本的烧橙字(那只有 1.78:1),改用暗角色
    ['夜 横幅字压在橙端', dark, '--qy-sg-band-fg', '--qy-sg-ember', 4.5],
  ]
  for (const [label, scope, fg, bg, floor] of cases) {
    test(`${label} ≥ ${floor}:1`, () => {
      const ratio = contrast(fg, bg, scope)
      assert.ok(
        ratio >= floor,
        `${fg} 压在 ${bg} 上只有 ${ratio.toFixed(2)}:1,低于下限 ${floor}:1`
      )
    })
  }
})
