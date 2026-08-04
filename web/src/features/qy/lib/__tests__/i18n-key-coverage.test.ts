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
import assert from 'node:assert/strict'
import { readFileSync, readdirSync } from 'node:fs'
import { dirname, join, relative } from 'node:path'
import { describe, test } from 'node:test'
import { fileURLToPath } from 'node:url'

import en from '@/i18n/qy/en.json'
import zh from '@/i18n/qy/zh.json'

/**
 * 「组件里 `t('qy_…')` 用到的键，必须真的在两份语言包里」的机器校验。
 *
 * 在它之前这条契约只靠人盯：漏落一个键不会有任何信号 —— `bun run typecheck`
 * exit 0，`bun test src/features/qy` 全绿，i18next 找不到键时直接把**键名本身**
 * 渲染出来。表现是页面上出现一行 `qy_cfg_health_modules`，而它只在真的打开
 * 那个页面、那个状态分支时才看得见（红色告警、失败态提示、aria-label 尤其
 * 藏得深）。这与本仓反复栽的那个形状同源：代码全都在，只是配置/文案没跟上，
 * 而且没有任何东西会红。
 *
 * 只扫 `t('…')` 这一种字面量形式，是为了让这条测试**零误报**：`qy_` 开头的
 * 字符串还有后端错误码（`qy_feature_off`）、表名（`qy_settings`）、查询键等，
 * 它们本来就不该在语言包里。查表型的动态键扫不到，见下面 QY_DYNAMIC_KEYS。
 */

// __tests__ → lib → qy → features → src
const srcDir = join(
  dirname(fileURLToPath(import.meta.url)),
  '..',
  '..',
  '..',
  '..'
)
const qyDir = join(srcDir, 'features', 'qy')

const enKeys = en as Record<string, string>
const zhKeys = zh as Record<string, string>

/**
 * 这些键不是写在 `t('…')` 里的，而是先进查表（`Record<状态, 键名>`）再由
 * `t(表[状态])` 取出来，因此上面那个扫描器看不见它们。
 *
 * 与 `pages/availability/__tests__/outcome-i18n-keys.test.ts` 同一个理由：
 * 按字面量扫描的工具会把这类键报成「零引用」并删掉，而删掉的直接后果是
 * 界面上出现裸键。清单对齐后端 `qianye/config/sections.go` 的 5 个段状态常量。
 */
const QY_DYNAMIC_KEYS = [
  'qy_cfg_health_module_st_declared',
  'qy_cfg_health_module_st_missing_section',
  'qy_cfg_health_module_st_missing_key',
  'qy_cfg_health_module_st_default_on',
  'qy_cfg_health_module_st_ungated',
]

/** 收集 `features/qy/` 下（不含测试）所有 `t('qy_xxx')` 的键 → 首个出现的文件。 */
function collectTranslationKeys(): Map<string, string> {
  const found = new Map<string, string>()
  const walk = (dir: string) => {
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      const full = join(dir, entry.name)
      if (entry.isDirectory()) {
        if (entry.name !== '__tests__') walk(full)
        continue
      }
      if (!/\.tsx?$/.test(entry.name)) continue
      const source = readFileSync(full, 'utf8')
      for (const m of source.matchAll(
        /(?:^|[^\w.$])t\(\s*'(qy_[a-z0-9_]+)'/g
      )) {
        if (!found.has(m[1])) found.set(m[1], relative(srcDir, full))
      }
    }
  }
  walk(qyDir)
  return found
}

describe('qy i18n key coverage', () => {
  test('每个 t() 用到的 qy_ 键在 en 与 zh 里都存在', () => {
    const used = collectTranslationKeys()
    assert.ok(
      used.size > 1000,
      `扫描器只找到 ${used.size} 个键，几乎可以肯定是正则失效了 —— ` +
        '一个扫不到任何东西的守卫会永远全绿'
    )

    const missing: string[] = []
    for (const [key, file] of used) {
      if (!(key in enKeys)) missing.push(`${key} (en) <- ${file}`)
      if (!(key in zhKeys)) missing.push(`${key} (zh) <- ${file}`)
    }
    assert.deepEqual(
      missing,
      [],
      `以下键在组件里被 t() 用到，但语言包里没有。i18next 会把键名原样渲染到界面上，而 typecheck 与其余单测都不会有任何信号：\n${missing.join('\n')}`
    )
  })

  test('查表得到的动态键同样必须在两份语言包里', () => {
    for (const key of QY_DYNAMIC_KEYS) {
      assert.ok(key in enKeys, `en.json 缺少动态键 ${key}`)
      assert.ok(key in zhKeys, `zh.json 缺少动态键 ${key}`)
    }
  })

  test('en 与 zh 的键集合完全一致', () => {
    const onlyEn = Object.keys(enKeys).filter((k) => !(k in zhKeys))
    const onlyZh = Object.keys(zhKeys).filter((k) => !(k in enKeys))
    // 只落一侧不会报错，只会在另一种语言下回落到对侧文案 ——
    // 表现为中文界面里夹着一句英文，评审基本看不出来。
    assert.deepEqual(onlyEn, [], `只在 en.json 里的键: ${onlyEn.join(', ')}`)
    assert.deepEqual(onlyZh, [], `只在 zh.json 里的键: ${onlyZh.join(', ')}`)
  })

  test('没有空文案', () => {
    const blank = Object.keys(enKeys)
      .filter((k) => enKeys[k].trim() === '' || zhKeys[k].trim() === '')
      .sort()
    assert.deepEqual(blank, [], `空文案会让界面上出现一处空白: ${blank}`)
  })
})
