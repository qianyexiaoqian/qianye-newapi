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
import { Link } from '@tanstack/react-router'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'

import { qyKeys } from '../../../lib/query-keys'
import { qyWindowIsUnlimited } from '../../../lib/violation-thresholds'
import { qyOpsErrorMessage } from '../../ops/errors'
import { deleteQyViolationBanPolicy, listQyViolationBanPolicies } from '../api'
import type { QyViolationBanPolicy } from '../types'
import { QyViolationBanPolicyDialog } from './ban-policy-dialog'

/**
 * 按用户分组的违规处置策略档。
 *
 * # 为什么它住在「封禁」这一页而不是规则页
 *
 * 阈值决定**谁被封**，规则决定**什么算违规**。把阈值摆在封禁列表旁边，管理员
 * 调完阈值抬眼就能看到这条线上现在有谁 —— 而放在规则页的话，它会淹没在一堆
 * 模式串里，而那些模式串回答的是完全另一个问题。
 *
 * # 界面上必须说清楚的三件事
 *
 *  1. **兜底档不可删**。它是没有专属档的分组的唯一落点。删除按钮对它直接不渲染，
 *     而不是渲染一个点了会报错的按钮。
 *  2. **停用一档 = 回落兜底，不是免罚**。这是最容易被误解的一处:
 *     「我把 vip 这档关了」在直觉上像是「vip 不再被封」，实际是「vip 按兜底档判」。
 *  3. **限制与封号是同一个账号状态**。主库只有一个非删除停用态,两档的差别
 *     只有「要不要把用户踢出控制台」。不写出来的话，界面看起来像有两种账号状态。
 *  4. **这一页的阈值不是唯一那条线**。违规类型页上每一类还有自己的线，两条是
 *     OR —— 任一越过即触发（后端 `anyReached` 是这个口径的唯一出口）。这句话
 *     必须在**两页**都出现：只写在类型页上的话，来这里把账号总量线调到 10 的
 *     管理员会以为"十次才封"，而某一类 3 次的线会先把人收走。它与类型页共用
 *     同一个 i18n 键，改口径只需改一处。
 */
export function QyViolationBanPoliciesTab() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [editing, setEditing] = useState<QyViolationBanPolicy | null>(null)
  const [creating, setCreating] = useState(false)

  const query = useQuery({
    queryKey: qyKeys.adminViolationBanPolicies(),
    queryFn: listQyViolationBanPolicies,
  })

  const removeMutation = useMutation({
    mutationFn: (id: number) => deleteQyViolationBanPolicy(id),
    onSuccess: async () => {
      toast.success(t('qy_vio_policy_deleted'))
      await queryClient.invalidateQueries({
        queryKey: qyKeys.adminViolationBanPolicies(),
      })
    },
    onError: (error) => toast.error(qyOpsErrorMessage(error, t)),
  })

  const items = query.data?.items ?? []
  const fallbackFromDb = query.data?.fallback_from_db ?? false

  return (
    <div className='space-y-3'>
      <div className='text-muted-foreground space-y-1 rounded-lg border p-3 text-xs'>
        <p className='text-foreground font-medium'>
          {t('qy_vio_policy_intro_title')}
        </p>
        <p>{t('qy_vio_policy_intro_fallback')}</p>
        <p>{t('qy_vio_policy_intro_disabled')}</p>
        <p>{t('qy_vio_policy_intro_actions')}</p>
        {/* 「到底几次封号」的另一半。这一页只配得到账号总量线，而真正生效的是
            两条线的 OR —— 少了这句，这一页上的数字看起来就是唯一那条线。

            文案**不能**与违规类型页共用一个键：那一句里的两个「本页」在这一页上
            都是假的 ——「②「单类型线」就是本页每一类自己的阈值」（这一页没有任何
            按类型的阈值）、「本页只决定「几次」」（这一页恰恰就是选处置动作的
            地方）。一个照着它找按类型阈值的管理员会找不到，同时会以为动作不在
            这里配。两条线的**口径**是共享的，两页各自的**位置**不是。 */}
        <p>{t('qy_vio_policy_two_lines_note')}</p>
        <Button
          variant='outline'
          size='sm'
          className='mt-1'
          render={<Link to='/qy/admin/violation-categories' />}
        >
          {t('qy_nav_a_violation_categories')}
        </Button>
      </div>

      {/* 兜底跑在 YAML 上是一个必须被看见的状态：此时在这张表里改任何东西
          都不会影响没配分组的用户。不摆出来的话，这个落差只有读源码才发现。 */}
      {!query.isPending && !fallbackFromDb && (
        <div className='rounded-lg border border-amber-500/40 bg-amber-500/10 p-3 text-xs'>
          {t('qy_vio_policy_fallback_from_yaml')}
        </div>
      )}

      <div className='flex justify-end'>
        <Button size='sm' onClick={() => setCreating(true)}>
          {t('qy_vio_policy_add')}
        </Button>
      </div>

      <div className='overflow-x-auto'>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t('qy_vio_policy_col_group')}</TableHead>
              <TableHead>{t('qy_vio_policy_col_window')}</TableHead>
              <TableHead>{t('qy_vio_policy_col_threshold')}</TableHead>
              <TableHead>{t('qy_vio_policy_col_action')}</TableHead>
              <TableHead>{t('qy_common_remark')}</TableHead>
              <TableHead className='text-right'>
                {t('qy_common_actions')}
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {items.map((policy) => (
              <TableRow key={policy.id}>
                <TableCell>
                  <div className='flex items-center gap-2'>
                    <span className='font-medium'>
                      {policy.is_default
                        ? t('qy_vio_policy_default_row')
                        : policy.user_group}
                    </span>
                    {policy.is_default && (
                      <Badge variant='secondary'>
                        {t('qy_vio_policy_badge_undeletable')}
                      </Badge>
                    )}
                    {!policy.is_default && !policy.enabled && (
                      <Badge variant='outline'>
                        {t('qy_vio_policy_badge_disabled')}
                      </Badge>
                    )}
                  </div>
                </TableCell>
                <TableCell>
                  {/* 哨兵直接渲染会变成「-1 小时」。这一列是管理员判断
                      "这一档到底怎么算次数"的唯一入口,印错口径等于印错判据。 */}
                  {qyWindowIsUnlimited(policy.window_hours)
                    ? t('qy_vio_window_unlimited')
                    : t('qy_vio_policy_hours', { hours: policy.window_hours })}
                </TableCell>
                <TableCell>
                  {policy.threshold > 0
                    ? t('qy_vio_policy_times', { times: policy.threshold })
                    : t('qy_vio_policy_threshold_off')}
                </TableCell>
                <TableCell>
                  {t(`qy_vio_policy_action_${policy.action}`)}
                </TableCell>
                <TableCell className='text-muted-foreground max-w-[24rem] text-xs'>
                  {policy.remark}
                </TableCell>
                <TableCell className='space-x-2 text-right'>
                  <Button
                    size='sm'
                    variant='outline'
                    onClick={() => setEditing(policy)}
                  >
                    {t('qy_common_edit')}
                  </Button>
                  {/* 兜底档不渲染删除按钮 —— 渲染一个点了必然报错的按钮
                      只会让人以为是系统坏了，而不是这件事本来就不该做。 */}
                  {!policy.is_default && (
                    <Button
                      size='sm'
                      variant='outline'
                      disabled={removeMutation.isPending}
                      onClick={() => removeMutation.mutate(policy.id)}
                    >
                      {t('qy_common_delete')}
                    </Button>
                  )}
                </TableCell>
              </TableRow>
            ))}
            {items.length === 0 && (
              <TableRow>
                <TableCell
                  colSpan={6}
                  className='text-muted-foreground text-sm'
                >
                  {query.isPending
                    ? t('qy_vio_policy_loading')
                    : t('qy_vio_policy_empty')}
                </TableCell>
              </TableRow>
            )}
          </TableBody>
        </Table>
      </div>

      <QyViolationBanPolicyDialog
        open={creating || editing != null}
        policy={editing}
        onClose={() => {
          setCreating(false)
          setEditing(null)
        }}
      />
    </div>
  )
}
