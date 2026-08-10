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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Power, PowerOff, ShieldAlert, Users, X } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ComboboxInput } from '@/components/ui/combobox-input'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { RadioGroup, RadioGroupItem } from '@/components/ui/radio-group'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

import { QyConfirmDialog } from '../../../components/qy-confirm-dialog'
import { QyResponsiveDialog } from '../../../components/qy-responsive-dialog'
import {
  qyGroupOptionLabel,
  qyGroupOptionsQuery,
} from '../../../lib/group-options'
import { qyKeys } from '../../../lib/query-keys'
import { qyOpsErrorMessage } from '../../ops/errors'
import {
  batchSetQyViolationRulesEnabled,
  batchSetQyViolationRulesGroupScope,
} from '../api'
import {
  QY_VIOLATION_BATCH_ITEM_I18N,
  QY_VIOLATION_BATCH_SCOPE_OPS,
  qyBatchEnableChangeCount,
  qyBatchNoteworthyItems,
  qyBatchResultTone,
  qyEnforceRulesPendingEnable,
} from '../lib/rule-batch'
import {
  QY_VIOLATION_GROUP_SCOPE_MODES,
  qyAppendViolationGroupScope,
  qySplitViolationGroupScope,
} from '../lib/rule-form'
import type {
  QyViolationBatchResult,
  QyViolationBatchScopeOp,
  QyViolationGroupScopeMode,
  QyViolationRule,
} from '../types'

type QyRuleBatchBarProps = {
  /** 当前勾选的规则**整行**，不只是 id —— 影响面要读 `mode` 与 `enabled`。 */
  selected: QyViolationRule[]
  /** 批次跑完之后清空勾选。 */
  onDone: () => void
  /** 清除勾选（不跑任何批次）。 */
  onClear: () => void
}

/**
 * 规则列表的多选批量操作条。
 *
 * 项目方原话：「违规规则配置，增加一个多选，可以批量进行作用分组的划分，启动，禁用。」
 *
 * # 三个动作的危险度不一样，所以交互也不一样
 *
 *   批量启用      二次确认。选中里有**当前停用的真实模式规则**时，确认框把这个数字
 *                 单独摆出来并升级成不可逆样式：它们下一秒开始真的扣费、阻断、累计封号
 *   批量禁用      二次确认。停用一条防护规则**没有任何症状** —— 接口 200、业务照常跑、
 *                 只是从此零命中，可以安静地躺几个月
 *   设置作用分组  先在弹窗里把「覆盖 / 追加 / 移除」与「白名单 / 豁免名单」两件事
 *                 选清楚，再走一次带影响面的二次确认
 *
 * # mode（影子 / 真实）**不在**这里
 *
 * 它是本模块唯一决定「要不要真的扣钱封号」的开关，而批量入口看不到 pattern 与
 * 作用域这些做判断必需的上下文。影子模式的唯一用途是「拿这条规则抓到的日志做
 * 误判分析」（项目方原话），转正的前提是逐条看过它的命中分布 —— 那是一个逐条的
 * 判断，批量把它抹平了。改 mode 只能在单条编辑抽屉里。
 */
export function QyRuleBatchBar(props: QyRuleBatchBarProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()

  const [pendingEnable, setPendingEnable] = useState<boolean | null>(null)
  const [scopeOpen, setScopeOpen] = useState(false)
  const [scopeConfirm, setScopeConfirm] = useState(false)
  const [report, setReport] = useState<{
    title: string
    result: QyViolationBatchResult
  } | null>(null)

  const [op, setOp] = useState<QyViolationBatchScopeOp>('append')
  const [scopeMode, setScopeMode] =
    useState<QyViolationGroupScopeMode>('include')
  const [groupText, setGroupText] = useState('')

  // 每次重新打开都从「追加 + 白名单 + 空名单」起步。留着上一次的选择，
  // 意味着一次「覆盖」之后紧接着的那次操作会默默还是覆盖 —— 而这两个动作的
  // 后果完全不同，让它继承是在给下一次误操作铺路。
  useEffect(() => {
    if (!scopeOpen) return
    setOp('append')
    setScopeMode('include')
    setGroupText('')
  }, [scopeOpen])

  const groupQuery = useQuery({
    ...qyGroupOptionsQuery(),
    enabled: scopeOpen,
  })
  const groupOptions = groupQuery.data?.options ?? []
  const groups = qySplitViolationGroupScope(groupText)

  const ids = props.selected.map((rule) => rule.id)
  const pendingEnforce = qyEnforceRulesPendingEnable(props.selected)

  const finish = (title: string) => (result: QyViolationBatchResult) => {
    const tone = qyBatchResultTone(result)
    // 失败与「一条都没改动」都必须把明细摊开：前者要人处理，后者通常意味着
    // 选错了范围。只有真的改动了且零失败才是一句 toast 就够的事。
    if (tone !== 'success') setReport({ title, result })
    void queryClient.invalidateQueries({ queryKey: qyKeys.all })
    props.onDone()

    if (tone === 'error') {
      toast.error(t('qy_vio_batch_toast_failed', { count: result.failed }))
      return
    }
    if (tone === 'warning') {
      toast.warning(t('qy_vio_batch_toast_nothing', { count: result.total }))
      return
    }
    toast.success(
      t('qy_vio_batch_toast_done', {
        done: result.succeeded,
        skipped: result.skipped,
      })
    )
  }

  const enableMutation = useMutation({
    mutationFn: (next: boolean) =>
      batchSetQyViolationRulesEnabled({
        ids,
        enabled: next,
        // 确认框已经把「其中 N 条是真实模式且当前停用」摆出来了，点下确认就是
        // 对它的应答。后端仍然会自己复核一遍：确认之后、写入之前被别人切成真实
        // 的那一条，不该借着这次已经确认过的批次溜进真实执行。
        ack_enforce: true,
      }),
    onSuccess: finish(t('qy_vio_batch_enabled_title')),
    onError: (error) => toast.error(qyOpsErrorMessage(error, t)),
  })

  const scopeMutation = useMutation({
    mutationFn: () =>
      batchSetQyViolationRulesGroupScope({
        ids,
        op,
        groups,
        group_scope_mode: scopeMode,
      }),
    onSuccess: (result) => {
      setScopeConfirm(false)
      setScopeOpen(false)
      finish(t('qy_vio_batch_scope_title'))(result)
    },
    onError: (error) => toast.error(qyOpsErrorMessage(error, t)),
  })

  const changeCount =
    pendingEnable == null
      ? 0
      : qyBatchEnableChangeCount(props.selected, pendingEnable)

  return (
    <>
      <div className='bg-muted/40 flex flex-wrap items-center gap-2 rounded-md border px-3 py-2'>
        <span className='text-sm font-medium'>
          {t('qy_vio_batch_selected', { count: props.selected.length })}
        </span>
        {pendingEnforce.length > 0 && (
          <Badge variant='destructive'>
            {t('qy_vio_batch_enforce_badge', { count: pendingEnforce.length })}
          </Badge>
        )}
        <span className='flex-1' />
        <Button
          type='button'
          variant='outline'
          size='sm'
          disabled={enableMutation.isPending}
          onClick={() => setPendingEnable(true)}
        >
          <Power aria-hidden='true' />
          {t('qy_vio_batch_enable')}
        </Button>
        <Button
          type='button'
          variant='outline'
          size='sm'
          disabled={enableMutation.isPending}
          onClick={() => setPendingEnable(false)}
        >
          <PowerOff aria-hidden='true' />
          {t('qy_vio_batch_disable')}
        </Button>
        <Button
          type='button'
          variant='outline'
          size='sm'
          onClick={() => setScopeOpen(true)}
        >
          <Users aria-hidden='true' />
          {t('qy_vio_batch_scope_open')}
        </Button>
        <Button type='button' variant='ghost' size='sm' onClick={props.onClear}>
          <X aria-hidden='true' />
          {t('qy_vio_batch_clear')}
        </Button>
      </div>

      {/* 启停的二次确认。两个方向都拦：
          启用方向可能一次把一批真实模式规则送上线；
          停用方向完全无症状，误关一条防护规则可以安静地躺几个月。 */}
      <QyConfirmDialog
        open={pendingEnable != null}
        onOpenChange={(open) => {
          if (!open) setPendingEnable(null)
        }}
        title={
          pendingEnable === true
            ? t('qy_vio_batch_enable')
            : t('qy_vio_batch_disable')
        }
        description={
          pendingEnable === true
            ? t('qy_vio_batch_enable_desc', {
                total: props.selected.length,
                count: changeCount,
              })
            : t('qy_vio_batch_disable_desc', {
                total: props.selected.length,
                count: changeCount,
              })
        }
        // 只有「批量启用里确实有真实模式规则会被送上线」才升级成不可逆样式。
        // 把每一次批量都做成必须勾选，只会训练人闭着眼睛勾。
        irreversible={pendingEnable === true && pendingEnforce.length > 0}
        details={
          pendingEnable === true && pendingEnforce.length > 0 ? (
            <Alert variant='destructive'>
              <ShieldAlert />
              <AlertTitle>
                {t('qy_vio_batch_enforce_title', {
                  count: pendingEnforce.length,
                })}
              </AlertTitle>
              <AlertDescription>
                <p>{t('qy_vio_batch_enforce_desc')}</p>
                <ul className='list-disc space-y-0.5 ps-4'>
                  {pendingEnforce.map((rule) => (
                    <li key={rule.id}>{rule.name}</li>
                  ))}
                </ul>
              </AlertDescription>
            </Alert>
          ) : undefined
        }
        confirmText={t('qy_common_confirm')}
        isLoading={enableMutation.isPending}
        onConfirm={() => {
          if (pendingEnable == null) return
          enableMutation.mutate(pendingEnable)
          setPendingEnable(null)
        }}
      />

      {/* 作用分组编辑器。**这一屏唯一的任务是让人不用猜。**
          「覆盖还是追加」与「白名单还是豁免名单」两个问题，各自是一个必选项 +
          一句说清后果的说明，而不是一个叫「批量设置分组」的按钮。 */}
      <QyResponsiveDialog
        open={scopeOpen}
        onOpenChange={setScopeOpen}
        title={t('qy_vio_batch_scope_title')}
        description={t('qy_vio_batch_scope_desc', {
          count: props.selected.length,
        })}
        contentClassName='sm:max-w-xl'
        footer={
          <>
            <Button
              type='button'
              variant='outline'
              onClick={() => setScopeOpen(false)}
            >
              {t('qy_common_cancel')}
            </Button>
            <Button
              type='button'
              // 追加 / 移除必须至少有一个分组名；覆盖允许空 —— 那是「清空作用域，
              // 对全部分组生效」，一个合法且危险的动作，下面单独有红字说明。
              disabled={op !== 'replace' && groups.length === 0}
              onClick={() => setScopeConfirm(true)}
            >
              {t('qy_common_confirm')}
            </Button>
          </>
        }
      >
        <div className='space-y-4 px-1'>
          <div className='space-y-2'>
            <Label>{t('qy_vio_batch_scope_op')}</Label>
            <RadioGroup
              value={op}
              onValueChange={(value) => setOp(value as QyViolationBatchScopeOp)}
              className='space-y-2'
            >
              {QY_VIOLATION_BATCH_SCOPE_OPS.map((item) => (
                <div key={item} className='flex items-start gap-2'>
                  <RadioGroupItem
                    value={item}
                    id={`qy-vio-batch-op-${item}`}
                    className='mt-1'
                  />
                  <Label
                    htmlFor={`qy-vio-batch-op-${item}`}
                    className='cursor-pointer flex-col items-start gap-0.5 font-normal'
                  >
                    <span className='font-medium'>
                      {t(`qy_vio_batch_op_${item}`)}
                    </span>
                    {/* 每一种写法都把「它到底会对已有名单做什么」写在旁边。
                        让人猜「批量设置分组」是覆盖还是追加，一次误判就是一批
                        规则的作用域被整串抹掉，而列表上那几条看起来一个字都没改。 */}
                    <span className='text-muted-foreground text-xs'>
                      {t(`qy_vio_batch_op_${item}_desc`)}
                    </span>
                  </Label>
                </div>
              ))}
            </RadioGroup>
          </div>

          <div className='space-y-2'>
            <Label>{t('qy_vio_field_group_scope_mode')}</Label>
            <Select
              value={scopeMode}
              onValueChange={(value) =>
                setScopeMode(value as QyViolationGroupScopeMode)
              }
            >
              <SelectTrigger className='w-full sm:w-72'>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {QY_VIOLATION_GROUP_SCOPE_MODES.map((mode) => (
                  <SelectItem key={mode} value={mode}>
                    {t(`qy_vio_group_scope_mode_${mode}`)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            {/* 方向是必选而不是可选，理由必须写出来：同一串分组名在两个方向下
                含义完全相反。给一条豁免名单追加 vip，是**多豁免了一个分组**，
                而操作者以为自己多防了一个。 */}
            <p className='text-muted-foreground text-xs'>
              {t('qy_vio_batch_scope_mode_desc')}
            </p>
            {op !== 'replace' && (
              <p className='text-warning text-xs'>
                {t('qy_vio_batch_scope_mode_mismatch_hint')}
              </p>
            )}
          </div>

          <div className='space-y-2'>
            <Label>{t('qy_vio_field_group_scope')}</Label>
            <ComboboxInput
              options={groupOptions.map((option) => ({
                value: option.name,
                label: qyGroupOptionLabel(
                  option,
                  groupQuery.data?.probe_ok === true,
                  t
                ),
              }))}
              value=''
              onValueChange={(picked) =>
                setGroupText(qyAppendViolationGroupScope(groupText, picked))
              }
              emptyText='qy_trg_group_picker_empty'
              placeholder={t('qy_vio_group_scope_pick')}
            />
            {/* 下拉解决「站点有哪些分组」，文本框解决「站点已经不认的历史分组
                仍要能配」—— 后者恰恰是最需要被规则覆盖的那批流量。
                拉不到清单也照样能填。 */}
            <Input
              value={groupText}
              onChange={(event) => setGroupText(event.target.value)}
              placeholder='default,vip'
            />
            {groups.length > 0 && (
              <div className='flex flex-wrap gap-1'>
                {groups.map((name, index) => (
                  <Badge
                    key={`${name}#${index}`}
                    variant='secondary'
                    className='font-normal'
                  >
                    {name}
                  </Badge>
                ))}
              </div>
            )}
            {groupQuery.isError && (
              <p className='text-warning text-xs'>
                {t('qy_vio_group_scope_failed')}
              </p>
            )}
          </div>

          {/* 清空作用域是一次**放宽**：这一批规则从此对全站所有模型分组生效。
              它是一个合法的动作，但绝不能靠「填空框然后点确定」静默发生。 */}
          {op === 'replace' && groups.length === 0 && (
            <Alert variant='destructive'>
              <ShieldAlert />
              <AlertTitle>{t('qy_vio_batch_scope_clear_title')}</AlertTitle>
              <AlertDescription>
                {t('qy_vio_batch_scope_clear_desc', {
                  count: props.selected.length,
                })}
              </AlertDescription>
            </Alert>
          )}
        </div>
      </QyResponsiveDialog>

      {/* 作用分组的二次确认：把「将影响多少条规则」与「具体会发生什么」
          用一句完整的话复述一遍。覆盖与清空是不可逆的 —— 被覆盖掉的旧作用域
          只存在于这次操作的审计快照里。 */}
      <QyConfirmDialog
        open={scopeConfirm}
        onOpenChange={setScopeConfirm}
        title={t('qy_vio_batch_scope_title')}
        description={t(`qy_vio_batch_scope_confirm_${op}`, {
          count: props.selected.length,
          groups: groups.join('、'),
          mode: t(`qy_vio_group_scope_mode_${scopeMode}`),
        })}
        irreversible={op === 'replace'}
        confirmText={t('qy_common_confirm')}
        isLoading={scopeMutation.isPending}
        onConfirm={() => scopeMutation.mutate()}
      />

      {/* 批次报告。失败与「本来就不用动」逐条列出来 —— 一次 20 条的批量里，
          「哪几条没做成、各自为什么」是管理员接下来唯一能依据的东西，
          而 toast 装不下 20 行，也留不住。 */}
      <QyResponsiveDialog
        open={report != null}
        onOpenChange={(open) => {
          if (!open) setReport(null)
        }}
        title={report?.title ?? ''}
        description={
          report == null
            ? ''
            : t('qy_vio_batch_report_summary', {
                total: report.result.total,
                done: report.result.succeeded,
                skipped: report.result.skipped,
                failed: report.result.failed,
              })
        }
        contentClassName='sm:max-w-xl'
        footer={
          <Button type='button' onClick={() => setReport(null)}>
            {t('qy_common_close')}
          </Button>
        }
      >
        <ul className='space-y-2 px-1 text-sm'>
          {report != null &&
            qyBatchNoteworthyItems(report.result).map((item) => (
              <li key={item.id} className='flex flex-col gap-0.5'>
                <span className='flex items-center gap-2'>
                  <Badge
                    variant={
                      item.outcome === 'failed' ? 'destructive' : 'secondary'
                    }
                  >
                    {t(`qy_vio_batch_outcome_${item.outcome}`)}
                  </Badge>
                  <span className='font-medium'>
                    {item.name === '' ? `#${item.id}` : item.name}
                  </span>
                </span>
                <span className='text-muted-foreground text-xs'>
                  {item.code != null &&
                  QY_VIOLATION_BATCH_ITEM_I18N[item.code] != null
                    ? t(QY_VIOLATION_BATCH_ITEM_I18N[item.code])
                    : (item.detail ?? '')}
                </span>
              </li>
            ))}
        </ul>
      </QyResponsiveDialog>
    </>
  )
}
