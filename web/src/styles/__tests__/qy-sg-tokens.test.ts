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
 * Midnight Signal 令牌层的契约测试。
 *
 * 这里钉的是七件"改坏了肉眼看不出来、但用户会中招"的事:
 *
 * 1. 一支色相。参考稿的原话是 the palette is 95% achromatic and one violet。
 *    落到硬规则:每一个 oklch 取值要么彩度恰好为 0(中性),要么色相角恰好是
 *    236(那支蓝)。例外只有四个功能性语义色与借用它们的两档图表色,
 *    在下面按名单登记。谁再塞一个族外的强调色进来,直接红。
 *    (色相角本身是可以换的 —— 参考稿给的是紫 305.1,项目方裁定换成浅蓝 236。
 *    本用例钉的是"全站只有一支有彩色相",不是"那支必须是某个具体值"。)
 * 2. 面的阶梯。六个面不各自挑颜色,而是同一支墨色的六档浓度:夜间全部比画布亮、
 *    昼间全部比画布暗,且离画布的距离单调递增、昼夜严格对称。
 *    方向搞反或次序搞乱在渲染上不报错,只是"层次有点怪"、hover 往错误方向走。
 * 3. 取材锚点。五个锚点直接来自参考稿的十六进制,精确换算不许目测。
 * 4. 昼夜覆写完整性。夜块只覆写"公式本身不同"的量,漏掉一个,夜间就会静默
 *    继承昼间的值;反过来,公式一致的量在夜块重复声明,就是同一概念的第二份
 *    拷贝,迟早各自漂移。
 * 5. 声明唯一性 + 消费方。--qy-sg-* 在三个文件里合起来只能声明一次,
 *    且每一个都必须有 var() 消费方 ——「定义了没有消费方」是本仓库累计出现
 *    十几次的头号缺陷形状,这一条是专门为它加的。
 * 6. 对比度下限。近黑画布给了很大余量,但六档面把次级正文两头都挤住:
 *    accent 的 9% 正是"次级正文压在 hover 底上仍有 4.5:1"的上界。
 *    有人为了"层次再明显一点"把 accent 拉到 12%,正文就掉到 4.1:1 ——
 *    只有算出来才知道。
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

/** 品牌色相角。全站唯一允许的有彩色相,换色时只改这一个常量与锚点表。 */
const BRAND_HUE = 236

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

const OKLCH_LITERAL = /^oklch\(\s*([\d.]+)\s+([\d.]+)\s+([\d.]+)\s*\)$/i

/** 解析一个可能是 oklch(...) 或 var(--other) 的取值,var 在同块内解一层。 */
function oklchOf(name: string, scope: Map<string, string>): Oklch {
  const value = scope.get(name) ?? light.get(name)
  assert.ok(value, `${name} 未声明`)
  const varMatch = /^var\(\s*(--[a-z0-9-]+)\s*\)$/i.exec(value)
  if (varMatch) return oklchOf(varMatch[1], scope)
  const m = OKLCH_LITERAL.exec(value)
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

/* ── 1. 一支色相 ─────────────────────────────────────────────────────── */

// 唯一允许落在族外色相上的四支:后台必须能把失败/成功/警告/提示区分开,
// 那是功能不是装饰。chart-3 / chart-4 是它们在图表里的别名,不是第五、第六种色。
const OFF_FAMILY = new Set([
  '--destructive',
  '--success',
  '--warning',
  '--info',
  '--chart-3',
  '--chart-4',
])

describe('qy-sg-tokens 一支色相', () => {
  for (const [modeLabel, scope] of [
    ['昼', light],
    ['夜', dark],
  ] as const) {
    test(`${modeLabel}间每个取值要么彩度为 0,要么色相角是 ${BRAND_HUE}`, () => {
      const offenders: string[] = []
      for (const [name, value] of scope) {
        const m = OKLCH_LITERAL.exec(value)
        if (!m || OFF_FAMILY.has(name)) continue
        const c = Number(m[2])
        const h = Number(m[3])
        if (c === 0 || h === BRAND_HUE) continue
        offenders.push(`${name}: ${value}`)
      }
      assert.deepEqual(
        offenders,
        [],
        `这些取值既不是中性档也不在那支蓝上,等于往 95% 中性的色板里塞了第三种颜色:\n${offenders.join('\n')}`
      )
    })
  }

  test('蓝的两档昼夜共用同一个色相角', () => {
    for (const name of ['--primary', '--qy-sg-mist']) {
      assert.equal(oklchOf(name, light).h, BRAND_HUE, `${name} 昼间不在族内`)
      assert.equal(oklchOf(name, dark).h, BRAND_HUE, `${name} 夜间不在族内`)
    }
  })

  test('昼间的蓝是夜间的"压深"档而不是另换一色', () => {
    // 明度必须降(纸面上才读得出),彩度不许暴涨(否则就不是同一支蓝了)。
    for (const name of ['--primary', '--qy-sg-mist']) {
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
})

/* ── 2. 面的阶梯 ─────────────────────────────────────────────────────── */

// 洗色浓度从低到高。语义上离画布越远表示层级越高:
// sidebar 最贴近画布,accent 是 hover 面。
const SURFACE_LADDER = [
  '--sidebar',
  '--card',
  '--popover',
  '--muted',
  '--secondary',
  '--accent',
]

describe('qy-sg-tokens 面的阶梯', () => {
  test('夜间每个面都比画布亮、昼间都比画布暗', () => {
    const nightBg = oklchOf('--background', dark).l
    const dayBg = oklchOf('--background', light).l
    for (const name of SURFACE_LADDER) {
      assert.ok(
        oklchOf(name, dark).l > nightBg,
        `${name} 夜间应比画布亮:面是墨色(纸白)洗上去的`
      )
      assert.ok(
        oklchOf(name, light).l < dayBg,
        `${name} 昼间应比纸面暗:面是墨色(近黑)洗上去的`
      )
    }
  })

  test('离画布的距离沿阶梯单调递增', () => {
    for (const [label, scope] of [
      ['昼', light],
      ['夜', dark],
    ] as const) {
      const bg = oklchOf('--background', scope).l
      const gaps = SURFACE_LADDER.map((n) => Math.abs(oklchOf(n, scope).l - bg))
      for (let i = 1; i < gaps.length; i += 1) {
        assert.ok(
          gaps[i] > gaps[i - 1],
          `${label}间 ${SURFACE_LADDER[i]} 没有比 ${SURFACE_LADDER[i - 1]} 更远离画布` +
            `(${gaps[i - 1].toFixed(3)} → ${gaps[i].toFixed(3)}),hover 会往错误方向走`
        )
      }
    }
  })

  test('昼夜的洗色浓度严格对称', () => {
    // 同一条公式的两个方向,浓度必须一致 —— 差了就说明有人只调了一边。
    const nightBg = oklchOf('--background', dark).l
    const dayBg = oklchOf('--background', light).l
    for (const name of SURFACE_LADDER) {
      const nightGap = oklchOf(name, dark).l - nightBg
      const dayGap = dayBg - oklchOf(name, light).l
      assert.ok(
        Math.abs(nightGap - dayGap) < 0.0015,
        `${name} 的洗色浓度昼夜不等:夜 ${nightGap.toFixed(3)} / 昼 ${dayGap.toFixed(3)}`
      )
    }
  })
})

/* ── 3. 取材锚点 ─────────────────────────────────────────────────────── */

describe('qy-sg-tokens 取材锚点', () => {
  // 全部来自 qianye/docs/ui-reference/DESIGN.md 的十六进制,精确换算。
  // 钉住它们是为了让"有人凭手感把颜色挪一挪"必须先解释清楚新的取材来源。
  const anchors: Array<[string, string, string]> = [
    ['--background', 'oklch(0.14 0 236)', '#090909 Near Black'],
    ['--foreground', 'oklch(0.981 0 236)', '#f7f9fa Almost White'],
    ['--primary', 'oklch(0.76 0.14 236)', '#43befd 品牌浅蓝'],
    ['--qy-sg-mist', 'oklch(0.86 0.075 236)', '#a2d9fc 蓝的第二档'],
    ['--muted-foreground', 'oklch(0.609 0 236)', '#828384 Steel'],
  ]
  for (const [name, expected, provenance] of anchors) {
    test(`夜 ${name} = ${provenance}`, () => {
      assert.equal(dark.get(name), expected)
    })
  }
})

/* ── 4. 昼夜覆写完整性 ───────────────────────────────────────────────── */

// 昼夜取值必须不同的私有派生量。漏一个,夜间就静默继承昼间的值。
const mustOverride = [
  '--qy-sg-hair',
  '--qy-sg-hair-strong',
  '--qy-sg-mist',
  '--qy-sg-glow',
]

// 公式两边一致的量【不能】在夜块重复声明:同一个概念两处定义会各自漂移。
// 它们 var() 引用的标准变量已经在夜块换了值,会自动跟着翻转。
const sharedFormula = [
  '--qy-sg-sans',
  '--qy-sg-mono',
  '--qy-sg-wash',
  '--qy-sg-wash-strong',
  '--qy-sg-faint',
  '--qy-sg-bloom',
  '--qy-sg-selection-bg',
  '--qy-sg-selection-fg',
  '--qy-sg-ease',
  '--qy-sg-pill',
  '--qy-sg-blur',
  '--qy-sg-sec-gap',
  '--qy-sg-head-gap',
  '--qy-sg-leading',
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

/* ── 5. 声明唯一性与消费方 ───────────────────────────────────────────── */

const themeFiles = readdirSync(stylesDir).filter(
  (f) => f.startsWith('qy-sg-') && f.endsWith('.css')
)
const themeSources = new Map(
  themeFiles.map((f) => [
    f,
    readFileSync(join(stylesDir, f), 'utf8').replaceAll(
      /\/\*[\s\S]*?\*\//g,
      ''
    ),
  ])
)

test('每个 --qy-sg-* 在三个文件里合起来只声明一次', () => {
  // tokens 管颜色与字体轴,shape 管几何量与配方,两者不许有交集;
  // apply 一个都不许声明 —— 它只写 var()。
  const owner = new Map<string, string>()
  const offenders: string[] = []
  for (const [file, text] of themeSources) {
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
  assert.equal(owner.get('--qy-sg-route-tile'), 'qy-sg-shape.css')
  assert.equal(owner.get('--qy-sg-bloom'), 'qy-sg-tokens.css')
})

test('每个 --qy-sg-* 都至少有一个 var() 消费方', () => {
  // 「定义了没有消费方」是本仓库累计出现十几次的头号缺陷形状:
  // 它在渲染上完全不存在,评审也看不出来,只有反向扫一遍才知道。
  const declared = new Set<string>()
  const consumed = new Set<string>()
  for (const text of themeSources.values()) {
    for (const m of text.matchAll(/(--qy-sg-[a-z0-9-]+)\s*:/g)) {
      declared.add(m[1])
    }
    for (const m of text.matchAll(/var\(\s*(--qy-sg-[a-z0-9-]+)/g)) {
      consumed.add(m[1])
    }
  }
  const orphans = [...declared].filter((n) => !consumed.has(n)).sort()
  assert.deepEqual(
    orphans,
    [],
    `这些派生量声明了但没人 var() 它:${orphans.join(', ')}`
  )
})

/* ── 6. 对比度下限 ───────────────────────────────────────────────────── */

describe('qy-sg-tokens 对比度', () => {
  // 正文与次级正文压在【每一个面】上,不只是压在画布上:
  // 一张卡片里的说明文字落在 --card 上,菜单项 hover 时落在 --accent 上。
  const surfaces = ['--background', ...SURFACE_LADDER]
  for (const [modeLabel, scope] of [
    ['昼', light],
    ['夜', dark],
  ] as const) {
    for (const fg of ['--foreground', '--muted-foreground']) {
      for (const bg of surfaces) {
        test(`${modeLabel} ${fg} 压在 ${bg} 上 ≥ 4.5:1`, () => {
          const ratio = contrast(fg, bg, scope)
          assert.ok(
            ratio >= 4.5,
            `只有 ${ratio.toFixed(2)}:1 —— 面的洗色浓度已经把次级正文挤下去了`
          )
        })
      }
    }
  }

  const pairs: Array<[string, string]> = [
    // shadcn 成对使用的前景/背景
    ['--card-foreground', '--card'],
    ['--popover-foreground', '--popover'],
    ['--secondary-foreground', '--secondary'],
    ['--accent-foreground', '--accent'],
    ['--sidebar-foreground', '--sidebar'],
    // 压在紫填充上的字(bg-primary text-primary-foreground)
    ['--primary-foreground', '--primary'],
    // 四个语义色会被当作正文色用(幽灵按钮就是 color: var(--destructive)),
    // 同时它们也当徽章底色用,所以两个方向都要过
    ['--destructive', '--background'],
    ['--success', '--background'],
    ['--warning', '--background'],
    ['--info', '--background'],
    ['--destructive-foreground', '--destructive'],
    ['--success-foreground', '--success'],
    ['--warning-foreground', '--warning'],
    ['--info-foreground', '--info'],
    // 蓝的两档都会当文字色用:--primary 是上游一堆 text-primary 的兜底,
    // --qy-sg-mist 是本主题自己的戳记色。昼间正是这一条把 --primary 逼到 L≈0.53 的。
    ['--primary', '--background'],
    ['--qy-sg-mist', '--background'],
  ]
  for (const [modeLabel, scope] of [
    ['昼', light],
    ['夜', dark],
  ] as const) {
    for (const [fg, bg] of pairs) {
      test(`${modeLabel} ${fg} 压在 ${bg} 上 ≥ 4.5:1`, () => {
        const ratio = contrast(fg, bg, scope)
        assert.ok(ratio >= 4.5, `只有 ${ratio.toFixed(2)}:1`)
      })
    }
  }
})
