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
 * 把"后端声称是数组的字段"收敛成一个真的能调 `.find` / `.map` 的数组。
 *
 * ## 为什么需要它
 *
 * 提现审核页整页白屏过一次：`Cannot read properties of null (reading 'find')`。
 * 后端 `handleAdminStats` 把 nil 切片序列化成 `null` 而不是 `[]`，
 * 前端 `stats.buckets.find(...)` 直接崩 —— 而 `buckets` 在 `types.ts` 里
 * 声明的是 `QyWithdrawBucket[]`，TypeScript 从头到尾没有任何警告。
 *
 * 根治在后端（见 `qianye/json_array_guard_test.go`），这里是第二道：
 * 契约违约不该以整页白屏收场，而 TS 的类型声明对运行期 JSON 一点约束力都没有。
 *
 * ## 为什么不是 `value ?? []`
 *
 * `??` 只挡 `null` / `undefined`。真实的契约违约还有别的形状：字段被写成
 * 对象（`{}`）、被写成字符串、或者上游反代塞回一段 HTML 被解析成别的东西。
 * 这些值在 `??` 下会原样穿过去，`.find` 照样崩，而报错信息会指向一个
 * 与真实原因毫无关系的地方。`Array.isArray` 一次挡住全部。
 *
 * ## 边界
 *
 * 它**不区分**"后端返回空数组"与"后端返回 null"—— 两者都得到 `[]`。
 * 需要把"接口坏了"与"确实没有数据"分开展示的页面，请自己判断原始值，
 * 不要指望这个函数替你保留那个信息。
 */
export function qyArray<T>(value: T[] | null | undefined): T[] {
  return Array.isArray(value) ? value : []
}
