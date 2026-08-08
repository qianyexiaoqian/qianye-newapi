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
 * 矩阵草稿的纯逻辑层：编辑意图 → 显式动作列表。
 *
 * 抽成无 React 依赖的纯函数不是为了好看，而是因为这一层的三条规则每一条错了
 * 都直接是钱：
 *
 *   1. **空输入 = 继承（null），敲进去的 0 = 显式免费。** 两者行为不同，
 *      合并成一个「假值」判断会让一个本该免费的分组静默变成兜底价。
 *   2. **回到原值 = 没有改动。** 用户点开又点回来，不能生成一条 grant + 一条
 *      revoke，那会让「未保存改动 N 项」这个数字骗人，也会让 draft_hash 与
 *      预览时算的对不上、白吃一个 409。
 *   3. **动作列表顺序确定。** 预览与保存必须提交逐字相同的 JSON，否则后端
 *      两次算出的 `draft_hash` 不同，闸门会把一次合法保存拦下来。
 */

import type { QyGmCell, QyGmCellChange } from '../types'

/**
 * 格子的复合键。
 *
 * 分隔符用 NUL（`\u0000`）而不是常见的 `-` 或 `:`：分组名是运营自由输入的
 * 字符串，站里已经有 `浅夜の梦专属号池` 这类名字，用可打印字符做分隔迟早会撞上
 * 一个含该字符的分组名，让两个不同的格子折叠成一个。NUL 不可能出现在分组名里。
 *
 * **必须写成 `\u0000` 转义，不能直接嵌一个裸字节。** 裸 NUL 会让 git 把整个
 * 文件判定成二进制：`git diff` 只输出 `Bin N -> M bytes`、PR 页面显示
 * 「Binary file not shown」、ripgrep 默认跳过、合并冲突只能整文件二选一。
 * 而这一层的三条规则每一条错了都直接是钱 —— 全仓最需要逐行复核的文件，
 * 不能恰好是唯一不可复核的那一个。转义写法的运行时行为完全相同。
 */
const QY_GM_KEY_SEP = '\u0000'

export function qyGmCellKey(userGroup: string, modelGroup: string): string {
  return `${userGroup}${QY_GM_KEY_SEP}${modelGroup}`
}

/**
 * 复合键 → 它属于哪个用户分组。
 *
 * 存在的唯一理由是**不让分隔符漏出这个文件**：主从式那一页要给「本次草稿动过」
 * 的分组在左列表上打标记，而它手上只有一把复合键。调用方自己 `split('\u0000')`
 * 就等于把键的编码复制了一份，日后分隔符一改，那一份不会有任何信号地失效
 * （表现是标记全部消失，而标记消失恰好长得像"没有未保存改动"）。
 *
 * 键里恒有分隔符（只能由 {@link qyGmCellKey} 产生）；万一没有，整串就是分组名，
 * 这个回落不会让调用方拿到一个空字符串去匹配。
 */
export function qyGmCellKeyUserGroup(key: string): string {
  const separator = key.indexOf(QY_GM_KEY_SEP)
  return separator < 0 ? key : key.slice(0, separator)
}

/**
 * 整列批量的三个动作。
 *
 * 定义在纯逻辑层而不是矩阵组件里：状态机（`lib/use-editor.ts`）与渲染它的
 * 表头菜单（`components/matrix-grid.tsx`）都要用它，而把它留在组件里会让
 * 状态机反向依赖组件层，或者更糟 —— 各写一份同名联合，某一天多出一个动作时
 * 只改了一边，另一边静默地少一个分支。
 */
export type QyGmColumnBulk = 'clear' | 'inherit_all' | 'select_all'

/** 倍率输入框的草稿值。 */
export type QyGmRatioDraft =
  /** 输入框是空的 —— 继承该模型分组的兜底倍率。 */
  | { kind: 'inherit' }
  /** 运营敲进去的原文。**保留字符串**，见 `qyGmParseRatio` 的说明。 */
  | { kind: 'set'; raw: string }

/** 一个格子上「运营动了什么」。字段缺省 = 这一维没动过。 */
export type QyGmDraftEntry = {
  granted?: boolean
  ratio?: QyGmRatioDraft
  /**
   * 按格备注的草稿原文。
   *
   * **刻意是一个裸 string 而不是 `{kind}` 联合**（倍率那样的三态）：备注只有
   * 「有一句」与「没有」两种，空串就是后者，而后者的行为恰好等于回落模型分组的
   * 默认备注。给它造第三态只会多出一个界面上表达不出来、行为上也没有差别的状态。
   */
  note?: string
}

/** 草稿：只装被动过的格子。没被动过的格子在这里查不到，这是刻意的。 */
export type QyGmDraft = ReadonlyMap<string, QyGmDraftEntry>

// ───────────────────────────── 倍率解析 ─────────────────────────────

/**
 * 输入框原文 → 倍率。
 *
 * 保留字符串再解析，而不是让 `<input type='number'>` 直接给 number：
 * number 型输入框在半输入状态（`0.`、`1e`）下 `valueAsNumber` 是 `NaN`，
 * 受控组件会当场把用户正在敲的字符抹掉。
 *
 * 返回 `'invalid'` 而不是抛异常：非法输入要停在格子上变红，不能中断整张表的
 * 渲染，也不能被静默当成 0（那正是本轮要消灭的「界面数字骗人」形状）。
 */
export function qyGmParseRatio(raw: string): 'invalid' | number {
  const text = raw.trim()
  if (text === '') return 'invalid'
  // 只认十进制字面量。`1e3` / `0x10` / `Infinity` 都是合法的 JS 数字字面量，
  // 但它们出现在倍率格里几乎必然是误输入，静默接受会把一个离谱的乘数写进计费。
  if (!/^\d+(\.\d+)?$/.test(text)) return 'invalid'
  const value = Number(text)
  if (!Number.isFinite(value)) return 'invalid'
  // 上限不是随手定的：倍率会乘进 quota，而 quota 列是 32 位整数。
  // 真正的饱和保护在后端（`common.QuotaFromFloat`），这里只是不让一个明显
  // 荒谬的值走完整条链路再被后端拒。
  if (value > 1000) return 'invalid'
  return value
}

/**
 * 格子当前显示的倍率草稿值（未编辑时回落服务端状态）。
 *
 * 服务端 `source === 'inherit'` 时输入框**留空**而不是预填兜底值 ——
 * 预填的后果是运营随手保存一遍，就把全站的继承格全部固化成硬编码覆盖，
 * 之后改兜底倍率再也不会影响它们，而没有任何人做过这个决定。
 */
export function qyGmRatioDraftOf(
  cell: QyGmCell | undefined,
  entry: QyGmDraftEntry | undefined
): QyGmRatioDraft {
  if (entry?.ratio != null) return entry.ratio
  if (cell == null || cell.source === 'inherit' || cell.ratio == null) {
    return { kind: 'inherit' }
  }
  return { kind: 'set', raw: String(cell.ratio) }
}

/**
 * 格子当前显示的备注草稿值（未编辑时回落服务端状态）。
 *
 * 后端尚未下发 `note` 时恒为空串 —— 与「这一格没写备注」同一个显示，
 * 而那正是那种后端下的真实行为。
 */
export function qyGmNoteDraftOf(
  cell: QyGmCell | undefined,
  entry: QyGmDraftEntry | undefined
): string {
  return entry?.note ?? cell?.note ?? ''
}

/** 格子当前的可选性草稿值（未编辑时回落服务端状态）。 */
export function qyGmGrantedOf(
  cell: QyGmCell | undefined,
  entry: QyGmDraftEntry | undefined
): boolean {
  if (entry?.granted != null) return entry.granted
  return cell?.granted ?? false
}

// ───────────────────────────── 动作列表 ─────────────────────────────

/** 草稿里所有解析不通过的格子。非空时禁止保存与预览。 */
export function qyGmInvalidCells(draft: QyGmDraft): string[] {
  const invalid: string[] = []
  for (const [key, entry] of draft) {
    if (entry.ratio?.kind !== 'set') continue
    if (qyGmParseRatio(entry.ratio.raw) === 'invalid') invalid.push(key)
  }
  return invalid.sort()
}

/** 动作在提交列表里的固定次序。见文件头第 3 条。 */
const ACTION_ORDER: Record<QyGmCellChange['action'], number> = {
  clear_ratio: 0,
  set_ratio: 1,
  revoke: 2,
  grant: 3,
  set_note: 4,
}

/**
 * 草稿 → 显式动作列表。**与服务端状态相同的格子不产出任何动作。**
 *
 * 排序是契约的一部分：预览与保存提交同一份草稿时必须得到逐字相同的 JSON，
 * 否则后端两次算出的 `draft_hash` 不同，闸门会把一次合法保存拦成 409。
 * 排序键取 (用户分组, 模型分组, 动作)，三者合起来在一份草稿里唯一。
 */
export function qyGmBuildChanges(
  draft: QyGmDraft,
  serverCells: ReadonlyMap<string, QyGmCell>
): QyGmCellChange[] {
  const changes: QyGmCellChange[] = []

  for (const [key, entry] of draft) {
    const separator = key.indexOf(QY_GM_KEY_SEP)
    if (separator < 0) continue
    const userGroup = key.slice(0, separator)
    const modelGroup = key.slice(separator + 1)
    const cell = serverCells.get(key)

    if (entry.granted != null && entry.granted !== (cell?.granted ?? false)) {
      changes.push({
        user_group: userGroup,
        model_group: modelGroup,
        action: entry.granted ? 'grant' : 'revoke',
        ratio: null,
      })
    }

    /*
      备注：与服务端一致就不产出动作，和上面两维同一条规则（文件头第 2 条）。

      比较**去两侧空白之后**的原文：运营在句末不小心敲了一个空格，实际显示给
      用户的那一句一个字都没变，却会产出一条 `set_note`。而备注动作与倍率动作
      走同一次两库写入，多一条无意义的动作就多一次"倍率成功、清单失败"的机会 ——
      本轮之前的经验是这条链上任何一次多余的写入最后都会被人在事故里数出来。
    */
    if (entry.note != null) {
      const nextNote = entry.note.trim()
      const serverNote = (cell?.note ?? '').trim()
      if (nextNote !== serverNote) {
        // 空串就是「清掉这一格的备注」（回落模型分组的默认备注）——
        // 后端刻意没有 `clear_note`：备注只有"有一句"与"没有"两种，
        // 造第三态只会多一条与它完全等价、却要在两侧各实现一遍的路径。
        changes.push({
          user_group: userGroup,
          model_group: modelGroup,
          action: 'set_note',
          ratio: null,
          note: nextNote,
        })
      }
    }

    if (entry.ratio == null) continue
    const serverRatio =
      cell == null || cell.source === 'inherit' ? null : cell.ratio

    if (entry.ratio.kind === 'inherit') {
      // 本来就是继承 → 不产出 clear_ratio。多发一条无效动作会让后端白写一次
      // `options`，也会把「未保存改动」的计数抬高。
      if (serverRatio == null) continue
      changes.push({
        user_group: userGroup,
        model_group: modelGroup,
        action: 'clear_ratio',
        ratio: null,
      })
      continue
    }

    const parsed = qyGmParseRatio(entry.ratio.raw)
    if (parsed === 'invalid') continue
    if (serverRatio != null && serverRatio === parsed) continue
    changes.push({
      user_group: userGroup,
      model_group: modelGroup,
      action: 'set_ratio',
      ratio: parsed,
    })
  }

  return changes.sort((a, b) => {
    if (a.user_group !== b.user_group) {
      return a.user_group < b.user_group ? -1 : 1
    }
    if (a.model_group !== b.model_group) {
      return a.model_group < b.model_group ? -1 : 1
    }
    return ACTION_ORDER[a.action] - ACTION_ORDER[b.action]
  })
}

/**
 * 顶部差异条的计数。
 *
 * **`revoke` 单独计数**：它是唯一会打断线上流量的动作类型，和改价混在一个
 * 数字里，运营就看不出这次保存到底会不会让人断服。
 */
export type QyGmDiffCounts = {
  grant: number
  revoke: number
  reprice: number
  /**
   * 备注改动。**不并进 `reprice`**：改价要过影响面闸门（保存成功那一秒起按新价
   * 扣钱），改一句给用户看的说明不会动任何一分钱、也不会让任何请求变 403。
   * 并进去的后果是运营改一句错别字都要先跑一次全站影响面预览，而那份报告里
   * 关于这次改动的部分是空的 —— 闸门一旦开始出现"看了也没用"的场景，它就会被
   * 当成走过场。
   */
  note: number
  total: number
}

export function qyGmCountChanges(changes: QyGmCellChange[]): QyGmDiffCounts {
  const counts: QyGmDiffCounts = {
    grant: 0,
    revoke: 0,
    reprice: 0,
    note: 0,
    total: changes.length,
  }
  for (const change of changes) {
    if (change.action === 'grant') counts.grant += 1
    else if (change.action === 'revoke') counts.revoke += 1
    else if (change.action === 'set_note') counts.note += 1
    else counts.reprice += 1
  }
  return counts
}

/**
 * 本次草稿是否含撤销边。
 *
 * 含撤销 → 保存按钮在看过预览之前禁用。放开与改价不需要这道闸：它们不会让
 * 任何一个正在跑的请求变成 403。
 */
export function qyGmHasRevoke(changes: QyGmCellChange[]): boolean {
  return changes.some((change) => change.action === 'revoke')
}

/**
 * 草稿的本地指纹 —— 用来判断「现在要保存的，是不是刚才预览过的那一份」。
 *
 * 与后端的 `draft_hash` **不是同一个东西也不需要相同**：这一份只在前端内部
 * 比对，作用是在运营预览之后又动了一格时把保存按钮重新锁上。真正的闸门是
 * 后端重算的三个哈希，前端这一层只是不让人白吃一个 409。
 */
export function qyGmDraftFingerprint(changes: QyGmCellChange[]): string {
  return changes
    .map(
      (change) =>
        // 备注原文进指纹：不进的话「把备注从 A 改成 B」与「从 A 改成 C」算出
        // 同一个指纹，运营预览之后又改一次备注，闸门不会重新锁上。
        `${change.user_group}\u0000${change.model_group}\u0000${change.action}\u0000${change.ratio ?? ''}\u0000${change.note ?? ''}`
    )
    .join('\u0001')
}

// ───────────────────────────── 索引与告警 ─────────────────────────────

/** 服务端格子的索引。矩阵是稀疏的，查不到 = 不可选且无覆盖。 */
export function qyGmIndexCells(cells: QyGmCell[]): Map<string, QyGmCell> {
  const map = new Map<string, QyGmCell>()
  for (const cell of cells) {
    map.set(qyGmCellKey(cell.user_group, cell.model_group), cell)
  }
  return map
}

/**
 * 两个轴上仅大小写不同的名字。
 *
 * **只告警，绝不折叠。** 倍率侧 `GetGroupGroupRatio(用户分组, 模型分组)` 是
 * 精确 map 查找，在三条计费路径里，我们无权改。成员资格侧一旦折叠大小写，
 * `users.group='VIP'` 会命中 `vip` 的清单拿到访问权，而倍率查不到 → 回落兜底：
 * 管理端矩阵显示 0.3、实际按 1.0 扣、零告警。那正是本轮要消灭的形状，
 * 不能一边修一个骗人的数字一边造一个新的。
 */
export function qyGmCaseNearMisses(
  // 只收名字：这一层要的就是两个轴上的字符串，收整行会让调用方（和测试）为了
  // 满足类型去凑十几个与判据无关的字段，而凑出来的那些值会被下一个人当真。
  userGroups: readonly { name: string }[],
  modelGroups: readonly { name: string }[]
): { left: string; right: string }[] {
  const seen = new Map<string, string>()
  const pairs: { left: string; right: string }[] = []
  for (const name of [
    ...userGroups.map((group) => group.name),
    ...modelGroups.map((group) => group.name),
  ]) {
    const folded = name.toLowerCase()
    const previous = seen.get(folded)
    if (previous == null) {
      seen.set(folded, name)
      continue
    }
    if (previous === name) continue
    if (pairs.some((pair) => pair.left === previous && pair.right === name)) {
      continue
    }
    pairs.push({ left: previous, right: name })
  }
  return pairs
}
