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
import { useQuery } from '@tanstack/react-query'
import { Copy } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { Spinner } from '@/components/ui/spinner'
import { copyToClipboard } from '@/lib/copy-to-clipboard'

import { qyApiAddressesQuery } from './api'
import { qyApiBaseWithV1, qyNormalizeApiBase } from './api-base-url'
import { qyResolveAddressOptions } from './resolve-options'

/**
 * 密钥页顶上那一条「API 地址」，两个复制按钮。
 *
 * ── 需求 ──
 * 项目方原话：「新增一个 API 地址，方便用户复制：带有 2 个按钮，1，复制，
 * 2，带 V1 复制，方便用户快速选择。」
 * 两个按钮的存在理由是客户端不统一：有的填基址自己拼 `/v1`（Cherry Studio、
 * CC Switch 的 Claude 侧），有的要求你把完整的 `/v1` 填进去（OpenAI 兼容的
 * 一大票、Codex）。让用户自己在输入框里加减 `/v1` 正是最容易出
 * `//v1` / `/v1/v1` 的地方。拼接规则见 {@link qyApiBaseWithV1}。
 *
 * ── 地址从哪来 ──
 * 复用**已有**的 API 地址簿（管理端 `qy/admin/api-address`，后端
 * `qianye/modules/apiaddr`），与「复制链接信息」「CC Switch」读的是同一个
 * react-query 键。不新造第二套线路配置：那样运营改一处、另一处不跟着变，
 * 而两处对不上时没有任何东西会红。
 *
 * 因此线路**可以是多条**（上限 30）。多于一条时这里给一个下拉，选哪条就
 * 复制哪条；只有一条时不出下拉（一个只有一个选项的下拉不提供任何信息）。
 * 一条都没配时 {@link qyResolveAddressOptions} 合成出站点自身那一条 ——
 * 也就是运营什么都不配 = 直接给本站地址。
 *
 * ── 复制失败怎么办 ──
 * `copyToClipboard` 自带 execCommand 回落，但非 HTTPS + 无剪贴板权限时仍然
 * 会全线失败。那时除了红 toast，还把**失败的那一串**填进上面的输入框并整段
 * 选中：用户按 Ctrl+C 就能拿到。只弹一句「复制失败」而不给出可选中的文本，
 * 等于告诉用户"自己想办法"——而带 `/v1` 的那一串本来就不在界面上，他连抄都
 * 没得抄。
 */
export function QyApiAddressCopyBar() {
  const { t } = useTranslation()
  const query = useQuery(qyApiAddressesQuery())
  const options = qyResolveAddressOptions(query.data, t('qy_aa_site_default'))

  // 清单还在路上、或者取数失败(扩展库 503)时，qyResolveAddressOptions 拿到的是
  // undefined，它与「运营一条都没配」走同一条分支 —— 于是这一条会把**站点自身
  // 的地址当成结论摆出来**：输入框有值、没有线路下拉、两个按钮都可点，
  // 而用户没有任何办法看出这是个占位值。
  //
  // 三种完全不同的状态(加载中 / 一条都没配 / 接口不可用)原先渲染得一模一样，
  // 其中 503 那一支还不是竞态而是**稳态**：retry:false + staleTime 5min，
  // 扩展库一挂，配了多条线路的站点上所有用户的复制条都无声退回主域，
  // 而页面上没有任何一处会红。
  //
  // 同目录的选择窗(picker-dialog)早就把这两态说出来了，其注释原文是
  // 「取数失败必须把"这不是完整清单"说出来，否则用户会以为运营一条都没配」——
  // 同一个 query、同一个功能，两处口径不能相反。
  const pending = query.isLoading
  const unavailable = query.isError

  // 用户选中的线路 id。null = 还没选过，用运营排在第一位的那条。
  //
  // 合成出来的站点条目 id 是 **0**，而 0 是个 falsy 值：这里必须用
  // `find(id === picked)` + null 判空，不能写 `picked || options[0].id`
  // ——后者会让"选中站点地址"这件事永远退回第一条。
  const [pickedId, setPickedId] = useState<number | null>(null)
  const active =
    options.find((option) => option.id === pickedId) ?? options[0] ?? null

  const base = qyNormalizeApiBase(active?.url ?? '')
  const withV1 = qyApiBaseWithV1(active?.url ?? '')

  // 复制失败时顶掉输入框里的展示值，好让用户手动选中那一串。
  const [manual, setManual] = useState<string | null>(null)
  const inputRef = useRef<HTMLInputElement>(null)
  useEffect(() => {
    if (manual == null) return
    inputRef.current?.select()
  }, [manual])

  const copy = async (text: string) => {
    if (text === '') return
    const ok = await copyToClipboard(text)
    if (ok) {
      setManual(null)
      toast.success(t('qy_aa_copied', { url: text }))
      return
    }
    setManual(text)
    toast.error(t('qy_aa_copy_failed'))
  }

  return (
    <div className='bg-card flex flex-wrap items-center gap-2 rounded-lg border px-3 py-2'>
      <span className='text-muted-foreground shrink-0 text-xs font-medium'>
        {t('qy_aa_bar_label')}
      </span>

      {options.length > 1 && (
        <NativeSelect
          size='sm'
          className='shrink-0'
          aria-label={t('qy_aa_bar_route')}
          value={String(active?.id ?? '')}
          onChange={(event) => {
            setManual(null)
            setPickedId(Number(event.target.value))
          }}
        >
          {options.map((option) => (
            <NativeSelectOption key={option.id} value={String(option.id)}>
              {option.name}
            </NativeSelectOption>
          ))}
        </NativeSelect>
      )}

      <Input
        ref={inputRef}
        readOnly
        aria-label={t('qy_aa_bar_label')}
        value={manual ?? base}
        onFocus={(event) => event.currentTarget.select()}
        className='h-7 min-w-[12rem] flex-1 font-mono text-xs'
      />

      <div className='flex shrink-0 items-center gap-2'>
        <Button
          size='sm'
          variant='outline'
          disabled={pending || base === ''}
          onClick={() => void copy(base)}
        >
          <Copy />
          {t('qy_aa_bar_copy')}
        </Button>
        <Button
          size='sm'
          variant='outline'
          disabled={pending || withV1 === ''}
          onClick={() => void copy(withV1)}
        >
          <Copy />
          {t('qy_aa_bar_copy_v1')}
        </Button>
      </div>

      {/* 加载中：把「这还不是结论」说出来，并且此刻不许复制 —— 那一刻交出去的
          是站点主域，而运营配备用域/加速线路恰恰是想让用户拿到那一条。 */}
      {pending && (
        <span className='text-muted-foreground flex w-full items-center gap-2 text-xs'>
          <Spinner className='size-3' />
          {t('qy_aa_bar_loading')}
        </span>
      )}
      {/* 取数失败:仍然给站点地址(它是能用的网关基址),但必须说明这不是完整清单。
          静默退回主域是「错在运行时、界面上不变红」的那一类。 */}
      {unavailable && (
        <span className='text-destructive w-full text-xs'>
          {t('qy_aa_bar_unavailable')}
        </span>
      )}
    </div>
  )
}
