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
import { readFileSync } from 'node:fs'
import { describe, test } from 'node:test'
import { fileURLToPath } from 'node:url'

import en from '@/i18n/qy/en.json'
import zh from '@/i18n/qy/zh.json'

import {
  QY_PAY_PWD_MAX_BYTES,
  QY_PAY_PWD_MIN_BYTES,
  qyPayPwdByteLength,
} from '../../../lib/pay-password'
import { qyPayPasswordSchema } from '../lib/schema'

const here = fileURLToPath(new URL('.', import.meta.url))
const read = (relative: string) => readFileSync(`${here}${relative}`, 'utf8')

describe('支付密码强度校验', () => {
  // 与后端 qianye/modules/paypass/hash.go 的 TestValidateStrength 是**同一张表**。
  // 两处口径必须一致：前端更松会让用户提交后才吃 400，前端更严会挡掉后端认可的密码。
  const cases: [string, boolean][] = [
    ['', false],
    ['12345', false],
    ['123456', false],
    ['654321', false],
    ['111111', false],
    ['192837', true],
    ['aaaaaa', false],
    ['abcdef', true],
    ['pay-pwd-8891', true],
    ['支付密码就是它', true],
    [`${'a'.repeat(64)}b`, false],
  ]

  for (const [password, want] of cases) {
    test(JSON.stringify(password), () => {
      assert.equal(qyPayPasswordSchema.safeParse(password).success, want)
    })
  }

  test('长度按 UTF-8 字节算，与 bcrypt 的截断口径一致', () => {
    // 七个汉字 = 21 字节，落在 [6,64] 内；按“字符数”算的话是 7，同样通过，
    // 所以这条断言必须挑一个两种算法结论**不同**的输入。
    const twentyTwoChars = '密'.repeat(22) // 66 字节 > 64，但只有 22 个字符
    assert.equal(qyPayPwdByteLength(twentyTwoChars), 66)
    assert.ok(twentyTwoChars.length <= QY_PAY_PWD_MAX_BYTES)
    assert.equal(qyPayPasswordSchema.safeParse(twentyTwoChars).success, false)

    const twoChars = '密码' // 6 字节但只有 2 个字符
    assert.equal(qyPayPwdByteLength(twoChars), 6)
    assert.ok(twoChars.length < QY_PAY_PWD_MIN_BYTES)
    assert.equal(qyPayPasswordSchema.safeParse(twoChars).success, true)
  })
})

describe('邮箱找回不得提供绑定邮箱的入口', () => {
  // 裁决 1 的红线：未绑定邮箱时只提示去绑定，绝不在找回路径上代为绑定。
  //
  // 这条断言看的是**源码**而不是渲染结果：找回卡片里只要出现一个邮箱输入框，
  // 那条被明令禁止的绕过路径就已经存在了，无论它当下是否可见。
  // 判据放宽到“任何 name/type 含 email 的输入”，因为绕过路径的形状不止一种。
  const source = read('../components/pay-password-recover-card.tsx')

  test('组件里没有邮箱输入控件', () => {
    assert.ok(
      !/<Input[^>]*type=['"]email['"]/.test(source),
      '找回卡片里出现了 type="email" 的输入框'
    )
    assert.ok(
      !/name=['"]email['"]/.test(source),
      '找回卡片里出现了 name="email" 的表单字段'
    )
  })

  test('发码接口不接受任何入参', () => {
    const api = read('../../../lib/pay-password.ts')
    assert.match(
      api,
      /export function qySendPayPasswordRecoverCode\(\)/,
      '发码接口一旦收下入参，前端就有能力指定收件邮箱 —— 那正是被禁止的改绑旁路'
    )
  })
})

describe('i18n', () => {
  const enKeys = Object.keys(en as Record<string, string>)
  const zhKeys = Object.keys(zh as Record<string, string>)

  test('en 与 zh 键数相等且键集相同', () => {
    assert.equal(enKeys.length, zhKeys.length)
    assert.deepEqual(
      enKeys.filter((k) => !(k in (zh as Record<string, string>))),
      []
    )
    assert.deepEqual(
      zhKeys.filter((k) => !(k in (en as Record<string, string>))),
      []
    )
  })

  test('组件里用到的 qy_pp_* 键都存在于两份词表', () => {
    // 扫源码取实际用到的键，而不是维护一张手写清单 —— 手写清单会漏，
    // 而漏掉的那个键在界面上会原样显示成 "qy_pp_xxx"。
    const sources = [
      read('../index.tsx'),
      read('../components/pay-password-status-card.tsx'),
      read('../components/pay-password-form-card.tsx'),
      read('../components/pay-password-recover-card.tsx'),
      read('../lib/schema.ts'),
      read('../../../components/qy-pay-password-field.tsx'),
    ].join('\n')

    const used = new Set(
      [...sources.matchAll(/'(qy_pp_[a-z0-9_]+)'/g)].map((m) => m[1])
    )
    assert.ok(used.size > 20, '扫到的 key 太少，正则可能没匹配上')
    for (const key of used) {
      assert.ok(key in (en as Record<string, string>), `en 缺少 ${key}`)
      assert.ok(key in (zh as Record<string, string>), `zh 缺少 ${key}`)
    }
  })

  test('导航与副标齐全', () => {
    // 侧栏英文副标（`qy_sg_nav_en_*`）已按需求 6 整体移除，所以这里只剩两件；
    // 它不在的事实由 `lib/__tests__/pages-table.test.ts` 反向钉住。
    for (const key of ['qy_nav_pay_password', 'qy_sg_jp_pay_password']) {
      assert.ok(key in (en as Record<string, string>), `en 缺少 ${key}`)
      assert.ok(key in (zh as Record<string, string>), `zh 缺少 ${key}`)
    }
  })
})
