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
import { existsSync, readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { describe, test } from 'node:test'
import { fileURLToPath } from 'node:url'

import { qyPageMeta } from '@/features/qy/lib/page-meta'
import en from '@/i18n/qy/en.json'
import zh from '@/i18n/qy/zh.json'

import { QY_BALANCE_SORTS } from '../types'

/**
 * 佣金余额页的接线断言。
 *
 * 这一页的真正风险不在渲染逻辑，而在三条**只会在运行时才现形**的链路：
 *
 *  1. 断链：它没有独立的侧栏入口（`qy-settlement` 分组已占满 7 行），
 *     唯一入口是佣金审核页上的那个按钮。按钮一旦被顺手删掉，整页就变成
 *     只有背下 URL 才能到达的死页，而所有单元测试照样全绿；
 *  2. 同一概念的第 N 份拷贝：排序口径与事由长度下限在前后端各写了一份，
 *     后端改了前端不会有任何反应 —— 用户看到的是"排序选了没用"和
 *     "填了 4 个字还被后端拒"；
 *  3. i18n 键缺失：`t()` 找不到键时原样吐出键名，页面上会出现
 *     `qy_cb_set_title` 这种字符串，而它不会让任何测试变红。
 */

// __tests__ → admin-commission-balances → pages → qy → features → src → web → 仓库根
const repoRoot = join(
  dirname(fileURLToPath(import.meta.url)),
  '..',
  '..',
  '..',
  '..',
  '..',
  '..',
  '..'
)
const webSrc = join(repoRoot, 'web', 'src')

const recordsPage = readFileSync(
  join(
    webSrc,
    'features',
    'qy',
    'pages',
    'admin-commission-records',
    'index.tsx'
  ),
  'utf8'
)
const routeFile = join(
  webSrc,
  'routes',
  '_authenticated',
  'qy',
  'admin',
  'commission-records',
  'balances',
  'index.tsx'
)
const backendSource = readFileSync(
  join(repoRoot, 'qianye', 'modules', 'commission', 'api_admin_balance.go'),
  'utf8'
)

const BALANCES_URL = '/qy/admin/commission-records/balances'
const enKeys = en as Record<string, string>
const zhKeys = zh as Record<string, string>

describe('佣金余额页的入口链路', () => {
  test('佣金审核页上必须有指向本页的链接 —— 那是它唯一的入口', () => {
    assert.ok(
      recordsPage.includes(`to='${BALANCES_URL}'`),
      `佣金审核页不再链向 ${BALANCES_URL}：本页没有侧栏入口，删掉这个按钮就等于把整页变成死链`
    )
  })

  test('路由文件存在且挂的是本页组件', () => {
    assert.ok(existsSync(routeFile), `缺少路由文件 ${routeFile}`)
    const source = readFileSync(routeFile, 'utf8')
    assert.ok(
      source.includes('QyAdminCommissionBalances'),
      '路由文件没有挂载 QyAdminCommissionBalances'
    )
    assert.ok(
      source.includes(
        "'/_authenticated/qy/admin/commission-records/balances/'"
      ),
      '路由 id 与目录结构不一致，生成的 routeTree 会指向别处'
    )
  })

  test('URL 嵌在 commission-records 之下，因此继承佣金审核的 LAB MEMO 编号', () => {
    // 这不是审美选择：`lib/pages.ts` 的 qy-settlement 分组已满 7 行，本页不能
    // 再登记一行；而没有登记的 URL 在 `qyPageMeta` 里会掉进 "00" 分支，
    // Steins Gate 区段头会显示 `LAB MEMO — 00`。嵌套让最长前缀规则接住它。
    const meta = qyPageMeta(BALANCES_URL)
    assert.notEqual(
      meta.no,
      '00',
      '本页脱离了 commission-records 前缀，区段头会退化成 LAB MEMO — 00'
    )
    assert.equal(meta.no, qyPageMeta('/qy/admin/commission-records').no)
    assert.equal(meta.jpKey, 'qy_sg_jp_a_commission_records')
  })
})

describe('前后端口径一致', () => {
  test('排序键与后端 balanceSortOrders 逐个对应', () => {
    // 后端拿不到白名单里的键就静默回落默认排序，前端多一个键的表现是
    // "选了没反应"，少一个键的表现是"后端支持的排序用不到"。两边都要钉。
    const block = backendSource.match(
      /var balanceSortOrders = map\[string\]string\{([\s\S]*?)\n\}/
    )
    assert.ok(block != null, '后端 balanceSortOrders 声明找不到了')
    const backendSorts = [...block[1].matchAll(/"([a-z_]+)":/g)].map(
      (m) => m[1]
    )
    assert.deepEqual([...QY_BALANCE_SORTS].sort(), backendSorts.sort())
  })

  test('事由长度下限与后端一致', () => {
    assert.ok(
      backendSource.includes('if len([]rune(reason)) < 4 {'),
      '后端事由下限变了，前端 REASON_MIN_RUNES 必须跟着改，否则按钮放行、后端 400'
    )
    const dialog = readFileSync(
      join(
        webSrc,
        'features',
        'qy',
        'pages',
        'admin-commission-balances',
        'components',
        'set-withdrawn-dialog.tsx'
      ),
      'utf8'
    )
    assert.ok(dialog.includes('const REASON_MIN_RUNES = 4'))
  })

  test('后端三个业务 code 都在前端 i18n 白名单里', () => {
    const api = readFileSync(
      join(webSrc, 'features', 'qy', 'lib', 'api.ts'),
      'utf8'
    )
    for (const code of [
      'qy_withdrawn_over_available',
      'qy_withdrawn_over_earned',
      'qy_withdrawn_overflow',
    ]) {
      assert.ok(
        backendSource.includes(`"${code}"`),
        `后端不再返回 ${code}，前端映射成了死代码`
      )
      assert.ok(
        api.includes(`${code}:`),
        `${code} 没登记进 QY_ERROR_CODE_I18N，用户会看到后端的中文原文`
      )
    }
  })
})

describe('i18n 键完整', () => {
  const used = [
    'qy_cb_title',
    'qy_cb_formula',
    'qy_cb_formula_hint',
    'qy_cb_earned',
    'qy_cb_clawback',
    'qy_cb_frozen',
    'qy_cb_withdrawn',
    'qy_cb_available',
    'qy_cb_check',
    'qy_cb_check_ok',
    'qy_cb_check_drift',
    'qy_cb_debt_blocked',
    'qy_cb_user_gone',
    'qy_cb_user_id_ph',
    'qy_cb_username_ph',
    'qy_cb_sort',
    'qy_cb_totals',
    'qy_cb_empty_title',
    'qy_cb_empty_desc',
    'qy_cb_set',
    'qy_cb_set_title',
    'qy_cb_set_target',
    'qy_cb_set_target_hint',
    'qy_cb_set_preview',
    'qy_cb_set_preview_noop',
    'qy_cb_set_dir_out',
    'qy_cb_set_dir_back',
    'qy_cb_set_over_ceiling',
    'qy_cb_set_reason',
    'qy_cb_set_reason_ph',
    'qy_cb_set_reason_hint',
    'qy_cb_set_submit',
    'qy_cb_set_ok',
    'qy_cb_set_noop',
    'qy_err_cb_over_available',
    'qy_err_cb_over_earned',
    'qy_err_cb_overflow',
    // 排序项的 key 是模板字符串拼出来的，静态扫不到，这里显式列全。
    ...QY_BALANCE_SORTS.map((sort) => `qy_cb_sort_${sort}`),
  ]

  test('本页用到的每个键在 en 与 zh 里都有', () => {
    for (const key of used) {
      assert.ok(zhKeys[key] != null, `zh.json 缺少 ${key}`)
      assert.ok(enKeys[key] != null, `en.json 缺少 ${key}`)
    }
  })

  test('带插值的文案两侧占位符一致', () => {
    // 占位符对不上时 i18next 不会报错，只会原样留下 `{{ceiling}}` ——
    // 而这几句恰好是在告诉运营"填这个数会发生什么"。
    for (const key of used) {
      const zhVars = [...zhKeys[key].matchAll(/\{\{(\w+)\}\}/g)]
        .map((m) => m[1])
        .sort()
      const enVars = [...enKeys[key].matchAll(/\{\{(\w+)\}\}/g)]
        .map((m) => m[1])
        .sort()
      assert.deepEqual(zhVars, enVars, `${key} 的插值变量两侧不一致`)
    }
  })
})
