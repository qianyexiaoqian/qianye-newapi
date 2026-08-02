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
 * 这里钉的是六件"改坏了肉眼看不出来、但用户会中招"的事:
 *
 * 1. 昼夜同族。第四版把昼间从夜间派生出来 —— 三个色相角(混凝土 197、
 *    金 88.5、橙 46.1)昼夜共用,只改明度与彩度。前三版的昼间是另一套品牌
 *    (Anthropic 色板,陶土色相 38.8),仓库里因此躺着两份色板,而且拨一下
 *    dark 开关看到的是另一个产品。第 1 组用例把色相角钉死:谁再塞一个族外
 *    的强调色进来,直接红。
 * 2. 层次方向镜像。夜间的面是从墙上凿【进去】的(card/popover/sidebar 比底色暗),
 *    昼间是纸上浮【起来】的。方向搞反在渲染上不报错,只是"层次有点怪"。
 * 3. 取材锚点。夜间每个基色都是从游戏截图里逐像素量出来的。
 * 4. 昼夜覆写完整性。夜块只覆写"公式本身不同"的量,漏掉一个,夜间就会静默
 *    继承昼间的值;反过来,公式一致的量在夜块重复声明,就是同一概念的第二份
 *    拷贝,迟早各自漂移。
 * 5. 声明唯一性。--qy-sg-* 在三个文件里合起来只能声明一次。
 * 6. 对比度下限。夜间底色是【中间调】混凝土灰,把语义色两头都挤住;
 *    昼间强调色被压到 L≈0.54 才能当文字色用。这套值的明度是反解出来的,
 *    有人为了"更亮更醒目"往回调,正文就会掉到 3:1 以下 —— 只有算出来才知道。
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

/* ── oklch 解析与换算 ─────────────────────────────────────────────────── */

interface Oklch {
  l: number
  c: number
  h: number
}

/** 解析一个可能是 oklch(...) 或 var(--other) 的取值,var 在同块内解一层。 */
function oklchOf(name: string, scope: Map<string, string>): Oklch {
  const value = scope.get(name) ?? light.get(name)
  assert.ok(value, `${name} 未声明`)
  const varMatch = /^var\(\s*(--[a-z0-9-]+)\s*\)$/i.exec(value)
  if (varMatch) return oklchOf(varMatch[1], scope)
  const m = /^oklch\(\s*([\d.]+)\s+([\d.]+)\s+([\d.]+)\s*\)$/i.exec(value)
  assert.ok(m, `${name} 的取值 ${value} 不是可解析的 oklch()`)
  return { l: Number(m[1]), c: Number(m[2]), h: Number(m[3]) }
}

/** 返回【线性光】三分量:relativeLuminance 直接吃它,不要再做一次传输函数。 */
function toLinearRgb({ l, c, h: hDeg }: Oklch): [number, number, number] {
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

function relativeLuminance([r, g, b]: [number, number, number]): number {
  return 0.2126 * r + 0.7152 * g + 0.0722 * b
}

function contrast(a: string, b: string, scope: Map<string, string>): number {
  const la = relativeLuminance(toLinearRgb(oklchOf(a, scope)))
  const lb = relativeLuminance(toLinearRgb(oklchOf(b, scope)))
  return (Math.max(la, lb) + 0.05) / (Math.min(la, lb) + 0.05)
}

/* ── 1. 昼夜同族:色相角共用 ─────────────────────────────────────────── */

describe('qy-sg-tokens 昼夜同族', () => {
  // 夜间的三个色相角是从游戏截图里逐像素量出来的;昼间原样沿用,只改明度/彩度。
  // 这一组是第四版最核心的契约:它把"昼间是另一套品牌色板"这条路彻底堵死。
  const sharedHue: Array<[string, string]> = [
    ['--primary', '金'],
    ['--qy-sg-ember', '橙'],
  ]
  for (const [name, label] of sharedHue) {
    test(`${name}(${label})昼夜共用同一个色相角`, () => {
      assert.equal(
        oklchOf(name, light).h,
        oklchOf(name, dark).h,
        `${name} 的色相角昼夜不一致 —— 昼间又变成另一套品牌色了`
      )
    })
  }

  test('昼间强调色是夜间的"压深"档而不是另换一色', () => {
    // 明度必须降(纸面上才读得出),彩度不许暴涨(否则就不是同一个金了)。
    for (const [name] of sharedHue) {
      const day = oklchOf(name, light)
      const night = oklchOf(name, dark)
      assert.ok(
        day.l < night.l,
        `${name} 昼间明度 ${day.l} 应低于夜间 ${night.l}`
      )
      assert.ok(
        day.c <= night.c,
        `${name} 昼间彩度 ${day.c} 不应高于夜间 ${night.c}`
      )
    }
  })

  test('纸面与墨色落在族内的两个色相角上', () => {
    // 纸面走金的色相角(所以是暖白不是冷白),墨色走混凝土的色相角。
    assert.equal(oklchOf('--background', light).h, oklchOf('--primary', dark).h)
    assert.equal(oklchOf('--foreground', light).h, 197)
    assert.equal(oklchOf('--background', dark).h, 197)
  })

  test('中性档的彩度足够低,不会把纸面染成有色底', () => {
    for (const name of ['--background', '--card', '--popover']) {
      assert.ok(
        oklchOf(name, light).c <= 0.012,
        `${name} 昼间彩度过高,纸面会读成有色底`
      )
    }
  })
})

/* ── 2. 层次方向昼夜镜像 ─────────────────────────────────────────────── */

describe('qy-sg-tokens 层次方向镜像', () => {
  // 夜:面从混凝土墙上凿【进去】,card/popover/sidebar 比底色暗;
  //     muted/secondary/accent 是抬起来的交互面,比底色亮。
  // 昼:整套翻过来。方向搞反在渲染上不报错,只是"层次有点怪"。
  const recessedAtNight = ['--card', '--popover', '--sidebar']
  const raisedAtNight = ['--muted', '--secondary', '--accent']

  for (const name of recessedAtNight) {
    test(`${name} 夜间比底色暗、昼间比纸面亮`, () => {
      assert.ok(
        oklchOf(name, dark).l < oklchOf('--background', dark).l,
        `${name} 夜间应比底色暗(面是凿进去的)`
      )
      assert.ok(
        oklchOf(name, light).l > oklchOf('--background', light).l,
        `${name} 昼间应比纸面亮(面是浮起来的)`
      )
    })
  }

  for (const name of raisedAtNight) {
    test(`${name} 夜间比底色亮、昼间比纸面暗`, () => {
      assert.ok(
        oklchOf(name, dark).l > oklchOf('--background', dark).l,
        `${name} 夜间应比底色亮`
      )
      assert.ok(
        oklchOf(name, light).l < oklchOf('--background', light).l,
        `${name} 昼间应比纸面暗`
      )
    })
  }
})

/* ── 3. 取材锚点 ─────────────────────────────────────────────────────── */

describe('qy-sg-tokens 取材锚点', () => {
  // 夜间的锚点全部是从游戏截图里逐像素量出来的。钉住它们是为了让"有人凭手感
  // 把颜色挪一挪"必须先解释清楚新的取材来源。
  const anchors: Array<[string, string]> = [
    ['--background', 'oklch(0.435 0.005 197)'], // #4E5252 混凝土
    ['--foreground', 'oklch(0.936 0.007 145.5)'], // #E7EBE7 字形峰值
    ['--primary', 'oklch(0.857 0.175 88.5)'], // #FFC800 金
    ['--qy-sg-ember', 'oklch(0.689 0.175 46.1)'], // #EF7129 橙
    ['--qy-sg-vignette', 'oklch(0.247 0.006 179.1)'], // #1E2221 暗角
  ]
  for (const [name, expected] of anchors) {
    test(`夜 ${name}`, () => {
      assert.equal(dark.get(name), expected)
    })
  }
})

/* ── 4. 昼夜覆写完整性 ───────────────────────────────────────────────── */

// 昼夜取值必须不同的私有派生量。漏一个,夜间就静默继承昼间的值。
const mustOverride = [
  '--qy-sg-hair',
  '--qy-sg-hair-strong',
  '--qy-sg-accent-soft',
  '--qy-sg-glow',
  '--qy-sg-ember',
  '--qy-sg-vignette',
  '--qy-sg-lift-shadow',
  '--qy-sg-overlay',
  '--qy-sg-selection-bg',
  '--qy-sg-selection-fg',
]

// 公式两边一致的量【不能】在夜块重复声明:同一个概念两处定义会各自漂移。
// 它们 var() 引用的标准变量已经在夜块换了值,会自动跟着翻转。
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
  // 第四版新增的两个:昼间强调色压到可读档之后,表头横幅终于可以昼夜同公式
  '--qy-sg-band',
  '--qy-sg-band-fg',
]

describe('qy-sg-tokens 昼夜覆写', () => {
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

  test('昼块的 --qy-sg-* 清单与上面两组完全吻合', () => {
    // 防止新增一个派生量却忘了归类 —— 忘了归类就等于没人检查它的昼夜行为。
    const declared = [...light.keys()]
      .filter((k) => k.startsWith('--qy-sg-'))
      .sort()
    assert.deepEqual(declared, [...mustOverride, ...sharedFormula].sort())
  })
})

/* ── 5. 声明唯一性 ───────────────────────────────────────────────────── */

test('每个 --qy-sg-* 在三个文件里合起来只声明一次', () => {
  // tokens 管颜色与字体轴,shape 管几何量与配方,两者不许有交集;
  // apply 一个都不许声明 —— 它只写 var()。
  const owner = new Map<string, string>()
  const offenders: string[] = []
  for (const file of readdirSync(stylesDir)) {
    if (!file.startsWith('qy-sg-') || !file.endsWith('.css')) continue
    const text = readFileSync(join(stylesDir, file), 'utf8').replaceAll(
      /\/\*[\s\S]*?\*\//g,
      ''
    )
    for (const m of text.matchAll(/(--qy-sg-[a-z0-9-]+)\s*:/g)) {
      // tokens 自己的昼/夜两块是同一个量的两个档位,不算重复声明
      const previous = owner.get(m[1])
      if (previous && previous !== file) {
        offenders.push(`${m[1]}: ${previous} 与 ${file}`)
        continue
      }
      owner.set(m[1], file)
    }
  }
  assert.deepEqual(offenders, [])
  assert.equal(owner.get('--qy-sg-frame-clip'), 'qy-sg-shape.css')
  assert.equal(owner.get('--qy-sg-band'), 'qy-sg-tokens.css')
})

/* ── 6. 对比度下限 ───────────────────────────────────────────────────── */

describe('qy-sg-tokens 对比度', () => {
  const cases: Array<[string, Map<string, string>, string, string, number]> = [
    // 正文与次级正文压在各自底色上
    ['夜 正文', dark, '--foreground', '--background', 4.5],
    ['夜 次级正文', dark, '--muted-foreground', '--background', 4.5],
    ['昼 正文', light, '--foreground', '--background', 4.5],
    ['昼 次级正文', light, '--muted-foreground', '--background', 4.5],
    // 四个语义色会被当作正文色用(幽灵按钮就是 color: var(--destructive))
    ['夜 destructive', dark, '--destructive', '--background', 4.5],
    ['夜 success', dark, '--success', '--background', 4.5],
    ['夜 warning', dark, '--warning', '--background', 4.5],
    ['夜 info', dark, '--info', '--background', 4.5],
    ['昼 destructive', light, '--destructive', '--background', 4.5],
    ['昼 success', light, '--success', '--background', 4.5],
    ['昼 warning', light, '--warning', '--background', 4.5],
    ['昼 info', light, '--info', '--background', 4.5],
    // 强调色本身是文字色:LAB MEMO 序号、编号行序号、铭牌全是 color: var(--primary)。
    // 昼间正是这一条把 --primary 逼到 L≈0.54 的。
    ['夜 强调色当文字', dark, '--primary', '--background', 4.5],
    ['昼 强调色当文字', light, '--primary', '--background', 4.5],
    // 压在强调色上的字(shadcn 的 bg-primary text-primary-foreground)
    ['夜 强调色上的字', dark, '--primary-foreground', '--primary', 4.5],
    ['昼 强调色上的字', light, '--primary-foreground', '--primary', 4.5],
    // 表头横幅昼夜同一条公式,两端(金 / 橙)的字都必须读得清
    ['夜 横幅字压在橙端', dark, '--qy-sg-band-fg', '--qy-sg-ember', 4.5],
    ['昼 横幅字压在橙端', light, '--qy-sg-band-fg', '--qy-sg-ember', 4.5],
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
