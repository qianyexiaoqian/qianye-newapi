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

import en from '../../../../../i18n/qy/en.json'
import zh from '../../../../../i18n/qy/zh.json'
import {
  QY_FIAT_RATE_DECIMALS,
  QY_MAX_FIAT_RATE,
  qyCommissionFieldMeta,
  qyIsValidFiatRate,
  qyIsValidPercent,
  qyNormalizeFiatRate,
} from '../lib/fields'

/**
 * fiat-rate.test.ts —— 「佣金余额 → 法币」折算比例在界面上的三条底线。
 *
 *  1. **它不是百分比。** 区间 `(0, 1000000]`、8 位小数,而百分比是 `[0, 100]`、
 *     2 位小数。混起来的具体后果:输入框旁边多一个 `%`,运营填 7.3 之后以为
 *     自己配的是 7.3%,而实际生效的是"一美元折 7.3 元" —— 差两个数量级。
 *  2. **0 不是"免费"。** 后端 400,前端也必须标红。放行的话额度照加、法币不加,
 *     两侧永久漂移,而账本上每一行仍然自洽,事后查不出来。
 *  3. **兜底档不可清空,但"从未配过"是合法状态。** 这两句话必须同时成立:
 *     前者放行就会有一批用户悄悄退回充值页汇率;后者判非法则会把**整张
 *     配置表单**锁死 —— 保存按钮是共用的,升级上来的站点从此一个参数都改不了。
 *
 * 第 3 条的判定散在 index.tsx 的 findInvalid / ConfigField 里,没有可单独导出
 * 的纯函数承载,因此对源文件断言。它恰恰是最容易在后续重构中被"简化"掉的
 * 形状(把两支空值判定合并成一句 `!qyIsValidFiatRate(raw)` 读起来顺眼得多)。
 */

const INDEX = readFileSync(new URL('../index.tsx', import.meta.url), 'utf8')

describe('法币折算比例的取值区间', () => {
  test('合法输入', () => {
    for (const raw of ['7.3', '7', '  7.3  ', '0.00000001', '1000000', '0.5']) {
      assert.equal(qyIsValidFiatRate(raw), true, `${raw} 应当合法`)
    }
  })

  test('0 与空都非法,而且理由不同', () => {
    // 0:后端把它当非法输入拒掉。它既不是"免费"也不是"没配" ——
    // applyFiat 会一分法币都不加而额度照加。
    assert.equal(qyIsValidFiatRate('0'), false)
    assert.equal(qyIsValidFiatRate('0.0'), false)
    assert.equal(qyIsValidFiatRate('0.00000000'), false)
    // 空:兜底档不可清空。分组档要取消有 DELETE 接口,兜底档只能改。
    assert.equal(qyIsValidFiatRate(''), false)
    assert.equal(qyIsValidFiatRate('   '), false)
  })

  test('负数、超上界、超精度、非数字一律拒', () => {
    for (const raw of [
      '-1',
      '-0.5',
      `${QY_MAX_FIAT_RATE + 1}`,
      '1000000.01',
      '0.000000001', // 9 位小数,超出存储列 decimal(18,8)
      'abc',
      '7.3%', // 运营把它当百分比填了
      '1e3', // 科学计数法后端的 decimal 收得下,但界面不该鼓励
    ]) {
      assert.equal(qyIsValidFiatRate(raw), false, `${raw} 应当被拒`)
    }
  })

  test('小数位上界与后端存储列一致', () => {
    assert.equal(QY_FIAT_RATE_DECIMALS, 8)
    assert.equal(qyIsValidFiatRate('1.' + '0'.repeat(8) + '1'), false)
    assert.equal(qyIsValidFiatRate('1.' + '1'.repeat(8)), true)
  })

  test('与百分比校验器互不覆盖 —— 两个区间没有包含关系', () => {
    // 25000(越南盾那一档的量级)是合法的折算比例,却不是合法的百分比。
    assert.equal(qyIsValidFiatRate('25000'), true)
    assert.equal(qyIsValidPercent('25000'), false)
    // 反过来,0% 是合法的费率,却不是合法的折算比例。
    assert.equal(qyIsValidPercent('0'), true)
    assert.equal(qyIsValidFiatRate('0'), false)
  })
})

describe('规范化', () => {
  test('与后端 decimal.String() 的形状对齐', () => {
    assert.equal(qyNormalizeFiatRate('7.30'), '7.3')
    assert.equal(qyNormalizeFiatRate('7.000'), '7')
    assert.equal(qyNormalizeFiatRate('007'), '7')
    assert.equal(qyNormalizeFiatRate('  7.3  '), '7.3')
    assert.equal(qyNormalizeFiatRate('0.50'), '0.5')
  })

  test('小数不会被 Number() 变成科学计数法', () => {
    // String(Number('0.00000001')) === '1e-8'。走那条路的话这个值既提交不上去,
    // 也会让"改了没改"的比较永远判成改了 —— 每次保存都写一条假审计。
    assert.equal(qyNormalizeFiatRate('0.00000001'), '0.00000001')
    assert.equal(qyNormalizeFiatRate('0.00001000'), '0.00001')
  })

  test('非法输入原样返回,不替运营编一个数出来', () => {
    assert.equal(qyNormalizeFiatRate('abc'), 'abc')
    assert.equal(qyNormalizeFiatRate('0'), '0')
    assert.equal(qyNormalizeFiatRate(''), '')
  })
})

describe('字段元数据', () => {
  test('兜底档登记成 fiat_rate 而不是 percent', () => {
    const meta = qyCommissionFieldMeta('fiat_rate_default')
    assert.notEqual(meta, null)
    // 登记成 percent 的话 ConfigField 会在输入框旁边画一个 `%`,
    // 而 isUsdField 又会因为 unit !== 'quota' 而放过它 —— 于是运营看到的是
    // "折算比例 7.3 %",填的却是一个乘数。
    assert.equal(meta?.unit, 'fiat_rate')
    assert.equal(meta?.max, QY_MAX_FIAT_RATE)
    // 登记成 quota 的话 isUsdField 会把它拉进 USD 换算通道,按 QuotaPerUnit
    // 再除一次 —— 界面上的 7.3 会变成一个和运营填的完全无关的数。
    assert.notEqual(meta?.unit, 'quota')
  })
})

describe('兜底档的空值分两种情况', () => {
  test('findInvalid 对空串要看当前值,不能一刀切', () => {
    // 从未配过 → 空合法(否则整张表单锁死);配过之后清空 → 非法(兜底档不可删)。
    // 这两支必须都在源码里:合并成一句 `!qyIsValidFiatRate(raw)` 会锁死表单,
    // 合并成"空一律放行"则等于把兜底档做成可删的。
    const findInvalid = INDEX.slice(INDEX.indexOf('function findInvalid'))
    assert.match(findInvalid, /raw\.trim\(\) === ''/)
    assert.match(findInvalid, /if \(current !== ''\) return key/)
  })

  test('collectChanges 绝不把空串或 0 提交上去', () => {
    // 可空百分比键的空串是一个**要发出去的取值**,法币比例的空串不是。
    // 两者共用同一个 patch,分支写错就会向后端发一个必然 400 的报文。
    const collect = INDEX.slice(INDEX.indexOf('function collectChanges'))
    const fiatBranch = collect.slice(collect.indexOf('fiatRateKeys.has(key)'))
    assert.match(fiatBranch, /if \(!qyIsValidFiatRate\(raw\)\) continue/)
  })

  test('输入框不带百分号', () => {
    // ConfigField 的 `%` 后缀只由 isPercent 控制。折算比例走 isFiatRate,
    // 两个标志必须是互斥的两支,而不是"顺手也画一个 %"。
    assert.match(INDEX, /isFiatRate=\{fiatRateKeys\.has\(key\)\}/)
    assert.match(INDEX, /props\.isPercent && \(/)
  })
})

describe('层级必须能在界面上看出来', () => {
  test('四个层级各有一条文案', () => {
    for (const key of [
      'qy_cm_fr_layer_group',
      'qy_cm_fr_layer_default',
      'qy_cm_fr_layer_global',
      'qy_cm_fr_layer_none',
    ]) {
      assert.equal(typeof (zh as Record<string, string>)[key], 'string', key)
      assert.equal(typeof (en as Record<string, string>)[key], 'string', key)
    }
  })

  test('表格同时显示配的值与实际生效值', () => {
    // 只显示 rate 的话,一条被禁用的规则和一条生效的规则长得一模一样,
    // 而"我给 vip 配了 9 为什么没生效"就没有任何地方回答得了。
    assert.match(INDEX, /rule\.effective_rate/)
    assert.match(INDEX, /fiatLayerLabelKey\(rule\.effective_layer\)/)
  })

  test('三层都不可用时画成故障而不是一个正常状态', () => {
    assert.match(INDEX, /fiat_rate_effective_layer === 'none'/)
    assert.match(INDEX, /qy_cm_fr_broken_title/)
  })
})

describe('文案', () => {
  const keys = [
    'qy_cm_f_fiat_rate_default',
    'qy_cm_f_fiat_rate_default_hint',
    'qy_cm_f_fiat_rate_follows_global',
    'qy_cm_f_fiat_rate_clear',
    'qy_cm_f_fiat_rate_clear_hint',
    'qy_cm_f_fiat_rate_clear_title',
    'qy_cm_f_fiat_rate_clear_desc',
    'qy_cm_fr_title',
    'qy_cm_fr_desc',
    'qy_cm_fr_scope_title',
    'qy_cm_fr_scope_desc',
    'qy_cm_fr_rate',
    'qy_cm_fr_rate_hint',
    'qy_cm_fr_effective',
    'qy_cm_fr_group_hint',
    'qy_cm_fr_enabled_hint',
    'qy_cm_fr_forward_only',
    'qy_cm_fr_add',
    'qy_cm_fr_empty',
    'qy_cm_fr_saved',
    'qy_cm_fr_deleted',
    'qy_cm_fr_delete_title',
    'qy_cm_fr_delete_desc',
    'qy_cm_fr_broken_title',
    'qy_cm_fr_broken_desc',
  ]

  test('中英两侧齐全', () => {
    for (const key of keys) {
      assert.equal(typeof (zh as Record<string, string>)[key], 'string', key)
      assert.equal(typeof (en as Record<string, string>)[key], 'string', key)
    }
  })

  test('口径与"不追溯"必须写在界面上', () => {
    // 项目方要的两句话。少了第一句,运营会以为这一档和上面那张费率表同口径;
    // 少了第二句,他会以为调高比例能给老用户补差价。
    const scopeZh = (zh as Record<string, string>).qy_cm_fr_scope_desc
    assert.ok(scopeZh.includes('上线') || scopeZh.includes('邀请人'))
    assert.ok(scopeZh.includes('冻结'))
    assert.ok(
      (zh as Record<string, string>).qy_cm_fr_forward_only.includes('此后')
    )
    const scopeEn = (en as Record<string, string>).qy_cm_fr_scope_desc
    assert.ok(scopeEn.includes('inviter'))
    assert.ok(scopeEn.includes('frozen'))
  })

  test('占位符两侧一致', () => {
    for (const key of keys) {
      const zhText = (zh as Record<string, string>)[key]
      const enText = (en as Record<string, string>)[key]
      const pick = (s: string) =>
        [...s.matchAll(/\{\{(\w+)\}\}/g)].map((m) => m[1]).sort()
      assert.deepEqual(pick(zhText), pick(enText), key)
    }
  })
})

describe('第三层必须回得去', () => {
  test('清空走独立按钮 + 独立确认，提交的是 null 不是空串', () => {
    // 在这条路补上之前，兜底档一旦配过就再也回不去 fiatRateFor 声明的第三层：
    // 写入侧对空串 400，而全仓没有任何 DELETE 入口。手填一个与当前
    // USDExchangeRate 相同的数字只是数值上的巧合 —— 充值汇率此后再改，
    // 佣金折算不会跟着走，界面上却仍写着"兜底档"。
    //
    // 同时要挡住反方向：清空**不能**做成"把输入框清空再保存"，那一步会被
    // 一次误触触发，而这一档的空值是资损形状（额度照加、法币不加）。
    assert.match(INDEX, /saveMutation\.mutate\(\{ fiat_rate_default: null \}\)/)
    assert.match(INDEX, /qy_cm_f_fiat_rate_clear_title/)
  })

  test('没配过的时候不画这个按钮', () => {
    // 第三层已经是当前状态，再给一个"回落到第三层"只会让人以为自己漏配了。
    assert.match(INDEX, /config\.effective\.fiat_rate_default !== ''/)
  })

  test('确认文案要说清回落到几，以及存量不重算', () => {
    const zhDesc = (zh as Record<string, string>).qy_cm_f_fiat_rate_clear_desc
    assert.ok(zhDesc.includes('{{rate}}'), '没说回落之后按几折算')
    assert.ok(zhDesc.includes('不会重算'), '没说已经入账的法币余额不受影响')
    assert.ok(zhDesc.includes('审计'), '没说这次改动会留痕')
    const enDesc = (en as Record<string, string>).qy_cm_f_fiat_rate_clear_desc
    assert.ok(enDesc.includes('{{rate}}'))
    assert.ok(enDesc.includes('not recomputed'))
    assert.ok(enDesc.includes('audit'))
  })

  test('提示里要写明"手填一个一样的数字顶不了"', () => {
    // 这是整条路存在的理由。不写的话运营会觉得这个按钮多余，
    // 下次仍然靠手填一个等于当前汇率的数字来"取消"。
    const zhHint = (zh as Record<string, string>).qy_cm_f_fiat_rate_clear_hint
    assert.ok(zhHint.includes('顶不了') || zhHint.includes('跟着走'))
    const enHint = (en as Record<string, string>).qy_cm_f_fiat_rate_clear_hint
    assert.ok(enHint.includes('not equivalent'))
  })

  test('字段提示不能再写"不可清空"', () => {
    // 旧文案与新按钮直接矛盾，两句话同时挂在界面上比缺一句更糟。
    const zhHint = (zh as Record<string, string>).qy_cm_f_fiat_rate_default_hint
    assert.ok(!zhHint.includes('不可再清空'))
    const enHint = (en as Record<string, string>).qy_cm_f_fiat_rate_default_hint
    assert.ok(!enHint.includes('cannot be cleared'))
  })
})
