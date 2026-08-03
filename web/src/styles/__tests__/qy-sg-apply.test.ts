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
 * Steins Gate 挂载层的契约测试。
 *
 * 令牌层那个测试管的是"颜色对不对";这个测试管的是"接上了没有"——
 * 主题瘦身之后最容易复发的四种缺陷,一条对一条:
 *
 * 1. 【断链 · CSS 侧】定义了一个 .qy-sg-* 类,却没有任何组件在用。
 *    第三版有 15 个这样的类(约 295 行实码),它们在渲染上完全不存在,
 *    评审也看不出来 —— 只有反向扫一遍 src 才知道。
 * 2. 【断链 · 组件侧】组件写了 className='qy-sg-xxx',而 CSS 里没有这个类。
 *    这一半更隐蔽:页面照常渲染,只是那处样式静默退回默认。
 * 3. 【规模回弹】挂载面从 25 个 slot 长回 184 个。名单在这里冻结,
 *    加一个都必须先改测试,顺带在 code review 里解释为什么值得。
 * 4. 【分层塌陷】shape 写规则、apply 写取值,两层职责一混就退回第三版那种
 *    "十个文件互相覆盖"的状态。这里从结构上钉住。
 *
 * 用例只解析源文件,不需要渲染引擎(本仓库前端测试跑在 node:test 下)。
 */
import assert from 'node:assert/strict'
import { readFileSync, readdirSync, statSync } from 'node:fs'
import { dirname, join, relative } from 'node:path'
import { describe, test } from 'node:test'
import { fileURLToPath } from 'node:url'

const stylesDir = join(dirname(fileURLToPath(import.meta.url)), '..')
const srcDir = join(stylesDir, '..')

const readCss = (name: string) => readFileSync(join(stylesDir, name), 'utf8')
const stripComments = (text: string) => text.replaceAll(/\/\*[\s\S]*?\*\//g, '')

const applyCss = readCss('qy-sg-apply.css')
const shapeCss = readCss('qy-sg-shape.css')
const entryCss = readCss('qy-steins-gate.css')
const themeCss = [readCss('qy-sg-tokens.css'), shapeCss, applyCss].join('\n')

/* ── 1. 三文件结构 ───────────────────────────────────────────────────── */

describe('Steins Gate 主题的文件结构', () => {
  test('只有 tokens / shape / apply 三个文件', () => {
    const files = readdirSync(stylesDir)
      .filter((f) => f.startsWith('qy-sg-') && f.endsWith('.css'))
      .sort()
    assert.deepEqual(files, [
      'qy-sg-apply.css',
      'qy-sg-shape.css',
      'qy-sg-tokens.css',
    ])
  })

  test('汇总入口按 tokens → shape → apply 的顺序引入,且不写规则', () => {
    const imports = [
      ...stripComments(entryCss).matchAll(/@import\s+'([^']+)'/g),
    ]
    assert.deepEqual(
      imports.map((m) => m[1]),
      ['./qy-sg-tokens.css', './qy-sg-shape.css', './qy-sg-apply.css']
    )
    // 入口只做 @import:剥掉注释与 import 之后不应剩下任何 `{`。
    const rest = stripComments(entryCss).replaceAll(/@import[^;]+;/g, '')
    assert.equal(rest.includes('{'), false, '汇总入口不允许写规则')
  })

  test('shape 层只声明自定义属性,不写任何组件选择器', () => {
    const body = stripComments(shapeCss)
    // 唯一允许的选择器就是主题根;有第二个 `{` 之前的选择器就说明分层塌了。
    const selectors = [...body.matchAll(/([^{}]+)\{/g)].map((m) =>
      m[1].trim().replaceAll(/\s+/g, ' ')
    )
    assert.deepEqual(selectors, ["[data-theme-preset='steins-gate']"])
    // 反过来,apply 层一个自定义属性都不许声明(取值只能来自 tokens / shape)。
    assert.equal(
      /(?<!var\()--qy-sg-[a-z0-9-]+\s*:/.test(stripComments(applyCss)),
      false,
      'apply 层不允许声明 --qy-sg-*,取值只能来自 tokens / shape'
    )
  })
})

/* ── 2. 挂载面冻结 ───────────────────────────────────────────────────── */

test('挂载的 data-slot 就是冻结的这 24 个', () => {
  // 第三版挂了 184 个。多出来的那些在"遮住颜色只看轮廓"的验收下一条也读不出来,
  // 却让每次上游升级都要重新核对。要加就先改这份名单。
  const mounted = [
    ...new Set(
      [...applyCss.matchAll(/data-slot='([a-z-]+)'/g)].map((m) => m[1])
    ),
  ].sort()
  assert.deepEqual(mounted, [
    'alert-dialog-content',
    'badge',
    'button',
    'card',
    'dialog-content',
    'drawer-content',
    'dropdown-menu-content',
    'input',
    'label',
    'native-select',
    'popover-content',
    'select-trigger',
    'sheet-content',
    'sidebar-group-label',
    'sidebar-header',
    'status-badge',
    'table',
    'table-body',
    'table-caption',
    'table-cell',
    'table-container',
    'table-head',
    'table-row',
    'textarea',
  ])
})

/* ── 3. 类名双向对账 ─────────────────────────────────────────────────── */

/** 递归收集 src 下的 ts/tsx(跳过 styles 自身与测试目录)。 */
function collectSources(dir: string, out: string[] = []): string[] {
  for (const entry of readdirSync(dir)) {
    const full = join(dir, entry)
    const skipped =
      entry === 'styles' || entry === '__tests__' || entry === 'node_modules'
    if (statSync(full).isDirectory()) {
      if (!skipped) collectSources(full, out)
      continue
    }
    if (/\.tsx?$/.test(entry)) out.push(full)
  }
  return out
}

const sourceFiles = collectSources(srcDir)

/**
 * 组件侧用到的 qy-sg-* 类名。
 *
 * 前面必须不是 `-`:这样 `--qy-sg-stat-cols`(自定义属性)与 `data-qy-kv`
 * 这类属性锚点都会被排除,它们各有自己的用例。
 * 扫描前先剥掉注释里的 `qy-sg-xxx.css` 文件名引用,否则文件名会被当成类名。
 */
const usedClasses = new Map<string, string[]>()
for (const file of sourceFiles) {
  const text = readFileSync(file, 'utf8').replaceAll(
    /qy-sg-[a-z0-9-]+\.css/g,
    ''
  )
  for (const m of text.matchAll(/(?<![-\w])qy-sg-[a-z0-9-]+/g)) {
    const list = usedClasses.get(m[0]) ?? []
    list.push(relative(srcDir, file))
    usedClasses.set(m[0], list)
  }
}

const definedClasses = new Set(
  [...stripComments(themeCss).matchAll(/\.(qy-sg-[a-z0-9-]+)/g)].map(
    (m) => m[1]
  )
)

describe('qy-sg-* 类名双向对账', () => {
  test('CSS 里定义的每个类都至少有一个组件在用', () => {
    const orphans = [...definedClasses]
      .filter((c) => !usedClasses.has(c))
      .sort()
    assert.deepEqual(
      orphans,
      [],
      `这些类零引用,等于写了没接上:${orphans.join(', ')}`
    )
  })

  test('组件里用到的每个类在 CSS 里都有定义', () => {
    const missing = [...usedClasses.keys()]
      .filter((c) => !definedClasses.has(c))
      .sort()
    assert.deepEqual(
      missing,
      [],
      `这些类被组件引用但 CSS 里没有,样式会静默退回默认:${missing.join(', ')}`
    )
  })

  test('类名规模没有回弹', () => {
    // 第三版是 34 个类,其中 15 个零引用。冻结上限,防止再长回去。
    assert.ok(
      definedClasses.size <= 20,
      `qy-sg-* 类已经涨到 ${definedClasses.size} 个,超出上限 20`
    )
  })
})

/* ── 4. 属性锚点与自定义属性的接线 ───────────────────────────────────── */

describe('属性锚点接线', () => {
  // 这两个锚点不是类名,上面的双向对账扫不到,单独钉。
  const wires: Array<[string, string]> = [
    ['data-qy-kv', '键值明细行'],
    ['--qy-sg-stat-cols', '概览数字栏的列数'],
  ]
  for (const [anchor, label] of wires) {
    test(`${anchor}(${label})两侧都在`, () => {
      assert.ok(applyCss.includes(anchor), `${anchor} 在 apply 层没有消费方`)
      assert.ok(
        sourceFiles.some((f) => readFileSync(f, 'utf8').includes(anchor)),
        `${anchor} 在组件侧没有产出方`
      )
    })
  }
})

/* ── 5. 六条签名形状都还在 ───────────────────────────────────────────── */

describe('六条签名形状的落点', () => {
  // 验收标准是"遮住颜色只看轮廓,仍要认得出不是默认主题"。承载它的就是这六处;
  // 瘦身的边界也在这里 —— 再删就不达标了。
  const signatures: Array<[string, RegExp]> = [
    ['① 标注走等宽宽字距大写', /text-transform:\s*uppercase/],
    ['② 双层直角框的环配方', /clip-path:\s*var\(--qy-sg-frame-clip\)/],
    ['③ 表格每格独立盒', /border-spacing:\s*var\(--qy-sg-cell-gap\)/],
    ['③ 强调色表头带', /background:\s*var\(--qy-sg-band\)/],
    ['④ 侧栏英文副标与走线', /mask-image:\s*var\(--qy-sg-trace-tile\)/],
    ['⑤ 区段头序号与日文副标', /\.qy-sg-no|\.qy-sg-jp/],
    ['⑥ 状态徽章直角', /\[data-slot='status-badge'\]/],
  ]
  for (const [label, pattern] of signatures) {
    test(label, () => {
      assert.ok(pattern.test(applyCss), `${label} 在 apply 层找不到落点`)
    })
  }

  test('铭牌保持 table-caption 盒,不退回 inline-block', () => {
    // 浏览器实测:给 <caption> 写 display:inline-block 会让它脱离 caption 盒、
    // 被塞进一个匿名表格行，铭牌于是渲染在【表头带与首行之间】而不是表格上方。
    // 第三版就是这么写的，看截图才发现。这条把它钉住。
    const rule = /\[data-slot='table-caption'\]\s*\{([^}]*)\}/.exec(applyCss)
    assert.ok(rule, '找不到 table-caption 的规则')
    assert.match(rule[1], /display:\s*table-caption/)
    assert.doesNotMatch(rule[1], /display:\s*inline-block/)
  })
})
