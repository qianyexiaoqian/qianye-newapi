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
 * 大小写敏感的串不许被主题整段大写。
 *
 * ## 缺陷形状
 *
 * 本站主题(qy-sg-apply.css)对 `[data-slot='label']` 施加
 * `text-transform: uppercase`，而 `<Label>` 组件的根节点上就写死了这个 slot。
 * 于是任何把 `<Label>` 当行容器用的地方，里面的内容会**整块继承**大写：
 *
 *   线路选择窗里的 URL   https://example.com/newApi/v1
 *                    →  HTTPS://EXAMPLE.COM/NEWAPI/V1
 *   提现收款账号(脱敏后的邮箱 / 卡号)同理
 *
 * URL 的路径段**区分大小写** —— 同目录 `api-base-url.ts` 专门为此论证过
 * 「`https://a.com/V1` 不是一个能用的端点」。用户照着屏幕念、照着往第三方
 * 客户端手填，就是一个 404。
 *
 * 中文界面上看不出来(中文字形对 uppercase 无反应)，所以它一直没被发现 ——
 * 登录页那条真实的 Label 就是 uppercase 的。
 *
 * ## 这条测试怎么守
 *
 * 两半都要断言，缺一条就会以错误的理由变绿：
 *   ① 主题**确实**还在对 label 施加 uppercase（哪天不施加了，这条修补就多余，
 *      该连同注释一起删掉，而不是留着一个没人懂的 normal-case）；
 *   ② 那几个渲染大小写敏感串的 Label 上确实写着 normal-case。
 */
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, test } from 'node:test'
import { fileURLToPath } from 'node:url'

const SRC = fileURLToPath(new URL('../../../../', import.meta.url))

function read(rel: string): string {
  return readFileSync(join(SRC, rel), 'utf-8')
}

describe('主题的大写规则', () => {
  test('确实还在对 [data-slot=label] 施加 uppercase —— 这是那几处 normal-case 的前提', () => {
    const css = read('styles/qy-sg-apply.css')
    const block = css.slice(0, css.indexOf('text-transform: uppercase'))
    assert.ok(
      block.includes("[data-slot='label']"),
      '主题不再大写 label 了：那几处 normal-case 就成了没人看得懂的补丁，' +
        '请连同它们的注释一起删掉，而不是留着'
    )
  })

  test('Label 组件根节点上确实带着 data-slot=label', () => {
    assert.ok(read('components/ui/label.tsx').includes("data-slot='label'"))
  })
})

describe('渲染大小写敏感串的 Label', () => {
  const cases = [
    {
      file: 'features/qy/pages/api-address-picker/picker-dialog.tsx',
      what: 'API 线路的 URL —— 路径段区分大小写，照着大写版本手填就是 404',
    },
    {
      file: 'features/qy/pages/withdraw/components/payee-section.tsx',
      what: '脱敏后的收款账号(邮箱 / 卡号) —— 大写之后用户对不上自己填过的那个',
    },
  ]

  for (const c of cases) {
    test(`${c.file} 必须 normal-case`, () => {
      assert.ok(
        read(c.file).includes('normal-case'),
        `${c.what}。主题会把整块 Label 大写，这里必须显式 normal-case 顶回去`
      )
    })
  }
})
