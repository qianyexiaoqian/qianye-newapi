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
import homeEn from './home-en.json'
import homeZhCN from './home-zh.json'
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
 *
 * ── 为什么 `home-*.json` 与 `{en,zh}.json` 分开 ──
 *
 * `{en,zh}.json` 里的键**全部**带 `qy_` 前缀，与上游不可能撞名；而
 * `home-*.json` 的键是**上游首页组件里的英文原文**，它存在的目的正是**覆盖**
 * 上游那份默认文案（`features/home/` 的 hero / features / how-it-works / cta
 * 三十余处 `t('…')`，上游的 `locales/*.json` 里一个都没登记，所有语种都直接
 * 显示英文原文）。两种截然相反的意图放在同一个文件里，日后必然有人往
 * `zh.json` 里塞一条不带前缀的键、再花半天查为什么某个上游页面的字变了。
 *
 * 覆盖能生效靠的是调用时机与 `overwrite=true`：见下面 `registerQyResources`。
 *
 * 这个覆盖面的两条硬约束由 `__tests__/home-copy.test.ts` 把关：
 *   1. 每个键都必须能在 `features/home/` 里找到引用 —— 否则就是写了没接上；
 *   2. 每个键都**不能**在 `features/home/` 之外被引用 —— 否则这条全局覆盖
 *      会顺手改掉别的页面的字。
 */
const QY_BUNDLES: Record<string, Record<string, string>> = {
  // key 必须与 config.ts 的 resources key 完全一致（zhCN 而不是 zh）
  en: { ...en, ...homeEn },
  zhCN: { ...zhCN, ...homeZhCN },
}

/**
 * 把 qy 的键并入默认命名空间。
 *
 * 必须在 `i18n.init()` 之后调用：init 传的是静态 resources 且没有 backend，
 * 资源仓库在 init 的调用栈内同步填充完毕，因此紧随其后调用是安全的。
 *
 * `deep=true` 深合并、`overwrite=true` 允许覆盖同名键。对 `{en,zh}.json` 那
 * 一半，`qy_` 前缀已经保证不会撞上上游的键，overwrite 只是让热更新行为可预期；
 * 对 `home-*.json` 那一半，overwrite **就是全部意义所在** —— 它要压掉上游
 * 首页的默认文案。两件事共用同一个调用，改动这一行前先看清楚上面的说明。
 */
export function registerQyResources(i18n: I18nInstance): void {
  for (const [language, bundle] of Object.entries(QY_BUNDLES)) {
    i18n.addResourceBundle(language, 'translation', bundle, true, true)
  }
}
