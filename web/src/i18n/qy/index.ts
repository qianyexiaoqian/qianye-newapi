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
import type { i18n as I18nInstance } from 'i18next'

import en from './en.json'
import zhCN from './zh.json'

/**
 * qy 扩展的翻译资源。
 *
 * **刻意不写进 `src/i18n/locales/*.json`**：那 7 个文件各有五千余行且是上游的
 * 高频改动区，往里插键等于每次合并上游都要手工解一遍行级冲突。放独立目录后
 * 冲突面为 0，并且 `bun run i18n:sync`（只扫 `locales/`）也扫不到我们
 * —— qy 相关改动**禁止**运行该脚本。
 *
 * 只提供 en 与 zh：`config.ts` 里 `fallbackLng: 'en'`，其余 5 个语种会自动
 * 回落英文，不必为了凑齐语言而机翻。
 */
const QY_BUNDLES: Record<string, Record<string, string>> = {
  // key 必须与 config.ts 的 resources key 完全一致（zhCN 而不是 zh）
  en,
  zhCN,
}

/**
 * 把 qy 的键并入默认命名空间。
 *
 * 必须在 `i18n.init()` 之后调用：init 传的是静态 resources 且没有 backend，
 * 资源仓库在 init 的调用栈内同步填充完毕，因此紧随其后调用是安全的。
 *
 * `deep=true` 深合并、`overwrite=true` 允许覆盖同名键（`qy_` 前缀已经保证
 * 不会撞上上游的键，overwrite 只是为了热更新时行为可预期）。
 */
export function registerQyResources(i18n: I18nInstance): void {
  for (const [language, bundle] of Object.entries(QY_BUNDLES)) {
    i18n.addResourceBundle(language, 'translation', bundle, true, true)
  }
}
