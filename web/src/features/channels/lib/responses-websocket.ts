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

/**
 * Channel types whose upstream can carry the Responses WebSocket protocol.
 *
 * 本仓后端只对 OpenAI(1)与 Codex(57)开了 Responses WebSocket;上游
 * fa3cc1c6b 把高级自定义 / Sub2API / New API 也加进来,但那依赖挂起的
 * ae249f4ec(WS 与 HTTP 共用路由)。这里只列后端真能用的类型,免得在别的
 * 渠道上给出一个保存了也不生效的开关(channel-form.ts 的 buildSettingJSON
 * 同样只对这两类写入)。
 */
const RESPONSES_WEBSOCKET_CHANNEL_TYPES: ReadonlySet<number> = new Set([1, 57])

export function supportsResponsesWebSocket(channelType: number): boolean {
  return RESPONSES_WEBSOCKET_CHANNEL_TYPES.has(channelType)
}
