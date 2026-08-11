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

import { QY_PAGES } from '@/features/qy/lib/pages'
import en from '@/i18n/qy/en.json'
import zh from '@/i18n/qy/zh.json'

import {
  qyCategoryTightens,
  qyEmptyCategoryForm,
  qyValidateCategoryForm,
} from '../lib/category-form'

/**
 * 违规类型页的接线断言。
 *
 * 这一页的真正风险不在渲染，而在四条**只会在运行时才现形**的链路：
 *
 *  1. **断链**。本仓已经四次出现"页面写完、路由建好、`lib/pages.ts` 里那一行
 *     忘了加"——站内一个入口都没有，只有手敲 URL 才到得了。全局守卫在
 *     `lib/__tests__/route-entry-guard.test.ts`，这里再钉一次本页那一行；
 *  2. **公示隔离被前端捅穿**。后端白名单不下发类型的内部名与内部说明，
 *     但只要用户端组件里出现一次 `remark` / `name`，隔离就白做了；
 *  3. **前后端校验漂移**。「勾了公示必须填公示标题」两边各写一份，
 *     前端放过的话用户端会看到一行空白标题；
 *  4. **i18n 键缺失**。`t()` 找不到键时原样吐出键名，页面上会出现
 *     `qy_vcat_col_name` 这种字符串,而它不会让任何测试变红。
 */

// __tests__ → admin-violation-categories → pages → qy → features → src → web → 仓库根
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
const CATEGORIES_URL = '/qy/admin/violation-categories'
const enKeys = en as Record<string, string>
const zhKeys = zh as Record<string, string>

const routeFile = join(
  webSrc,
  'routes',
  '_authenticated',
  'qy',
  'admin',
  'violation-categories',
  'index.tsx'
)

describe('违规类型页的入口链路', () => {
  test('页面表里必须有这一行 —— 本仓第四次栽在"写了但找不到"上', () => {
    const page = QY_PAGES.find((p) => p.url === CATEGORIES_URL)
    assert.ok(
      page != null,
      `${CATEGORIES_URL} 没有登记进 lib/pages.ts：路由在、组件在、站内零入口，只能手敲 URL`
    )
    assert.equal(
      page?.feature,
      'violation',
      '必须挂 violation 功能开关：站点关掉违规检测时这一页要跟着消失，而不是点进去空空如也'
    )
  })

  test('路由文件存在且挂的是本页组件', () => {
    assert.ok(existsSync(routeFile), `缺少路由文件 ${routeFile}`)
    const source = readFileSync(routeFile, 'utf8')
    assert.ok(
      source.includes('QyAdminViolationCategories'),
      '路由文件没有挂载 QyAdminViolationCategories'
    )
    assert.ok(
      source.includes("'/_authenticated/qy/admin/violation-categories/'"),
      '路由 id 与目录结构不一致，生成的 routeTree 会指向别处'
    )
  })
})

describe('用户端公示不得泄漏内部文案', () => {
  const card = readFileSync(
    join(
      webSrc,
      'features',
      'qy',
      'pages',
      'violations',
      'components',
      'categories-card.tsx'
    ),
    'utf8'
  )
  const userTypes = readFileSync(
    join(webSrc, 'features', 'qy', 'pages', 'violations', 'types.ts'),
    'utf8'
  )

  test('公示卡片里不出现 remark / 内部名字段', () => {
    // 后端根本不下发这两列，所以这里出现它们只可能是有人"从别的接口补回来"。
    // 内部说明写的就是匹配判据，公示它等于把绕过方法印给用户。
    for (const forbidden of ['.remark', 'category.name', 'internal']) {
      assert.ok(
        !card.includes(forbidden),
        `公示卡片里出现了 ${forbidden}：内部说明与内部名是"怎么判的"，公示它等于教用户怎么绕过`
      )
    }
  })

  test('用户端类型的字段集就是白名单本身', () => {
    // 与后端 userCategoryView 逐字对齐。多一列都要先回答
    // 「这一列会不会告诉用户怎么绕过」。
    const block = userTypes.slice(
      userTypes.indexOf('export type QyMyViolationCategory = {')
    )
    const body = block.slice(0, block.indexOf('}'))
    for (const forbidden of ['remark', 'key:', 'name:']) {
      assert.ok(
        !body.includes(forbidden),
        `QyMyViolationCategory 里出现了 ${forbidden}：它不在后端白名单里，写在这里只会诱导下一个人去补一个泄漏字段`
      )
    }
    for (const required of ['title', 'description', 'threshold', 'hit_count']) {
      assert.ok(
        body.includes(required),
        `QyMyViolationCategory 少了 ${required}：公示要回答"哪些类型、几次会被处置、我现在几次"`
      )
    }
  })
})

describe('前端校验与后端同口径', () => {
  test('勾了公示就必须填公示标题', () => {
    const t = ((key: string) => key) as never
    const values = {
      ...qyEmptyCategoryForm(),
      key: 'spam',
      name: '垃圾内容',
      published: true,
      public_title: '',
    }
    assert.equal(
      qyValidateCategoryForm(values, t),
      'qy_vcat_err_public_title_required',
      '放过它的话用户端会看到一行空白标题，而下一个人最省事的修法是回落到内部名'
    )
    assert.equal(
      qyValidateCategoryForm({ ...values, public_title: '垃圾内容' }, t),
      null
    )
    // 不公示时公示标题可以为空。
    assert.equal(
      qyValidateCategoryForm({ ...values, published: false }, t),
      null
    )
  })

  test('key 的取值域与后端 validateCategory 一致', () => {
    const t = ((key: string) => key) as never
    const base = { ...qyEmptyCategoryForm(), name: '类型' }
    assert.equal(
      qyValidateCategoryForm({ ...base, key: 'spam_v3-x' }, t),
      null
    )
    assert.equal(
      qyValidateCategoryForm({ ...base, key: '垃圾' }, t),
      'qy_vcat_err_key_charset'
    )
    assert.equal(
      qyValidateCategoryForm({ ...base, key: 'bad key' }, t),
      'qy_vcat_err_key_charset'
    )
    assert.equal(
      qyValidateCategoryForm({ ...base, key: '  ' }, t),
      'qy_vcat_err_key_required'
    )
  })

  test('"会不会扩大处置面"的判据与后端 categoryTightens 同形', () => {
    const form = (over: Record<string, unknown>) => ({
      ...qyEmptyCategoryForm(),
      enabled: true,
      threshold: '10',
      window_hours: '24',
      ...over,
    })
    const before = {
      id: 1,
      key: 'spam',
      name: 'n',
      remark: '',
      public_title: '',
      public_desc: '',
      published: false,
      enabled: true,
      window_hours: 24,
      threshold: 10,
      sort_order: 100,
      is_fallback: false,
      created_at: 0,
      updated_at: 0,
    }
    // 新建一律算收紧：判错方向的后果不对称 —— 漏判会让一批存量账号在管理员
    // 毫不知情的情况下越线，误判只是多点一次。
    assert.equal(qyCategoryTightens(null, form({})), true)
    assert.equal(qyCategoryTightens(null, form({ threshold: '0' })), false)
    assert.equal(qyCategoryTightens(null, form({ enabled: false })), false)
    assert.equal(
      qyCategoryTightens(before, form({ threshold: '3' })),
      true,
      '阈值调小 = 扩大处置面'
    )
    assert.equal(qyCategoryTightens(before, form({ threshold: '20' })), false)
    assert.equal(
      qyCategoryTightens(before, form({ window_hours: '72' })),
      true,
      '窗口变长 = 更久以前的命中重新算数'
    )
    assert.equal(
      qyCategoryTightens({ ...before, threshold: 0 }, form({})),
      true,
      '从"不出线"变成"出线"'
    )
  })
})

describe('i18n 键齐全', () => {
  const keys = [
    'qy_nav_a_violation_categories',
    'qy_sg_jp_a_violation_categories',
    'qy_vcat_create',
    'qy_vcat_edit',
    'qy_vcat_archive',
    'qy_vcat_saved',
    'qy_vcat_archived',
    'qy_vcat_col_name',
    'qy_vcat_col_threshold',
    'qy_vcat_col_rules',
    'qy_vcat_col_published',
    'qy_vcat_col_public_title',
    'qy_vcat_flag_fallback',
    'qy_vcat_threshold_value',
    'qy_vcat_threshold_off',
    'qy_vcat_published_yes',
    'qy_vcat_published_no',
    'qy_vcat_two_lines_note',
    'qy_vcat_form_desc',
    'qy_vcat_sec_internal',
    'qy_vcat_sec_public',
    'qy_vcat_sec_public_warning',
    'qy_vcat_sec_threshold',
    'qy_vcat_sec_threshold_desc',
    'qy_vcat_field_key',
    'qy_vcat_field_key_desc',
    'qy_vcat_field_key_fallback',
    'qy_vcat_field_name',
    'qy_vcat_field_name_desc',
    'qy_vcat_field_remark',
    'qy_vcat_field_remark_desc',
    'qy_vcat_field_published',
    'qy_vcat_field_public_title',
    'qy_vcat_field_public_desc',
    'qy_vcat_field_enabled',
    'qy_vcat_field_threshold',
    'qy_vcat_field_threshold_desc',
    'qy_vcat_field_window',
    'qy_vcat_field_sort',
    'qy_vcat_err_key_required',
    'qy_vcat_err_key_charset',
    'qy_vcat_err_name_required',
    'qy_vcat_err_public_title_required',
    'qy_vcat_err_window_range',
    'qy_vcat_err_threshold_range',
    'qy_vcat_confirm_title',
    'qy_vcat_confirm_desc',
    'qy_vcat_archive_title',
    'qy_vcat_archive_desc',
    'qy_vcat_archive_records_note',
    'qy_vio_col_category',
    'qy_vio_cat_title',
    'qy_vio_cat_desc',
    'qy_vio_cat_account_line',
    'qy_vio_cat_account_line_off',
    'qy_vio_cat_threshold',
    'qy_vio_cat_threshold_off',
    'qy_vio_cat_progress',
    'qy_vio_cat_progress_off',
    'qy_vio_cat_any_line_note',
  ]

  test('zh / en 都有,且没有一个是空串', () => {
    for (const key of keys) {
      assert.ok(zhKeys[key] != null && zhKeys[key] !== '', `zh 缺少 ${key}`)
      assert.ok(enKeys[key] != null && enKeys[key] !== '', `en 缺少 ${key}`)
    }
  })

  test('归档文案必须说清"历史记录保留"', () => {
    // 这句话是删除语义的全部:管理员按下"归档"之前必须知道历史记录不会被删。
    // 文案退化成"确定删除吗"就等于把一个可逆动作说成不可逆的。
    assert.ok(
      zhKeys.qy_vcat_archive_records_note.includes('不是删除'),
      '归档确认文案不再说明"归档不是删除、历史记录全部保留"'
    )
    assert.ok(
      enKeys.qy_vcat_archive_records_note.includes('not deletion'),
      'archive note no longer states that records are kept'
    )
  })
})
