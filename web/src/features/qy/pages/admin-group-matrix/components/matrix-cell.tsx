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
import { CornerDownRight, Plus, X } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Input } from '@/components/ui/input'
import { cn } from '@/lib/utils'

import {
  qyGmParseRatio,
  type QyGmRatioDraft,
  type QyGmDraftEntry,
} from '../lib/draft'
import type { QyGmCell } from '../types'

export type QyGmMatrixCellProps = {
  userGroup: string
  modelGroup: string
  /** 服务端真实状态；`undefined` = 该组合从未出现过（不可选、无覆盖）。 */
  cell: QyGmCell | undefined
  entry: QyGmDraftEntry | undefined
  granted: boolean
  ratio: QyGmRatioDraft
  /** 该模型分组的兜底倍率。继承格显示它，但**不预填进输入框**。 */
  baseRatio: string
  /** 该用户分组是否已被接管。未接管时清单不可编辑（但倍率仍然是真的）。 */
  managed: boolean
  /** 对角线格（用户分组 == 模型分组），带那条反直觉提示。 */
  selfEdge: boolean
  onToggleGranted: (granted: boolean) => void
  onRatioChange: (draft: QyGmRatioDraft) => void
}

/**
 * 矩阵的一个格子 —— 四态，**形状 + 颜色双编码**。
 *
 *   1. 留白（不可选）      虚线框 + `+`，点一下放开
 *   2. 可选 · 继承         灰色斜体的兜底值 + `↳` 箭头，输入框**空着**
 *   3. 可选 · 自定义       黑色粗体数字
 *   4. 可选 · 显式 0       绿色「免费」+ 0
 *
 * 双编码不是无障碍教条：这一页上「免费」与「兜底 1.0」的区别全在颜色上，
 * 而看不出这个区别的人会按错误的价放行一批用户。
 *
 * ── 为什么继承格不预填数字 ──
 * 预填之后运营随手保存一遍，全站的继承格就被固化成硬编码覆盖，之后再改
 * 模型分组的兜底倍率对它们不再有任何影响 —— 而没有任何人做过这个决定。
 * 空输入框提交 `null`（继承），`0` 必须是运营主动敲进去的。
 */
export function QyGmMatrixCell(props: QyGmMatrixCellProps) {
  const { t } = useTranslation()

  const raw = props.ratio.kind === 'set' ? props.ratio.raw : ''
  const parsed = raw === '' ? null : qyGmParseRatio(raw)
  const invalid = parsed === 'invalid'
  const isFree = parsed === 0
  const inherit = props.ratio.kind === 'inherit'
  const dirty = props.entry != null

  const names = { userGroup: props.userGroup, modelGroup: props.modelGroup }
  const toggleLabel = props.granted
    ? t('qy_group_matrix_cell_revoke_label', names)
    : t('qy_group_matrix_cell_denied', names)
  // 未接管的行两个方向都点不动，提示要先说清楚这一点；对角线格则要先说那条
  // 反直觉的事实（删掉对角线不会让这一档的人发不出请求）。
  let toggleTitle = toggleLabel
  if (!props.managed) toggleTitle = t('qy_group_matrix_managed_hint')
  else if (props.selfEdge) toggleTitle = t('qy_group_matrix_self_edge_hint')

  return (
    <div
      className={cn(
        'flex h-11 items-center gap-1 rounded-md border p-0.5',
        !props.granted && 'border-dashed',
        dirty && 'border-warning border-solid',
        props.granted && isFree && 'border-success/50 bg-success/5',
        invalid && 'border-destructive',
        props.selfEdge && !dirty && 'bg-muted/40'
      )}
    >
      {inherit && (
        <CornerDownRight
          aria-hidden='true'
          className='text-muted-foreground/60 ms-1 size-3 shrink-0'
        />
      )}
      {/*
        倍率输入框**在不可选的格子上同样可编辑**。

        它曾经只在 granted 时渲染，并把不可选格子上已有的覆盖值画成删除线。
        那是一句假陈述：`GroupGroupRatio` 与可选清单是两份独立的数据，
        倍率在下面三种情形里 100% 在扣钱 ——
          · 该用户分组尚未接管（清单根本不生效，上游白名单说了算）；
          · 已接管但处于影子模式（同上）；
          · 对角格（分组为空的令牌恒等于属主的用户分组，整段跳过可选性检查）。
        再加上一条:全新安装时一个 scope 行都没有，若倍率格随 granted 一起消失，
        这一页会变成「一个倍率都改不了」，运营唯一的出路是去把某一行切成 managed
        —— 那是访问控制接管，不是改价操作。
      */}
      <Input
        value={raw}
        inputMode='decimal'
        aria-invalid={invalid}
        aria-label={t('qy_group_matrix_cell_ratio_label', {
          userGroup: props.userGroup,
          modelGroup: props.modelGroup,
        })}
        placeholder={props.baseRatio}
        title={
          inherit
            ? t('qy_group_matrix_ratio_inherit_hint', {
                ratio: props.baseRatio,
              })
            : t('qy_group_matrix_ratio_zero_hint')
        }
        onChange={(event) => {
          const next = event.target.value
          props.onRatioChange(
            next.trim() === ''
              ? { kind: 'inherit' }
              : { kind: 'set', raw: next }
          )
        }}
        className={cn(
          'h-full border-0 bg-transparent px-1 text-center text-xs tabular-nums shadow-none focus-visible:ring-0',
          inherit && 'text-muted-foreground placeholder:italic',
          !inherit && 'font-semibold',
          props.granted && isFree && 'text-success',
          !props.granted && 'opacity-60'
        )}
      />
      {/* 显式 0 = 免费。**必须有文字**，不能只靠绿色：这一页上「免费」与
          「按兜底 1.0 收费」的区别如果全押在颜色上，看不出这个区别的人会按
          错误的价放行一批用户。 */}
      {props.granted && isFree && (
        <span className='text-success shrink-0 text-[10px] leading-none'>
          {t('qy_group_matrix_cell_free')}
        </span>
      )}
      {/*
        可选性开关。未接管的行**两个方向都禁用**：没有权威清单时，grant 与
        revoke 生成的动作后端一律拒绝，而运营从界面上看不出保存为什么失败。
        （早先只禁用了 grant 那一半。）
      */}
      <button
        type='button'
        disabled={!props.managed}
        onClick={() => props.onToggleGranted(!props.granted)}
        aria-label={toggleLabel}
        title={toggleTitle}
        className={cn(
          'focus-visible:ring-ring me-0.5 shrink-0 rounded p-0.5 focus-visible:ring-2 focus-visible:outline-none',
          props.managed
            ? 'text-muted-foreground/60 hover:text-destructive cursor-pointer'
            : 'cursor-not-allowed opacity-40'
        )}
      >
        {props.granted ? (
          <X aria-hidden='true' className='size-3' />
        ) : (
          <Plus aria-hidden='true' className='size-3' />
        )}
      </button>
    </div>
  )
}
