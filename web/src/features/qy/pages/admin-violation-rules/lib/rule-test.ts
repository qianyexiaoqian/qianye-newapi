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
  QyViolationMatchType,
  QyViolationPhase,
  QyViolationTestInput,
} from '../types'

/**
 * 作用域输入：它们只决定「这条规则在不在作用域内」，一个字节都不参与
 * 「内容匹不匹配」。界面上必须与内容输入分开摆，并且永远可选 ——
 * 把它们和样本混在一排，正是「试跑还要我填模型、分组」这个抱怨的来源。
 */
export const QY_VIOLATION_TEST_SCOPE_INPUTS: QyViolationTestInput[] = [
  'model',
  'group',
]

/**
 * 一条规则**真正会读到**的试跑输入，顺序即渲染顺序。
 *
 * 这是后端 `ruleTestInputs` 的镜像。两份判据本来就该只有一份，但表单要在
 * 第一次试跑**之前**就知道该渲染哪几个格子，而那时还没有任何响应可读。
 * 因此：这里负责首屏，跑完之后面板改用响应里的 `inputs`（后端说了算），
 * 并有一条守卫测试钉住「后端新增的输入标识，前端必须认识」。
 */
export function qyRuleTestInputs(
  phase: QyViolationPhase,
  matchType: QyViolationMatchType
): QyViolationTestInput[] {
  let out: QyViolationTestInput[]
  switch (matchType) {
    case 'request_rate':
      // 频率判据不看任何文本：模式串是阈值，输入是窗口内的请求条数。
      out = ['rate_count']
      break
    case 'error_code':
      out = ['error_code']
      break
    case 'status_code':
      out = ['status_code']
      break
    // `ai_review` 读的是外部模型对上下文给出的**结论**，不是上下文本身。问一段
    // 文本等于把面板变成「我猜模型会怎么判」——而那正是这条规则唯一无法在本地
    // 复现的一步。管理员真正要验证的是「模型判了 sexual@0.72，我这条
    // min_confidence=0.8 的规则会不会命中」，那个问题本地完全答得出来。
    //
    // 用字符串比较而不是把 'ai_review' 加进 QyViolationMatchType：那个联合类型
    // 与它的下拉框标签属于 AI 审核那一摊，不该由试跑面板顺手改掉。
    case 'ai_review' as QyViolationMatchType:
      out = ['ai_verdict', 'ai_category', 'ai_confidence']
      break
    default:
      // keyword / regex / upstream_text 都是文本判据，差别只在扫哪一段文本：
      // prompt 阶段扫请求上下文，上游阶段扫「上游正文 + 软违规原因」的拼接。
      out =
        phase === 'prompt'
          ? ['request_text']
          : ['upstream_text', 'reject_reason']
  }
  // 状态码作用域与全部匹配方式正交：哪怕判据是错误码，一条配了 status_scope=400
  // 的规则在没有状态码输入的试跑里恒为「不在作用域」。
  //
  // 只有这两个阶段手上真的有上游状态码。写成 `phase !== 'prompt'` 会把
  // post_async（转发后异步审核）也算进来，而那一档跑在异步 worker 上、根本没有
  // 上游响应 —— 多摆一格填了也不生效的输入，与这次改动要修的毛病同形。
  if (
    (phase === 'upstream_err' || phase === 'reject_reason') &&
    matchType !== 'status_code'
  ) {
    out = [...out, 'status_code']
  }
  return [...out, ...QY_VIOLATION_TEST_SCOPE_INPUTS]
}

/** 内容输入 = 全部输入减去作用域输入。「能不能试跑」只看这一批填没填。 */
export function qyRuleTestContentInputs(
  inputs: QyViolationTestInput[]
): QyViolationTestInput[] {
  return inputs.filter((id) => !QY_VIOLATION_TEST_SCOPE_INPUTS.includes(id))
}

/**
 * 试跑面板认识的**全部内容维度**，顺序即渲染顺序。
 *
 * 这是一份完整清单，不是某条规则的清单：任何一条规则都只读其中一小部分。
 * 剩下的那些不会消失 —— 它们进「这条规则读不到」的只读说明里。
 */
export const QY_VIOLATION_TEST_DIMENSIONS: QyViolationTestInput[] = [
  'request_text',
  'upstream_text',
  'reject_reason',
  'status_code',
  'error_code',
  'rate_count',
  'ai_verdict',
  'ai_category',
  'ai_confidence',
]

/** 一个维度**缺席的理由**。每一个取值对应一条 `qy_vio_test_absent_*` 文案。 */
export type QyViolationTestAbsentReason =
  /** 匹配方式是 AI 审核以外的判据，没有模型结论可读。 */
  | 'match_not_ai'
  /** 匹配方式不是错误码判据，不比对上游返回代码。 */
  | 'match_not_error_code'
  /** 匹配方式不是频率判据，不数窗口内的请求条数。 */
  | 'match_not_rate'
  /** 匹配方式压根不读文本（频率 / 错误码 / 状态码 / AI 审核）。 */
  | 'match_not_text'
  /** 转发后异步审核跑在队列上，本次请求的上游响应早已交付。 */
  | 'phase_async'
  /** 阶段在转发前，那时上游还没返回。 */
  | 'phase_no_upstream'
  /** 阶段在上游侧，判据扫的是上游返回的内容而不是请求上下文。 */
  | 'phase_not_prompt'

export type QyViolationTestAbsentDimension = {
  id: QyViolationTestInput
  reason: QyViolationTestAbsentReason
}

/**
 * 这条规则**读不到**的维度，以及每一个读不到的**理由**。
 *
 * 为什么要有这个函数：面板此前只渲染规则会读的那几格，其余静默省略。于是一条
 * prompt 阶段的关键词规则在界面上只剩「请求上下文」一格 —— 项目方看到的正是
 * 这一屏，得出的结论是「上游返回文本、返回代码、状态码这些维度根本没做」。
 * 功能一直是对的，缺的是**判据本身没被摆出来**。
 *
 * 「asked」一律由 `qyRuleTestInputs` 反推，绝不另写一套判据：两份会漂移，而漂移
 * 的表现是最坏的一种 —— 一格既被说明成「读不到」，又真的被发去后端参与判定。
 * 这里只额外负责一件事：那一格为什么不在里面。
 */
export function qyRuleTestAbsentDimensions(
  phase: QyViolationPhase,
  matchType: QyViolationMatchType
): QyViolationTestAbsentDimension[] {
  const asked = new Set<QyViolationTestInput>(
    qyRuleTestInputs(phase, matchType)
  )
  // 文本判据：这三种扫的都是一段文本，差别只在扫哪一段。其余判据（频率 /
  // 错误码 / 状态码 / AI 审核）一个字节的文本都不读，缺席该怪匹配方式而不是阶段。
  const textual =
    matchType === 'keyword' ||
    matchType === 'regex' ||
    matchType === 'upstream_text'

  const out: QyViolationTestAbsentDimension[] = []
  for (const id of QY_VIOLATION_TEST_DIMENSIONS) {
    if (asked.has(id)) continue
    switch (id) {
      case 'request_text':
        out.push({
          id,
          reason: textual ? 'phase_not_prompt' : 'match_not_text',
        })
        break
      case 'upstream_text':
      case 'reject_reason':
        out.push({
          id,
          reason: textual ? 'phase_no_upstream' : 'match_not_text',
        })
        break
      case 'status_code':
        // 状态码判据恒问状态码，上游两阶段也恒问，所以缺席只剩两种可能：
        // 转发前上游还没返回，或者转发后异步手上已经没有响应了。
        out.push({
          id,
          reason: phase === 'post_async' ? 'phase_async' : 'phase_no_upstream',
        })
        break
      case 'error_code':
        out.push({ id, reason: 'match_not_error_code' })
        break
      case 'rate_count':
        out.push({ id, reason: 'match_not_rate' })
        break
      default:
        // ai_verdict / ai_category / ai_confidence 三格同进同出。
        out.push({ id, reason: 'match_not_ai' })
    }
  }
  return out
}
