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
import type {
  QyUgrBlockCode,
  QyUgrImpact,
  QyUgrMigrationDiff,
  QyUgrResidue,
  QyUgrUsableChange,
} from '../types'

/**
 * `model.User.Group` 上 `gorm:"default:'default'"` 兜底出来的那个名字。
 *
 * 与后端 `groupns.upstreamDefaultUserGroup` 同值。这是本文件里**唯一**一处
 * 前端自己持有的判据，理由是它必须在**打开影响面之前**就能回答：删除入口在
 * 表行上，而影响面要点开弹窗才拉。让运营点开弹窗、选完迁移目标、再发现按钮
 * 是死的，正是项目方这次报上来的那一幕。
 *
 * 它的漂移风险接近零（改掉它等于改数据库列默认值），而后端
 * `TestUserGroupDefaultIsNeverRemovable` 把这个名字与两条拒绝分支钉在一起。
 */
export const QY_UGR_UPSTREAM_DEFAULT_GROUP = 'default'

/** 与后端 `normalizeGroupKey` 同口径：去两侧空白 + 折叠大小写。 */
export function qyUgrIsUpstreamDefaultGroup(name: string): boolean {
  return name.trim().toLowerCase() === QY_UGR_UPSTREAM_DEFAULT_GROUP
}

/**
 * 表行上删除入口的状态。
 *
 * ── 入口级闸门只许照抄后端真的有的那一条拒绝 ──
 *
 * 早先这里对**所有未登记的分组**关掉删除按钮，理由写着「先点编辑保存一次，
 * 保存会自动补登记，再回来删」。那是一条前端自己发明的闸门：后端
 * `adminDeleteUserGroup` 的注释逐字写着「不要求有登记行 —— 只在 users.group
 * 里的历史遗留分组同样要能删(而且要能带迁移地删)，它们正是最需要被清掉的
 * 那一类」。表现是运营被要求先把一个正要删掉的分组**建出来**，而这一整步毫无
 * 意义。真正会 400 的只有 `lookupUserGroup` 那一条：既没有登记行、users.group
 * 里也没有人挂着（这种行进得了矩阵行轴，因为它还是 `GroupGroupRatio` 的一个
 * 外层键或有一条 scope 行）。
 *
 * ── `default` 不在这里拦 ──
 *
 * 它当然永远删不掉，但**那句话属于后端**：`deletability()` 对它返回一段完整的
 * 理由，而删除弹窗是全站唯一渲染 `block_reason` 的地方。把按钮关掉等于让那段
 * 理由永远到不了屏幕，运营读到的是前端另写的一份副本 —— 两份文本之间没有任何
 * 跨语言的一致性检查，后端哪天改了口径，界面上仍旧是旧的那一份而没有任何测试
 * 会红。所以这里只在按钮旁边印一句**短的状态标签**（不复述理由），按钮照常
 * 打开弹窗，完整的原因与替代做法由后端原话 + `block_code` 指路负责。
 */
export type QyUgrDeleteEntryBlock =
  /** 删除端点的 `lookupUserGroup` 找不到这个名字：没有登记行、也没有人挂着。 */
  'not_addressable'

/**
 * 删除入口此刻的样子。
 *
 * `enabled` 与 `noteKey` 是**两件事**：能按不等于删得掉（`default` 能打开弹窗、
 * 但弹窗里的删除键是灰的），不能按时也必须有字。
 */
export type QyUgrDeleteEntry = {
  /** 按钮能不能按。`false` 只在删除端点根本寻址不到这个名字时出现。 */
  enabled: boolean
  /**
   * 常驻在按钮下方的**一句短话**的文案键。`null` = 不印。
   *
   * 刻意保持短：它落在表格最右一列（`max-w-64`），完整的段落会把 `default` 那
   * 一行撑到七八行高，而这句话对运营是"读一次就够"的常识。完整理由在弹窗里。
   */
  noteKey: string | null
}

export function qyUgrDeleteEntry(row: {
  name: string
  registered: boolean
  user_count: number
}): QyUgrDeleteEntry {
  if (!row.registered && row.user_count === 0) {
    return { enabled: false, noteKey: 'qy_ug_delete_not_addressable' }
  }
  if (qyUgrIsUpstreamDefaultGroup(row.name)) {
    return { enabled: true, noteKey: 'qy_ug_delete_default_note' }
  }
  return { enabled: true, noteKey: null }
}

/**
 * 表行上**迁移**入口的状态。
 *
 * ── 它刻意不照抄删除那一套闸门 ──
 *
 * 迁移入口存在的全部理由就是：`default` 永远删不掉，而那 700 个人必须能挪走。
 * 把 `deletability()` 的四条(default / 套餐引用 / block 残留 / 没有目标)搬过来，
 * 这个入口就会在最需要它的那一行上灰掉，而项目方报上来的正是那一幕。
 *
 * 后端 `adminMigrateUserGroup` 真正会拒的只有三条：寻址不到这个名字、这一档
 * 现在没有人、以及目标那一侧的问题(目标必填/不能是自己/必须已登记)。
 * 前两条在表行上就能判，第三条属于弹窗。
 */
export type QyUgrMigrateEntryBlock =
  /** 迁移端点的 `lookupUserGroup` 找不到这个名字：没有登记行、也没有人挂着。 */
  | 'not_addressable'
  /** 这一档现在一个在册用户都没有 —— 没有东西可迁。 */
  | 'no_users'

export type QyUgrMigrateEntry = {
  enabled: boolean
  /** 常驻在按钮下方的一句短话的文案键。`null` = 不印。 */
  noteKey: string | null
}

export function qyUgrMigrateEntry(row: {
  registered: boolean
  user_count: number
}): QyUgrMigrateEntry {
  if (!row.registered && row.user_count === 0) {
    return { enabled: false, noteKey: 'qy_ug_delete_not_addressable' }
  }
  if (row.user_count === 0) {
    return { enabled: false, noteKey: 'qy_ugr_migrate_no_users' }
  }
  return { enabled: true, noteKey: null }
}

/**
 * 迁移按钮此刻能不能按，以及为什么不能。
 *
 * 与 {@link qyUgrDeleteBlock} 同一个手法：**一个判据都不自己发明**，只把后端已经
 * 算好的结论翻译成按钮状态。唯一属于前端的是"影响面还没拉回来"。
 *
 * 注意这里**不看 `impact.deletable`** —— 那是删除的结论，而迁移与它无关：
 * `default` 的 `deletable` 恒为 `false`，照着它灰按钮等于把这个功能关掉。
 */
export type QyUgrMigrateBlock =
  /** 影响面还在路上。此时的一切数字都还不存在。 */
  | 'loading'
  /** 这一档现在没有人可迁（打开弹窗之后被别人挪空了）。 */
  | 'no_users'
  /** 目标分组一个模型分组都用不了，必须显式勾选覆盖。 */
  | 'needs_ack'
  /** 还没选目标。 */
  | 'needs_target'

/** 「迁移键为什么是灰的」——每一种状态各一句，穷尽 `Record`。 */
export const QY_UGR_MIGRATE_GATE_KEY: Record<QyUgrMigrateBlock, string> = {
  loading: 'qy_ugr_gate_loading',
  needs_ack: 'qy_ugr_migrate_gate_needs_ack',
  needs_target: 'qy_ugr_migrate_gate_needs_target',
  no_users: 'qy_ugr_migrate_gate_no_users',
}

export function qyUgrMigrateBlock(
  impact: QyUgrImpact | null,
  target: string,
  ack: boolean
): QyUgrMigrateBlock | null {
  if (impact == null) return 'loading'
  if (impact.users === 0) return 'no_users'
  if (target === '') return 'needs_target'
  if (impact.diff?.loses_everything === true && !ack) return 'needs_ack'
  return null
}

/**
 * 表行/编辑弹窗上**改名**入口的入口级闸门。
 *
 * 与删除不同，改名端点 `adminRenameUserGroup` 第一件事就是
 * `Where("name = ?", from).Take(&before)`，没有登记行直接 400 —— 所以"未登记"
 * 在这里是后端真的有的一条拒绝，不是发明出来的。
 *
 * 它只覆盖后端 `renamability()` 四条里的两条中的一条（`default`）加上登记行
 * 这一条；**block 残留那一条要服务端探测**，由编辑弹窗拉一次影响面、按
 * `impact.renamable / rename_block_reason / rename_block_code` 给出，见
 * `user-group-dialog.tsx`。只接一半的表现是「点得动、提交才知道不行」，
 * 那与项目方报上来的缺陷是同一个形状，只是从删除挪到了改名。
 */
export type QyUgrRenameEntryBlock =
  /** `default` 档，永不可改名。 */
  | 'upstream_default'
  /** 只在 `users.group` 里被观测到，登记表里没有行 —— 改名端点无从下手。 */
  | 'unregistered'

export function qyUgrRenameEntryBlock(row: {
  name: string
  registered: boolean
}): QyUgrRenameEntryBlock | null {
  // 先判 default：一个未登记的 `default` 补一次登记之后仍然改不了名，
  // 先说"去补登记"会把运营支到一条走不通的路上。
  if (qyUgrIsUpstreamDefaultGroup(row.name)) return 'upstream_default'
  if (!row.registered) return 'unregistered'
  return null
}

/**
 * 改名入口闸门的文案。穷尽 `Record`，理由同 {@link QY_UGR_BLOCK_NEXT_STEP_KEY}。
 *
 * 同样保持短：编辑弹窗在影响面回来之后会把**后端原话**印在同一个位置，
 * 这两句只是那之前的占位与状态标签，不复述后端的理由。
 */
export const QY_UGR_RENAME_ENTRY_BLOCK_KEY: Record<
  QyUgrRenameEntryBlock,
  string
> = {
  unregistered: 'qy_ug_rename_needs_registry',
  upstream_default: 'qy_ug_rename_blocked_upstream_default',
}

/**
 * 「那我该干什么」——每一条拒绝分支各一句指路。
 *
 * 与 {@link QyUgrBlockCode} 一一对应且**必须穷尽**：`Record` 而不是
 * `Partial<Record>`，后端新增一条分支、前端跟着加进联合类型时，漏掉这里
 * TypeScript 当场报错。少一条的表现是弹窗上只剩「为什么不行」，
 * 而运营为此花的时间正是这次被投诉的那一段。
 */
export const QY_UGR_BLOCK_NEXT_STEP_KEY: Record<QyUgrBlockCode, string> = {
  blocking_plans: 'qy_ugr_next_blocking_plans',
  blocking_residues: 'qy_ugr_next_blocking_residues',
  no_migration_target: 'qy_ugr_next_no_migration_target',
  upstream_default: 'qy_ugr_next_upstream_default',
}

/**
 * 删除按钮此刻能不能按,以及为什么不能。
 *
 * ── 为什么把它抽成一个纯函数 ──
 *
 * 这四道闸门里有三道在后端也各有一份(`deletability`、迁移目标必填、
 * `LosesEverything` 的强制勾选),而两边各写一遍必然漂移。漂移的方向永远是
 * **按钮亮着、点下去 400** —— 运营会以为是系统坏了,重试三次,然后去问人。
 *
 * 所以这里只做一件事:把后端已经算好的结论(`deletable` / `block_reason` /
 * `diff.loses_everything`)翻译成按钮状态。**一个判据都不自己发明**,
 * 唯一属于前端的那条是"影响面还没拉回来"。
 */
export type QyUgrDeleteBlock =
  /** 服务端说了不能删(套餐引用 / block 残留 / default 档)。 */
  | 'blocked'
  /** 影响面还在路上。此时的一切数字都还不存在。 */
  | 'loading'
  /** 目标分组一个模型分组都用不了,必须显式勾选覆盖。 */
  | 'needs_ack'
  /** 这一档还有人,迁移目标必填。 */
  | 'needs_target'

/**
 * 「删除键为什么是灰的」——四种状态各一句，**一条都不许沉默**。
 *
 * 穷尽 `Record`：新增一种闸门而漏掉文案时 TypeScript 当场报错。漏掉的表现是
 * 一个按不动、也不解释的按钮，而那正是这次被投诉的那一幕。
 *
 * `blocked` 那条只说"上面那段红框说明了原因"：原因与指路已经在弹窗顶部给全，
 * 同一段话在一屏里出现两遍会让人以为是两件事。
 */
export const QY_UGR_DELETE_GATE_KEY: Record<QyUgrDeleteBlock, string> = {
  blocked: 'qy_ugr_gate_blocked',
  loading: 'qy_ugr_gate_loading',
  needs_ack: 'qy_ugr_gate_needs_ack',
  needs_target: 'qy_ugr_gate_needs_target',
}

export function qyUgrDeleteBlock(
  impact: QyUgrImpact | null,
  target: string,
  ack: boolean
): QyUgrDeleteBlock | null {
  if (impact == null) return 'loading'
  if (!impact.deletable) return 'blocked'
  if (impact.users > 0 && target === '') return 'needs_target'
  // `loses_everything` 只在 diff 真的算过(带 target 请求过一次)之后才可信。
  // 没有 diff 时不拦:此时要么没人要迁,要么目标还没选 —— 后者已经被上一条拦住。
  if (impact.diff?.loses_everything === true && !ack) return 'needs_ack'
  return null
}

/**
 * 「迁完之后仍然会有人被放回这一档」的两类来源。
 *
 * ── 为什么它必须在**按下确认之前**出现在屏幕上 ──
 *
 * 迁移刻意不拦、也不改这两处引用（拦掉的话 `default` 又变成挪不走的了，而那
 * 正是这次报上来的缺陷本身）。代价是迁完之后这一档会被重新填回来 —— 而这句话
 * 恰恰是运营决定「要不要迁、迁之前要不要先去改注册默认分组」的前提。只在迁移
 * 成功之后用一条 toast 说，等于没给他做决定的机会：707 个人挪走，几天后人数
 * 自己长回去，而屏幕上没有任何一处解释过为什么。
 *
 * 两类来源与后端 `groupns.userGroupRefillSources` 一一对应：
 *
 *   plans     升级/降级分组仍然指向它的套餐（`blocking_plans`）
 *   residues  处置为 `rewrite` 且真的有行的残留 —— 最典型的就是
 *             「新注册用户默认分组」，而 `default` 几乎必然同时是它
 *
 * 只画其中一半是最坏的形状：屏幕上有一段"会被填回来"的告警，运营据此认为
 * 已经看全了，而真正会把人送回来的那一处恰好不在里面。
 */
export function qyUgrRefillSources(impact: QyUgrImpact | null | undefined): {
  plans: string[]
  residues: QyUgrResidue[]
} {
  return {
    plans: impact?.blocking_plans ?? [],
    residues: (impact?.residues ?? []).filter(
      (residue) => residue.disposition === 'rewrite' && residue.rows > 0
    ),
  }
}

/**
 * 把可用清单的变化按方向分成三堆。
 *
 * 三堆的后果完全不同,混在一个列表里读不出来:
 *
 *	removed  这批人手上指向这些模型分组的令牌,在迁移完成的那一刻同时 403
 *	added    他们多了几个池子可选 —— 通常是好事,但也可能是一次意外的开放
 *	repriced 从下一秒开始的账单变化。数字是**十进制字符串**,不是 number
 */
export function qyUgrGroupChanges(diff: QyUgrMigrationDiff | null | undefined) {
  const removed: QyUgrUsableChange[] = []
  const added: QyUgrUsableChange[] = []
  const repriced: QyUgrUsableChange[] = []
  for (const change of diff?.changes ?? []) {
    if (change.kind === 'removed') removed.push(change)
    else if (change.kind === 'added') added.push(change)
    else repriced.push(change)
  }
  return { added, removed, repriced }
}
