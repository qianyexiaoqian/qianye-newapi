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
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'

import { QyResponsiveDialog } from '../../../components/qy-responsive-dialog'
import type {
  QyGmMode,
  QyGmPreviewResponse,
  QyGmScopeRequest,
  QyGmUserGroup,
} from '../types'

type QyGmScopeFormProps = {
  /**
   * 表单是否可见。**每次由不可见变为可见都要从服务端状态重置** ——
   * 残留上一次编辑的另一个分组的值，会让运营在完全不知情的情况下把 A 组的
   * 设置按到 B 组头上。
   */
  open: boolean
  userGroup: QyGmUserGroup | null
  /** 该用户分组当前草稿里在范围内的模型分组数，用于把「空范围」说清楚。 */
  grantedCount: number
  /** 矩阵上还有没保存的格子。切 enforce 之前必须先保存（见下）。 */
  hasUnsavedDraft: boolean
  /**
   * 针对**这一个用户分组**的影响面预览结果，没预览过时为 `null`。
   *
   * 只有它非空且 `preview_incomplete` 为 false 时才允许切 `enforce` ——
   * 切 enforce 是这一页唯一会让一批用户当场 403 的动作，没看过影响面就不许按。
   * `shadow` 与取消接管不需要（两者都不打断任何流量）。
   *
   * **必须是单分组预览**，不能复用矩阵页那份通用预览：服务端在切换时用
   * `previewDigest(userGroup)` 只重算这一个分组，两侧取值范围不同则
   * `impact_hash` 永不相等，enforce 会被 409 永久锁死。
   */
  enforcePreview: QyGmPreviewResponse | null
  isPreviewing: boolean
  onPreviewForEnforce: () => void
  isSaving: boolean
  onSubmit: (body: QyGmScopeRequest) => void
  /** 取消键。抽屉里给一个"关掉"，内嵌进配置弹窗时不给（弹窗自己有关闭）。 */
  onCancel?: () => void
}

type QyGmScopeSheetProps = QyGmScopeFormProps & {
  onOpenChange: (open: boolean) => void
}

/**
 * 单个用户分组的**模型分组范围**设置（独立抽屉形态）。
 *
 * 正文与提交动作在 {@link QyGmScopeForm} 里，这里只负责外壳：需求 4 的配置弹窗
 * 要把同一段表单**内嵌**进自己（不能再叠一层浮层，见 `QyGmPreviewBody` 的说明），
 * 而"设定范围 / 影子 / 强制"这三态的判定与切 enforce 的闸门只能有一份。
 */
export function QyGmScopeSheet(props: QyGmScopeSheetProps) {
  const { t } = useTranslation()
  if (props.userGroup == null) return null

  return (
    <QyResponsiveDialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={t('qy_group_scope_edit_title', {
        userGroup: props.userGroup.name,
      })}
      description={t('qy_group_scope_edit_desc')}
    >
      <QyGmScopeForm {...props} onCancel={() => props.onOpenChange(false)} />
    </QyResponsiveDialog>
  )
}

/**
 * 范围设置表单本体。
 *
 * ── 三个状态，不是两个 ──
 *   · 未设定范围（`managed:false`）= 扩展库里没有这一行 = **全部模型分组可用，
 *     各按自己的兜底倍率**。这是零行为变更的默认态，也是回退（删掉行即可）。
 *   · 设定范围 + shadow            = 范围已经是权威的，但只记录不阻断。
 *   · 设定范围 + enforce           = 不在范围里的模型分组，令牌选不了、请求 403。
 *
 * 「设了范围、但范围是空的」是**合法且危险**的配置（隔离组 / 封禁组），必须能
 * 表达，所以它不能靠「有没有 grant 行」来推断 —— 那会让「一个都不许用」与
 * 「没设过范围」不可区分，而这两个错误方向分别是整组用户 403 与整组用户全放行。
 *
 * 文案上**绝不出现「接管」二字**：新口径下「未接管」会被读成「不能用」，
 * 而它的真实含义恰好是「全都能用」。
 *
 * ── 为什么提交键在表单末尾而不是外壳的固定底栏 ──
 * 这段表单要同时出现在两种外壳里，其中一种（配置弹窗）的底栏已经被"保存
 * 可选性与倍率"占着，而那是另一个端点、另一份数据。两个保存键并排放在同一个
 * 固定底栏里、各自保存不同的东西，正是这套两库设计最容易被点错的地方。
 * 表单本身只有五个控件，按钮跟着它一起结束是够得着的。
 */
export function QyGmScopeForm(props: QyGmScopeFormProps) {
  const { t } = useTranslation()
  const [managed, setManaged] = useState(false)
  const [mode, setMode] = useState<QyGmMode>('shadow')
  const [allowAuto, setAllowAuto] = useState(true)
  const [note, setNote] = useState('')

  const userGroup = props.userGroup
  const userGroupName = userGroup?.name ?? null

  /*
    最新一份服务端行。重置时读它，但**重置本身不以它为触发条件** ——
    见下面那段。用 ref 而不是依赖数组里的对象，是因为两者要的东西不同：
    重置要的是"最新的值"，触发要的是"换了一个人"。
  */
  const latestRow = useRef(userGroup)
  latestRow.current = userGroup

  /*
    重置只由两件事触发：表单由不可见变为可见，或者换了一个用户分组。

    ── 为什么依赖里不能放 userGroup 这个对象 ──
    这段表单有两种外壳，其中一种（需求 4 的配置弹窗）把它与矩阵差异条放在
    **同一屏**，且那里的 `open` 是硬编码字面量 `true`。矩阵保存成功之后
    `applyServerState` 会 `queryClient.setQueryData` 写入一个全新的响应对象，
    于是 `userGroups.find(...)` 得到的是一个新的对象身份 —— 依赖数组里放对象
    就等于"按一次矩阵保存 = 把范围表单整个重置回服务端旧值"。

    那条路径还恰好是 UI 主动把运营推上去的：切 enforce 要求
    `!hasUnsavedDraft`，界面文案明写"先保存草稿"。于是运营的动作序列必然是
    改格子 → 填 enforce + 备注 → 按保存 → 表单被静默清回 shadow，
    屏幕上只有一句绿色的「已保存」（那是矩阵保存）。他再按「应用范围设置」，
    提交的是 managed:false / shadow 的旧值 —— 他以为这一档人已经被 enforce
    限制住，实际一道闸门都没设上。而 enforce 正是这一页唯一会让一批用户
    当场 403 的动作。
  */
  useEffect(() => {
    if (!props.open || userGroupName == null) return
    const row = latestRow.current
    if (row == null) return
    setManaged(row.managed)
    setMode(row.mode)
    setAllowAuto(row.allow_auto)
    setNote(row.note ?? '')
  }, [props.open, userGroupName])

  if (userGroup == null) return null

  const enforceUnlocked =
    props.enforcePreview != null &&
    !props.enforcePreview.preview_incomplete &&
    !props.hasUnsavedDraft
  const enforceBlocked = managed && mode === 'enforce' && !enforceUnlocked
  const emptyList = managed && props.grantedCount === 0

  return (
    <div className='space-y-4'>
      {/*
          关掉这个开关**不是"禁用"，是"放开"**。

          这是整个抽屉里最容易被读反的一句话：运营的直觉是"关掉限制开关 = 这一档
          的人被限制住了"。实际相反 —— 没有范围行时，那一档的人能用**全部**模型
          分组，各按兜底倍率。开关旁边这段说明因此不能省。
        */}
      <div className='flex items-start justify-between gap-4'>
        <div className='space-y-0.5'>
          <Label>{t('qy_group_scope_switch_label')}</Label>
          <p className='text-muted-foreground text-xs'>
            {managed
              ? t('qy_group_scope_switch_on_desc')
              : t('qy_group_scope_switch_off_desc')}
          </p>
        </div>
        <Switch checked={managed} onCheckedChange={setManaged} />
      </div>

      {managed && (
        <>
          <div className='space-y-2'>
            {/*
              ── 这里曾经是「影子 / 强制」二选一 ──

              shadow 已整体下线:有范围行就立即生效,没有第二档可选。留着一组
              选中了也不会有任何差别的单选框,正是这一轮在消灭的形状
              (后端 putScopeReq 已经不再解析 mode 字段)。

              「打开范围 = 这一档从此只能用清单里的模型分组」这句话没有丢,
              它就在上面那个开关的说明里。
            */}
            {/*
                强制模式**不是绝对的**，而这一点在别处一个字都看不到：
                套餐解锁是并集，且叠在范围替换之后（读侧 QyUsableGroupsForUser、
                写侧 CheckTokenGroup 与 playground 都各有一条短路）。于是买了
                解锁贵分组套餐的用户，照样能把令牌指向范围之外的那个分组。
                运营用 enforce 去硬性挡住一档人时，必须先知道这条穿透存在。
              */}
            {mode === 'enforce' && (
              <p className='text-muted-foreground text-xs'>
                {t('qy_group_scope_enforce_plan_bypass_note')}
              </p>
            )}
            {mode === 'enforce' && (
              <div className='space-y-2'>
                {enforceBlocked && (
                  <p className='text-destructive text-xs'>
                    {props.hasUnsavedDraft
                      ? t('qy_group_matrix_enforce_needs_saved_draft')
                      : t('qy_group_matrix_mode_switch_confirm')}
                  </p>
                )}
                {/* 这一份预览**只评估这个用户分组**，与矩阵页顶部那份通用预览
                      刻意分开：服务端切换时也只重算这一个分组，取值范围不同的
                      两个 impact_hash 永远不相等，enforce 会被 409 锁死。 */}
                <Button
                  type='button'
                  variant='outline'
                  size='sm'
                  disabled={props.isPreviewing || props.hasUnsavedDraft}
                  onClick={props.onPreviewForEnforce}
                >
                  {t('qy_group_matrix_enforce_preview_btn', {
                    userGroup: userGroup.name,
                  })}
                </Button>
                {props.enforcePreview != null && (
                  <p className='text-muted-foreground text-xs tabular-nums'>
                    {t('qy_group_matrix_enforce_preview_summary', {
                      broken: props.enforcePreview.newly_broken.length,
                      tokens:
                        props.enforcePreview.total_newly_broken_tokens ?? 0,
                    })}
                  </p>
                )}
              </div>
            )}
          </div>

          <div className='flex items-start justify-between gap-4'>
            <div className='space-y-0.5'>
              <Label>{t('qy_group_matrix_allow_auto')}</Label>
              <p className='text-muted-foreground text-xs'>
                {t('qy_group_matrix_allow_auto_desc')}
              </p>
            </div>
            <Switch checked={allowAuto} onCheckedChange={setAllowAuto} />
          </div>

          {/* 空范围是合法配置（隔离组 / 封禁组），所以这里是警告不是拦截。
                但它必须被说出来：设了范围却一格都没勾，enforce 之后这一组人
                一个模型分组都选不了。 */}
          {emptyList && (
            <p className='text-warning rounded-md border border-dashed p-2 text-xs'>
              {t('qy_group_scope_empty_warning', {
                userGroup: userGroup.name,
              })}
            </p>
          )}
        </>
      )}

      <div className='space-y-2'>
        <Label htmlFor='qy-gm-scope-note'>{t('qy_common_remark')}</Label>
        <Input
          id='qy-gm-scope-note'
          value={note}
          maxLength={255}
          onChange={(event) => setNote(event.target.value)}
          placeholder={t('qy_group_matrix_note_placeholder')}
        />
      </div>

      <div className='flex flex-wrap justify-end gap-2'>
        {props.onCancel != null && (
          <Button type='button' variant='outline' onClick={props.onCancel}>
            {t('qy_common_cancel')}
          </Button>
        )}
        <Button
          type='button'
          disabled={props.isSaving || enforceBlocked}
          onClick={() =>
            props.onSubmit({
              managed,
              mode,
              allow_auto: allowAuto,
              note: note.trim(),
            })
          }
        >
          {t('qy_group_scope_apply')}
        </Button>
      </div>
    </div>
  )
}
