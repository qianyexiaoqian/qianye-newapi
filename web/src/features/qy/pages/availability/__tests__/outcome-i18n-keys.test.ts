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
import { describe, test } from 'node:test'

import en from '@/i18n/qy/en.json'
import zh from '@/i18n/qy/zh.json'

import { qyAvailOutcomeKey } from '../constants'

/**
 * `qy_avl_outcome_*` 是**动态拼出来的** i18n key（`qyAvailOutcomeKey(row.top_reason)`），
 * 所以任何按字面量扫描 `t('...')` 的清理工具都会把这 12 个键报成「零引用」。
 * 那是假阳性：删掉的直接后果是用户端可用率页的「失败原因」列、口径卡片与趋势
 * 悬浮层全部退化成裸 key。
 *
 * 移除管理端可用率页那一轮差点踩中，这里用断言把它钉死。清单直接对齐
 * `qianye/modules/availability/outcome.go` 的 Outcome 枚举：后端加一态而前端漏补
 * 文案，同样会在这里红。
 */
const QY_AVAIL_OUTCOMES = [
  'success',
  'soft_fail',
  'no_channel',
  'timeout',
  'upstream_error',
  'rate_limit',
  'client_error',
  'internal_error',
  'quota_error',
  'violation',
  'client_gone',
  'channel_test',
]

/** 六态样式表的 key 同样是运行时查表得到的（`getQyAvailStateStyle`），一并守住。 */
const QY_AVAIL_STATES = [
  'ok',
  'degraded',
  'down',
  'low_sample',
  'no_data',
  'not_offered',
  'unknown',
]

/** 只服务已移除的管理端可用率页的键前缀，留下就是零消费方的死键。 */
const QY_AVAIL_ADMIN_ONLY_PREFIXES = [
  'qy_avl_admin_',
  'qy_avl_alert_',
  'qy_avl_pipeline_',
  'qy_avl_stat_',
  'qy_avl_storage_',
]

const QY_AVAIL_ADMIN_NAV_KEYS = [
  'qy_nav_a_availability',
  'qy_sg_jp_a_availability',
  'qy_sg_nav_en_a_availability',
]

const enMap = en as Record<string, string>
const zhMap = zh as Record<string, string>

describe('qy availability i18n keys', () => {
  test('keeps every dynamically built outcome key in both locales', () => {
    for (const outcome of QY_AVAIL_OUTCOMES) {
      const key = qyAvailOutcomeKey(outcome)
      assert.ok(enMap[key], `en.json 缺少 ${key}`)
      assert.ok(zhMap[key], `zh.json 缺少 ${key}`)
    }
  })

  test('keeps every state label in both locales', () => {
    for (const state of QY_AVAIL_STATES) {
      const key = `qy_avl_state_${state}`
      assert.ok(enMap[key], `en.json 缺少 ${key}`)
      assert.ok(zhMap[key], `zh.json 缺少 ${key}`)
    }
  })

  test('drops every admin-only availability key', () => {
    for (const prefix of QY_AVAIL_ADMIN_ONLY_PREFIXES) {
      assert.deepEqual(
        Object.keys(enMap).filter((k) => k.startsWith(prefix)),
        [],
        `en.json 残留已移除管理页的键前缀 ${prefix}`
      )
      assert.deepEqual(
        Object.keys(zhMap).filter((k) => k.startsWith(prefix)),
        [],
        `zh.json 残留已移除管理页的键前缀 ${prefix}`
      )
    }
    for (const key of QY_AVAIL_ADMIN_NAV_KEYS) {
      assert.ok(!(key in enMap), `en.json 残留已移除的导航键 ${key}`)
      assert.ok(!(key in zhMap), `zh.json 残留已移除的导航键 ${key}`)
    }
  })

  test('keeps en and zh key sets identical', () => {
    assert.deepEqual(Object.keys(enMap).sort(), Object.keys(zhMap).sort())
  })
})
