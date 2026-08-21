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
import {
  qyApiBaseWithV1,
  qyNormalizeApiBase,
} from '@/features/qy/pages/api-address-picker/api-base-url'
import { readSiteServerAddress } from '@/lib/channel-connection-info'

/**
 * 拼 `ccswitch://v1/import` 那串导入 URL —— 用户点「打开 CC Switch」时交给桌面
 * 客户端的就是它。
 *
 * `apiAddress` 是用户在前置的线路选择窗口里选中的那一条（见
 * `useQyApiAddressPicker`）。空串表示这个窗口不是从那条路进来的，回落到站点
 * 地址 —— 也就是加线路选择之前的行为。
 *
 * `homepage` **不**跟着走选中的线路：它是 CC Switch 里那个「服务商主页」链接，
 * 指的是站点本身。加速线路 / 备用 API 域名只保证转发 API，未必伺服 Web 界面，
 * 把它填进 homepage 会给用户一个点开是白页的链接。
 *
 * 单独成文件（而不是留在配置窗口组件里）有两个理由：参数名、`/v1` 后缀、以及
 * "endpoint 用选中线路而 homepage 用站点"这三件事是对外契约，值得被直接测；
 * 而从一个组件文件里再导出一个函数会破掉 fast refresh。
 */
export function buildCCSwitchURL(
  app: string,
  name: string,
  models: Record<string, string>,
  apiKey: string,
  apiAddress: string
): string {
  const siteAddress = readSiteServerAddress()
  const base = qyNormalizeApiBase(apiAddress !== '' ? apiAddress : siteAddress)
  // Codex 要的是完整的 `/v1` 端点，Claude/Gemini 要的是基址。
  // 拼接走共用的那条规则（见 qyApiBaseWithV1）而不是 `${base}/v1`：运营把
  // 线路配成 `https://a.com/v1` 时，裸拼会导出一个 `.../v1/v1` 的端点，
  // 而这件事只有用户导进客户端、跑出 404 之后才会被发现。
  const endpoint = app === 'codex' ? qyApiBaseWithV1(base) : base
  const params = new URLSearchParams()
  params.set('resource', 'provider')
  params.set('app', app)
  params.set('name', name)
  params.set('endpoint', endpoint)
  params.set('apiKey', apiKey)
  for (const [k, v] of Object.entries(models)) {
    if (v) params.set(k, v)
  }
  params.set('homepage', siteAddress)
  params.set('enabled', 'true')
  return `ccswitch://v1/import?${params.toString()}`
}
