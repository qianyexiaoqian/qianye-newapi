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
import { ShieldOff, TriangleAlert } from 'lucide-react'
import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'

import { QyResponsiveDialog } from '../../../components/qy-responsive-dialog'
import { QY_EMPTY_TEXT, formatQyCount, formatQyTs } from '../../ops/format'
import type { QyGmImpactPair, QyGmPreviewResponse } from '../types'

type QyGmPreviewDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  preview: QyGmPreviewResponse | null
  isLoading: boolean
}

/**
 * 影响面预览。
 *
 * ── 这一屏唯一的设计要求 ──
 * **块 A（本次新造出来的断链）与块 B（本来就已经断的）必须分开合计，永不合并。**
 * 本站块 B 有 545 条历史孤儿，块 A 可能只有 3 条。合并之后 545 会把 3 淹掉，
 * 而 545 是历史欠账、3 才是运营这次要负责的东西。这不是排版偏好：合并的直接
 * 后果是运营看到一个巨大的数字，判断「反正一直都这样」，然后照按不误。
 *
 * ── 为什么读侧没有影子拒绝计数器 ──
 * 上游 `middleware/auth.go` 的调用形式是
 * `GetUserUsableGroups(userGroup)[tokenGroup]` —— 挂载点拿得到 userGroup 与
 * 整张 map，**拿不到 tokenGroup**。它记不出「这次查的是哪个 key」，硬做出来的
 * 「本可拒绝数」是假的，而且列表接口（模型广场、价格表）的调用会混进同一个
 * 计数器，一个开着模型广场的管理员就能把它刷成几千。所以证据来源是
 * 静态计数（块 A/B）+ 日志聚合（近 N 天真实请求数），这个不对称是有意的。
 */
export function QyGmPreviewDialog(props: QyGmPreviewDialogProps) {
  const { t } = useTranslation()
  const preview = props.preview

  return (
    <QyResponsiveDialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={t('qy_group_matrix_preview_title')}
      description={t('qy_group_matrix_preview_desc')}
      contentClassName='sm:max-w-4xl'
      footer={
        <Button
          type='button'
          variant='outline'
          onClick={() => props.onOpenChange(false)}
        >
          {t('qy_common_close')}
        </Button>
      }
    >
      {props.isLoading || preview == null ? (
        <p className='text-muted-foreground py-6 text-center text-sm'>
          {t('qy_group_matrix_preview_loading')}
        </p>
      ) : (
        <div className='space-y-4'>
          {/* 超时/超限时禁止切 enforce —— 宁可切不了，也不能在看不见影响面的
              情况下切。这里只是把原因说清楚，闸门在页面上。 */}
          {preview.preview_incomplete && (
            <p className='text-destructive rounded-md border border-dashed p-2 text-xs'>
              {t('qy_group_matrix_preview_truncated')}
            </p>
          )}
          {preview.approximate_user_group && (
            <p className='text-muted-foreground rounded-md border border-dashed p-2 text-xs'>
              {t('qy_group_matrix_preview_approximate_user_group')}
            </p>
          )}

          <QyGmPreviewBlock
            tone='danger'
            title={t('qy_group_matrix_preview_newly_broken')}
            count={preview.newly_broken.length}
          >
            <QyGmPairTable
              pairs={preview.newly_broken}
              logDays={preview.log_days}
            />
          </QyGmPreviewBlock>

          <QyGmPreviewBlock
            tone='muted'
            title={t('qy_group_matrix_preview_already_broken')}
            count={preview.already_broken.length}
          >
            <QyGmPairTable
              pairs={preview.already_broken}
              logDays={preview.log_days}
            />
          </QyGmPreviewBlock>

          <QyGmPreviewBlock
            tone='muted'
            title={t('qy_group_matrix_preview_newly_allowed')}
            count={preview.newly_allowed.length}
          >
            <ul className='space-y-1 text-xs'>
              {preview.newly_allowed.map((pair) => (
                <li
                  key={`${pair.user_group} ${pair.model_group}`}
                  className='flex flex-wrap items-center gap-2'
                >
                  <span className='font-medium'>
                    {pair.user_group} → {pair.model_group}
                  </span>
                  <span className='text-muted-foreground tabular-nums'>
                    {t('qy_group_matrix_preview_users', {
                      count: pair.user_count,
                    })}
                  </span>
                  <Badge variant='outline' className='px-1 py-0 text-[10px]'>
                    {pair.source === 'override'
                      ? t('qy_group_matrix_cell_override')
                      : t('qy_group_matrix_cell_inherit')}
                  </Badge>
                  <span className='tabular-nums'>×{pair.effective_ratio}</span>
                </li>
              ))}
            </ul>
          </QyGmPreviewBlock>

          {/* 这两栏哪怕是 0 也必须留着：空分组令牌的「结构性免疫」不说清楚，
              运营看到站里 200 个空分组令牌会以为要炸；而 auto_groups 被静默
              缩短这件事**没有任何其它信号**。 */}
          <div className='grid gap-2 sm:grid-cols-2'>
            <p className='text-muted-foreground rounded-md border p-2 text-xs'>
              {t('qy_group_matrix_preview_empty_group_tokens_safe', {
                count: preview.empty_group_tokens,
              })}
            </p>
            <p className='text-muted-foreground rounded-md border p-2 text-xs'>
              {t('qy_group_matrix_preview_auto_groups_shrink', {
                count: preview.auto_groups_shrink,
              })}
            </p>
          </div>

          <QyGmPreviewBlock
            tone='muted'
            title={t('qy_group_matrix_conflict_special_rules')}
            count={preview.overridden_rules.length}
          >
            <ul className='space-y-1 text-xs'>
              {preview.overridden_rules.map((rule) => (
                <li key={`${rule.user_group} ${rule.rule}`}>
                  <span className='font-medium'>{rule.user_group}</span>
                  <code className='bg-muted mx-1 rounded px-1'>
                    {rule.rule}
                  </code>
                  {/* 「这条规则从来没生效过」必须点名：站里那条 `-:自己` 就是
                      因为上游在差分算完之后无条件把 userGroup 补回去而永远无效。
                      不说的话运营会以为是本次改动让它失效的。 */}
                  {!rule.was_effective && (
                    <span className='text-warning'>
                      {t('qy_group_matrix_rule_never_effective')}
                    </span>
                  )}
                </li>
              ))}
            </ul>
          </QyGmPreviewBlock>

          <QyGmPreviewBlock
            tone='warning'
            title={t('qy_group_matrix_warn_group_missing')}
            count={preview.orphan_group_names.length}
          >
            <ul className='space-y-1 text-xs'>
              {preview.orphan_group_names.map((item) => (
                <li key={`${item.source} ${item.group}`}>
                  <span className='font-medium'>{item.group}</span>
                  <span className='text-muted-foreground ms-1'>
                    {item.source} · {formatQyCount(item.count)}
                  </span>
                </li>
              ))}
            </ul>
          </QyGmPreviewBlock>

          <QyGmPreviewBlock
            tone='muted'
            title={t('qy_group_matrix_conflict_case_near_miss')}
            count={preview.case_near_miss.length}
          >
            <ul className='space-y-1 text-xs'>
              {preview.case_near_miss.map((pair) => (
                <li key={`${pair.left} ${pair.right}`}>
                  {pair.left}（{pair.left_source}） / {pair.right}（
                  {pair.right_source}）
                </li>
              ))}
            </ul>
          </QyGmPreviewBlock>
        </div>
      )}
    </QyResponsiveDialog>
  )
}

type QyGmPreviewBlockProps = {
  title: string
  count: number
  tone: 'danger' | 'muted' | 'warning'
  children: ReactNode
}

/** 一块统计。**计数永远显示，即使是 0** —— 空块消失会让人以为那一项没算。 */
function QyGmPreviewBlock(props: QyGmPreviewBlockProps) {
  const { t } = useTranslation()

  return (
    <section
      className={cn(
        'rounded-lg border p-3',
        props.tone === 'danger' && props.count > 0 && 'border-destructive/60',
        props.tone === 'warning' && props.count > 0 && 'border-warning/60'
      )}
    >
      <h3 className='flex items-center gap-2 text-sm font-medium'>
        {props.tone === 'danger' && props.count > 0 && (
          <ShieldOff aria-hidden='true' className='text-destructive size-4' />
        )}
        {props.tone === 'warning' && props.count > 0 && (
          <TriangleAlert aria-hidden='true' className='text-warning size-4' />
        )}
        {props.title}
        <Badge variant='outline' className='tabular-nums'>
          {props.count}
        </Badge>
      </h3>
      {props.count === 0 ? (
        <p className='text-muted-foreground mt-1 text-xs'>
          {t('qy_group_matrix_preview_block_empty')}
        </p>
      ) : (
        <div className='mt-2'>{props.children}</div>
      )}
    </section>
  )
}

/** 断链明细。样本只给管理员，且每对上限由后端的 `preview_sample_limit` 控制。 */
function QyGmPairTable(props: { pairs: QyGmImpactPair[]; logDays: number }) {
  const { t } = useTranslation()

  return (
    <div className='space-y-3'>
      {props.pairs.map((pair) => (
        <div key={`${pair.user_group} ${pair.model_group}`} className='text-xs'>
          <div className='flex flex-wrap items-center gap-x-3 gap-y-1'>
            <span className='font-medium'>
              {pair.user_group} → {pair.model_group}
            </span>
            <span className='tabular-nums'>
              {t('qy_group_matrix_preview_users', { count: pair.user_count })}
            </span>
            <span className='tabular-nums'>
              {t('qy_group_matrix_preview_tokens', { count: pair.token_count })}
            </span>
            <span className='tabular-nums'>
              {t('qy_group_matrix_preview_tokens_active', {
                count: pair.token_active_30d,
              })}
            </span>
            {/* `undefined` = 日志库没查成功，**不是 0**。把「没查」画成 0 会让
                运营以为这条边没人在用而直接切 enforce。 */}
            <span
              className={cn(
                'tabular-nums',
                (pair.traffic_requests ?? 0) > 0 &&
                  'text-destructive font-semibold'
              )}
            >
              {pair.traffic_requests == null
                ? QY_EMPTY_TEXT
                : t('qy_group_matrix_preview_traffic_7d', {
                    count: pair.traffic_requests,
                    days: props.logDays,
                  })}
            </span>
          </div>
          {pair.samples.length > 0 && (
            <ul className='text-muted-foreground mt-1 space-y-0.5 ps-3'>
              {pair.samples.map((sample) => (
                <li key={sample.token_id}>
                  {sample.token_name} · {sample.key_masked} · {sample.username}(
                  {sample.user_id}) · {formatQyTs(sample.accessed_time)}
                </li>
              ))}
              {pair.samples_truncated && (
                <li>{t('qy_group_matrix_preview_samples_truncated')}</li>
              )}
            </ul>
          )}
        </div>
      ))}
    </div>
  )
}
