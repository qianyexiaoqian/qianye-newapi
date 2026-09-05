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
import { useIsMobile } from '@/hooks/use-mobile'
import { getBgColorClass } from '@/lib/colors'
import { copyToClipboard } from '@/lib/copy-to-clipboard'

import { qyApiAddressesQuery, type QyApiAddressOption } from './api'
import { qyApiBaseWithV1, qyNormalizeApiBase } from './api-base-url'
import { qyResolveAddressOptions } from './resolve-options'

/**
 * 密钥页顶上那一条「API 地址」。
 *
 * ── 需求 ──
 * 项目方原话：「新增一个 API 地址，方便用户复制：带有 2 个按钮，1，复制，
 * 2，带 V1 复制，方便用户快速选择。」后一轮又要求：「电脑端改成卡片式，
 * 一条线路一张卡；保留一个展示上限；手机端保持当前这样。」
 * 两个按钮的存在理由是客户端不统一：有的填基址自己拼 `/v1`（Cherry Studio、
 * CC Switch 的 Claude 侧），有的要求你把完整的 `/v1` 填进去（OpenAI 兼容的
 * 一大票、Codex）。让用户自己在输入框里加减 `/v1` 正是最容易出
 * `//v1` / `/v1/v1` 的地方。拼接规则见 {@link qyApiBaseWithV1}。
 *
 * ── 两种形态 ──
 * 桌面（≥768px）：一条线路一张卡（复制 / 带 V1 复制 / 圆点 / 名称 / 基址），
 * 最多铺 {@link QY_AA_BAR_MAX_CARDS} 张 —— 上限挡的是"运营配了几十条,
 * 密钥列表被推到两屏之外"；被截掉的条数会写出来,完整清单在「复制链接信息」
 * 的选择窗里仍然全量可选。移动端：下拉 + 输入框 + 两个按钮（原形态,小屏塞
 * 不下一排卡）。用 useIsMobile 二选一渲染而不是 CSS 显隐:两份同文案的按钮
 * 同时在 DOM 里,读屏与测试都分不清该按哪一个。
 *
 * ── 地址从哪来 ──
 * 复用**已有**的 API 地址簿（管理端 `qy/admin/api-address`，后端
 * `qianye/modules/apiaddr`），与「复制链接信息」「CC Switch」读的是同一个
 * react-query 键（surface=picker，服务端已按用户分组与展示位置过滤）。
 * 一条都没配时 {@link qyResolveAddressOptions} 合成出站点自身那一条 ——
 * 也就是运营什么都不配 = 直接给本站地址。
 *
 * ── 复制失败怎么办 ──
 * `copyToClipboard` 自带 execCommand 回落，但非 HTTPS + 无剪贴板权限时仍然
 * 会全线失败。那时除了红 toast，还把**失败的那一串**填进一个只读输入框并整段
 * 选中（桌面形态平时没有输入框,失败时现出这一条）：用户按 Ctrl+C 就能拿到。
 * 只弹一句「复制失败」而不给出可选中的文本,等于告诉用户"自己想办法"。
 */

/**
 * 桌面卡片的展示上限。表的总量上限是 100(后端 maxAddresses),全铺出来会把
 * 密钥列表推到两屏之外;运营排的顺序就是优先级,截掉的尾部在「复制链接信息」
 * 的选择窗里仍然全量可选。
 */
export const QY_AA_BAR_MAX_CARDS = 6

export function QyApiAddressCopyBar() {
  const { t } = useTranslation()
  const isMobile = useIsMobile()
  const query = useQuery(qyApiAddressesQuery('picker'))
  const options = qyResolveAddressOptions(query.data, t('qy_aa_site_default'))

  // 清单还在路上、或者取数失败(扩展库 503)时，qyResolveAddressOptions 拿到的是
  // undefined，它与「运营一条都没配」走同一条分支 —— 于是会把**站点自身的地址
  // 当成结论摆出来**。三种状态(加载中 / 一条都没配 / 接口不可用)必须分开说，
  // 其中 503 那一支还不是竞态而是**稳态**(retry:false + staleTime 5min)。
  // 同目录的选择窗(picker-dialog)同一套口径。
  const pending = query.isLoading
  const unavailable = query.isError

  // 用户选中的线路 id(移动端下拉)。null = 还没选过，用运营排在第一位的那条。
  //
  // 合成出来的站点条目 id 是 **0**，而 0 是个 falsy 值：这里必须用
  // `find(id === picked)` + null 判空，不能写 `picked || options[0].id`
  // ——后者会让"选中站点地址"这件事永远退回第一条。
  const [pickedId, setPickedId] = useState<number | null>(null)
  const active =
    options.find((option) => option.id === pickedId) ?? options[0] ?? null

  const base = qyNormalizeApiBase(active?.url ?? '')
  const withV1 = qyApiBaseWithV1(active?.url ?? '')

  // 复制失败时展示失败的那一串，好让用户手动选中。
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

  const shown = options.slice(0, QY_AA_BAR_MAX_CARDS)
  const hiddenCount = options.length - shown.length

  return (
    <div className='bg-card flex flex-wrap items-center gap-2 rounded-lg border px-3 py-2'>
      <span className='text-muted-foreground shrink-0 text-xs font-medium'>
        {t('qy_aa_bar_label')}
      </span>

      {isMobile ? (
        <>
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
        </>
      ) : (
        <div className='grid w-full grid-cols-[repeat(auto-fill,minmax(20rem,1fr))] gap-2'>
          {shown.map((option) => (
            <QyAddressCard
              key={option.id}
              option={option}
              disabled={pending}
              onCopy={(text) => void copy(text)}
            />
          ))}
          {hiddenCount > 0 && (
            <span className='text-muted-foreground self-center text-xs'>
              {t('qy_aa_bar_more', { count: hiddenCount })}
            </span>
          )}
          {/* 桌面形态平时没有输入框；复制失败时现出这一条可手动选中的。 */}
          {manual != null && (
            <Input
              ref={inputRef}
              readOnly
              aria-label={t('qy_aa_bar_label')}
              value={manual}
              onFocus={(event) => event.currentTarget.select()}
              className='col-span-full h-7 font-mono text-xs'
            />
          )}
        </div>
      )}

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

/** 桌面形态的一张线路卡：复制 / 带 V1 复制 / 圆点 / 名称 / 基址。 */
function QyAddressCard(props: {
  option: QyApiAddressOption
  disabled: boolean
  onCopy: (text: string) => void
}) {
  const { t } = useTranslation()
  const base = qyNormalizeApiBase(props.option.url)
  const withV1 = qyApiBaseWithV1(props.option.url)

  return (
    <div className='flex min-w-0 items-center gap-2 rounded-lg border px-3 py-2'>
      <Button
        size='sm'
        variant='outline'
        disabled={props.disabled || base === ''}
        onClick={() => props.onCopy(base)}
      >
        <Copy />
        {t('qy_aa_bar_copy')}
      </Button>
      <Button
        size='sm'
        variant='outline'
        disabled={props.disabled || withV1 === ''}
        onClick={() => props.onCopy(withV1)}
      >
        <Copy />
        {t('qy_aa_bar_copy_v1')}
      </Button>
      <span
        className={`size-2 shrink-0 rounded-full ${getBgColorClass(props.option.color)}`}
        aria-hidden='true'
      />
      <div className='min-w-0 flex-1'>
        <div className='truncate text-sm font-medium'>{props.option.name}</div>
        <div className='text-muted-foreground truncate font-mono text-xs'>
          {base}
        </div>
      </div>
    </div>
  )
}
