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
import { CornerDownRight, Package, TriangleAlert } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Checkbox } from '@/components/ui/checkbox'
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
  /**
   * 该用户分组是否**已设定范围**。
   *
   * 未设定范围时可选性不可编辑：那一档的人本来就能用全部模型分组，
   * 在这里勾掉一格没有任何含义 —— 要限制就先给这个分组设定范围。
   */
  scoped: boolean
  /** 这一格的可达性从哪来。`undefined` = 后端尚未下发，退化成只按 `granted` 渲染。 */
  reachableVia?: 'both' | 'none' | 'plan' | 'scope'
  /** 让这一格可达的套餐名。 */
  planTitles?: string[]
  /** 对角线格（用户分组 == 模型分组），带那条反直觉提示。 */
  selfEdge: boolean
  /**
   * 勾选框的 DOM id，供外壳把**可见的**行标题拿 `<label htmlFor>` 绑上去。
   *
   * 缺省时勾选框只有 `aria-label`（整页矩阵里没有可当标题用的可见文字，
   * 每一格的行列身份由粘性行头与列头给出）。配置弹窗那一列有模型分组名，
   * 绑上之后名字本身成为勾选框的标签：点名字即可切换，读屏也念得出这个
   * 勾选框管的是哪一个模型分组。
   */
  toggleId?: string
  onToggleGranted: (granted: boolean) => void
  onRatioChange: (draft: QyGmRatioDraft) => void
}

/**
 * 矩阵的一个格子。
 *
 * ── 倍率三态，**空白格子必须不存在** ──
 *
 *   · 继承        灰色 `↳ 继承 x.xx` —— 显示**继承来的实际值**，输入框空着
 *   · 显式数值    黑色粗体数字；其中 `0` 额外标绿字「免费」
 *   · 兜底缺失    红色「按 1.0 计费」—— 该列在 `options.GroupRatio` 里没有值，
 *                 上游 `GetGroupRatio` 会静默 return 1 且只写一条日志
 *
 * 一个空白的格子是 0 还是继承，人永远猜不对，而两者的差价是全额。所以宁可
 * 每一格都写字，也不留空。
 *
 * ── 「恢复继承」与「设为免费」是两个不同的动作 ──
 * 清空输入框 = 继承（提交 `null`，后端**删掉** `GroupGroupRatio` 的那个 key）；
 * 敲一个 `0` = 显式免费（那个 key 存在且值为 0）。两者在扣费上差一个全额，
 * 在数据上差一个 key 的存在性，所以界面上必须是两个不同的操作、两套文案。
 *
 * ── 为什么继承格不预填数字 ──
 * 预填之后运营随手保存一遍，全站的继承格就被固化成硬编码覆盖，之后再改
 * 模型分组的兜底倍率对它们不再有任何影响 —— 而没有任何人做过这个决定。
 *
 * ── 可达性双来源 ──
 * 一格可以由「范围授予」或「套餐解锁」变得可达，两者独立。只画前者会让运营
 * 以为这一档的人碰不到那一列，而他改那一列兜底倍率时正在给一批人改价。
 */
export function QyGmMatrixCell(props: QyGmMatrixCellProps) {
  const { t } = useTranslation()

  const raw = props.ratio.kind === 'set' ? props.ratio.raw : ''
  const parsed = raw === '' ? null : qyGmParseRatio(raw)
  const invalid = parsed === 'invalid'
  const isFree = parsed === 0
  const inherit = props.ratio.kind === 'inherit'
  const dirty = props.entry != null

  // 该列在 options.GroupRatio 里没有兜底倍率 —— 继承会静默落到 1.0。
  // 这是唯一一处「界面上什么都不说、后台按原价扣钱」的组合，必须标红。
  //
  // 判据是后端下发的 base_missing，**不是**「列头 base_ratio 是不是空串」：
  // 列轴本身就是从 options.GroupRatio 的键生成的，base_ratio 永不为空，
  // 那个判据下这个红色态在结构上永远不可能出现，整块是死代码。
  // 后端未下发（旧版本）时才退回那个近似判据。
  const missingBase = props.cell?.base_missing ?? props.baseRatio.trim() === ''
  const viaPlan = props.reachableVia === 'plan' || props.reachableVia === 'both'

  const names = { userGroup: props.userGroup, modelGroup: props.modelGroup }
  /*
    勾选框的标签说的是**这一格代表什么**，不是"点下去会发生什么"。

    早先这里按 granted 在「撤销 X 对 Y 的可选性」与「X 选不了 Y，点击放开」之间
    切换，因为当时的控件是一个只画动作图标（＋ / ✕）的裸按钮。换成勾选框之后
    那套文案会当场反过来：勾中态本身已经念作"已选中"，再叠一句「撤销…」，读屏
    读出来的是"已选中，撤销 X 对 Y 的可选性"，而运营用眼睛看到的 ✕ 同样出现在
    **已放开**的行上 —— 项目方那句"一行里没有任何可以勾选的东西"正是这么来的：
    控件在，但它画的是动作，人找的是状态。状态归勾选框，标签归身份。
  */
  const toggleLabel = t('qy_gm_cell_grant_toggle', names)
  // 未设定范围的行两个方向都点不动，提示要先说清楚这一点；对角线格则要先说
  // 那条反直觉的事实（删掉对角线不会让这一档的人发不出请求）。
  let toggleTitle = toggleLabel
  if (!props.scoped) toggleTitle = t('qy_group_scope_unset_hint')
  else if (props.selfEdge) toggleTitle = t('qy_group_matrix_self_edge_hint')

  return (
    <div
      className={cn(
        'flex h-11 items-center gap-1 rounded-md border p-0.5',
        !props.granted && 'border-dashed',
        // 范围里没有它、却因为套餐而可达：独立视觉状态。既不是"可选"也不是
        // "不可选"，画成任何一个都是假陈述。
        !props.granted && viaPlan && 'border-info/60 bg-info/5',
        dirty && 'border-warning border-solid',
        props.granted && isFree && 'border-success/50 bg-success/5',
        invalid && 'border-destructive',
        props.selfEdge && !dirty && 'bg-muted/40'
      )}
    >
      {/*
        可选性开关 —— **一个真的勾选框，在这一格的最前面**。

        它此前是一个贴在格子最右端、`size-3` 的裸图标按钮（未放开画 ＋、已放开
        画 ✕）。三件事叠在一起让它在界面上等于不存在：
          · 图标画的是**动作**不是**状态**，于是"已放开"的那些行上显示的是一个 ✕，
            运营扫一眼读到的是"这行被禁掉了"，恰好与事实相反；
          · 它紧贴倍率输入框的右内边，看起来像输入框自带的"清空"角标；
          · 12px、`text-muted-foreground/60`，且完全没有文字。
        项目方的原话是「一行里没有任何可以勾选的东西」—— 回调链路四层全通，
        控件也在，但没有人认得出它是控件。所以这里换成尺寸、形状、选中态都
        与全站一致的 `Checkbox`，并挪到行首：一格的第一个像素就回答"这一格
        现在放开了没有"。

        放在倍率输入框**前面**还有一个附带好处：勾选框与倍率是两份独立的数据
        （可选性落 `qy_group_grants`，倍率落上游 `options.GroupGroupRatio`），
        排在输入框里侧时它看起来像输入框的一部分。

        ── 未设定范围的行**两个方向都禁用** ──
        那一档的人本来就能用全部模型分组，往它头上写 grant / revoke 生成的动作
        后端一律拒绝，而运营从界面上看不出保存为什么失败。禁用而不是隐藏：
        隐藏会让这一页退回到项目方投诉的那个形状（找不到可勾的东西），
        而禁用态的勾选框仍然在原位说明"这里本该有一个开关"，`title` 与外壳上的
        那句说明再补上"先去设定范围"。
      */}
      <Checkbox
        id={props.toggleId}
        checked={props.granted}
        disabled={!props.scoped}
        onCheckedChange={(checked) => props.onToggleGranted(checked)}
        aria-label={toggleLabel}
        title={toggleTitle}
        /*
          禁用态挂 `data-disabled:` 而不是 `disabled:`：Base UI 的勾选框渲染成
          一个 `<span role=checkbox aria-disabled>`（原生 `<input>` 是旁边那个
          隐藏的镜像），而 Tailwind 的 `disabled:` 变体只对真的带 `disabled`
          属性的表单元素生效 —— 共用组件里那两条 `disabled:opacity-50` 在这里
          一条都不会命中，未设定范围的那一整档会画得和可点的一模一样。
        */
        className='ms-1.5 shrink-0 data-disabled:cursor-not-allowed data-disabled:opacity-50'
      />
      {inherit && (
        <CornerDownRight
          aria-hidden='true'
          className='text-muted-foreground/60 size-3 shrink-0'
        />
      )}
      {/*
        倍率输入框**在不可选的格子上同样可编辑**。

        它曾经只在 granted 时渲染，并把不可选格子上已有的覆盖值画成删除线。
        那是一句假陈述：`GroupGroupRatio` 与可选清单是两份独立的数据，
        倍率在下面三种情形里 100% 在扣钱 ——
          · 该用户分组尚未设定范围（此时它能用全部模型分组，每一格都在真实扣钱）；
          · 设定了范围但处于影子模式（同上）；
          · 对角格（分组为空的令牌恒等于属主的用户分组，整段跳过可选性检查）。
        再加上一条:全新安装时一个 scope 行都没有 —— 而那恰好是本轮的**默认状态**。
        若倍率格随 granted 一起消失，这一页会变成「一个倍率都改不了」，运营唯一的
        出路是去给某一行设定范围 —— 那是访问控制动作，不是改价动作。
      */}
      <Input
        value={raw}
        inputMode='decimal'
        aria-invalid={invalid}
        aria-label={t('qy_group_matrix_cell_ratio_label', {
          userGroup: props.userGroup,
          modelGroup: props.modelGroup,
        })}
        placeholder={missingBase ? '1' : props.baseRatio}
        title={t('qy_group_ratio_inherit_vs_zero_hint')}
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
          isFree && 'text-success',
          !props.granted && 'opacity-60'
        )}
      />
      {/*
        三态里的「有字」那一半。空白格子必须不存在 —— 一个空格子是 0 还是继承，
        人永远猜不对，而两者差一个全额。
      */}
      {inherit && !missingBase && (
        <span
          className='text-muted-foreground shrink-0 text-[10px] leading-none'
          title={t('qy_group_ratio_inherit', { ratio: props.baseRatio })}
        >
          {t('qy_group_ratio_inherit', { ratio: props.baseRatio })}
        </span>
      )}
      {/* 该列没有兜底倍率：上游 GetGroupRatio 找不到时静默 return 1 且只写
          一条日志。这是全站唯一一处「界面不说、后台按原价扣钱」的组合。 */}
      {inherit && missingBase && (
        <span
          className='text-destructive inline-flex shrink-0 items-center gap-0.5 text-[10px] leading-none'
          title={t('qy_group_ratio_missing_fallback')}
        >
          <TriangleAlert aria-hidden='true' className='size-2.5' />
          {t('qy_group_ratio_missing_fallback')}
        </span>
      )}
      {/* 显式 0 = 免费。**必须有文字**，不能只靠绿色：这一页上「免费」与
          「按兜底 1.0 收费」的区别如果全押在颜色上，看不出这个区别的人会按
          错误的价放行一批用户。 */}
      {isFree && (
        <span className='text-success shrink-0 text-[10px] leading-none'>
          {t('qy_group_ratio_zero_free')}
        </span>
      )}
      {/* 经套餐可达。它与「可选」是两条不同的授权链路，必须各有各的形状。 */}
      {viaPlan && (
        <span
          className='text-info inline-flex shrink-0 items-center gap-0.5 text-[10px] leading-none'
          title={t('qy_group_matrix_reachable_via_plan', {
            plans: (props.planTitles ?? []).join('、'),
          })}
        >
          <Package aria-hidden='true' className='size-2.5' />
        </span>
      )}
    </div>
  )
}
