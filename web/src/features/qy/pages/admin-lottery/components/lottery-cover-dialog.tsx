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
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'

import { QyResponsiveDialog } from '../../../components/qy-responsive-dialog'
import { qyErrorMessage } from '../../../lib/api'
import { qyKeys } from '../../../lib/query-keys'
import { qyAdminLotConfigQuery, setQyLotActivityCover } from '../api'
import { QyLotCoverField, type QyLotCoverValue } from './lottery-cover-field'

/**
 * 换掉一场活动的卡片背景图。
 *
 * ## 为什么这个动作不受活动状态限制
 *
 * 创建向导只在 `draft` 阶段可用，而封面**不进 commit / rules / spec 三个哈希
 * 原像的任何一个** —— 它不参与开奖结果的推导，也不是对用户的任何一项承诺。
 * 于是它是 publish 之后仍然可写的极少数字段之一，而这正是它需要单独一个入口
 * 的理由：一个 404 的外链封面挂在一场正在进行的活动上时，没有这个入口就永远
 * 修不好（活动一旦发布，`PUT /activities/:act_no` 整条路都被 409 挡死）。
 *
 * 弹窗的说明必须把这条边界写出来，否则运营会以为"原来发布之后还能改东西"。
 */
export function QyLotCoverDialog(props: {
  open: boolean
  onOpenChange: (open: boolean) => void
  actNo: string
  coverUrl: string
  coverRef: string
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const configQuery = useQuery({
    ...qyAdminLotConfigQuery(),
    enabled: props.open,
  })

  const [value, setValue] = useState<QyLotCoverValue>({
    cover_ref: props.coverRef,
    cover_url: props.coverUrl,
  })
  // 每次打开都从服务端那份重新起草：弹窗关掉不代表改动被应用，留着上一次的
  // 草稿会让人以为"我明明改过了"。
  useEffect(() => {
    if (!props.open) return
    setValue({ cover_ref: props.coverRef, cover_url: props.coverUrl })
  }, [props.open, props.coverRef, props.coverUrl])

  const save = useMutation({
    mutationFn: () =>
      setQyLotActivityCover(props.actNo, {
        cover_url: value.cover_ref === '' ? value.cover_url.trim() : '',
        cover_ref: value.cover_ref,
      }),
    onSuccess: async () => {
      toast.success(t('qy_lot_cover_saved'))
      props.onOpenChange(false)
      await queryClient.invalidateQueries({ queryKey: qyKeys.all })
    },
    onError: (error) => toast.error(qyErrorMessage(error, t)),
  })

  return (
    <QyResponsiveDialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={t('qy_lot_cover_change')}
      description={t('qy_lot_cover_change_desc')}
      footer={
        <>
          <Button
            type='button'
            variant='outline'
            onClick={() => props.onOpenChange(false)}
          >
            {t('qy_common_cancel')}
          </Button>
          <Button
            type='button'
            disabled={save.isPending}
            onClick={() => save.mutate()}
          >
            {t('qy_common_submit')}
          </Button>
        </>
      }
    >
      <QyLotCoverField
        value={value}
        config={configQuery.data}
        onChange={setValue}
        variant='hero'
      />
    </QyResponsiveDialog>
  )
}
