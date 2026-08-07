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
  QY_LOT_PPM_DEN,
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

/**
 * `lot-v2`：概率制 + 文本奖 + 双色球。
 *
 * ## 改了什么、没改什么
 *
 * 改的只有**四处原像**：`spec` / `commit` / `chain` / `roster` 的域前缀各自
 * 从 `-v1` 变成 `-v2`，并各自追加了新字段。
 *
 * `rules` / `final` / `ticket` / `user_ref` 四处**一个字节都没动**。这不是省事：
 * 「票面的推导过程完全没变」本身就是"概率制没有引入任何新随机源"的最好证据 ——
 * 摇号量 r 是从同一张票面里截出来的，而不是另外摇了一次。
 *
 * ## v1 为什么必须原样冻结
 *
 * 已经开完的活动全都是 v1。顺手"统一"一下原像，就等于让**所有历史活动的
 * 公正性查询集体变成 FAIL** —— 而那时没有任何人能分辨是协议改了还是数据被
 * 篡改了。所以两套分派按 `proof.algo` 走，v1 分支逐字保留。
 */
const ALGO_V2 = 'lot-v2'

/** 未知版本一律拒绝：给一个可能是假的绿勾，比不提供验证更糟。 */
function isKnownAlgo(algo: string): boolean {
  return algo === ALGO || algo === ALGO_V2
}

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
    if (proof.algo === ALGO_V2) {
      // v2 的奖档行有十个字段，**恒等式位也必须写出来**：quota 档的
      // `text_desc` 是空串、rank 模式的 `win_ppm` 是 0、非双色球的三列是 0。
      // 少一个占位就等于允许管理员在不动哈希的前提下把一档额度奖改成文本奖。
      return qyLotTiers(proof.spec).map((tier) =>
        [
          dec(tier.tier),
          tier.name,
          tier.prize_type ?? '',
          dec(tier.amount_quota),
          dec(tier.count),
          dec(tier.win_ppm ?? 0),
          tier.text_desc ?? '',
          dec(tier.red_match ?? 0),
          dec(tier.blue_match ?? 0),
          dec(tier.pool_share_bps ?? 0),
        ].join(SEP)
      )
    }
    return qyLotTiers(proof.spec).map((tier) =>
      [dec(tier.tier), tier.name, dec(tier.amount_quota), dec(tier.count)].join(
        SEP
      )
    )
  }
  // 竞猜的选项行 v1/v2 完全相同 —— 概率与文本奖在竞猜里无处安放（它是奖池制，
  // 赔付是连续的池子份额，不存在离散奖档），所以那一侧没有任何新字段。
  // 变的只有域前缀，因为版本是整份文档级的。
  return qyLotOptions(proof.spec).map((option) =>
    [dec(option.opt_no), option.label, bool(option.is_catch_all)].join(SEP)
  )
}

export function qyLotSpecHash(lines: string[], algo = ALGO): Promise<string> {
  return sha256Hex([
    algo === ALGO_V2 ? 'qylot-spec-v2' : 'qylot-spec-v1',
    ...lines,
  ])
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
  const head = [
    proof.algo === ALGO_V2 ? 'qylot-commit-v2' : 'qylot-commit-v1',
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
  ]
  if (proof.algo !== ALGO_V2) return sha256Hex([...head, proof.seed])
  // 玩法与池子的每一个入参都在种子之前进原像。**非双色球活动这些位恒为
  // 空串 / 0，但必须出现**：少一个占位，管理员就能在不改承诺的前提下把一场
  // 普通抽奖悄悄办成双色球，或者把初始池子从 0 改成一个数。
  return sha256Hex([
    ...head,
    proof.draw_mode ?? '',
    proof.series_no ?? '',
    dec(proof.issue_no ?? 0),
    dec(proof.pool_seed_quota ?? 0),
    dec(proof.pool_carry_quota ?? 0),
    dec(proof.pool_open_quota ?? 0),
    dec(proof.pool_share_bps ?? 0),
    dec(proof.ball_red_pool ?? 0),
    dec(proof.ball_red_pick ?? 0),
    dec(proof.ball_blue_pool ?? 0),
    dec(proof.ball_blue_pick ?? 0),
    proof.seed,
  ])
}

export function qyLotChainNext(
  prev: string,
  actNo: string,
  entry: Pick<
    QyLotProofEntry,
    'amount' | 'entry_no' | 'opt_no' | 'pick' | 'seq' | 'user_ref'
  >,
  algo = ALGO
): Promise<string> {
  const head = [
    algo === ALGO_V2 ? 'qylot-chain-v2' : 'qylot-chain-v1',
    prev,
    actNo,
    dec(entry.seq),
    entry.entry_no,
    entry.user_ref,
    dec(entry.opt_no),
    dec(entry.amount),
  ]
  // 选号进链是双色球唯一必须改协议的地方：不进链的话，平台可以在开奖之后把
  // 某个人的号改成中奖号，而链尾、seq 连续、名单重算三道校验会**照常全部通过**。
  if (algo !== ALGO_V2) return sha256Hex(head)
  return sha256Hex([...head, entry.pick ?? ''])
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
  roster: QyLotProofEntry[],
  algo = ALGO
): Promise<string> {
  const v2 = algo === ALGO_V2
  const lines = roster.map((entry) => {
    const cells = [
      entry.entry_no,
      entry.user_ref,
      dec(entry.opt_no),
      dec(entry.amount),
    ]
    if (v2) cells.push(entry.pick ?? '')
    return cells.join(SEP)
  })
  return sha256Hex([
    v2 ? 'qylot-roster-v2' : 'qylot-roster-v1',
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

// ─────────────────────────── 摇号量与概率区间（lot-v2） ───────────────────────────

/**
 * 票面 → 摇号量 `r ∈ [0, 999999]`。
 *
 * `r = floor(u64 × 1_000_000 / 2^64)`，其中 `u64` 是票面前 16 个十六进制字符。
 *
 * ## 为什么是"截前 64 位再缩放"而不是拿整个 256 位比阈值
 *
 * 因为**落选的人必须当场看懂自己为什么没中**，而这正是概率制相对名次制需要
 * 额外证明的那一点。「你的摇号结果是 384217，二等奖区间是 [1000, 11000)」是
 * 一句人话；把一个 64 位十六进制串念给用户听等于没解释。可读性在这里是可
 * 验证性的一部分，不是装饰。
 *
 * 附带收益：前端不需要在 2^256 上做大整数比较，那条"Number/BigInt 静默算错"
 * 的高危路径根本不存在。
 *
 * ## 偏差有多大（不掩饰）
 *
 * 2^64 个均匀值分进 10^6 个桶，桶大小最多相差 1，相对偏差 < 2^-44。
 *
 * ## 为什么不用取模
 *
 * `u % 1e6` 有模偏置，而且一定会被人拿出来吵。乘法 + 右移是精确的截断映射，
 * 零争议、零除法，三种语言（Go 的 `bits.Mul64`、Python 的原生大整数、
 * 这里的 `BigInt`）各自十行以内，逐位一致。
 */
export function qyLotRollPpm(ticket: string): number {
  const head = ticket.trim().toLowerCase().slice(0, 16)
  // 票面恒由 `qyLotTicket` 产出（64 个十六进制字符）。解不开只可能是调用方传错
  // 了东西，此时返回 `PpmDen` —— 它落在**全部**中奖区间之外，即"这张票不中"。
  // 回落成 0 会让一张畸形的票直接中一等奖，那是方向完全错误的失败。
  // 与后端 `RollPpm` 逐字同一条口径。
  if (head.length !== 16 || !/^[0-9a-f]{16}$/.test(head)) {
    return QY_LOT_PPM_DEN
  }
  // 结果 ≤ 999999，转 Number 是精确的。
  return Number((BigInt(`0x${head}`) * BigInt(QY_LOT_PPM_DEN)) >> 64n)
}

/** 一档的摇号区间，左闭右开。 */
export type QyLotBand = {
  tier: number
  loPpm: number
  hiPpm: number
}

/**
 * 各档按 tier 升序累加 `win_ppm`，得到互不相交的左闭右开区间。
 *
 * **绝不对每一档独立摇一次号**：那会产生档与档之间的相关性，还要公布 N 个
 * 随机量。一次摇号 + 区间归属，是唯一同时满足"概率严格等于公示值"与
 * "第三方十行以内能复算"的做法。
 *
 * `Σwin_ppm > 1_000_000` 直接抛错，**不猜**：那说明公示的概率表本身是错的，
 * 而验证器在这种时候给出任何结论都是在替平台圆场。
 */
export function qyLotBands(tiers: QyLotTier[]): QyLotBand[] {
  const bands: QyLotBand[] = []
  let cursor = 0
  for (const tier of [...tiers].sort((a, b) => a.tier - b.tier)) {
    const ppm = Math.trunc(tier.win_ppm ?? 0)
    if (ppm < 0) throw new Error(`negative win_ppm at tier ${tier.tier}`)
    bands.push({ tier: tier.tier, loPpm: cursor, hiPpm: cursor + ppm })
    cursor += ppm
  }
  if (cursor > QY_LOT_PPM_DEN) {
    throw new Error(`win_ppm sum ${cursor} > ${QY_LOT_PPM_DEN}`)
  }
  return bands
}

/** 摇号量落在哪一档；`null` = 落在全部区间之外，也就是**没中**。 */
export function qyLotBandOf(
  bands: QyLotBand[],
  roll: number
): QyLotBand | null {
  return bands.find((band) => roll >= band.loPpm && roll < band.hiPpm) ?? null
}

/**
 * 双色球摇号：对号池里的每一个球号算一次 HMAC，按 `(哈希, 号码)` 升序取前 k。
 *
 * ## 为什么不是 `1 + h(i) % pool` 撞重重取
 *
 * 与 {@link qyLotPickWinners} 不用 Fisher–Yates 是同一条理由，而且更尖锐：
 * 拒绝采样要求第三方精确复现计数器怎么推进、蓝球从第几个下标起算、撞到重复
 * 时消不消耗随机数 —— 三处实现自由度，每一处差一点都会算出**完全不同的
 * 七个号码**，而验证者无法判断是谁错了。
 *
 * 选排序法零实现自由度、零模偏置，平局用号码本身定死。
 */
export function qyLotBallDraw(
  finalSeed: string,
  actNo: string,
  color: 'blue' | 'red',
  poolN: number,
  pickK: number
): Promise<number[]> {
  return (async () => {
    const scored: { ball: number; hash: string }[] = []
    for (let ball = 1; ball <= poolN; ball += 1) {
      scored.push({
        ball,
        hash: await hmacSha256Hex(
          finalSeed,
          ['qylot-ball-v2', actNo, color, dec(ball)].join(SEP)
        ),
      })
    }
    scored.sort((a, b) => byteCompare(a.hash, b.hash) || a.ball - b.ball)
    return scored
      .slice(0, pickK)
      .map((item) => item.ball)
      .sort((a, b) => a - b)
  })()
}

/** 规范化选号串 `03,05,12|08` → `{reds, blues}`。格式不对时抛错，不猜。 */
export function qyLotParsePick(pick: string): {
  blues: number[]
  reds: number[]
} {
  const [redPart = '', bluePart = ''] = pick.split('|')
  const parse = (text: string) =>
    text === ''
      ? []
      : text.split(',').map((cell) => {
          if (!/^\d{2}$/.test(cell)) throw new Error(`invalid pick: ${pick}`)
          return Number.parseInt(cell, 10)
        })
  return { reds: parse(redPart), blues: parse(bluePart) }
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

/**
 * 重算概率制中奖名单（`draw_mode='prob'`）。
 *
 * ## 与名次制的差别只有一处：随机量的**作用域**
 *
 * 名次制是「全场按票面排序，前 N 名中奖」——一张票的结果取决于其他所有票。
 * 概率制是「每张票各摇一次号，落进哪个区间就中哪一档」——一张票的结果
 * **完全不依赖其他票**。两者共用同一个 `ticket`，因此没有引入任何新随机源。
 *
 * 这带来一个安全性上的副产品：概率制下"多买多摇"没有任何可利用的结构，
 * 老老实实按公示概率付费，反而比名次制**更**抗操纵。
 *
 * ## 超募时摊薄的是金额，不是概率
 *
 * `count` 在这里的语义是**本档预算 = count × amount**，不是名额。命中人数 W
 * 超过 count 时，预算由全部 W 人均分（逐笔向零截断、残差归 `entry_no` 字节序
 * 最大者，与竞猜奖池 {@link qyLotSplitPool} 逐字节同一套口径）。
 *
 * 另一种做法是"按票面顺序取前 count 张、其余落空"，本方案**明确否掉**：那样
 * 一张票的实际中奖概率会变成 `win_ppm × min(1, count/W)`，也就是说卡片上印的
 * 「中奖概率 1%」在超募时是**假的**。整套设计的立身之本是公示的数字为真，
 * 所以让概率恒等于公示值，让金额去浮动。
 *
 * ## 文本奖不参与摊薄
 *
 * 文本奖的 `amount` 恒为 0，"均分 0" 没有意义 —— `count` 对它而言是**实物
 * 份数**而不是预算。这里如实把全部命中者都列为中奖者（W > count 时就是超发），
 * 不自作主张地裁掉谁：验证器的职责是复算，不是替平台决定谁该落空。
 */
export async function qyLotPickWinnersProb(
  finalSeed: string,
  actNo: string,
  roster: QyLotProofEntry[],
  tiers: QyLotTier[]
): Promise<QyLotProofWinner[]> {
  const bands = qyLotBands(tiers)
  const byTier = new Map<number, QyLotProofEntry[]>()
  for (const entry of roster) {
    const ticket = await qyLotTicket(finalSeed, actNo, entry.entry_no)
    const band = qyLotBandOf(bands, qyLotRollPpm(ticket))
    // 落在全部区间之外 = 没中。这是一等公民结果，不是异常分支 ——
    // 项目方要的正是「不是说必须要有中奖人」。
    if (band == null) continue
    const bucket = byTier.get(band.tier)
    if (bucket == null) byTier.set(band.tier, [entry])
    else bucket.push(entry)
  }

  const winners: QyLotProofWinner[] = []
  for (const tier of [...tiers].sort((a, b) => a.tier - b.tier)) {
    const hit = byTier.get(tier.tier) ?? []
    if (hit.length === 0) continue
    const diluted =
      tier.prize_type !== 'text' &&
      tier.amount_quota > 0 &&
      hit.length > tier.count
    const budget = BigInt(tier.count) * BigInt(tier.amount_quota)
    let acc = 0n
    hit.forEach((entry, index) => {
      // 均分：前 W-1 笔向零截断，最后一笔吃掉残差。`hit` 是按 `entry_no`
      // 字节序升序的（roster 就是这个序），所以"最后一笔"就是 entry_no 最大的
      // 那一位 —— 与竞猜奖池的残差归属规则完全一致，零新增舍入约定。
      let amount = BigInt(tier.amount_quota)
      if (diluted) {
        // 最后一笔吃掉残差，其余向零截断。
        amount =
          index === hit.length - 1 ? budget - acc : budget / BigInt(hit.length)
      }
      acc += amount
      winners.push({
        pos: winners.length,
        tier: tier.tier,
        entry_no: entry.entry_no,
        user_ref: entry.user_ref,
        amount: Number(amount),
      })
    })
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
 * 复算双色球的开奖号与中奖档位，并与平台公布的那一份比对。
 *
 * ## 验得了什么
 *
 * 1. **开奖号**：`qyLotBallDraw(final_seed, act_no, 号池)` 是纯函数，号池四元组
 *    进了 `commit_hash`，`final_seed` 由已揭示的种子推出 —— 所以平台公布的
 *    `ball_result` 只要被改过一个数字，这里就会红。
 * 2. **中奖名单的档位**：`MatchTier` 是 `(开奖号, 我的选号, 各档门槛)` 的纯函数，
 *    而选号 `pick` 进了哈希链、门槛进了 `spec_hash`。于是"谁中了、中的第几档"
 *    整份可复算，多一个人、少一个人、改一个人的档位都会红。
 *
 * ## 验不了什么（必须说出来，不能用绿勾盖过去）
 *
 * **金额**。浮动奖档是 `本期池子 × 占比 ÷ 同档人数`，而池子随投注变化、
 * 同档人数只有开奖后才知道，两者都不在承诺原像里。所以这一步只比"谁中了哪一档"，
 * detail 里明写 `amounts-not-checked`。
 */
async function verifyBallResult(
  proof: QyLotProof,
  roster: QyLotProofEntry[]
): Promise<QyLotVerifyStep> {
  const redPool = proof.ball_red_pool ?? 0
  const bluePool = proof.ball_blue_pool ?? 0
  if (redPool <= 0) {
    // 号池是发布期硬校验过的，这里为 0 只可能是证据链本身残缺。
    return step('result', 'fail', 'ball_red_pool=0')
  }

  const finalSeed = await qyLotFinalSeed(proof)
  const drawnReds = await qyLotBallDraw(
    finalSeed,
    proof.act_no,
    'red',
    redPool,
    proof.ball_red_pick ?? 0
  )
  const drawnBlues = await qyLotBallDraw(
    finalSeed,
    proof.act_no,
    'blue',
    bluePool,
    proof.ball_blue_pick ?? 0
  )

  // 与后端 BallResultText / FormatPick 同一个规范化格式：两位补零、逗号分隔、
  // 红蓝之间一个竖线。格式差一位就会被判成不一致，那正是我们要的严格度。
  const recomputed = qyLotFormatPick(drawnReds, drawnBlues)
  if (proof.ball_result !== recomputed) {
    return step(
      'result',
      'fail',
      `ball_result ${proof.ball_result ?? ''} != ${recomputed}`
    )
  }

  // 档位复算：与后端 MatchTier 逐条对应 —— tier 升序、命中即停、一票只中一档。
  const tiers = qyLotTiers(proof.spec)
    .map((tier) => ({
      tier: tier.tier,
      red: tier.red_match ?? 0,
      blue: tier.blue_match ?? 0,
    }))
    .sort((a, b) => a.tier - b.tier)

  const expected: { entry_no: string; tier: number }[] = []
  for (const entry of roster) {
    let reds: number[] = []
    let blues: number[] = []
    try {
      const parsed = qyLotParsePick(entry.pick ?? '')
      reds = parsed.reds
      blues = parsed.blues
    } catch {
      // 后端 splitPickLoose 解不开时按"未中奖"处理，这里必须同口径：
      // 抛出去会让一张脏票把整场活动判成作弊。
      continue
    }
    const matchRed = reds.filter((ball) => drawnReds.includes(ball)).length
    const matchBlue = blues.filter((ball) => drawnBlues.includes(ball)).length
    const hit = tiers.find(
      (tier) => matchRed >= tier.red && matchBlue >= tier.blue
    )
    if (hit != null) expected.push({ entry_no: entry.entry_no, tier: hit.tier })
  }

  const key = (item: { entry_no: string; tier: number }) =>
    `${item.entry_no}#${item.tier}`
  const actual = proof.winners.map((winner) => ({
    entry_no: winner.entry_no,
    tier: winner.tier,
  }))
  const mine = [...expected].map(key).sort()
  const theirs = [...actual].map(key).sort()
  const same =
    mine.length === theirs.length && mine.every((row, i) => row === theirs[i])

  return same
    ? step(
        'result',
        'ok',
        `${recomputed} · ${expected.length} · amounts-not-checked`
      )
    : step('result', 'fail', `winners ${theirs.length} != ${mine.length}`)
}

/** 规范化开奖号 / 选号串：`[3,9,12] + [5]` → `03,09,12|05`。 */
function qyLotFormatPick(reds: number[], blues: number[]): string {
  const pad = (list: number[]) =>
    [...list]
      .sort((a, b) => a - b)
      .map((ball) => String(ball).padStart(2, '0'))
      .join(',')
  return `${pad(reds)}|${pad(blues)}`
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

  if (!isKnownAlgo(proof.algo)) {
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
    specHash = await qyLotSpecHash(qyLotSpecLines(proof), proof.algo)
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
      chain = await qyLotChainNext(chain, proof.act_no, entry, proof.algo)
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
    const hash = await qyLotRosterHash(
      proof.act_no,
      proof.commit_hash,
      roster,
      proof.algo
    )
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
    // 双色球：复算**开奖号**与**中奖名单的档位**，但不复算金额。
    //
    // 曾经这里是一整条 `skipped`，那是这份面板上最贵的一个洞：平台改一次
    // `ball_result`（payout 表不动）能让六步全绿而没有一个红叉，用户只能靠
    // 在两屏之间肉眼比对数字才可能发现。开奖号是 `(final_seed, act_no, 号池)`
    // 的纯函数，档位是 `(开奖号, 我的选号, 各档门槛)` 的纯函数 —— 两者都在
    // 证据链里，没有任何理由不自动比。
    //
    // 金额仍然验不了：浮动奖取决于本期池子与同档中签人数，而池子会随投注变化，
    // 不在承诺原像里。这一条如实写进 detail，不用一个绿勾把它盖过去。
    if (proof.draw_mode === 'ball') {
      try {
        steps.push(await verifyBallResult(proof, roster))
      } catch (error) {
        steps.push(step('result', 'fail', String(error)))
      }
      return steps
    }
    try {
      const finalSeed = await qyLotFinalSeed(proof)
      const tiers = qyLotTiers(proof.spec)
      const winners =
        proof.draw_mode === 'prob'
          ? await qyLotPickWinnersProb(finalSeed, proof.act_no, roster, tiers)
          : await qyLotPickWinners(
              finalSeed,
              proof.act_no,
              roster,
              tiers,
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
