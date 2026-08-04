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
import {
  isQyLotVoided,
  qyLotOptions,
  qyLotTiers,
  type QyLotProof,
  type QyLotProofEntry,
  type QyLotProofWinner,
  type QyLotTier,
} from '../types'

/**
 * `lot-v1` 公正性协议的**浏览器内实现**。
 *
 * ## 为什么这段代码必须存在
 *
 * 「下载数据自己去验」在实践中等于没人验。用户真正会做的是点一下按钮，
 * 亲眼看着复算在**自己的机器上**跑出与平台一模一样的名单。所以这里用
 * WebCrypto 完整重跑一遍协议，**不请求任何服务端验证接口** —— 那会让
 * 「自己验」退化成「让平台说它验过了」，而后者一点公正性都没有。
 *
 * ## 与后端的关系
 *
 * 这是协议的**第二个独立实现**（第一个是 `qianye/modules/lottery/commit.go`，
 * 第三个是 `qianye/docs/lottery-verify.py`）。三份实现互为对照：任何一份写错
 * 都会在这里表现为"验证不通过"，而不是悄悄地一起错。因此本文件**绝不允许**
 * 复用后端算好的中间值（`final_seed`、`ticket`、名次），一切从 `seed` 与
 * 条目原文重新算起。
 *
 * ## 编码约定（改一个字节就是换了一套协议）
 *
 * - `SEP = 0x1F`（单元分隔符）。选它是因为它不会出现在业务串里，
 *   `"a|b"` 与 `"a" + "|b"` 撞哈希这类经典错误在这里不可能发生。
 * - `dec(n)` = 十进制、无前导零、负号照写（业务上不会出现负数）。
 * - `bool(b)` = `"true"` / `"false"`（与 Go `strconv.FormatBool` 一致）。
 * - `rules_text` 哈希的是**落库的那份字节**，前端只读不重序列化 ——
 *   JS 与 Go 的对象序列化顺序不一致，重序列化必然算出另一个哈希。
 */

/**
 * 单元分隔符 `0x1F`。写成转义而不是字面控制字符：源码里的裸控制字符会被
 * 编辑器、格式化工具、复制粘贴悄悄吃掉，而那会让整套哈希静默变成另一套。
 */
const SEP = '\u001F'

const ALGO = 'lot-v1'

/** 恒定值，进 `commit_hash` 原像。见设计文档 §5：全部猜错一律全额退回。 */
const NO_WINNER_POLICY = 'refund_all'

function dec(value: number): string {
  return String(Math.trunc(value))
}

function bool(value: boolean): string {
  return value ? 'true' : 'false'
}

function toHex(buffer: ArrayBuffer): string {
  const bytes = new Uint8Array(buffer)
  let out = ''
  for (const byte of bytes) out += byte.toString(16).padStart(2, '0')
  return out
}

function fromHex(hex: string): Uint8Array {
  const clean = hex.trim().toLowerCase()
  if (clean.length % 2 !== 0 || !/^[0-9a-f]*$/.test(clean)) {
    throw new Error(`invalid hex: ${hex.slice(0, 16)}`)
  }
  const out = new Uint8Array(clean.length / 2)
  for (let i = 0; i < out.length; i += 1) {
    out[i] = Number.parseInt(clean.slice(i * 2, i * 2 + 2), 16)
  }
  return out
}

/**
 * WebCrypto 只在安全上下文（https / localhost）里存在。
 *
 * 拿不到时**必须明确报错**，绝不能悄悄回落到一个自己写的 sha256 ——
 * 那等于让用户以为验过了，而实际上验的是另一套东西。
 */
function subtle(): SubtleCrypto {
  const api = globalThis.crypto?.subtle
  if (api == null) throw new Error('webcrypto-unavailable')
  return api
}

async function sha256Hex(parts: string[]): Promise<string> {
  const data = new TextEncoder().encode(parts.join(SEP))
  return toHex(await subtle().digest('SHA-256', data))
}

async function hmacSha256Hex(keyHex: string, message: string): Promise<string> {
  const key = await subtle().importKey(
    'raw',
    fromHex(keyHex) as unknown as BufferSource,
    { name: 'HMAC', hash: 'SHA-256' },
    false,
    ['sign']
  )
  const data = new TextEncoder().encode(message)
  return toHex(await subtle().sign('HMAC', key, data))
}

// ─────────────────────────── 协议里的五个哈希 ───────────────────────────

export function qyLotRulesHash(rulesText: string): Promise<string> {
  return sha256Hex(['qylot-rules-v1', rulesText])
}

/**
 * 奖档 / 选项集合的规范化行。
 *
 * 抽奖按 `tier` 升序、竞猜按 `opt_no` 升序 —— 顺序是协议的一部分，
 * 排序键定死之后不给任何实现留自由度。
 */
export function qyLotSpecLines(proof: QyLotProof): string[] {
  if (proof.kind === 'draw') {
    return qyLotTiers(proof.spec).map((tier) =>
      [
        dec(tier.tier),
        tier.name,
        dec(tier.amount_quota),
        dec(tier.count),
      ].join(SEP)
    )
  }
  return qyLotOptions(proof.spec).map((option) =>
    [dec(option.opt_no), option.label, bool(option.is_catch_all)].join(SEP)
  )
}

export function qyLotSpecHash(lines: string[]): Promise<string> {
  return sha256Hex(['qylot-spec-v1', ...lines])
}

/**
 * 承诺哈希。
 *
 * 覆盖的不只是种子：参与条件、奖档集合、四个时刻、算法版本，以及每一个会影响
 * 结果的布尔与数值。漏掉任何一项，管理员都能在不碰种子的前提下把结果算成他想要
 * 的样子，而验证者只会看到"对不上"却举证不出是哪一边改的。
 */
export function qyLotCommitHash(
  proof: QyLotProof,
  rulesHash: string,
  specHash: string
): Promise<string> {
  return sha256Hex([
    'qylot-commit-v1',
    proof.act_no,
    proof.kind,
    proof.algo,
    rulesHash,
    specHash,
    dec(proof.stake_quota),
    dec(proof.open_at),
    dec(proof.close_at),
    dec(proof.draw_at),
    dec(proof.settle_deadline),
    bool(proof.allow_multi_win),
    dec(proof.fee_bps),
    NO_WINNER_POLICY,
    dec(proof.min_entries_to_hold),
    proof.seed,
  ])
}

export function qyLotChainNext(
  prev: string,
  actNo: string,
  entry: Pick<
    QyLotProofEntry,
    'amount' | 'entry_no' | 'opt_no' | 'seq' | 'user_ref'
  >
): Promise<string> {
  return sha256Hex([
    'qylot-chain-v1',
    prev,
    actNo,
    dec(entry.seq),
    entry.entry_no,
    entry.user_ref,
    dec(entry.opt_no),
    dec(entry.amount),
  ])
}

/**
 * 有效名单哈希。
 *
 * 行与行之间用 `\n` 连接（**不是 SEP**），整体再作为一个字段进 SEP 拼接。
 * 这是协议里唯一一处两级分隔，照抄不要"顺手统一"。
 */
export async function qyLotRosterHash(
  actNo: string,
  commitHash: string,
  roster: QyLotProofEntry[]
): Promise<string> {
  const lines = roster.map((entry) =>
    [entry.entry_no, entry.user_ref, dec(entry.opt_no), dec(entry.amount)].join(
      SEP
    )
  )
  return sha256Hex([
    'qylot-roster-v1',
    actNo,
    commitHash,
    dec(roster.length),
    lines.join('\n'),
  ])
}

/**
 * 最终随机源。
 *
 * **它绑定了名单**，这不是装饰：只依赖种子的话，知道种子的人（管理员、DBA）
 * 每报一次名就能立刻算出自己的名次并锁定，可以在开放期从容 grinding。
 * 混进 `roster_hash` 之后，任何后来者的加入都会重排全部票面 —— 除非他能保证
 * 自己是最后一个报名的人，否则他锁不住任何结果。
 */
export function qyLotFinalSeed(proof: QyLotProof): Promise<string> {
  return sha256Hex([
    'qylot-final-v1',
    proof.act_no,
    proof.seed,
    proof.roster_hash,
    dec(proof.roster_count),
    proof.algo,
  ])
}

export function qyLotTicket(
  finalSeed: string,
  actNo: string,
  entryNo: string
): Promise<string> {
  return hmacSha256Hex(finalSeed, ['qylot-ticket-v1', actNo, entryNo].join(SEP))
}

// ─────────────────────────── 抽取与分配 ───────────────────────────

/** 排序用：字节序升序。ASCII 范围内 JS 的字符串比较就是字节序。 */
function byteCompare(a: string, b: string): number {
  if (a < b) return -1
  if (a > b) return 1
  return 0
}

/**
 * 重算中奖名单（选排序法）。
 *
 * ## 为什么不是 Fisher–Yates
 *
 * 可复现性本身就是公正性的一部分。FY 要求第三方精确复现计数器编码、字节序、
 * 拒绝采样的边界、跳票时是否消耗随机数 —— 每一处细微差异都会算出**完全不同**
 * 的名单，而验证者无法判断是谁错了。排序法无状态、零模偏置（256 位输出下碰撞
 * 可忽略）、任何语言十行以内，且跳票不消耗随机性，所以没有额外要公布的中间量。
 *
 * 平局用 `entry_no` 定死，不给实现留自由度。
 */
export async function qyLotPickWinners(
  finalSeed: string,
  actNo: string,
  roster: QyLotProofEntry[],
  tiers: QyLotTier[],
  allowMultiWin: boolean
): Promise<QyLotProofWinner[]> {
  const ticketed: { entry: QyLotProofEntry; ticket: string }[] = []
  for (const entry of roster) {
    ticketed.push({
      entry,
      ticket: await qyLotTicket(finalSeed, actNo, entry.entry_no),
    })
  }

  ticketed.sort(
    (a, b) =>
      byteCompare(a.ticket, b.ticket) ||
      byteCompare(a.entry.entry_no, b.entry.entry_no)
  )

  let ranked = ticketed.map((item) => item.entry)
  if (!allowMultiWin) {
    // 同一个人只保留最靠前的那一张。被跳过的票**不消耗任何随机性**，
    // 因此不需要公布跳票列表 —— 验证者按同样的规则跳一遍即可。
    const seen = new Set<string>()
    ranked = ranked.filter((entry) => {
      if (seen.has(entry.user_ref)) return false
      seen.add(entry.user_ref)
      return true
    })
  }

  const winners: QyLotProofWinner[] = []
  let cursor = 0
  for (const tier of [...tiers].sort((a, b) => a.tier - b.tier)) {
    for (let i = 0; i < tier.count; i += 1) {
      // 票不够则该档空缺，如实公布，**不补抽**。
      if (cursor >= ranked.length) break
      winners.push({
        pos: cursor,
        tier: tier.tier,
        entry_no: ranked[cursor].entry_no,
        user_ref: ranked[cursor].user_ref,
        amount: tier.amount_quota,
      })
      cursor += 1
    }
  }
  return winners
}

/** 复算出来的一笔份额。刻意不带 `kind` / `status`：那是状态机的事实，不是计算结果。 */
export type QyLotShare = {
  entry_no: string
  amount: number
}

export type QyLotSplitResult = {
  fee: number
  payouts: QyLotShare[]
  /** 是否走了"全额退回本金"分支（全错 / 无输家 / 无对手盘）。 */
  refundedAll: boolean
}

/**
 * 重算竞猜奖池分配。
 *
 * 用 `BigInt` 而不是浮点：守恒式 `Σpay + fee == pool` 必须**精确**成立，
 * 差一个单位就意味着有人的钱不见了或平台倒贴了，而浮点的舍入误差恰好会
 * 在几万条投注上累积到那个量级。
 *
 * 逐笔截断 + 残差归最后一位赢家，是唯一同时满足"守恒"与"可复现"的做法：
 * 各自四舍五入会让 `Σpay` 或大于或小于 `net`，两者都不能接受。残差上界小于
 * 赢家人数，归 `entry_no` 字节序最大的那一位 —— 顺序确定，第三方能复算出是谁。
 */
export function qyLotSplitPool(
  roster: QyLotProofEntry[],
  winOptNo: number,
  feeBps: number
): QyLotSplitResult {
  const pool = roster.reduce((sum, entry) => sum + BigInt(entry.amount), 0n)
  const winners = roster.filter((entry) => entry.opt_no === winOptNo)
  const win = winners.reduce((sum, entry) => sum + BigInt(entry.amount), 0n)

  // 全部猜错 / 无输家 / 无对手盘：没有任何再分配发生，收费就没有对价。
  // 只要平台在"没人猜中"时有收益，它就有动机去设置不可能达成的选项 ——
  // 那是激励层面的漏洞，任何审计都补不上。
  if (win === 0n || win === pool) {
    return {
      fee: 0,
      payouts: roster.map((entry) => ({
        entry_no: entry.entry_no,
        amount: entry.amount,
      })),
      refundedAll: true,
    }
  }

  const fee = (pool * BigInt(Math.trunc(feeBps))) / 10000n
  const net = pool - fee

  const payouts: QyLotShare[] = []
  let acc = 0n
  winners.forEach((entry, index) => {
    const amount =
      index === winners.length - 1
        ? net - acc
        : (net * BigInt(entry.amount)) / win
    acc += amount
    payouts.push({ entry_no: entry.entry_no, amount: Number(amount) })
  })

  return { fee: Number(fee), payouts, refundedAll: false }
}

// ─────────────────────────── 验证流程 ───────────────────────────

export type QyLotVerifyStatus = 'fail' | 'ok' | 'skipped'

/**
 * 一步验证结果。
 *
 * `labelKey` 是 i18n key，`detail` 是**不需要翻译的技术细节**（哈希、数字）——
 * 失败时用户要能把它原样贴给别人，所以不做本地化。
 */
export type QyLotVerifyStep = {
  key: string
  labelKey: string
  status: QyLotVerifyStatus
  detail?: string
}

/** 未揭示时哪些步骤做不了 —— 如实标 `skipped`，绝不显示成"通过"。 */
const STEP_KEYS = [
  'rules',
  'spec',
  'commit',
  'chain',
  'roster',
  'result',
] as const

function step(
  key: (typeof STEP_KEYS)[number],
  status: QyLotVerifyStatus,
  detail?: string
): QyLotVerifyStep {
  return { key, labelKey: `qy_lot_vf_step_${key}`, status, detail }
}

/**
 * 在用户自己的浏览器里跑完整的 `lot-v1` 验证。
 *
 * 返回的是**逐步结果**而不是一个布尔：用户需要看到"哪一步过了、哪一步没过"，
 * 一个红叉说明不了任何问题，也没法举证。
 *
 * 任何一步抛异常都会被捕获成该步的 `fail` 并附上原因，后续步骤照跑 ——
 * 半路 return 会让用户以为"后面的没问题"。
 */
export async function verifyQyLotProof(
  proof: QyLotProof
): Promise<QyLotVerifyStep[]> {
  const steps: QyLotVerifyStep[] = []

  if (proof.algo !== ALGO) {
    return STEP_KEYS.map((key) => step(key, 'fail', `algo=${proof.algo}`))
  }

  // ① 承诺没有被替换。种子未揭示时算不了，如实跳过。
  let rulesHash = ''
  let specHash = ''
  try {
    rulesHash = await qyLotRulesHash(proof.rules_text)
    steps.push(
      rulesHash === proof.rules_hash
        ? step('rules', 'ok')
        : step('rules', 'fail', `${rulesHash} != ${proof.rules_hash}`)
    )
  } catch (error) {
    steps.push(step('rules', 'fail', String(error)))
  }

  try {
    specHash = await qyLotSpecHash(qyLotSpecLines(proof))
    steps.push(
      specHash === proof.spec_hash
        ? step('spec', 'ok')
        : step('spec', 'fail', `${specHash} != ${proof.spec_hash}`)
    )
  } catch (error) {
    steps.push(step('spec', 'fail', String(error)))
  }

  const revealed = proof.seed !== ''
  if (!revealed) {
    steps.push(step('commit', 'skipped'))
  } else {
    try {
      const commit = await qyLotCommitHash(proof, rulesHash, specHash)
      steps.push(
        commit === proof.commit_hash
          ? step('commit', 'ok')
          : step('commit', 'fail', `${commit} != ${proof.commit_hash}`)
      )
    } catch (error) {
      steps.push(step('commit', 'fail', String(error)))
    }
  }

  // ② 逐条哈希链。分页取回的那一份验不了 —— 少一条链就断，而"断了"与
  //    "被篡改了"在结果上无法区分，那正是最不该含糊的地方。
  const entries = [...proof.entries].sort((a, b) => a.seq - b.seq)
  if (entries.length !== proof.total) {
    steps.push(
      step('chain', 'skipped', `${entries.length}/${proof.total}`),
      step('roster', 'skipped'),
      step('result', 'skipped')
    )
    return steps
  }

  try {
    let chain = proof.commit_hash
    let expected = 1
    for (const entry of entries) {
      if (entry.seq !== expected) {
        throw new Error(`seq gap at ${entry.seq}, expected ${expected}`)
      }
      if (entry.prev_hash !== chain) {
        throw new Error(`prev_hash mismatch at seq ${entry.seq}`)
      }
      chain = await qyLotChainNext(chain, proof.act_no, entry)
      if (chain !== entry.chain_hash) {
        throw new Error(`chain_hash mismatch at seq ${entry.seq}`)
      }
      expected += 1
    }
    if (chain !== proof.chain_head) {
      throw new Error(`chain_head mismatch: ${chain} != ${proof.chain_head}`)
    }
    steps.push(step('chain', 'ok', `${entries.length}`))
  } catch (error) {
    steps.push(step('chain', 'fail', String(error)))
  }

  // ③ 有效名单在揭示种子之前就已冻结。
  const roster = entries
    .filter((entry) => entry.status === 'success')
    .sort((a, b) => byteCompare(a.entry_no, b.entry_no))

  let rosterOk = false
  try {
    const hash = await qyLotRosterHash(proof.act_no, proof.commit_hash, roster)
    rosterOk =
      hash === proof.roster_hash && roster.length === proof.roster_count
    steps.push(
      rosterOk
        ? step('roster', 'ok', `${roster.length}`)
        : step('roster', 'fail', `${hash} != ${proof.roster_hash}`)
    )
  } catch (error) {
    steps.push(step('roster', 'fail', String(error)))
  }

  // ④⑤ 重算结果。名单没对上就没有复算的意义 —— 那只会算出一个"当然不一样"。
  if (!rosterOk) {
    steps.push(step('result', 'skipped'))
    return steps
  }

  if (proof.kind === 'draw') {
    if (!revealed) {
      steps.push(step('result', 'skipped'))
      return steps
    }
    // 取消 / 流局的抽奖没有开出结果，`winners` 恒为空。拿它去比对复算名单只会
    // 得到一个必然的红叉，而真实情况是平台已经全额退款 —— 官方工具在诚实数据上
    // 指控平台作弊，比不提供验证更糟。这里如实标"没有开出结果"，
    // 复算出的**本应中奖名单**留给离线脚本打印（那是判断"是不是看了结果才取消"
    // 的唯一材料，但判断本身要人来做）。
    if (isQyLotVoided(proof.outcome)) {
      steps.push(step('result', 'skipped', `outcome=${proof.outcome}`))
      return steps
    }
    try {
      const finalSeed = await qyLotFinalSeed(proof)
      const winners = await qyLotPickWinners(
        finalSeed,
        proof.act_no,
        roster,
        qyLotTiers(proof.spec),
        proof.allow_multi_win
      )
      const actual = [...proof.winners].sort((a, b) => a.pos - b.pos)
      const same =
        winners.length === actual.length &&
        winners.every(
          (winner, index) =>
            winner.pos === actual[index].pos &&
            winner.tier === actual[index].tier &&
            winner.entry_no === actual[index].entry_no &&
            winner.amount === actual[index].amount
        )
      steps.push(
        same
          ? step('result', 'ok', `${winners.length}`)
          : step('result', 'fail', `${winners.length} vs ${actual.length}`)
      )
    } catch (error) {
      steps.push(step('result', 'fail', String(error)))
    }
    return steps
  }

  // 竞猜：结果还没录时没有可复算的分配。
  if (proof.win_opt_no === 0 && proof.payouts.length === 0) {
    steps.push(step('result', 'skipped'))
    return steps
  }

  try {
    const split = qyLotSplitPool(roster, proof.win_opt_no, proof.fee_bps)
    const expected = new Map(
      split.payouts.map((payout) => [payout.entry_no, payout.amount])
    )
    // 只比对本次分配产生的那一类出款。同一场活动里还可能有"封盘时未落定的
    // 条目"的退款，它们不是分配结果 —— 混进来会让一场诚实的结算被判成对不上。
    const settled = proof.payouts.filter((payout) =>
      split.refundedAll ? payout.kind === 'refund' : payout.kind === 'win'
    )
    const actual = new Map(
      settled.map((payout) => [payout.entry_no, payout.amount])
    )
    const paid = settled.reduce((sum, payout) => sum + payout.amount, 0)
    const pool = roster.reduce((sum, entry) => sum + entry.amount, 0)

    const same =
      split.fee === proof.fee_quota &&
      expected.size === actual.size &&
      [...expected].every(([entryNo, amount]) => actual.get(entryNo) === amount)
    // 守恒式必须精确成立。它是独立于逐笔比对的第二道：即便两边的分配算法
    // 一起写错，只要总额对不上就一定会在这里红。
    const conserved = paid + proof.fee_quota === pool

    steps.push(
      same && conserved
        ? step('result', 'ok', `fee=${split.fee}`)
        : step(
            'result',
            'fail',
            `fee ${split.fee} vs ${proof.fee_quota}; sum ${paid + proof.fee_quota} vs ${pool}`
          )
    )
  } catch (error) {
    steps.push(step('result', 'fail', String(error)))
  }

  return steps
}
