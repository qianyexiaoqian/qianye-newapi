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

import { QY_ERROR_CODE_I18N } from '@/features/qy/lib/api'
import en from '@/i18n/qy/en.json'
import zh from '@/i18n/qy/zh.json'

import {
  QY_UPDATE_ERROR_CODES,
  QY_UPDATE_STATUS_I18N,
} from '../update-check-copy'

/*
 * update-check-copy.test.ts —— 二开检查更新的**最后一米**。
 *
 * 后端把这次检查的结局拆成了 6 种成功状态 + 6 种失败 code,理由写在
 * `qianye/controller/update_check.go` 的文件注释里:管理员要做的下一步在这些
 * 分支之间完全不同(去发版 / 等一小时 / 查仓库地址 / 查出站 IP / 什么都不用做)。
 *
 * 那一层拆分可以在前端被**无声地抹平**,而且有两种抹法,两种都不会让页面报错:
 *
 *   1. 文案键写错或没登进语言包 —— i18next 回落成键名本身,界面上出现一串
 *      `qy_upd_no_release` 这样的英文标识符。它不像报错,像是页面没写完。
 *   2. 后端 code 没登记进 `QY_ERROR_CODE_I18N` —— 五个 502 会一起按 HTTP 状态
 *      塌成 `qy_err_server`("服务器出错了"),429 塌成"请求过于频繁"。
 *      后端那六句精心分开的话一个字都到不了界面,而且六种失败互相不可区分。
 *
 * 这两种坏法在 typecheck、format、以及任何"渲染一下看看崩不崩"的用例上都
 * 完全不可见,所以只能逐键核对。
 */

const zhKeys = zh as Record<string, string>
const enKeys = en as Record<string, string>

describe('二开检查更新:结局文案', () => {
  test('六种结局各有一句话,zh/en 都登记了', () => {
    const entries = Object.entries(QY_UPDATE_STATUS_I18N)
    // 数量本身是断言的一部分:后端少一种 / 多一种都要有人来这里对一次。
    assert.equal(entries.length, 6)

    for (const [status, key] of entries) {
      assert.ok(
        zhKeys[key] != null && zhKeys[key] !== '',
        `${status} 的文案键 ${key} 在 i18n/qy/zh.json 里不存在,界面会显示键名本身`
      )
      assert.ok(
        enKeys[key] != null && enKeys[key] !== '',
        `${status} 的文案键 ${key} 在 i18n/qy/en.json 里不存在`
      )
    }
  })

  test('六种结局不许共用同一句话', () => {
    const keys = Object.values(QY_UPDATE_STATUS_I18N)
    assert.equal(
      new Set(keys).size,
      keys.length,
      '两种结局映射到了同一个文案键 —— 后端把它们分开正是因为下一步动作不同'
    )
    // 文案本身也不许撞。键不同、译文抄成一样,用户看到的仍然是同一句话。
    const zhTexts = keys.map((k) => zhKeys[k])
    assert.equal(new Set(zhTexts).size, keys.length, '两种结局的中文译文相同')
  })

  test('「还没发过版」不许说成「已是最新」或「检查失败」', () => {
    // 这是本 fork 此刻的真实状态,也是最容易被说错的一档:仓库是通的,
    // 只是我们还没打过 tag。说成"已是最新"会让人以为没事,
    // 说成"检查失败"会把人推去查网络 —— 而该做的是去发版。
    const text = zhKeys[QY_UPDATE_STATUS_I18N.no_release]
    assert.ok(!text.includes('已是最新'), text)
    assert.ok(!text.includes('失败'), text)
    assert.ok(
      text.includes('发布') || text.includes('Release'),
      `「还没发过版」这一档必须说清下一步是去发版,当前文案:${text}`
    )
  })

  test('「本机更新」不许说成「已是最新」', () => {
    // ahead 出现在"改完还没发版"的机器上。说成"已是最新"会掩盖"忘了发版"。
    const ahead = zhKeys[QY_UPDATE_STATUS_I18N.ahead]
    const upToDate = zhKeys[QY_UPDATE_STATUS_I18N.up_to_date]
    assert.notEqual(ahead, upToDate)
    assert.ok(!ahead.includes('已是最新'), ahead)
  })

  test('结果面板必须写明本站不自动下载/不自动更新', () => {
    for (const [name, bundle] of [
      ['zh', zhKeys],
      ['en', enKeys],
    ] as const) {
      const text = bundle['qy_upd_no_auto_download']
      assert.ok(
        text != null && text !== '',
        `${name} 缺 qy_upd_no_auto_download`
      )
    }
  })
})

describe('二开检查更新:失败 code', () => {
  test('六个后端 code 全部登记,且映射到六句不同的话', () => {
    assert.equal(QY_UPDATE_ERROR_CODES.length, 6)

    const mapped = QY_UPDATE_ERROR_CODES.map((code) => {
      const key = QY_ERROR_CODE_I18N[code]
      assert.ok(
        key != null,
        `${code} 没登进 QY_ERROR_CODE_I18N —— 它会按 HTTP 状态码塌成一句通用报错,` +
          '后端把失败拆成六档的那一层工作在这里被抹平'
      )
      return key
    })

    assert.equal(
      new Set(mapped).size,
      mapped.length,
      '两个 code 映射到了同一句话 —— 例如把「连不上」和「被限流」说成同一件事'
    )

    for (const key of mapped) {
      assert.ok(zhKeys[key] != null && zhKeys[key] !== '', `zh 缺 ${key}`)
      assert.ok(enKeys[key] != null && enKeys[key] !== '', `en 缺 ${key}`)
    }
  })

  test('「限流」与「被拒」两句话必须给出相反的下一步', () => {
    // 这两条都是 403,最容易被合并。限流等一会儿就好,被拒等多久都没用。
    const limited = zhKeys[QY_ERROR_CODE_I18N['qy_update_rate_limited']]
    const forbidden = zhKeys[QY_ERROR_CODE_I18N['qy_update_forbidden']]
    assert.ok(limited.includes('稍后'), `限流那句没告诉人可以等:${limited}`)
    assert.ok(
      forbidden.includes('不会让它恢复') || forbidden.includes('不是额度'),
      `被拒那句没说清等待无效:${forbidden}`
    )
  })

  test('「仓库不存在」必须与「还没发过版」在文案上分开', () => {
    const missing = zhKeys[QY_ERROR_CODE_I18N['qy_update_source_missing']]
    // 两者在 GitHub 的 /releases/latest 上都是 404,后端换用 /releases 列表
    // 才把它们分开。文案上也必须点破,否则运维会以为是同一件事。
    assert.ok(
      missing.includes('不是一回事') || missing.includes('还没发过'),
      `文案没有把「仓库不存在」与「还没发过版」区分开:${missing}`
    )
  })
})

describe('后端 code 清单与 Go 源码同源', () => {
  test('Go 里定义的 update code 与前端清单逐字一致', () => {
    // 只核对清单本身。前端这份数组是手写的,而它一旦与后端漂移,上面那条
    // "六个都登记了"就会去核对一批**不存在的 code**,全绿却什么都没守住。
    const source = readFileSync(
      new URL(
        '../../../../../../qianye/controller/update_check.go',
        import.meta.url
      ),
      'utf8'
    )
    const found = [
      ...source.matchAll(/codeUpdate\w+\s*=\s*"(qy_update_[a-z_]+)"/g),
    ].map((m) => m[1])

    assert.deepEqual(
      [...found].sort(),
      [...QY_UPDATE_ERROR_CODES].sort(),
      'qianye/controller/update_check.go 里的 code 与前端清单对不上'
    )
  })

  test('Go 里定义的成功 status 与前端文案表逐字一致', () => {
    // 六个**失败 code** 早就有这条同源守卫,六个**成功 status** 却没有。
    //
    // update-check-copy.ts 的注释写着"穷尽性由 TypeScript 保证" —— 那只保证
    // TS 联合类型与这张表一致,保证不了它与 Go 一致。Go 侧改一个字(改名,
    // 或者更现实的:加第七种 status 而前端不知道),
    // `t(QY_UPDATE_STATUS_I18N[status])` 拿到 undefined,而 i18next 的
    // translate() 第一行就是 `if (keys == null) return ''` —— 结论那一行渲染成
    // **空串**,任何 parseMissingKeyHandler / fallback 都轮不到。
    //
    // 实测:把 Go 里 updateStatusNoRelease 的字面量改成 "none_released",
    // `go test ./qianye/...` 全绿、`bun test src/features/system-settings` 全绿,
    // 而界面上那一行从"仓库可访问,但还没有发布过任何版本…"变成空白。
    // 而 no_release 正是本 fork 此刻的真实状态(实测 GitHub 返回 200 + []),
    // 空白与"检查通过没事"在视觉上完全一样 —— update-check-copy.ts 自己把这
    // 称作最坏的失败形态。
    const source = readFileSync(
      new URL(
        '../../../../../../qianye/controller/update_check.go',
        import.meta.url
      ),
      'utf8'
    )
    const found = [
      ...source.matchAll(/updateStatus\w+\s*=\s*"([a-z_]+)"/g),
    ].map((m) => m[1])

    assert.ok(
      found.length >= 6,
      `只从 Go 源码里抽到 ${found.length} 个 status,正则八成认不出新写法了 —— ` +
        '空转的守卫比没有守卫更坏'
    )
    assert.deepEqual(
      [...found].sort(),
      Object.keys(QY_UPDATE_STATUS_I18N).sort(),
      'qianye/controller/update_check.go 里的成功 status 与前端文案表对不上;' +
        '缺一个就会让结论那一行渲染成空白,而空白与「检查通过没事」看起来一模一样'
    )
  })

  test('每个 status 都映到一句**不同**的话', () => {
    const values = Object.values(QY_UPDATE_STATUS_I18N)
    assert.equal(
      new Set(values).size,
      values.length,
      '两个结局共用一句文案等于把它们抹平 —— 例如把 no_release 说成「已是最新」,' +
        '那会让人以为没事,而正确的下一步是去发版'
    )
    for (const key of values) {
      assert.ok(
        key.startsWith('qy_upd_'),
        `${key} 不像一个 qy 文案键,多半是把文案字面量直接写进表里了`
      )
    }
  })
})
