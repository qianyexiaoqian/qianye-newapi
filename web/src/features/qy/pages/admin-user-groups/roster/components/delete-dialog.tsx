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
import { AlertTriangle } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

import { QyResponsiveDialog } from '../../../../components/qy-responsive-dialog'
import {
  QY_UGR_BLOCK_NEXT_STEP_KEY,
  QY_UGR_DELETE_GATE_KEY,
  qyUgrDeleteBlock,
  qyUgrGroupChanges,
} from '../lib/gates'
import type { QyUgrImpact } from '../types'

/**
 * 删除一个用户分组的确认弹窗 —— 需求 1 的本体。
 *
 * ── 项目方原话 ──
 *
 * 「用户分组删除时,若存在用户,弹出选择窗口,这批用户要迁移到哪个用户分组?」
 *
 * ── 为什么这里不是一个「确定要删除吗?」 ──
 *
 * 删掉一个用户分组同时是一次**批量改价**和一次**批量权限变更**,而这两件事
 * 在「删除分组」这句话里一个字都没提到。迁过去之后:
 *
 *   · 少掉的每一个模型分组 = 那批人手上对应令牌的当场 403;
 *   · 多出来的每一格倍率差 = 从下一秒开始的账单变化。
 *
 * 所以这个弹窗把四件事摆在同一屏上,而不是分两次请求、分两个页面:
 *
 *   人数 / 令牌数    项目方点名的两个数字。它们同时是后端的一致性闸门 ——
 *                    回传的人数与此刻不符会返回 409,而不是默默按新数字删。
 *   迁移目标         有人时**必填**。直接删会让这批账号的 users.group 指向一个
 *                    不存在的分组,而分组倍率对孤儿是 fail-open 返回 1.0:
 *                    他们会静默按原价扣费,零告警。
 *   迁移后的差异     选中目标之后现算(服务端与鉴权、计费同源),不选就没有。
 *   连带清理清单     各模块自己声明的表与处置。0 行的也列出来 ——
 *                    「这里本来就没有」与「这里没查」必须是两件事。
 */
export function QyUgrDeleteDialog(props: {
  open: boolean
  onOpenChange: (open: boolean) => void
  impact: QyUgrImpact | null
  /** 第一次拉取、屏幕上还什么都没有。整块显示 `Loading...`。 */
  isLoading: boolean
  /**
   * 换了迁移目标、新的一份还在路上，而屏幕上仍是**上一个目标**的差异。
   *
   * 它必须与 `isLoading` 分开：整块塌成一行 `Loading...` 会让"在两个候选目标
   * 之间比较三堆差异"这件事变得不可能，而那是这个弹窗存在的唯一理由。
   * 代价是屏幕上这一份可能过期，所以刷新期间既要标出来，也要禁掉删除键。
   */
  isRefreshing: boolean
  isDeleting: boolean
  target: string
  onTargetChange: (target: string) => void
  ack: boolean
  onAckChange: (ack: boolean) => void
  onConfirm: () => void
}) {
  const { t } = useTranslation()
  const impact = props.impact
  const block = qyUgrDeleteBlock(impact, props.target, props.ack)
  const changes = qyUgrGroupChanges(impact?.diff)

  /**
   * 删除键此刻为什么按不动 —— 一句话，常驻在按钮上方。
   *
   * 与 footer 里那串 `disabled` 表达式**逐项对应**：闸门四条走
   * {@link QY_UGR_DELETE_GATE_KEY}，"差异正在按新目标重算"是这里独有的一条
   * （它不是闸门，是屏幕上这一份可能过期）。`isDeleting` 不给文案：按钮自己
   * 已经写着「保存中…」。
   *
   * 这一段**画在 `isLoading` 分支之外**。画在里面时 `loading` 那一条永远轮不到
   * 渲染（`impact == null` 的那一支把整个正文换成了「Loading...」），于是穷尽
   * `Record` 里那一条成了没有人读得到的死文案 —— 穷尽挡得住"漏写文案"，
   * 挡不住"写了但那一支不可达"。两句话的主语也不同：「Loading...」说的是这块
   * 面板，这一句说的是**那个灰着的按钮**。
   */
  let gateKey: string | null = null
  if (block != null) gateKey = QY_UGR_DELETE_GATE_KEY[block]
  else if (props.isRefreshing) gateKey = 'qy_ugr_gate_refreshing'

  return (
    <QyResponsiveDialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={t('qy_ugr_delete_title', { name: impact?.name ?? '' })}
      description={t('qy_ugr_delete_desc')}
      contentClassName='sm:max-w-2xl'
      footer={
        <>
          <Button variant='outline' onClick={() => props.onOpenChange(false)}>
            {t('Cancel')}
          </Button>
          <Button
            variant='destructive'
            disabled={
              props.isLoading ||
              props.isRefreshing ||
              props.isDeleting ||
              block != null
            }
            onClick={props.onConfirm}
          >
            {props.isDeleting ? t('Saving...') : t('Delete')}
          </Button>
        </>
      }
    >
      <div className='space-y-4 text-sm'>
        {props.isLoading || impact == null ? (
          <p className='text-muted-foreground text-sm'>{t('Loading...')}</p>
        ) : (
          <div className='space-y-4 text-sm'>
            {/*
            ── 拒绝的两句话必须一起出现：为什么不行 + 那我该干什么 ──────────

            后端的 `block_reason` 只回答前半句。运营读完「以下套餐的升级/降级
            分组指向它」之后，还要自己推断该去哪一页改哪个字段 —— 项目方为此
            花掉的时间正是这次报上来的缺陷。指路那半句与界面强相关，只能由前端
            按 `block_code` 给，**不能按 `block_reason` 的中文子串去猜分支**：
            任何一次文案润色都会让指路静默消失。
          */}
            {!impact.deletable && (
              <Alert variant='destructive'>
                <AlertTriangle className='h-4 w-4' />
                <AlertTitle>{t('qy_ugr_delete_blocked_title')}</AlertTitle>
                <AlertDescription className='space-y-1.5 whitespace-pre-wrap'>
                  <span className='block'>{impact.block_reason}</span>
                  {impact.block_code != null && (
                    <span className='block font-medium'>
                      {t('qy_ugr_next_step_prefix')}
                      {t(QY_UGR_BLOCK_NEXT_STEP_KEY[impact.block_code])}
                    </span>
                  )}
                </AlertDescription>
              </Alert>
            )}

            {/*
            改名也被同一道 block 残留闸门挡着时，在这里一并说出来。

            不说的话，运营的下一步几乎必然是「删不掉就改个名」—— 而那是同一次
            事故换了个入口，并且他要等到提交之后才会知道这条路也走不通。
          */}
            {!impact.renamable && impact.rename_block_code != null && (
              <Alert>
                <AlertTriangle className='h-4 w-4' />
                <AlertTitle>{t('qy_ugr_rename_blocked_title')}</AlertTitle>
                <AlertDescription className='space-y-1.5 whitespace-pre-wrap'>
                  <span className='block'>{impact.rename_block_reason}</span>
                  <span className='block font-medium'>
                    {t('qy_ugr_next_step_prefix')}
                    {t(QY_UGR_BLOCK_NEXT_STEP_KEY[impact.rename_block_code])}
                  </span>
                </AlertDescription>
              </Alert>
            )}

            <dl className='grid grid-cols-2 gap-x-4 gap-y-2'>
              <QyUgrFact
                label={t('qy_ugr_impact_users')}
                value={String(impact.users)}
              />
              <QyUgrFact
                label={t('qy_ugr_impact_tokens')}
                value={t('qy_ugr_impact_tokens_value', {
                  tokens: impact.tokens,
                  empty: impact.empty_group_tokens,
                })}
              />
              <QyUgrFact
                label={t('qy_ugr_impact_subscriptions')}
                value={String(impact.subscriptions)}
              />
              <QyUgrFact
                label={t('qy_ugr_impact_plans')}
                value={
                  impact.blocking_plans.length === 0
                    ? t('qy_ugr_impact_none')
                    : impact.blocking_plans.join('、')
                }
              />
            </dl>

            {/*
            迁移目标。**有人时必填** —— 这一段就是项目方点名的那个「选择窗口」。

            下拉里带上每一档的在册人数与可用模型分组数:迁进一个 usable=0 的
            分组等于让这批人的全部令牌当场停摆,而光看名字看不出来这件事。

            ── 服务端已经拒绝时**不画它** ──
            项目方原话：「我选择了其他分组仍然无法删除」。选目标在那一刻是一次
            纯粹的白工：`deletability` 已经给出结论，选哪一档都不会让删除键亮
            起来。留着它就是在邀请运营去做那件白工，然后自己去猜为什么没用。
          */}
            {impact.deletable && impact.users > 0 && (
              <div className='space-y-1.5'>
                <Label htmlFor='qy-ugr-target'>
                  {t('qy_ugr_migrate_to_label', { users: impact.users })}
                </Label>
                <Select
                  value={props.target === '' ? undefined : props.target}
                  onValueChange={(value) => props.onTargetChange(value ?? '')}
                >
                  <SelectTrigger id='qy-ugr-target'>
                    <SelectValue placeholder={t('qy_ugr_migrate_to_empty')} />
                  </SelectTrigger>
                  <SelectContent>
                    {impact.targets.map((target) => (
                      <SelectItem key={target.name} value={target.name}>
                        {t('qy_ugr_migrate_target_option', {
                          name: target.name,
                          users: target.users,
                          usable: target.usable_groups,
                        })}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                <p className='text-muted-foreground text-xs leading-5'>
                  {t('qy_ugr_migrate_to_hint')}
                </p>
              </div>
            )}

            {/*
            迁移前后的可用清单与倍率差。

            只显示「把 N 个人从 A 迁到 B」而不显示这一段,运营就是在盲按。
            三堆分开列,因为后果的方向完全不同(见 `qyUgrGroupChanges`)。
          */}
            {impact.diff != null && (
              <div className='space-y-1.5'>
                <p className='font-medium'>
                  {t('qy_ugr_diff_title', {
                    from: impact.diff.from,
                    to: impact.diff.to,
                  })}
                  {/*
                  换目标之后这一整段仍然是**上一个目标**的差异，直到新的一份
                  回来为止。不标出来的话，运营会拿着 A 的「失去/获得/改价」
                  按下删到 B 的按钮。删除键在同一条件下是禁用的。
                */}
                  {props.isRefreshing && (
                    <span className='text-muted-foreground ml-2 text-xs font-normal'>
                      {t('qy_ugr_diff_stale')}
                    </span>
                  )}
                </p>
                {impact.diff.loses_everything && (
                  <Alert variant='destructive'>
                    <AlertTriangle className='h-4 w-4' />
                    <AlertDescription>
                      {t('qy_ugr_diff_loses_everything', {
                        target: impact.diff.to,
                        users: impact.users,
                      })}
                    </AlertDescription>
                  </Alert>
                )}
                {impact.diff.changes.length === 0 ? (
                  <p className='text-muted-foreground'>
                    {t('qy_ugr_diff_unchanged', {
                      count: impact.diff.unchanged,
                    })}
                  </p>
                ) : (
                  <div className='space-y-1.5'>
                    <QyUgrChangeRow
                      label={t('qy_ugr_diff_removed')}
                      tone='destructive'
                      names={changes.removed.map(
                        (change) => change.model_group
                      )}
                    />
                    <QyUgrChangeRow
                      label={t('qy_ugr_diff_added')}
                      tone='outline'
                      names={changes.added.map((change) => change.model_group)}
                    />
                    <QyUgrChangeRow
                      label={t('qy_ugr_diff_repriced')}
                      tone='secondary'
                      names={changes.repriced.map((change) =>
                        t('qy_ugr_diff_repriced_item', {
                          group: change.model_group,
                          from: change.from_ratio,
                          to: change.to_ratio,
                        })
                      )}
                    />
                  </div>
                )}
              </div>
            )}

            <div className='space-y-1.5'>
              <p className='font-medium'>{t('qy_ugr_impact_residues')}</p>
              <ul className='space-y-1'>
                {impact.residues.map((residue) => (
                  <li
                    key={`${residue.module}/${residue.table}/${residue.label}`}
                    className='flex flex-wrap items-center justify-between gap-2 rounded-md border px-2 py-1.5'
                  >
                    <span className='min-w-0'>
                      {residue.label}
                      <code className='bg-muted mx-1 rounded px-1 text-xs'>
                        {residue.table}
                      </code>
                    </span>
                    <span className='tabular-nums'>
                      {t('qy_ugr_impact_residue_rows', {
                        rows: residue.rows,
                        disposition: t(
                          `qy_ugr_disposition_${residue.disposition}`
                        ),
                      })}
                    </span>
                  </li>
                ))}
              </ul>
            </div>

            {/*
            只在真的会发生时渲染这个勾选框:常驻一个未勾选的红框会让
            「这次迁移毫无风险」与「这 700 个人的令牌下一秒全部 403」长得一样。
          */}
            {impact.diff?.loses_everything === true && (
              <div className='border-destructive/40 flex items-start gap-2 rounded-md border p-2'>
                <Checkbox
                  id='qy-ugr-ack'
                  checked={props.ack}
                  onCheckedChange={(value) => props.onAckChange(value === true)}
                />
                <Label
                  htmlFor='qy-ugr-ack'
                  className='text-sm leading-5 font-normal'
                >
                  {t('qy_ugr_ack_loses_everything', {
                    target: impact.diff.to,
                    users: impact.users,
                  })}
                </Label>
              </div>
            )}
          </div>
        )}

        {/*
          ── 删除键为什么是灰的：四种状态各有一句，一条都不许沉默 ─────────

          此前这里只画了 `needs_target` 一条。剩下三条（服务端拒绝、还没勾
          强制覆盖、差异正在按新目标重算）都只表现为一个按不动的按钮，而
          「按了没反应、屏幕上没有一个字」正是项目方报上来的那一幕。

          `blocked` 那条的正文已经在最上面的红框里给全了（原因 + 指路），
          这里只说一句"上面那段说明了原因"，避免同一段话在一屏里出现两遍。

          它是 `isLoading` 分支的**兄弟**而不是子节点：放在里面时 `loading`
          那一条永远画不出来。
        */}
        {gateKey != null && (
          <p
            data-slot='qy-ugr-delete-gate'
            className='text-destructive text-xs leading-5'
          >
            {t(gateKey)}
          </p>
        )}
      </div>
    </QyResponsiveDialog>
  )
}

function QyUgrFact(props: { label: string; value: string }) {
  return (
    <div className='min-w-0'>
      <dt className='text-muted-foreground text-xs'>{props.label}</dt>
      <dd className='break-words'>{props.value}</dd>
    </div>
  )
}

function QyUgrChangeRow(props: {
  label: string
  tone: 'destructive' | 'outline' | 'secondary'
  names: string[]
}) {
  if (props.names.length === 0) return null
  return (
    <div className='flex flex-wrap items-center gap-1.5'>
      <span className='text-muted-foreground text-xs'>{props.label}</span>
      {props.names.map((name) => (
        <Badge key={name} variant={props.tone} className='max-w-64 truncate'>
          {name}
        </Badge>
      ))}
    </div>
  )
}
