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
import { Download, Wrench } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'

import { QY_EMPTY_TEXT, formatQyCount, formatQyTs } from '../../ops/format'
import { qyGmDownloadCsv, qyGmOrphansCsv } from '../lib/csv'
import type { QyGmOrphanRow, QyGmOrphansResponse } from '../types'

type QyGmOrphansPanelProps = {
  data: QyGmOrphansResponse
  repairingTokenId: number | null
  onRepairToken: (tokenId: number, tokenName: string) => void
}

/**
 * L0 孤儿基线 —— **常驻，与本次草稿无关**。
 *
 * 它必须在运营按保存之前就摆在这里并标明「与本次改动无关」。理由是数字量级：
 * 本站有 545 条令牌今天就已经是「保存得下、一发请求就 403」的孤儿。第一次
 * 打开影响面预览时弹出这个数字，而没有任何东西说明它是历史欠账，运营的合理
 * 反应是放弃整个功能。先给基线，预览里才敢只说「本次新增的那几条」。
 *
 * ── 为什么只有单条修复、没有批量 ──
 * 「置空分组」是唯一既安全又不替用户猜意图的修复：置空之后使用分组恒等于属主的
 * 用户分组，令牌立刻恢复且该用户的可用范围一点没变。而批量改写数百行不可逆，
 * 「用户当初选这个分组是有原因的」这件事系统不知道。
 */
export function QyGmOrphansPanel(props: QyGmOrphansPanelProps) {
  const { t } = useTranslation()
  const data = props.data

  const sections = [
    {
      category: 'orphan_tokens',
      title: t('qy_group_matrix_orphan_tokens'),
      rows: data.orphan_tokens,
      repairable: true,
    },
    {
      category: 'deprecated_tokens',
      title: t('qy_group_matrix_orphan_deprecated'),
      rows: data.deprecated_tokens,
      repairable: true,
    },
    {
      category: 'auto_group_tokens',
      title: t('qy_group_matrix_orphan_auto_groups'),
      rows: data.auto_group_tokens,
      repairable: false,
    },
    {
      category: 'orphan_users',
      title: t('qy_group_matrix_orphan_users_alert'),
      rows: data.orphan_users,
      repairable: false,
    },
  ]

  return (
    <div className='space-y-4'>
      <div className='flex flex-wrap items-center justify-between gap-2'>
        <div>
          <h2 className='text-sm font-medium'>
            {t('qy_group_matrix_orphan_title')}
          </h2>
          <p className='text-muted-foreground text-xs'>
            {t('qy_group_matrix_orphan_desc', {
              tokens: formatQyCount(data.token_total),
              users: formatQyCount(data.user_total),
            })}
          </p>
        </div>
        <Button
          type='button'
          variant='outline'
          size='sm'
          onClick={() =>
            qyGmDownloadCsv(
              'qy-group-matrix-orphans.csv',
              qyGmOrphansCsv(
                sections.map((section) => ({
                  category: section.category,
                  rows: section.rows,
                }))
              )
            )
          }
        >
          <Download aria-hidden='true' />
          {t('qy_group_matrix_orphan_export')}
        </Button>
      </div>

      {/*
        空分组令牌单独一栏并显式标注「结构性免疫」：上游 `middleware/auth.go` 的
        `if tokenGroup != ""` 让它们整段跳过可选性检查。不说的话，运营看到站里
        两百个空分组令牌会以为收紧之后要炸一大片，从而做出完全错误的决定 ——
        往任一方向误判都会。
      */}
      <p className='text-muted-foreground rounded-md border border-dashed p-2 text-xs'>
        {t('qy_group_matrix_preview_empty_group_tokens_safe', {
          count: data.empty_group_tokens,
        })}
      </p>

      {data.incomplete && (
        <p className='text-warning text-xs'>
          {t('qy_group_matrix_preview_truncated')}
        </p>
      )}

      {/*
        这份基线的判据走 `service.GetUserUsableGroups`，而那个函数已被本功能接管。
        因此某个用户分组切到 enforce 之后，**被那次改动打断的令牌会自动流进这份
        报告**。不说这件事，事故复盘时「这批令牌是本来就坏的还是我切坏的」
        就会得到一个错误的答案。
      */}
      {data.enforced_groups.length > 0 && (
        <p className='text-warning rounded-md border border-dashed p-2 text-xs'>
          {t('qy_group_matrix_orphan_scope_note', {
            groups: data.enforced_groups.join('、'),
          })}
        </p>
      )}

      {sections.map((section) => (
        <section key={section.category} className='rounded-lg border'>
          <h3 className='flex items-center gap-2 border-b px-3 py-2 text-sm font-medium'>
            {section.title}
            <Badge variant='outline' className='tabular-nums'>
              {section.rows.reduce((sum, row) => sum + row.count, 0)}
            </Badge>
          </h3>
          {section.rows.length === 0 ? (
            <p className='text-muted-foreground px-3 py-2 text-xs'>
              {t('qy_group_matrix_preview_block_empty')}
            </p>
          ) : (
            <ul className='divide-y'>
              {section.rows.map((row) => (
                <QyGmOrphanGroupRow
                  key={row.group}
                  row={row}
                  repairable={section.repairable}
                  repairingTokenId={props.repairingTokenId}
                  onRepairToken={props.onRepairToken}
                />
              ))}
            </ul>
          )}
        </section>
      ))}
    </div>
  )
}

function QyGmOrphanGroupRow(props: {
  row: QyGmOrphanRow
  repairable: boolean
  repairingTokenId: number | null
  onRepairToken: (tokenId: number, tokenName: string) => void
}) {
  const { t } = useTranslation()
  const row = props.row

  return (
    <li className='px-3 py-2 text-xs'>
      <div className='flex flex-wrap items-center gap-x-3 gap-y-1'>
        <span className='font-medium'>{row.group}</span>
        <span className='tabular-nums'>
          {t('qy_group_matrix_preview_tokens', { count: row.count })}
        </span>
        <span className='tabular-nums'>
          {t('qy_group_matrix_orphan_enabled', { count: row.enabled_count })}
        </span>
        <span className='tabular-nums'>
          {t('qy_group_matrix_preview_tokens_active', {
            count: row.active_30d,
          })}
        </span>
      </div>
      {/* 后端下发的是**上游原话**（「无权访问 %s 分组」/「分组 %s 已被弃用」），
          前端不二次转译：运营要拿它去和用户报上来的报错逐字对上。 */}
      <p className='text-muted-foreground mt-0.5'>
        {row.reason === '' ? QY_EMPTY_TEXT : row.reason}
      </p>
      {row.samples.length > 0 && (
        <ul className='mt-1 space-y-0.5'>
          {row.samples.map((sample) => (
            <li
              key={sample.token_id}
              className='flex flex-wrap items-center gap-2'
            >
              <span className='text-muted-foreground'>
                {sample.token_name} · {sample.key_masked} · {sample.username}(
                {sample.user_id}) · {formatQyTs(sample.accessed_time)}
              </span>
              {props.repairable && (
                <Button
                  type='button'
                  variant='ghost'
                  size='sm'
                  className='h-6 px-1.5'
                  disabled={props.repairingTokenId != null}
                  onClick={() =>
                    props.onRepairToken(sample.token_id, sample.token_name)
                  }
                >
                  <Wrench aria-hidden='true' />
                  {t('qy_group_matrix_orphan_repair')}
                </Button>
              )}
            </li>
          ))}
        </ul>
      )}
    </li>
  )
}
