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
 * `bun:test` 的最小类型声明。
 *
 * ## 为什么需要它
 *
 * 本仓的测试统一写 `node:test` + `node:assert`。但 `bun test` 对 `node:test`
 * 的垫片有一条未实现分支（`describe() inside another test()`，
 * oven-sh/bun#5090）：同一个跑测单元里，只要前一个文件还有异步用例没跑完，
 * 后面每个文件的顶层 `describe` 就会抛错，整份文件被静默吞掉。
 * `scripts/run-tests.mjs` 的文件头记了这件事，也点名 `bun:test` 是当前唯一的
 * 规避方式 —— 它走 bun 自己的注册表，不经过那条垫片。
 *
 * 所以个别落在"已经有异步用例在跑"的目录里的测试文件必须用 `bun:test`。
 * 而 `tsconfig.app.json` 的 `types` 只有 `node`，装 `@types/bun` 会为了三个
 * 函数名把一整套 Bun 全局类型引进来（它会覆写 `fetch`、`Response` 这些
 * DOM 里已有的声明）。这里只声明真正用到的那三个。
 *
 * **不要在这里补全整套 API。** 少一个函数名的表现是编译期报错，一目了然；
 * 而声明得比实际宽会让人以为某个 bun-only 的能力在别处也能用。
 */
declare module 'bun:test' {
  export function describe(name: string, fn: () => void): void
  export function test(name: string, fn: () => Promise<void> | void): void
  export function afterAll(fn: () => Promise<void> | void): void
}
