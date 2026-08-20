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

/**
 * qy 请求路径的**唯一**提取实现。
 *
 * 谁在用：
 *   - `lib/__tests__/route-contract.test.ts` —— 前后端路径对账守卫（会变红的那条）；
 *   - 任何需要"前端到底调了哪些 qy 路径"的审计。
 *
 * 为什么只能有一份：这一轮修的缺陷（违规类型页 `/violation/categories` 少了
 * `/admin` 前缀，项目方报了三次）的形状就是"同一件事写了两遍、然后漂移"——
 * 兄弟页 `admin-violation-rules/api.ts` 写对了，本页写错了。再写两份提取逻辑，
 * 审计与守卫会以同样的方式分家：审计说没问题，守卫说有问题，或者反过来。
 *
 * ## 这是个"够用的求值器"，不是 TypeScript 解析器
 *
 * 仓库里没有可用的 TS AST 依赖（只有 `tsgo`，它不暴露 API），所以这里是手写的
 * 词法扫描 + 表达式求值：字符串字面量、模板串、三元、文件内的 `const` 与单表达式
 * helper 函数。**求不出来的一律显式报告**（{@link QyUnresolvedRequest}），绝不
 * 静默丢弃 —— 静默丢弃等于把守卫关掉，而那正是本轮要防的事。
 */

import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join, relative, sep } from 'node:path'

/** 后端 gin 分组前缀，与 `qianye/router.go` 的 `engine.Group("/api/qy")` 对齐。 */
export const QY_API_PREFIX = '/api/qy'

/** "这一段是运行时才知道的值"的哨兵。刻意用控制字符：源码里不可能出现。 */
const DYN = '\u0000'
/** "这一段展开成了一个路径参数"的哨兵。 */
const PARAM = '\u0001'

/** 一处 qy 请求的调用点。 */
export type QyRequestSite = {
  /** 相对 `web/src` 的路径，用 `/` 分隔。 */
  file: string
  /** 1 起算的行号。 */
  line: number
  method: 'DELETE' | 'GET' | 'PATCH' | 'POST' | 'PUT'
  /** 源码里那一段路径表达式的原文，报错时给人看。 */
  expr: string
  /**
   * 归一后的候选路由模板（含 `/api/qy` 前缀）。
   *
   * 多于一条只有两个来源：源码里的三元分支（两个分支都必须真实存在），以及求不
   * 出来的 `${}` —— 后者会同时展开成"一个路径参数"与"空串"，因为一段拼接既可能
   * 是路径段（`/${id}`），也可能是查询串（`${'?reassign_to=3'}`）。
   */
  candidates: string[]
}

/** 一处求值失败的调用点。它必须要么被修好，要么进豁免清单。 */
export type QyUnresolvedRequest = {
  file: string
  line: number
  expr: string
}

export type QyRequestScan = {
  sites: QyRequestSite[]
  unresolved: QyUnresolvedRequest[]
}

// ───────────────────────── 词法：把注释抹成空格 ─────────────────────────

type Mode = 'code' | 'tpl'

/**
 * 去掉注释，保留字符数与换行（行号才不会错位）。
 *
 * 必须做：文档注释里写着"路径是 A 而不是 B"这类**反例**（见
 * `features/subscriptions/api.ts` 里关于 `plans-usage` 的那段），照单全收会让
 * 守卫对着一条注释里的假路径报错。
 *
 * 已知边界：不识别正则字面量。`/\/\//` 这种"正则里带两个斜杠"会被误当成行注释。
 * 全仓当前没有这种写法，真出现时表现是漏扫一处调用点（假阴性），不会误报。
 */
export function stripComments(src: string): string {
  const out: string[] = []
  const stack: Mode[] = []
  let mode: Mode = 'code'
  let brace = 0
  const braces: number[] = []
  let i = 0

  const eat = (n: number) => {
    for (let k = 0; k < n; k += 1) out.push(src[i + k] ?? '')
    i += n
  }

  while (i < src.length) {
    const c = src[i]
    if (mode === 'tpl') {
      if (c === '\\') {
        eat(2)
        continue
      }
      if (c === '`') {
        eat(1)
        mode = stack.pop() ?? 'code'
        continue
      }
      if (c === '$' && src[i + 1] === '{') {
        eat(2)
        stack.push('tpl')
        braces.push(brace)
        brace = 0
        mode = 'code'
        continue
      }
      eat(1)
      continue
    }

    if (c === '/' && src[i + 1] === '/') {
      while (i < src.length && src[i] !== '\n') {
        out.push(' ')
        i += 1
      }
      continue
    }
    if (c === '/' && src[i + 1] === '*') {
      out.push(' ', ' ')
      i += 2
      while (i < src.length && !(src[i] === '*' && src[i + 1] === '/')) {
        out.push(src[i] === '\n' ? '\n' : ' ')
        i += 1
      }
      out.push(' ', ' ')
      i += 2
      continue
    }
    if (c === "'" || c === '"') {
      eat(1)
      while (i < src.length && src[i] !== c) {
        eat(src[i] === '\\' ? 2 : 1)
      }
      eat(1)
      continue
    }
    if (c === '`') {
      eat(1)
      stack.push(mode)
      mode = 'tpl'
      continue
    }
    if (c === '{') {
      brace += 1
      eat(1)
      continue
    }
    if (c === '}') {
      if (brace === 0 && stack.length > 0 && stack.at(-1) === 'tpl') {
        stack.pop()
        brace = braces.pop() ?? 0
        eat(1)
        mode = 'tpl'
        continue
      }
      brace -= 1
      eat(1)
      continue
    }
    eat(1)
  }
  return out.join('')
}

// ───────────────────────── 表达式求值 ─────────────────────────

type Helper = { params: { name: string; def: string }[]; body: string }

type Ctx = {
  consts: Map<string, string[]>
  helpers: Map<string, Helper>
  locals: Map<string, string[]>
  depth: number
}

/** 按顶层深度切分，`seps` 里的字符只在括号/引号深度 0 时算分隔符。 */
function splitTop(text: string, seps: string): string[] {
  const parts: string[] = []
  let depth = 0
  let start = 0
  let q: string | null = null
  for (let i = 0; i < text.length; i += 1) {
    const c = text[i]
    if (q != null) {
      if (c === '\\') {
        i += 1
        continue
      }
      if (c === q) q = null
      continue
    }
    if (c === "'" || c === '"' || c === '`') {
      q = c
      continue
    }
    if ('([{'.includes(c)) depth += 1
    else if (')]}'.includes(c)) depth -= 1
    else if (depth === 0 && seps.includes(c)) {
      parts.push(text.slice(start, i))
      start = i + 1
    }
  }
  parts.push(text.slice(start))
  return parts
}

/** 顶层三元运算符的 `[?, :]` 位置；没有返回 null。`??` 与 `?.` 不算。 */
function findTernary(text: string): [number, number] | null {
  let depth = 0
  let q: string | null = null
  let qm = -1
  for (let i = 0; i < text.length; i += 1) {
    const c = text[i]
    if (q != null) {
      if (c === '\\') {
        i += 1
        continue
      }
      if (c === q) q = null
      continue
    }
    if (c === "'" || c === '"' || c === '`') {
      q = c
      continue
    }
    if ('([{'.includes(c)) depth += 1
    else if (')]}'.includes(c)) depth -= 1
    else if (depth === 0 && c === '?') {
      if (text[i + 1] === '?' || text[i + 1] === '.' || text[i - 1] === '?') {
        i += 1
        continue
      }
      if (qm === -1) qm = i
    } else if (depth === 0 && c === ':' && qm !== -1) {
      return [qm, i]
    }
  }
  return null
}

const MAX_CANDIDATES = 64

function product(parts: string[][]): string[] {
  let acc = ['']
  for (const part of parts) {
    const next: string[] = []
    for (const a of acc) for (const b of part) next.push(a + b)
    acc = [...new Set(next)].slice(0, MAX_CANDIDATES)
  }
  return acc
}

/** 求值一段路径表达式。返回候选串（可能含 {@link DYN}），求不出返回 null。 */
function evalExpr(raw: string, ctx: Ctx): string[] | null {
  if (ctx.depth > 8) return null
  let text = raw.trim()
  // 去掉整体包裹的一层括号。
  while (text.startsWith('(') && text.endsWith(')')) {
    const inner = text.slice(1, -1)
    if (splitTop(inner, ')').length !== 1) break
    text = inner.trim()
  }
  if (text === '') return null

  const ternary = findTernary(text)
  if (ternary != null) {
    const [q, colon] = ternary
    const a = evalExpr(text.slice(q + 1, colon), {
      ...ctx,
      depth: ctx.depth + 1,
    })
    const b = evalExpr(text.slice(colon + 1), { ...ctx, depth: ctx.depth + 1 })
    if (a == null || b == null) return null
    return [...new Set([...a, ...b])]
  }

  const quote = text[0]
  if (
    (quote === "'" || quote === '"') &&
    text.endsWith(quote) &&
    text.length >= 2
  ) {
    const body = text.slice(1, -1)
    if (body.includes(quote) || body.includes('\\')) return null
    return [body]
  }

  if (quote === '`' && text.endsWith('`') && text.length >= 2) {
    const body = text.slice(1, -1)
    const parts: string[][] = []
    let lit = ''
    let i = 0
    while (i < body.length) {
      if (body[i] === '\\') {
        lit += body[i + 1] ?? ''
        i += 2
        continue
      }
      if (body[i] === '$' && body[i + 1] === '{') {
        parts.push([lit])
        lit = ''
        let depth = 1
        let j = i + 2
        let q: string | null = null
        for (; j < body.length && depth > 0; j += 1) {
          const c = body[j]
          if (q != null) {
            if (c === '\\') {
              j += 1
              continue
            }
            if (c === q) q = null
            continue
          }
          if (c === "'" || c === '"' || c === '`') q = c
          else if (c === '{') depth += 1
          else if (c === '}') depth -= 1
        }
        const inner = body.slice(i + 2, j - 1)
        parts.push(evalExpr(inner, { ...ctx, depth: ctx.depth + 1 }) ?? [DYN])
        i = j
        continue
      }
      lit += body[i]
      i += 1
    }
    parts.push([lit])
    return product(parts)
  }

  if (/^[A-Za-z_$][\w$]*$/.test(text)) {
    return ctx.locals.get(text) ?? ctx.consts.get(text) ?? null
  }

  const call = /^([A-Za-z_$][\w$]*)\s*\(([\s\S]*)\)$/.exec(text)
  if (call != null && splitTop(text.slice(call[1].length), ')').length === 1) {
    const helper = ctx.helpers.get(call[1])
    // 不认识的调用（`encodeURIComponent(x)`、`String(id)`…）：内容是运行时值。
    if (helper == null) return [DYN]
    const args = call[2].trim() === '' ? [] : splitTop(call[2], ',')
    const locals = new Map(ctx.locals)
    helper.params.forEach((p, idx) => {
      const source = args[idx] ?? p.def
      locals.set(
        p.name,
        source.trim() === ''
          ? ['']
          : (evalExpr(source, { ...ctx, depth: ctx.depth + 1 }) ?? [DYN])
      )
    })
    return evalExpr(helper.body, { ...ctx, locals, depth: ctx.depth + 1 })
  }

  return null
}

// ───────────────────────── 文件级符号表 ─────────────────────────

/** 读 `=` 之后**同一行**的值表达式（去掉行尾 `;`）。 */
function restOfLine(src: string, from: number): string {
  let end = src.indexOf('\n', from)
  if (end === -1) end = src.length
  return src.slice(from, end).trim().replace(/;$/, '').trim()
}

const HELPER_RE =
  /(?:function\s+([A-Za-z_$][\w$]*)\s*\(([^)]*)\)(?:\s*:\s*[\w<>[\]|\s]+?)?\s*\{[\s\S]{0,40}?return\s+([^\n]+)|const\s+([A-Za-z_$][\w$]*)\s*=\s*\(([^)]*)\)(?:\s*:\s*[\w<>[\]|\s]+?)?\s*=>\s*([^\n]+))/g

const CONST_RE = /\bconst\s+([A-Za-z_$][\w$]*)\s*(?::\s*string\s*)?=\s*(?!\()/g

function buildCtx(src: string): Ctx {
  const ctx: Ctx = {
    consts: new Map(),
    helpers: new Map(),
    locals: new Map(),
    depth: 0,
  }

  HELPER_RE.lastIndex = 0
  let m: RegExpExecArray | null
  while ((m = HELPER_RE.exec(src)) != null) {
    const name = m[1] ?? m[4]
    const body = (m[3] ?? m[6] ?? '').trim().replace(/;$/, '').trim()
    if (name == null || body === '') continue
    const params = splitTop(m[2] ?? m[5] ?? '', ',')
      .map((p) => p.trim())
      .filter((p) => p !== '')
      .map((p) => {
        const halves = splitTop(p, '=')
        return {
          name: halves[0].split(':')[0].trim(),
          def: halves.length > 1 ? halves.slice(1).join('=').trim() : '',
        }
      })
    ctx.helpers.set(name, { params, body })
  }

  // `const NAME = <单行表达式>`。跨行的一律不登记 —— 求不出来时调用点会展开成
  // "路径参数 or 空串"两种候选，那是安全的降级；猜一个值不是。
  CONST_RE.lastIndex = 0
  while ((m = CONST_RE.exec(src)) != null) {
    const name = m[1]
    if (ctx.helpers.has(name)) continue
    const value = evalExpr(restOfLine(src, m.index + m[0].length), ctx)
    if (value == null) continue
    const prev = ctx.consts.get(name)
    // 同名多次声明（不同函数作用域里的 `base`）：两个取值都算候选。
    ctx.consts.set(
      name,
      prev == null ? value : [...new Set([...prev, ...value])]
    )
  }
  return ctx
}

// ───────────────────────── 调用点扫描 ─────────────────────────

const METHOD_BY_HELPER: Record<string, QyRequestSite['method']> = {
  Delete: 'DELETE',
  Get: 'GET',
  Patch: 'PATCH',
  Post: 'POST',
  Put: 'PUT',
}

const METHOD_BY_AXIOS: Record<string, QyRequestSite['method']> = {
  delete: 'DELETE',
  get: 'GET',
  patch: 'PATCH',
  post: 'POST',
  put: 'PUT',
}

/** 从 `(` 之后读第一个实参的原文。 */
function firstArg(src: string, open: number): string {
  let depth = 0
  let q: string | null = null
  let i = open + 1
  for (; i < src.length; i += 1) {
    const c = src[i]
    if (q != null) {
      if (c === '\\') {
        i += 1
        continue
      }
      if (c === q) q = null
      continue
    }
    if (c === "'" || c === '"' || c === '`') {
      q = c
      continue
    }
    if ('([{'.includes(c)) depth += 1
    else if (')]}'.includes(c)) {
      if (depth === 0) break
      depth -= 1
    } else if (c === ',' && depth === 0) break
  }
  return src.slice(open + 1, i).trim()
}

/**
 * 跳过 `qyGet<…>` 的泛型实参，返回 `(` 的下标；不是调用则返回 -1。
 *
 * 这里**不能**把 `;` 当成"不是泛型"的终止符。内联对象类型里的成员分隔符正是
 * `;`（`qyPost<{ a: number; b: boolean }>(path, …)`），而遇到它就 return -1
 * 会让调用方的 `if (open === -1) continue` 把整个调用点**静默丢弃**：既不记
 * site 也不记 unresolved。实测这条路吃掉了 13 处 qy 调用点（管理端申诉批准、
 * 违规计数重置、佣金冲正/结算/重跑/关系拉黑、分组与法币费率删除、用户分组
 * 默认值、违规规则启停、抽奖封面——全是写接口），把它们的路径改成一条后端
 * 根本不存在的字符串，本守卫 5 pass 0 fail 全绿。
 *
 * 而本文件自己的纪律是"求不出来的一律显式报告，绝不静默丢弃 —— 静默丢弃等于
 * 把守卫关掉"。深度计数本身已经能正确跨过 `<…>`，`;` 那一支只是在挡它自己。
 */
function skipGenerics(src: string, from: number): number {
  let i = from
  while (i < src.length && /\s/.test(src[i])) i += 1
  if (src[i] === '<') {
    let depth = 0
    for (; i < src.length; i += 1) {
      if (src[i] === '<') depth += 1
      else if (src[i] === '>') {
        depth -= 1
        if (depth === 0) {
          i += 1
          break
        }
      }
    }
    while (i < src.length && /\s/.test(src[i])) i += 1
  }
  return src[i] === '(' ? i : -1
}

/** 把 {@link DYN} 展开成"路径参数"与"空串"两种，再归一成路由模板。 */
function normalize(candidates: string[]): string[] {
  const out = new Set<string>()
  for (const raw of candidates) {
    let expanded = [raw]
    while (expanded.some((s) => s.includes(DYN))) {
      const next: string[] = []
      for (const s of expanded) {
        const at = s.indexOf(DYN)
        if (at === -1) {
          next.push(s)
          continue
        }
        next.push(
          s.slice(0, at) + PARAM + s.slice(at + 1),
          s.slice(0, at) + s.slice(at + 1)
        )
      }
      expanded = [...new Set(next)].slice(0, MAX_CANDIDATES)
    }
    for (const s of expanded) {
      const full = s.startsWith(QY_API_PREFIX) ? s : QY_API_PREFIX + s
      const segments = full
        .split('?')[0]
        .split('#')[0]
        .split('/')
        .filter((seg) => seg !== '')
        .map((seg) => (seg.includes(PARAM) ? ':param' : seg))
      out.add(`/${segments.join('/')}`)
    }
  }
  return [...out]
}

function listSourceFiles(root: string): string[] {
  const files: string[] = []
  const walk = (dir: string) => {
    for (const entry of readdirSync(dir)) {
      if (entry === 'node_modules' || entry === '__tests__') continue
      const p = join(dir, entry)
      if (statSync(p).isDirectory()) walk(p)
      else if (/\.tsx?$/.test(entry) && !/\.(test|spec)\.tsx?$/.test(entry)) {
        files.push(p)
      }
    }
  }
  walk(root)
  return files.sort()
}

/**
 * 扫出前端**全部** qy 请求路径。
 *
 * 认三种形状：
 *   1. `qyGet/qyPost/qyPut/qyPatch/qyDelete(path, …)` —— 绝大多数；
 *   2. `api.get(\`${QY_API_PREFIX}…\`)` —— 取二进制的那几处刻意绕开信封解包；
 *   3. 任何以 QY_API_PREFIX 开头的模板串 —— 不经过任何请求库、直接交给浏览器的
 *      地址（抽奖封面的 `<img src>` 走匿名端点，属于这一档）。按 GET 记账；
 *      与前两条重叠的会被记两次，无害 —— 对账按候选路径逐条判定，结果相同。
 *
 * `qyMutate(method, path)` 不认：只有 `lib/api.ts` 里那四个转发壳在用它，路径是
 * 形参。那个文件整体在守卫的豁免清单里。
 *
 * 测试文件（`__tests__/`、`*.test.ts`）不扫：里面的路径是断言用的样本，不是调用。
 */
export function collectQyRequestPaths(srcRoot: string): QyRequestScan {
  const sites: QyRequestSite[] = []
  const unresolved: QyUnresolvedRequest[] = []

  for (const abs of listSourceFiles(srcRoot)) {
    const original = readFileSync(abs, 'utf-8')
    if (!/\bqy(Get|Post|Put|Patch|Delete)\b|QY_API_PREFIX/.test(original)) {
      continue
    }
    const src = stripComments(original)
    const file = relative(srcRoot, abs).split(sep).join('/')
    const ctx = buildCtx(src)
    const lineAt = (idx: number) => src.slice(0, idx).split('\n').length

    const push = (
      idx: number,
      method: QyRequestSite['method'],
      expr: string,
      value: string[] | null
    ) => {
      const line = lineAt(idx)
      if (value == null) {
        unresolved.push({ file, line, expr })
        return
      }
      sites.push({ file, line, method, expr, candidates: normalize(value) })
    }

    const helperRe = /\bqy(Delete|Get|Patch|Post|Put)\b/g
    let m: RegExpExecArray | null
    while ((m = helperRe.exec(src)) != null) {
      const open = skipGenerics(src, m.index + m[0].length)
      if (open === -1) continue
      const arg = firstArg(src, open)
      if (arg === '') continue
      push(m.index, METHOD_BY_HELPER[m[1]], arg, evalExpr(arg, ctx))
    }

    const axiosRe = /\bapi\.(delete|get|patch|post|put)\b/g
    while ((m = axiosRe.exec(src)) != null) {
      const open = skipGenerics(src, m.index + m[0].length)
      if (open === -1) continue
      const arg = firstArg(src, open)
      if (!arg.includes('QY_API_PREFIX')) continue
      const inlined = arg.replaceAll('QY_API_PREFIX', `'${QY_API_PREFIX}'`)
      push(m.index, METHOD_BY_AXIOS[m[1]], arg, evalExpr(inlined, ctx))
    }

    // 第三种形状：**不经过任何请求库**、直接交给浏览器的地址。
    //
    // 目前只有抽奖封面在用（`<img src={qyLotCoverSrc(activity)}>` 走匿名端点，
    // 所以它是一个纯字符串，从头到尾没有一层请求库经手）。对它瞎着会很贵：
    // 一条写错的 `<img src>` 不抛异常、不进任何失败统计，表现只是"大厅每张卡
    // 都是碎图标" —— 而本守卫存在的那类缺陷（少一个 /admin 前缀）正是以这种
    // 方式躲过三轮排查的。
    //
    // 只认以 `${QY_API_PREFIX}` 开头的模板串；与前两条形状重叠的会被记两次，
    // 无害 —— 对账按候选路径逐条判定，同一条路径判两次结果相同。
    const rawRe = /`\$\{QY_API_PREFIX\}[^`]*`/g
    while ((m = rawRe.exec(src)) != null) {
      const inlined = m[0].replaceAll('QY_API_PREFIX', `'${QY_API_PREFIX}'`)
      push(m.index, 'GET', m[0], evalExpr(inlined, ctx))
    }
  }

  const order = (a: { file: string; line: number }, b: typeof a) =>
    a.file.localeCompare(b.file) || a.line - b.line
  sites.sort(order)
  unresolved.sort(order)
  return { sites, unresolved }
}

// ───────────────────────── 后端清单 ─────────────────────────

export type QyRoute = { method: string; path: string; segments: string[] }

/**
 * 读后端导出的路由清单。
 *
 * 清单由 `qianye/route_manifest_test.go` 从**真实挂上的 gin 路由树**导出，不是
 * 手抄、也不是从 Go 源码正则抠的 —— 组前缀与参数段名因此逐字一致。
 */
export function loadQyRouteManifest(repoRoot: string): QyRoute[] {
  const text = readFileSync(
    join(repoRoot, 'qianye', 'route_manifest.txt'),
    'utf-8'
  )
  const routes: QyRoute[] = []
  for (const line of text.split('\n')) {
    const trimmed = line.trim()
    if (trimmed === '' || trimmed.startsWith('#')) continue
    const space = trimmed.indexOf(' ')
    const path = trimmed.slice(space + 1)
    routes.push({
      method: trimmed.slice(0, space),
      path,
      segments: path.split('/').filter((s) => s !== ''),
    })
  }
  return routes
}

/**
 * 后端的 `:x` 只吃前端的 `:param` 占位段，**不吃字面量**。
 *
 * 放宽成「`:x` 吃任何一段」曾经让守卫对整整一类缺陷是瞎的：`/admin/withdraw/stats`
 * 少写 `/admin` 变成 `/withdraw/stats`，会被 `GET /api/qy/withdraw/:id` 吸收掉而判为
 * 命中。线上这条不是 404 —— gin 真会把它交给取单条提现的 handler，参数是字符串
 * "stats" —— 所以它比 404 更难查：页面拿到的是一条 400/500，而不是"路由不存在"。
 * 收紧后 202 处调用点一处不掉，等于纯赚。
 *
 * 已知仍然照不到的一档：**同名路径在用户端与管理端各注册了一条**（`/ticket/images`、
 * `/withdraw/:id/proof` 等）。这时漏写 `/admin` 得到的是一条真实存在的用户端路由，
 * 存在性对账按定义看不出来 —— 那是越权/串档，要靠后端鉴权和页面级测试挡，不是这里。
 */
function segmentsMatch(want: string[], route: QyRoute): boolean {
  const got = route.segments
  for (let i = 0; i < got.length; i += 1) {
    if (got[i].startsWith('*')) return true
    if (i >= want.length) return false
    if (got[i].startsWith(':')) {
      if (want[i] !== ':param') return false
      continue
    }
    if (got[i] !== want[i]) return false
  }
  return want.length === got.length
}

/** 任一候选命中后端某条路由即算存在，返回命中的那一条。 */
export function matchQyRoute(
  site: QyRequestSite,
  routes: QyRoute[]
): QyRoute | null {
  for (const candidate of site.candidates) {
    const want = candidate.split('/').filter((s) => s !== '')
    for (const route of routes) {
      if (route.method !== site.method) continue
      if (segmentsMatch(want, route)) return route
    }
  }
  return null
}
