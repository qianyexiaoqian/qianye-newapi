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
 * 首页默认文案覆盖的契约测试。
 *
 * `home-{en,zh}.json` 是一份**故意覆盖上游键**的资源包：键是
 * `features/home/` 组件里 `t('…')` 的英文原文，靠 `registerQyResources` 的
 * `overwrite=true` 压掉上游默认文案。上游 `locales/*.json` 里这些键一个都没
 * 登记，所以改文案只能走这条路 —— 代价是**一旦键写错，页面照常渲染，只是
 * 静默显示回英文原文**，评审看不出来。
 *
 * 三组用例分别堵住三种失败：
 *
 * 1. 【断链】某个键在 `features/home/` 里根本不存在（拼错、上游改了文案、
 *    或者一开始就抄错了）。覆盖不生效，首页显示英文原文。
 * 2. 【误伤】某个键同时被 `features/home/` 之外的组件用。覆盖是全局的，
 *    会把别处的字一起改掉 —— 例如 `Rate Limiting` 在系统设置里也在用，
 *    所以它刻意不在这份覆盖表里。
 * 3. 【语种漂移】en 与 zh 的键集合不一致。缺的那一侧会回落到英文原文，
 *    表现为"中文首页里夹着两句英文"。
 *
 * 注意第 2 组是**双向**的：新增覆盖键时它会红，别处新引用同名键时它也会红。
 */
import assert from 'node:assert/strict'
import { readFileSync, readdirSync, statSync } from 'node:fs'
import { dirname, join, relative } from 'node:path'
import { describe, test } from 'node:test'
import { fileURLToPath } from 'node:url'

const qyDir = join(dirname(fileURLToPath(import.meta.url)), '..')
const srcDir = join(qyDir, '..', '..')
const homeDir = join(srcDir, 'features', 'home')

const readJson = (path: string): Record<string, string> =>
  JSON.parse(readFileSync(path, 'utf8')) as Record<string, string>

const homeEn = readJson(join(qyDir, 'home-en.json'))
const homeZh = readJson(join(qyDir, 'home-zh.json'))

/** 递归收集 ts/tsx 源文件。 */
function collectSources(dir: string, out: string[] = []): string[] {
  for (const entry of readdirSync(dir)) {
    const full = join(dir, entry)
    if (statSync(full).isDirectory()) {
      if (entry === '__tests__' || entry === 'node_modules') continue
      collectSources(full, out)
      continue
    }
    if (/\.tsx?$/.test(entry)) out.push(full)
  }
  return out
}

const homeSources = collectSources(homeDir).map((f) => ({
  path: relative(srcDir, f),
  text: readFileSync(f, 'utf8'),
}))

/**
 * `i18n/static-keys.ts` 是给 `bun run i18n:sync` 看的键清单，不渲染任何东西
 * （它把首页那批常量表里的键列出来，好让扫描器找得到）。它命中一个键只说明
 * 那个键属于首页，正是我们要覆盖的对象，不是"首页之外的消费方"。
 */
const KEY_REGISTRY_ONLY = join(srcDir, 'i18n', 'static-keys.ts')

const outsideSources = collectSources(srcDir)
  .filter(
    (f) =>
      !f.startsWith(homeDir) && !f.startsWith(qyDir) && f !== KEY_REGISTRY_ONLY
  )
  .map((f) => ({ path: relative(srcDir, f), text: readFileSync(f, 'utf8') }))

/** 键在源码里的写法恒为单引号字符串字面量（`t('…')` 或常量表里的数组项）。 */
const literalOf = (key: string) => `'${key}'`

/* ── 1. 每个覆盖键都真的落在首页上 ──────────────────────────────────── */

describe('首页文案覆盖有落点', () => {
  for (const key of Object.keys(homeZh)) {
    test(`features/home 里存在 ${JSON.stringify(key.slice(0, 40))}`, () => {
      const hit = homeSources.some((f) => f.text.includes(literalOf(key)))
      assert.ok(
        hit,
        `覆盖键在 features/home/ 里找不到引用，改了也不会显示：${key}`
      )
    })
  }
})

/* ── 2. 覆盖不会波及首页之外 ────────────────────────────────────────── */

test('没有一个覆盖键被 features/home 之外的组件使用', () => {
  const collateral: string[] = []
  for (const key of Object.keys(homeZh)) {
    for (const file of outsideSources) {
      if (file.text.includes(literalOf(key))) {
        collateral.push(`${key} ← ${file.path}`)
      }
    }
  }
  assert.deepEqual(
    collateral,
    [],
    `这些键在首页之外也有人用，全局覆盖会顺手改掉别处的字：\n${collateral.join('\n')}`
  )
})

/* ── 3. en / zh 键集合一致 ──────────────────────────────────────────── */

describe('首页文案的两个语种对齐', () => {
  test('键数相等', () => {
    assert.equal(Object.keys(homeEn).length, Object.keys(homeZh).length)
  })

  test('键集合完全相同', () => {
    assert.deepEqual(Object.keys(homeEn).sort(), Object.keys(homeZh).sort())
  })

  test('没有空文案', () => {
    for (const [key, value] of [
      ...Object.entries(homeEn),
      ...Object.entries(homeZh),
    ]) {
      assert.ok(value.trim().length > 0, `${key} 的译文为空`)
    }
  })
})

/* ── 4. 覆盖包确实被注册进去了 ──────────────────────────────────────── */

test('home-*.json 被 registerQyResources 合并进 bundle', () => {
  // 断链的最后一种形态：文件写好了，但没人 import。
  const indexText = readFileSync(join(qyDir, 'index.ts'), 'utf8')
  assert.match(indexText, /from '\.\/home-en\.json'/)
  assert.match(indexText, /from '\.\/home-zh\.json'/)
  assert.match(indexText, /\.\.\.homeEn/)
  assert.match(indexText, /\.\.\.homeZhCN/)
})
