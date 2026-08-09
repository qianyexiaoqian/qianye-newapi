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
import { useMutation, useQuery } from '@tanstack/react-query'
import { Eye, EyeOff } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'

import { QyPageBoundary } from '../../../components/qy-page-boundary'
import { QyResponsiveDialog } from '../../../components/qy-responsive-dialog'
import { QyGmDiffBar } from '../../admin-group-matrix/components/diff-bar'
import { QyGmPreviewBody } from '../../admin-group-matrix/components/preview-dialog'
import { QyGmStatusBanners } from '../../admin-group-matrix/components/status-banners'
import { useQyGmEditor } from '../../admin-group-matrix/lib/use-editor'
import type { QyGmUserGroup } from '../../admin-group-matrix/types'
import { qyOpsErrorMessage } from '../../ops/errors'
import { qyUgParseTopupInput, qyUgTopupInputOf } from '../lib/merged-rows'
import { qyUgrImpactQuery, qyUgrRename, qyUgrUpdate } from '../roster/api'
import { QyUgrActionBlockNote } from '../roster/components/action-block-note'
import { QyUgrRenameDialog } from '../roster/components/rename-dialog'
import {
  QY_UGR_BLOCK_NEXT_STEP_KEY,
  QY_UGR_RENAME_ENTRY_BLOCK_KEY,
  qyUgrRenameEntryBlock,
} from '../roster/lib/gates'
import { QyUgGroupDetail } from './user-group-detail'

/**
 * 「用户分组」表行内的**编辑**弹窗。
 *
 * ── 它取代了什么 ──
 *
 * 合并之前，改一档人的东西散在三个地方：备注在「用户分组登记」那张表的行内、
 * 充值倍率在「用户分组」那张表的行内、可用模型分组与倍率在另一个弹窗里。
 * 项目方原话：「一个列表框即可……用户分组选定指定模型分组，可以单独配置模型
 * 分组备注……单独给这个用户的模型分组调整倍率。」
 *
 * 于是表只负责看，这个弹窗负责改：一档人的**全部**配置都在这一屏里。
 *
 * ── 为什么弹窗里还留着影响面闸门那一整条链 ──
 *
 * 勾掉一个模型分组会让一批正在跑的令牌当场 403，改一格倍率会在保存成功那一秒
 * 起按新价扣钱。所以状态横幅 → 差异条（含「没预览过不许保存」）→ 范围设置 →
 * 逐格可选性 / 倍率 / 备注 → 影响面报告，一段都不能少：少任何一段，弹窗就成了
 * 一条绕开闸门的近路，而绕开闸门的那次点击恰好是最危险的一次。
 * 实现上共用 {@link useQyGmEditor} 这一份状态机，两个外壳的闸门不可能松紧不一。
 *
 * ── 关掉即卸载，草稿不跨次留存 ──
 * 正文只在打开时挂载（Base UI 的 Portal 默认不 keepMounted），关掉弹窗就是丢弃
 * 草稿。残留一份看不见的草稿，下一次打开另一档时会把上一次的改动混进影响面与
 * 保存动作里，而运营完全不知道自己在提交什么。
 */
export function QyUgGroupDialog(props: {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** 要编辑的用户分组名。`null` = 没有选中任何一档（弹窗此时也不该是开的）。 */
  userGroup: string | null
  /**
   * 表里那一行。`null` = 清单里已经没有它了（另一个管理员删掉了）。
   *
   * 备注 / 充值倍率 / 在册人数全部取自它，而不是弹窗自己再拉一次：两处各拉
   * 一份的表现是表上写 0.8、点开弹窗是 0.9，而运营会照着弹窗里的那个数按保存。
   */
  row: QyGmUserGroup | null
  /** 保存成功之后刷新外壳那张表。 */
  onSaved: () => Promise<void> | void
}) {
  const { t } = useTranslation()

  return (
    <QyResponsiveDialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={t('qy_ug_dialog_title', { userGroup: props.userGroup ?? '' })}
      description={t('qy_ug_dialog_desc')}
      contentClassName='sm:max-w-4xl'
    >
      {props.userGroup == null ? null : (
        <QyUgGroupDialogBody
          userGroup={props.userGroup}
          row={props.row}
          onSaved={props.onSaved}
        />
      )}
    </QyResponsiveDialog>
  )
}

function QyUgGroupDialogBody(props: {
  userGroup: string
  row: QyGmUserGroup | null
  onSaved: () => Promise<void> | void
}) {
  const { t } = useTranslation()
  const editor = useQyGmEditor()
  const data = editor.data
  const row = editor.userGroups.find((item) => item.name === props.userGroup)

  /**
   * 影响面报告**内联展开**，不叠第二层浮层。
   *
   * 报告是保存前的必读材料，而握着草稿的是这个弹窗本身：再叠一层浮层之后，
   * Esc 键关掉的是哪一层不确定，运营按一次可能连草稿一起丢。
   */
  const [reportOpen, setReportOpen] = useState(false)
  const showReport = reportOpen || editor.isPreviewing

  return (
    <QyPageBoundary
      query={editor.query}
      isEmpty={data != null && row == null}
      emptyTitle={t('qy_group_scope_dialog_gone')}
      emptyDescription={t('qy_group_scope_dialog_gone_desc')}
    >
      {data != null && row != null && (
        <div className='space-y-3'>
          <QyUgBasicsSection row={row} onSaved={props.onSaved} />

          <QyGmStatusBanners
            snapshot={data.snapshot}
            partial={editor.partial}
            ratioDrift={editor.ratioDrift}
            selfExcluded={editor.selfExcluded}
            caseNearMiss={editor.caseNearMiss}
            warnings={data.warnings}
            shadowWriteDenies={data.shadow_write_denies}
            emptyScopeGroups={editor.emptyScopeGroups}
            onReload={editor.reload}
          />

          {/* 差异条与整页视图上是同一个组件、同一道闸门。它 `sticky top-0`，
              在弹窗的滚动正文里同样贴顶 —— 保存键不会随着模型分组清单滚走。 */}
          <QyGmDiffBar
            counts={editor.counts}
            invalidCount={editor.invalidCells.length}
            hasDraft={editor.draft.size > 0}
            needsPreview={editor.needsPreview}
            isPreviewing={editor.isPreviewing}
            isSaving={editor.isSaving}
            onPreview={() => {
              setReportOpen(true)
              editor.runPreview()
            }}
            onSave={editor.runSave}
            onReset={editor.resetDraft}
          />

          {showReport && (
            <section className='space-y-2 rounded-lg border p-3'>
              <div className='flex flex-wrap items-center justify-between gap-2'>
                <h3 className='text-sm font-medium'>
                  {t('qy_group_matrix_preview_title')}
                </h3>
                <Button
                  type='button'
                  variant='ghost'
                  size='sm'
                  onClick={() => setReportOpen(false)}
                >
                  <EyeOff aria-hidden='true' />
                  {t('qy_group_scope_dialog_hide_report')}
                </Button>
              </div>
              <QyGmPreviewBody
                preview={editor.preview}
                isLoading={editor.isPreviewing}
              />
            </section>
          )}
          {!showReport && editor.preview != null && (
            <Button
              type='button'
              variant='outline'
              size='sm'
              onClick={() => setReportOpen(true)}
            >
              <Eye aria-hidden='true' />
              {t('qy_group_scope_dialog_show_report')}
            </Button>
          )}

          {/*
            ── 这里曾经有一整段「可用范围的力度」表单 ──

            它是 `qy_group_scopes` 有没有这一行的直接投影：不先在那里把开关
            打开，下面整张清单的勾选框全是灰的。项目方带截图报的原话是"为何
            不可以删除/添加模型分组"——初见只能读成「什么都点不动」，而屏幕上
            唯一的解释要求他先理解一个内部数据模型。

            现在那件事由列表内容隐式表达（第一次增删时建 scope 行、清空时删），
            灰度维度（先观察 / 现在就生效）挪进列表上方的状态条。范围写入仍然
            只有 `editor.submitScope` 这一条通道，切 enforce 的双哈希闸门原封
            不动 —— 少的是那个前置开关，不是那道闸。
          */}
          <QyUgGroupDetail
            data={data}
            userGroup={row}
            serverCells={editor.serverCells}
            draft={editor.draft}
            grantedCount={editor.grantedCounts.get(row.name) ?? 0}
            onToggleGranted={(modelGroup, granted) =>
              editor.toggleGranted(row.name, modelGroup, granted)
            }
            onRatioChange={(modelGroup, ratio) =>
              editor.changeRatio(row.name, modelGroup, ratio)
            }
            /*
              按格备注只在**这个**外壳上画。

              整页矩阵那个外壳一次要看多档，行里再塞一个自由文本框会把它压成
              一堵墙；而这里一次只看一档，备注与倍率并排恰好是运营做的那一个
              决定（「这一档人能不能用这批渠道、用的话按什么价、下拉里显示什么」）。
            */
            onNoteChange={(modelGroup, note) =>
              editor.changeNote(row.name, modelGroup, note)
            }
            onCopyFrom={(fromUserGroup) =>
              editor.copyRow(fromUserGroup, row.name)
            }
            scope={{
              isSaving: editor.isScopeSaving,
              hasUnsavedDraft: editor.counts.total > 0,
              enforcePreview:
                editor.enforcePreview?.userGroup === row.name
                  ? editor.enforcePreview.result
                  : null,
              isPreviewing: editor.isEnforcePreviewing,
              onPreviewForEnforce: () => editor.runEnforcePreview(row.name),
              onSubmit: (body, afterApply) =>
                editor.submitScope(row.name, body, afterApply),
            }}
            scrollList={false}
          />
        </div>
      )}
    </QyPageBoundary>
  )
}

/**
 * 「这一档人本身」的那几项：备注与充值倍率。
 *
 * ── 一个保存键，一次 PUT ──
 *
 * 运营在这里做的是一个决定（「这一档人是什么、按几折充值」）。后端把两者收在
 * 同一个 `PUT /user-groups/:name` 上，并把充值倍率的写入排在展示属性之后 ——
 * 前者失败时后者一个字节都没动（零资金影响），反过来做的话收款金额已经变了而
 * 界面上那一行看起来什么都没保存，运营的下一步必然是"再改一次"。
 *
 * 部分失败由后端在响应里说出来，这里原样弹出并**留在屏幕上**。
 *
 * ── 「未登记」不需要一个补登记按钮 ──
 *
 * 只在 `users.group` 里见过、不在 `qy_user_groups` 里的名字（导入、改名残留、
 * 手工改库）此前没有地方挂备注。本轮后端在 PUT 里**自动补登记**：对着这样一行
 * 按编辑却收到「还没有登记」，运营唯一能做的事是去别处找一个叫「回填」的按钮，
 * 而那个按钮解决的是一个他不需要知道的内部区分。
 *
 * 界面上因此只留一句说明（"保存时会自动登记"），不留按钮 —— 一个只有一种正确
 * 点法的按钮不该出现。
 */
function QyUgBasicsSection(props: {
  row: QyGmUserGroup
  onSaved: () => Promise<void> | void
}) {
  const { t } = useTranslation()

  const name = props.row.name
  const serverNote = props.row.note
  const serverTopup = qyUgTopupInputOf(props.row.topup_ratio)

  const [note, setNote] = useState(serverNote)
  const [topup, setTopup] = useState(serverTopup)
  const [renaming, setRenaming] = useState(false)
  const [newName, setNewName] = useState(name)
  const renameRow = { name, users: props.row.user_count }

  /*
    换了一档人、或服务端回读带来新值时重置草稿。

    依赖里放**这三个原始值**而不是整个行对象：外壳每次重渲染都会新造一份行，
    按对象比会让这个 effect 在别的档保存成功、矩阵回读到达时把正在编辑的备注清掉。
  */
  useEffect(() => {
    setNote(serverNote)
    setTopup(serverTopup)
  }, [name, serverNote, serverTopup])

  const saveMutation = useMutation({
    mutationFn: () => {
      const parsed = qyUgParseTopupInput(topup)
      // 非法输入在按钮上就被拦住了（见下面的 disabled），这里是最后一道 ——
      // 提交一个 NaN 会让整份 TopupGroupRatio 序列化成 null，全站充值折扣失效。
      if (parsed.kind === 'invalid') {
        return Promise.reject(new Error(t('qy_ug_topup_invalid')))
      }
      return qyUgrUpdate(name, {
        note,
        // 两个字段刻意分开：Go 里 `"topup_ratio": null` 与字段缺席都得到 nil
        // 指针，两者不可区分，而「这次没打算改」与「删掉这个键、回落兜底」
        // 的后果不同 —— 后者会改变收款金额。
        ...(parsed.kind === 'clear'
          ? { clear_topup_ratio: true }
          : { topup_ratio: parsed.value }),
      })
    },
    onSuccess: async (result) => {
      // 展示属性与充值倍率落两个库，不原子。半成状态必须原样弹出来并留在屏幕上：
      // 报一句绿色的「已保存」然后走人，是这套设计最坏的失败方式。
      if (result.partial != null) {
        toast.error(result.partial.message, {
          duration: Number.POSITIVE_INFINITY,
        })
      } else {
        toast.success(t('qy_ugr_note_saved'))
      }
      if (result.backfilled) {
        toast.info(t('qy_ug_backfilled', { name }))
      }
      await props.onSaved()
    },
    onError: (error) => toast.error(qyOpsErrorMessage(error, t)),
  })

  const renameMutation = useMutation({
    mutationFn: () =>
      qyUgrRename(name, {
        // 回传运营在界面上看到的人数。对不上时后端返回 409 而不是照着新数字改。
        expect_users: props.row.user_count,
        new_name: newName.trim(),
      }),
    onSuccess: async (result) => {
      // 半成状态必须原样弹出来并留在屏幕上：报成功然后走人是这套跨库设计最坏的
      // 失败方式 —— 线上停在中间态，而没有任何人知道。`stragglers` 同理：那几个
      // 账号此刻正指着一个已经不存在的分组名。
      if (result.partial != null) {
        toast.error(result.partial.message, {
          duration: Number.POSITIVE_INFINITY,
        })
      } else if (result.stragglers > 0) {
        toast.warning(t('qy_ugr_stragglers', { count: result.stragglers }), {
          duration: Number.POSITIVE_INFINITY,
        })
      } else {
        toast.success(t('qy_ugr_renamed', { from: result.from, to: result.to }))
      }
      setRenaming(false)
      await props.onSaved()
    },
    onError: (error) => toast.error(qyOpsErrorMessage(error, t)),
  })

  const topupParsed = qyUgParseTopupInput(topup)
  const dirty = note !== serverNote || topup !== serverTopup

  /*
    ── 改名的四条拒绝分支，全部要在**点开改名弹窗之前**就说出来 ──────────

    入口级判据只答得出两条：`default` 永远改不了名、未登记的名字在登记表里
    没有行（改名端点第一句 `Take(&before)` 就 400）。后端 `renamability()`
    还有第三条 —— 处置为 block 的残留（抽奖名单在发布时进了 commit_hash，
    对一份冻结的引用来说"改个名"与"删掉"完全等价）。那一条要服务端探测。

    只接前两条的表现是：有残留的那一档改名按钮亮着，运营输完新名字、提交，
    吃一个 400 —— 与项目方投诉的形状完全一致（动作被静默挡住、屏幕上事先无
    字），只是从删除挪到了改名。而唯一的预告写在**删除弹窗**里，要求他先去
    点删除，可他此刻做的是改名。

    所以这里拉一次影响面。它只在弹窗打开时挂载（Base UI 的 Portal 不
    keepMounted），一次冷请求换掉一次注定失败的提交。目标传空串：改名不需要
    迁移目标，那一段 diff 也就不必算。
  */
  const impactQuery = useQuery({ ...qyUgrImpactQuery(name, ''), retry: false })
  const impact = impactQuery.data ?? null
  const renameEntryBlock = qyUgrRenameEntryBlock(props.row)
  const renameBlocked = renameEntryBlock != null || impact?.renamable === false

  return (
    <section className='space-y-3 rounded-lg border p-3'>
      <h3 className='text-sm font-medium'>{t('qy_ug_basics_section')}</h3>

      {!props.row.registered && (
        <p className='text-muted-foreground bg-muted/40 rounded-md border border-dashed p-2 text-xs leading-5'>
          {t('qy_ug_unregistered_hint')}
        </p>
      )}

      <div className='grid gap-3 sm:grid-cols-2'>
        <div className='space-y-1.5'>
          <Label htmlFor='qy-ug-note'>{t('qy_ug_col_note')}</Label>
          <Input
            id='qy-ug-note'
            value={note}
            placeholder={t('qy_ugr_field_note_placeholder')}
            onChange={(event) => setNote(event.target.value)}
          />
        </div>
        <div className='space-y-1.5'>
          <Label htmlFor='qy-ug-topup'>{t('Top-up ratio')}</Label>
          <Input
            id='qy-ug-topup'
            type='number'
            min={0}
            step={0.1}
            value={topup}
            // 空 = 删掉这个键（回落上游兜底），0 = 显式免费充值。**不预填 1**：
            // 预填之后运营随手保存一遍就把一档"没配过"固化成一条显式记录，
            // 此后上游兜底再改也影响不到它，而没有任何人做过这个决定。
            placeholder={t('qy_ug_topup_placeholder', {
              ratio: props.row.topup_ratio_effective,
            })}
            aria-invalid={topupParsed.kind === 'invalid'}
            onChange={(event) => setTopup(event.target.value)}
          />
          <p className='text-muted-foreground text-xs leading-5'>
            {topupParsed.kind === 'invalid'
              ? t('qy_ug_topup_invalid')
              : t('qy_ug_topup_hint_inline')}
          </p>
        </div>
      </div>

      <div className='flex flex-col items-end gap-1'>
        <div className='flex flex-wrap justify-end gap-2'>
          {/*
            改名单独一个按钮、单独一个确认弹窗。

            它要横跨两个库改六处（`users.group`、已售订阅的三列快照、套餐升降级
            分组、`GroupGroupRatio` 的外层键、`TopupGroupRatio` 的键，以及各模块
            声明的范围/授权/费率/划转规则）。任何一处漏掉的表现都是「一批账号挂在
            一个不存在的分组上、按 1.0 兜底扣费」且静默。它绝不能和上面那个存备注
            的按钮共用一次点击。

            禁用时的理由印在按钮下面，**不放 `title`**：禁用的按钮在多数浏览器上
            不派发指针事件，`title` 永远不出现。改名与删除是同一种病 —— 后端
            `renamability` 对 `default` 与 block 残留同样拒绝，而此前这里只是把
            按钮变灰。
          */}
          <Button
            type='button'
            size='sm'
            variant='outline'
            disabled={renameBlocked}
            onClick={() => {
              setNewName(name)
              setRenaming(true)
            }}
          >
            {t('qy_ugr_rename_action')}
          </Button>
          <Button
            type='button'
            size='sm'
            disabled={
              saveMutation.isPending || !dirty || topupParsed.kind === 'invalid'
            }
            onClick={() => saveMutation.mutate()}
          >
            {saveMutation.isPending ? t('Saving...') : t('Save')}
          </Button>
        </div>
        {/*
          影响面回来之后，理由用**后端原话** + 按 `rename_block_code` 给的指路。

          前端那两句只是短状态标签（"系统默认档" / "还没有正式建立"），
          在影响面到达之前顶着；一旦后端说得出话就让位给后端 —— 两份文本各写
          一遍必然漂移，而漂移的方向是界面上留着一句谁也没在维护的旧口径。
        */}
        {impact != null && !impact.renamable ? (
          <div className='w-full max-w-md space-y-1 text-right'>
            <p className='text-destructive text-xs leading-5 font-medium'>
              {t('qy_ug_rename_blocked_title')}
            </p>
            <p className='text-muted-foreground text-xs leading-5 break-words whitespace-pre-wrap'>
              {impact.rename_block_reason}
            </p>
            {impact.rename_block_code != null && (
              <p className='text-muted-foreground text-xs leading-5 break-words'>
                {t('qy_ugr_next_step_prefix')}
                {t(QY_UGR_BLOCK_NEXT_STEP_KEY[impact.rename_block_code])}
              </p>
            )}
          </div>
        ) : (
          <QyUgrActionBlockNote
            noteKey={
              renameEntryBlock == null
                ? null
                : QY_UGR_RENAME_ENTRY_BLOCK_KEY[renameEntryBlock]
            }
            className='max-w-md text-right'
          />
        )}
      </div>

      <QyUgrRenameDialog
        open={renaming}
        onOpenChange={setRenaming}
        row={renameRow}
        newName={newName}
        onNewNameChange={setNewName}
        isSaving={renameMutation.isPending}
        onConfirm={() => renameMutation.mutate()}
      />
    </section>
  )
}
