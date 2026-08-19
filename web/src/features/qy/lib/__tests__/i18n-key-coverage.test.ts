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

import { KIND_I18N_KEY, QY_ERROR_CODE_I18N } from '../api'

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
  // 渠道批量操作的单条结局码 → 文案。走 QY_CHANNEL_BATCH_ITEM_I18N 查表，
  // 再由 `t(known)` 取出，字面量扫描器同样看不见。清单对齐后端
  // `qianye/modules/channelops/errors.go` 的五个 itemCode 常量。
  'qy_chops_item_not_found',
  'qy_chops_item_db_error',
  'qy_chops_item_aborted',
  'qy_chops_item_no_change',
  'qy_chops_item_not_applied',
  // 抽奖出款的类型码 → 文案。走 `t(\`qy_lot_payout_kind_${kind}\`)`，键写在模板
  // 字符串里，上面那个字面量扫描器同样看不见。清单对齐后端
  // `qianye/modules/lottery/model.go` 的四个 Payout Kind 常量。
  //
  // `_text` 曾经落在这条清单与扫描器的**交集盲区**里：它既不是字面量、也没登记
  // 在这里，于是其余 50 个键补进语言包之后测试会全绿，而文本奖那一行仍然渲染成
  // 英文单词 `text`（t() 带了 defaultValue: kind，所以连裸键都看不到）。
  'qy_lot_payout_kind_prize',
  'qy_lot_payout_kind_win',
  'qy_lot_payout_kind_refund',
  'qy_lot_payout_kind_text',
  // 「为什么是这个结果」复算不出结论时的四种如实交代。同样是模板字符串键
  // （`t(\`qy_lot_why_blocked_${explain.blocked}\`)`）。清单对齐
  // `pages/lottery/lib/explain.ts` 的 QyLotExplainBlocked 四个取值。
  //
  // 这四条尤其不能漏：它们出现的时刻正是"复算给不出结论"的时刻，而那时用户
  // 最需要读懂的就是这句话；渲染成裸键会让人以为是页面坏了，而不是证据链缺页。
  'qy_lot_why_blocked_incomplete',
  'qy_lot_why_blocked_not_revealed',
  'qy_lot_why_blocked_not_in_roster',
  'qy_lot_why_blocked_voided',
  // 套餐发售时间窗的四态徽章。`planSaleBadge()` 返回 `{ labelKey }`，
  // 单元格写的是 `t(badge.labelKey)` —— 字面量扫描器看不见。清单对齐
  // `features/subscriptions/lib/sale-window.ts` 里 planSaleBadge 的四个返回值。
  //
  // 漏掉的表现是管理端套餐列表的「状态」列整列渲染成裸键 `qy_plan_sale_state_*`，
  // 而那一列正是运营判断"这个套餐现在卖不卖得出去"的唯一入口。
  'qy_plan_sale_state_disabled',
  'qy_plan_sale_state_upcoming',
  'qy_plan_sale_state_on_sale',
  'qy_plan_sale_state_ended',
  // 受限账号可达面的分档文案。页面写的是
  // `t(\`qy_ra_cap_${cap.key}\`, { defaultValue: cap.key })` —— 字面量扫描器
  // 看不见它，而 defaultValue 会把缺键渲染成 `ticket` 这种裸英文单词，
  // 连"裸键"这个明显信号都没有。清单对齐后端
  // `qianye/controller/restricted_accounts.go` 里 restrictedCapabilities 的
  // 四个 Key，那份表由 Go 测试与会话白名单双向核对。
  'qy_ra_cap_self',
  'qy_ra_cap_notice',
  'qy_ra_cap_violation',
  'qy_ra_cap_ticket',
]

/**
 * 收集 `src/` 下（不含测试）所有 `t('qy_xxx')` 的键 → 首个出现的文件。
 *
 * # 为什么扫整个 src 而不只是 features/qy
 *
 * 扩展功能并不全都住在 `features/qy/` 里：套餐下钻弹窗挂在上游的
 * `features/subscriptions/` 下（它要出现在上游那张表的单元格里），渠道批量操作
 * 的接线在 `features/channels/`。只扫 qy 目录的话，这些文件里的 `qy_` 键处于
 * **守卫盲区** —— 别处的键补齐之后这条测试会转绿，而它们仍然缺失且没有任何信号，
 * 表现是弹窗标题渲染成裸键 `qy_plan_holders_title`。
 *
 * 判据本来就该是"键的前缀"而不是"文件的位置"：`t('qy_…')` 就是一个 qy 语言包的键。
 */
function collectTranslationKeys(): Map<string, string> {
  const found = new Map<string, string>()
  const walk = (dir: string) => {
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      const full = join(dir, entry.name)
      if (entry.isDirectory()) {
        if (entry.name !== '__tests__' && entry.name !== 'i18n') walk(full)
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
  walk(srcDir)
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

  test('错误码映射表指向的每个键都在两份语言包里', () => {
    // QY_ERROR_CODE_I18N / KIND_I18N_KEY 的**值**同样是 `t()` 的入参，只是
    // 经过一次查表，字面量扫描器看不见。原来全仓没有任何一处断言这些目标键
    // 真的落库了：漏掉一个的表现是 toast 上直接弹出字符串
    // `qy_err_chops_too_many`，而管理员正需要那句话告诉他下一步做什么。
    const missing: string[] = []
    for (const [source, key] of [
      ...Object.entries(QY_ERROR_CODE_I18N),
      ...Object.entries(KIND_I18N_KEY),
    ]) {
      if (!(key in enKeys)) missing.push(`${key} (en) <- code ${source}`)
      if (!(key in zhKeys)) missing.push(`${key} (zh) <- code ${source}`)
    }
    assert.deepEqual(missing, [], missing.join('\n'))
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
