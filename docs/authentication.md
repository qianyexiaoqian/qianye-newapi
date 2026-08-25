# 用户鉴权与登录会话

面板鉴权采用短期 Access Token、HttpOnly Refresh Cookie 与服务端登录会话控制面的组合。面板请求不再依赖 Gin session，也不再要求 `New-Api-User` 请求头。

## 鉴权模型

- Access Token 是有效期 15 分钟的 JWT，只保存在浏览器内存中，通过 `Authorization: Bearer <token>` 发送。
- Refresh Token 是随机不透明值，有效期最长 30 天。浏览器只通过 `HttpOnly`、`SameSite=Strict` Cookie 持有它；服务端仅保存 HMAC 摘要，并在每次刷新时轮换。
- `user_sessions` 是登录会话控制面，记录设备、IP、登录方式、最后活跃时间、到期时间和撤销状态。数据库中的 Session 状态是最终权威；撤销传播速度取决于下文所述的 Redis 拓扑。
- 用户的密码、状态、角色或安全因子发生安全相关变化时，`auth_version` 会递增并使旧登录会话失效。订阅带来的分组升降级只刷新授权缓存，不会退出任何登录设备。
- Redis 缓存保存用户鉴权快照和登录会话快照。版本栅栏和撤销 tombstone 防止旧缓存重新授权；Session 快照使用跟随 `SYNC_FREQUENCY` 的短 TTL，缓存未命中或未启用 Redis 时回退到数据库校验。

`SESSION_SECRET` 用于派生 Access Token、Security Proof、Refresh Token 摘要和 AuthFlow 摘要的不同用途密钥。生产环境及多节点部署必须在所有节点配置相同的高强度随机值；更换该值会使现有登录、临时鉴权流程和 Security Proof 全部失效。

## 多节点 Redis 拓扑

多节点部署必须共用同一主数据库。登录 Session、账户级活跃 Session 上限和签发窗口计数都以数据库为权威，因此这些限制在应用节点间全局生效。Redis 中的 Session Hash（包含 `revoking`/`revoked` tombstone）只是缓存，其 TTL 为 Session 剩余寿命与有效 `SYNC_FREQUENCY` 中的较小值；`SYNC_FREQUENCY` 默认及非法值回退均为 `60` 秒。读取缓存不会续期，过期后会按 SID 回源数据库。延迟完成的 active 缓存回写只能使用其数据库观察窗口尚未消耗的 TTL，不能在撤销 tombstone 到期后重新启动一个完整缓存周期。

| Redis 部署方式 | Session 状态传播 | 限流语义 |
| --- | --- | --- |
| 所有节点共享 Redis | 正常撤销和版本发布通过同一缓存即时传播 | Redis 限流额度在所有节点间共享 |
| 每个节点使用独立 Redis | 最迟在该节点 Session 缓存 TTL 到期后回源收敛，即不超过有效 `SYNC_FREQUENCY`；版本轮换期间，新 Token 在持有旧缓存的节点上可能短暂返回 401 | 每个节点独立计数，集群总额度最坏约为单节点阈值乘以节点数 |
| 不使用 Redis | 每次 Session 校验直接读取数据库 | 使用各节点的内存限流器，额度同样按节点独立 |

`SYNC_FREQUENCY` 越大，独立 Redis 部署的陈旧窗口越长；值越小，每个活跃 SID 在每个节点上回源数据库的频率越高。默认配置下，持续活跃的 Session 每个节点最多约每 60 秒增加一次数据库主键点查。共享 Redis 时，撤销 tombstone 和版本发布仍保持即时传播。

所有节点必须使用相同的 `SESSION_SECRET`。当多个节点连接同一个 Redis 时，还必须使用相同的 `CRYPTO_SECRET`，否则节点生成的缓存键摘要不一致，无法正确共享缓存。上述保证只覆盖登录 Session 鉴权的有界陈旧语义；限流额度及其他 Redis 缓存仍会受到 Redis 拓扑影响，不能据此认为整个控制面与拓扑无关。

## 浏览器接口

登录成功后，密码登录、2FA、Passkey、OAuth、WeChat 和 Telegram 登录均返回统一数据：

```json
{
  "success": true,
  "data": {
    "access_token": "...",
    "token_type": "Bearer",
    "access_expires_at": 1730000000,
    "user": {},
    "session": {
      "sid": "...",
      "current": true,
      "login_method": "password",
      "ip": "...",
      "user_agent": "...",
      "created_at": 1730000000,
      "last_active_at": 1730000000,
      "expires_at": 1732592000
    }
  }
}
```

会话相关接口：

| 接口 | 鉴权 | 用途 |
| --- | --- | --- |
| `POST /api/user/auth/refresh` | Refresh Cookie；Secure 模式附加 Origin 校验 | 轮换 Refresh Token 并签发新的 Access Token |
| `POST /api/user/auth/logout` | Refresh Cookie；Secure 模式附加 Origin 校验，可同时携带 Bearer | 撤销当前登录会话并清除 Cookie |
| `GET /api/user/sessions` | Bearer | 查看当前鉴权版本的有效登录会话，当前会话优先，最多 100 条 |
| `DELETE /api/user/sessions/:sid` | Bearer | 撤销指定登录会话，包括当前会话 |
| `POST /api/user/sessions/revoke-others` | Bearer | 保留当前会话并撤销其他会话 |

客户端内存中已有会话时，应在 refresh/logout 请求中发送 `X-Auth-Session: <sid>`。Refresh Cookie 与该 SID 不一致时，两个端点都返回 `409 AUTH_SESSION_MISMATCH`，且不会轮换、撤销或清除任何会话；客户端先通过 refresh 清除本标签页的旧 SID、恢复 Cookie 当前对应的会话，再重试 logout。冷启动尚无内存会话时可以省略该请求头。

并发使用同一个 Refresh Token 时，服务端通过确定性轮换恢复同一个后继 Token，多个浏览器标签页不会因丢失“胜者”响应而被迫退出。最近一代 Refresh Token 在短暂容错窗口结束后再次出现会撤销对应会话；无法识别的更早代或随机 Token 只会被拒绝，不会允许攻击者凭猜测踢掉会话。

前端使用 Web Locks 串行化同一浏览器配置文件中的刷新，并通过 BroadcastChannel（不支持时回退到 `storage` 事件）仅同步会话标识和登录/退出事件；Access Token 与 Refresh Token 都不会通过跨标签页消息传递或持久化到 Web Storage。

前端将冷启动状态与登录状态分开管理。网络或服务端临时故障允许后续导航重试 refresh；服务端确认 Refresh Cookie 无效时才进入已完成的匿名状态。内存 SID 与 Cookie SID 不一致时，客户端清除旧内存身份并在不携带旧 SID 的情况下重试一次。

## Session 签发限额与保留策略

服务端在所有登录方式的统一 Session 签发出口执行两级账户限制：

- `USER_SESSION_ACTIVE_LIMIT`（默认 `50`）：单用户未过期且状态为 active 的 Session 上限。达到上限时新登录返回 `409 AUTH_SESSION_LIMIT`。
- `USER_SESSION_ISSUANCE_LIMIT`（默认 `100`）和 `USER_SESSION_ISSUANCE_WINDOW_SECONDS`（默认 `86400`）：统计窗口内该用户创建的所有 Session，包含已撤销和旧鉴权版本的记录。达到上限时返回 `429 AUTH_SESSION_ISSUANCE_LIMIT`。
- 这两次计数与插入不加跨数据库锁；极端并发登录可能出现少量超额，但计数失败会拒绝签发，不会降级放行。

升级时已经超过活跃上限的账户不会被自动下线或挤掉旧会话；限制只作用于后续的新 Session 签发。

`USER_SESSION_REVOKED_RETENTION_DAYS`（默认 `7`）控制 revoked 行的审计保留期。签发计数依赖窗口内的行仍存在，因此签发窗口不得超过 revoked 保留期。如果配置超出，启动时会记录告警并将实际窗口钳制到保留期，避免提前删除 revoked 行导致限流计数被低估。

定时清理即使发现 `expires_at` 已过期，也不会删除 `created_at` 仍落在实际签发窗口内的行；尚未达到 revoked 保留期的撤销记录同样会继续保留。这样在扩大配置窗口时，过期清理不会静默削弱签发计数或审计保留。

活跃数量会计入状态仍为 active 但 `user_auth_version` 已过期的异常残留行，而设备列表只展示当前鉴权版本。因此遇到 `AUTH_SESSION_LIMIT` 时，应优先在仍已登录的设备上执行“撤销其他会话”，该操作会同时清理不可见的旧版本 active 行；没有可用设备时可使用密码重置撤销所有会话。密码重置不会清空签发窗口计数。

仅 master 节点每小时分批删除过期 Session 和超过保留期的 revoked Session。`USER_SESSION_HOURLY_ALERT_THRESHOLD`（默认 `5000`）只在最近一小时全局签发量异常时记录告警，不会形成可被滥用的全站登录拒绝开关。

## Refresh/Logout 的 Origin 校验

refresh/logout 的 Origin 防护与 Refresh Cookie 的 Secure 模式绑定：

- 未配置 `SESSION_COOKIE_SECURE` 或显式设为 `false` 时，Refresh Cookie 可用于本地 HTTP，refresh/logout 的 OriginGuard 关闭，并且不得配置 `SESSION_COOKIE_TRUSTED_URL`。这使 `http://localhost` 上不同端口的 Rsbuild/Vite 开发代理可以正常转发请求。该模式仅用于可信的本地开发环境，不应暴露到公网。
- `SESSION_COOKIE_SECURE=true` 时，Refresh Cookie 仅通过 HTTPS 发送，同时启用严格 OriginGuard。`POST /api/user/auth/refresh` 和 `POST /api/user/auth/logout` 会校验浏览器的 `Origin`；缺少 `Origin` 时只接受合法的单一 `Referer` 作为回退。允许来源包括请求自身的精确 Origin，以及 `SESSION_COOKIE_TRUSTED_URL` 中配置的精确 Origin。

Secure 模式的 Origin 校验不信任客户端直接发送的 `X-Forwarded-Proto`。TLS 在反向代理终止时，应将面板的公开 HTTPS Origin 明确写入 `SESSION_COOKIE_TRUSTED_URL`。

`SESSION_COOKIE_TRUSTED_URL` 现在具有明确的新语义：它是 refresh/logout Cookie 端点的可信 Origin 列表，不是 CORS 白名单。配置规则如下：

- 仅在 `SESSION_COOKIE_SECURE=true` 时配置；多个值用英文逗号分隔。
- 每项必须是精确的 HTTPS Origin，例如 `https://panel.example.com` 或 `https://panel.example.com:8443`。
- 不接受通配符、路径、查询参数、用户信息或域名后缀匹配。
- 不会修改 relay、旧 billing dashboard、`/api/usage/token` 或 `/api/log/token` 的 CORS 行为。浏览器使用 `sk-` key 直连 relay 的场景保持不变。

本地 HTTP 开发示例（OriginGuard 关闭）：

```env
SESSION_SECRET=<local-random-value>
SESSION_COOKIE_SECURE=false
# SESSION_COOKIE_TRUSTED_URL 不得设置
```

生产 HTTPS 示例（OriginGuard 开启）：

```env
SESSION_SECRET=<high-entropy-random-value>
SESSION_COOKIE_SECURE=true
SESSION_COOKIE_TRUSTED_URL=https://panel.example.com,https://admin.example.com
```

该开关只控制面板 Refresh Cookie 和 refresh/logout 的 OriginGuard，不会修改 relay、旧 billing dashboard、`/api/usage/token` 或 `/api/log/token` 的 CORS 行为。

## 客户端 IP 的获取

### 为什么这是一条安全配置而不是日志字段

客户端 IP 是四处判据的取值来源，它们必须看到同一个字符串：

| 用途 | 位置 |
| --- | --- |
| 令牌 `allow_ips` | `middleware/auth.go` 的 `TokenAuth` / `TokenAuthReadOnly`。密钥泄漏之后用户唯一的自助止损手段 |
| 全部按 IP 计的限流 | `GlobalAPIRateLimit` / `CriticalRateLimit` / `EmailVerificationRateLimit`，桶键就是它 |
| 审计与资金台账 | `qy_request_audits.ip`、划转 / 提现 / 工单 / 违规记录的 `client_ip` |
| 风控与去重 | 抽奖 `dedup_ip`、Turnstile 的 `remoteip` |

因此全站只有**一处**取法：`common.ClientIP(c)`（`common/client_ip.go`）。仓库里禁止再调 gin 的 `c.ClientIP()`，由 `common/client_ip_single_source_guard_test.go` 逐文件校验。混用会造成"限流按真实 IP、台账记伪造 IP"这种自相矛盾——两边都不报错，只有真出事的时候才发现取证数据是假的。

### 信任链

判据只有一条：**只有当直连对端（TCP 层的 `RemoteAddr`）本身是受信代理时，才采信它带来的转发头**。直连对端是唯一无法被 HTTP 客户端伪造的东西。

采信之后，`X-Forwarded-For` **从右往左**读：每一跳代理把"连上自己的那个地址"追加到最右边，所以最右边是离本进程最近的一跳，最左边是客户端自己写的、谁都能编的前缀。从右往左跳过所有落在受信网段（全部受信来源的并集）里的地址，第一个不受信的地址就是真实客户端，再往左的内容一律作废。链从中间就坏掉（出现解析不出来的条目）时整条作废，退到下一个请求头，最终退到直连对端——那是保守的一侧。

反过来（从左往右取第一个）是最常见的错误实现：客户端只要自带 `X-Forwarded-For: 1.2.3.4` 就能把结果指成任意值。

### 各部署形态的正确配置

| 部署形态 | 代理实际发什么 | 配置 |
| --- | --- | --- |
| 直连，没有反代 | 客户端可能自带伪造的 XFF | `TRUSTED_PROXIES=none`。**不配不等于这一档**：不配用的是上游默认，回环与私网对端的转发头照样作数 |
| 同机 Nginx / Caddy | `X-Forwarded-For: $proxy_add_x_forwarded_for` | 不配即可（上游默认已含回环）。想收窄就写 `TRUSTED_PROXIES=loopback` |
| Nginx / Caddy / Traefik，地址固定 | 同上，客户端前缀在左、真实客户端在最右 | `TRUSTED_PROXIES=<反代自身 IP>/32` |
| Docker Compose / K8s，多层转发 | ingress、sidecar 逐跳追加 | `TRUSTED_PROXIES=<Pod/网桥网段>`（如 `10.42.0.0/16,10.43.0.0/16`）。地址完全不固定时 `private` |
| Cloudflare 直接回源 | `CF-Connecting-IP`（边缘覆盖写，恒为单个地址）+ XFF | `TRUSTED_PROXIES=cloudflare` |
| Cloudflare → 自建 Nginx | XFF = `客户端, CF 边缘`，对端是 Nginx | `TRUSTED_PROXIES=private,cloudflare`（或 `<nginx IP>,cloudflare`）。剥离用全部受信网段的并集，CF 边缘那一跳会被跳过 |
| Akamai / Fastly / 其它 CDN | `True-Client-IP` / `Fastly-Client-IP` | `TRUSTED_PROXIES=<CDN 回源网段>` 加 `CLIENT_IP_HEADERS=True-Client-IP,X-Forwarded-For` |
| 阿里 / 腾讯 SLB 七层、AWS ALB | 标准 XFF，真实客户端在最右 | `TRUSTED_PROXIES=<LB 网段>`。ALB 的 `X-Forwarded-For` 语义与 Nginx 一致 |
| AWS NLB / HAProxy TCP 模式 | 只有 PROXY protocol，没有 HTTP 头 | **不支持**：本进程不解析 PROXY protocol。让 LB 走七层（ALB/CLB HTTP 监听器），或在前面加一层 Nginx（`proxy_protocol` + `proxy_set_header X-Forwarded-For`），再按 Nginx 那一行配 |
| 只配了 `X-Real-IP` 的 Nginx | `proxy_set_header X-Real-IP $remote_addr`（覆盖写），**不配 XFF** | `TRUSTED_PROXIES=<nginx IP>/32` 加 `CLIENT_IP_HEADERS=X-Real-IP`。不加这一项的代价见下方「只配 X-Real-IP 时会发生什么」 |
| RFC 7239 `Forwarded` | `Forwarded: for=1.2.3.4;proto=https` | `CLIENT_IP_HEADERS=Forwarded`，同样按链从右往左剥离 |

#### 只配 `X-Real-IP` 时会发生什么

因为**普通 Nginx 默认把客户端带来的任意请求头原样透传给上游**——要删掉某个头
必须显式写 `proxy_set_header X-Forwarded-For "";`。而本进程的默认请求头顺序是
`X-Forwarded-For, X-Real-IP`：XFF 排在前面。

于是在「只配了 X-Real-IP 的 Nginx」这一种部署上，客户端只要自己加一个
`X-Forwarded-For`，就能顶掉反代诚实写下的 `X-Real-IP`。实测（隔离实例 + 真实令牌，
令牌 `allow_ips=203.0.113.5/32`）：

| 请求 | 结果 |
| --- | --- |
| 不带任何转发头 | 403 |
| 只带反代写的 `X-Real-IP: 198.51.100.7` | 403 |
| 上面这条 **加上** 客户端自造的 `X-Forwarded-For: 203.0.113.5` | **200** |

限流侧同形：带诚实 `X-Real-IP` + 逐条轮换 XFF 打 420 次 `/api/status`，一条 429
都没有；不带头的对照组第 361 次就 429（默认 360 次 / 180 秒）。受影响的是令牌
IP 白名单、按 IP 的限流、审计台账里的来源 IP、抽奖同 IP 去重与 Turnstile 校验。

写上 `CLIENT_IP_HEADERS=X-Real-IP` 之后，XFF 根本不进判据，这条路就断了。
标准 Nginx（`proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;`）不受影响：
反代追加的那一段恒在最右，从右往左剥离依然正确。

这一栏是**提示**，不是强制：`GET /api/qy/admin/client-ip` 的 `request.conflicts`
（管理端卡片上的「请求头结论冲突」）会列出「另一个受信请求头给出了不同的答案」
——例如 `X-Real-IP` 说 `198.51.100.7`，而结论用的是 XFF 里的 `203.0.113.5`。
如果你装在反代后面而这里出现了冲突，说明反代写的头与客户端带来的头指向两个人，
上面那条路对你是通的；两个头都由反代写、且写的是同一个人时这一栏是空的。
结论一个字都不会因为这一栏而改变，也不会有任何请求因此被拒。

地址形态一律归一化后再使用：`1.2.3.4:56789`（Azure App Service 与部分云 LB 的写法）会去掉端口；`[2001:db8::1]:443` 去括号去端口；`::ffff:203.0.113.5` 折回 `203.0.113.5`；`fe80::1%eth0` 去掉 zone；IPv6 统一小写压缩形式。**这不是显示层的事**：不归一化的话同一个客户端会落进两个限流桶、在台账里显示成两个来源，而这不需要攻击者做任何事就会自然发生。

同名请求头出现多次时按序全部拼接（`Header.Values`），而不是只取第一个（`Header.Get`）。客户端自带一个 XFF、反代再追加一个时，Go 会把它们保留成两个头，只取第一个拿到的恰好是客户端伪造的那条。

### `TRUSTED_PROXIES` 取值

逗号分隔，关键字与 IP/CIDR 可以混用（`none` 除外，它必须单独使用）：

- `none`：谁都不信，转发头一律忽略。显式表达"我确认前面没有反代"。
- `loopback`：`127.0.0.0/8`、`::1`。
- `private`：`127.0.0.0/8`、`::1`、`10.0.0.0/8`、`172.16.0.0/12`、`192.168.0.0/16`、`fc00::/7`。**刻意不含** CGNAT（`100.64.0.0/10`）与链路本地：前者被部分云厂商 CNI 与部分 ISP 同时使用，把它放进一个"一行搞定"的档位等于在运维不知情的前提下把信任面扩大到运营商网络。需要的部署显式写 CIDR。
- `cloudflare`：内置的 Cloudflare 边缘网段快照，并且**只有这一档**采信 `CF-Connecting-IP`。
- IP / CIDR：只信这些地址，填反向代理**自身**的地址而不是客户端网段。裸 IP 按 `/32`（`/128`）处理。

显式网段排在关键字档之前匹配：网段重叠时由逐条写出来的地址决定用哪一组请求头。非法 CIDR、空列表、重复关键字、把 `none` 与其他值混用都会阻止服务启动——这条配置的两个失败方向都是沉默的，所以它的语法错误必须响亮。

`0.0.0.0/0` 是合法配置（确实有人要），但会打一条启动告警：它让任何客户端都能自己决定自己的 IP。

### 厂商专有头为什么必须绑定到来源

`CF-Connecting-IP`、`True-Client-IP`、`Fastly-Client-IP` 都**不在默认请求头列表里**，而且 `cloudflare` 档的 `CF-Connecting-IP` 只对落在 Cloudflare 网段里的对端生效。

理由是：普通 Nginx 默认**原样透传**客户端带来的任意请求头。在"没有 Cloudflare、只有 Nginx"的部署里，客户端塞一个 `CF-Connecting-IP: 9.9.9.9` 会一路到达应用。如果像 gin 的 `TrustedPlatform` 那样无条件采信，判据就又回到了客户端手里。把请求头绑定到"它是从哪一档可信来源进来的"，`CF-Connecting-IP` 只有 Cloudflare 的网段说了才算。

### Cloudflare 网段怎么维护

默认是**编译进二进制的快照**（`common/cloudflare_ranges.go`，取自 `https://www.cloudflare.com/ips-v4` 与 `ips-v6`，带取得日期）。快照落后于 Cloudflare 的后果是**兼容性**下降——新网段的边缘节点不受信，那部分流量的客户端 IP 退化成边缘地址——而不是安全性下降，失效方向是保守的。

要更新，把 `curl https://www.cloudflare.com/ips-v4` 与 `ips-v6` 的原样输出拼进一个文件（一行一个 CIDR，`#` 开头是注释），用 `TRUSTED_PROXIES_CLOUDFLARE_FILE` 指过去。它**替代**内置快照而不是叠加——叠加语义会让"把清单收窄"变成不可能。文件内容要过合法性校验：包含私网、回环、链路本地、组播或过宽前缀（v4 宽于 `/12`、v6 宽于 `/24`）的清单会让服务**起不来**，一条坏的就整份拒绝。

**刻意不做启动时联网拉取。** 那会把一条安全判据的取值接到一次启动期网络请求上：拉取失败变成新的启动失败模式（或者更糟，静默退回空列表）；拉取成功但内容被篡改（DNS 投毒、企业中间人 CA、透明代理）会把信任面**扩大**到攻击者自己的地址，而且每次重启都重新赌一遍。Cloudflare 的网段是年级别稳定的，为此换来一条常驻的 TOFU 通道不划算。

### 未配置时的默认

优先级只有两档：

1. **显式的 `TRUSTED_PROXIES` 永远最高**，不做任何"聪明"的补充。
2. 未配置 → 用**上游那份默认**：`127.0.0.0/8`、`::1`、`10.0.0.0/8`、`172.16.0.0/12`、`192.168.0.0/16`、`fc00::/7`（等价于 `private`），并在启动日志里打一条 WARNING。

这一档与上游 `middleware/trusted_proxies.go` 的 `defaultTrustedProxyCIDRs` 逐条相同，也与上游一样**不做任何强制**：不拒绝启动，不因为"看起来装在反代后面"而报错。`BIND_ADDRESS` 不参与这个判断。

代价写清楚，不糊过去：这些网段里任何能打到本端口的东西（容器网桥、K8s Pod 网段、同 VPC 主机、本机上的任意进程）都可以用一个 `X-Forwarded-For` 决定自己在令牌 `allow_ips` 与限流桶里的取值。实测：`allow_ips=203.0.113.5/32` 的令牌从本机加一个 `X-Forwarded-For: 203.0.113.5` 请求 `/v1/models`，从 403 变 200；全局限流用轮换的 `10.20.x.x` 打 915 次一条 429 都没有。不接受这一点的部署显式写 `TRUSTED_PROXIES=none`，或把反代自身的地址逐条写出来——那是一次显式的决定，而不是一条替所有人改掉的默认。

默认覆盖不到的是反代坐在**公网地址**上的部署（CDN 回源、独立 LB 主机）：那种站点所有人的 IP 都会变成反代的地址。进程会记下"直连对端不受信、却带着转发头"的那些对端，并在管理端给出可以直接粘贴的 CIDR。那是提示，不改变任何结论。

### 排查：`GET /api/qy/admin/client-ip`

管理端「配置健康」页上的"客户端 IP 识别"卡片，回答的不是"我的 IP 是什么"（台账里就有），而是**"为什么是这个值"**：

- `request`：这一条请求的完整取值过程——直连对端、对端落在哪一档受信来源、结论从哪个请求头取的、转发链原文、因为对端不受信而被丢掉的头。
- `policy`：当前策略档、原始 `TRUSTED_PROXIES` 取值、每一档受信来源的网段与请求头列表。
- `observations`：直连对端不受信却带着转发头的那些对端，带次数、首末次时间，以及**可以直接粘进 `TRUSTED_PROXIES` 的 `suggestion`**（`/32` 或 `/128` 单机地址；建议网段等于替运维决定"这一整段里的东西都可信"，而那是运维自己才做得了的判断）。

同一条信息在启动时也会打一行日志（策略档 + 受信网段 + 代价）。这条配置的失败模式全是沉默的，启动日志与这张卡片是它仅有的两个常驻信号。

接口挂在 `AdminAuth` 后面而不是做成人人可查的"查我的 IP"：响应里有完整的受信网段清单，也就是信任面本身——知道哪些网段被信任，就知道从哪里打过来可以伪造来源 IP。

它只读、没有写端点：受信网段是环境变量，改它必须重启进程。做成可热改的接口等于给"运行中的进程被远程放宽信任面"开一扇门。

Redis 限流使用原子 Lua 固定窗口，替代旧的近似滑动窗口 List 实现。这是有意的语义变化：窗口边界两侧可分别打满一次，极短时间内通过量最高约为配置值的两倍。例如 `20 次/20 分钟` 在边界可通过约 40 次。帐户级 Session 上限和签发窗口继续控制数据库增长；如未来需要严格抑制边界突发，需单独迁移为 ZSET 滑动窗口。

用户级模型成功请求限流仍使用原有 Redis List 近似滑动窗口，但列表时间戳统一写为 UTC。滚动升级期间，旧节点写入的本地时间字符串和新节点写入的 UTC 字符串无法从格式上区分，可能在一个模型限流窗口内临时误放行或误拒绝。所有节点升级完成并经过一个完整窗口后会自然收敛；本次升级不会切换 Key 或主动删除现有列表。

开放注册仍会受 Critical IP 限流保护，但分布式 IP 多账号攻击不能仅靠 IP 限流阻止。公网开放注册的部署应同时启用 Turnstile 和邮箱验证；更强的设备或多维风控需作为独立安全项目设计。

## PAT 调用契约

`User.AccessToken`（面板 PAT）继续支持 `Authorization: Bearer <pat>`，也兼容原有的单值 `Authorization: <pat>`。`New-Api-User` 不再参与鉴权，外部脚本不需要再发送 Bearer 与用户 ID 双请求头。这是有意的调用契约简化；旧 PAT 本身无需重新生成。

PAT 不是浏览器登录会话，不能调用登录会话管理接口，也不能签发绑定具体登录会话的 Security Proof。

## 临时鉴权流程与二次验证

OAuth state、2FA pending、Passkey ceremony、Telegram bind 等临时状态存放在 `auth_flows`。客户端只持有随机 `flow_token`，数据库仅保存 HMAC 摘要；流程具有用途、provider、intent、用户和登录会话绑定，并且只能原子消费一次。OAuth 注册的 affiliate code 也随登录 AuthFlow 保存。

标准 OAuth 绑定回调由 popup 通过同源 `postMessage` 交给 opener；只有 opener 使用自身内存中的 Bearer 调用后端绑定接口。Telegram 绑定先由已登录前端创建绑定 AuthFlow，再让 widget 回调携带路径中的 `flow_token`，回调时会重新确认原登录会话仍有效。Telegram 的已签名 widget assertion 也会登记为一次性凭据，重复回放会被拒绝。

敏感操作使用有效期 5 分钟的 `X-Security-Proof`：

- `channel.key.read`：查看渠道密钥；
- `passkey.register`：注册 Passkey；
- `passkey.delete`：删除 Passkey。

Proof 同时绑定用户、登录会话、用户鉴权版本、会话版本和 scope，不能跨用户、跨会话或跨用途复用。

启用了 2FA 的用户注册 Passkey 时，register begin 与 finish 都必须携带有效的 `passkey.register` Proof；finish 会在消费一次性 AuthFlow 之前重新验证 Proof。未启用 2FA 的首次 Passkey 注册不要求该请求头。

## 升级注意事项

- 旧 `session` Cookie 不再使用；升级后现有面板登录会失效，用户需要重新登录。
- 数据库迁移会新增 `user_sessions`、`auth_flows`、`external_identity_claims` 和 `users.auth_version`，并为已有用户初始化鉴权版本、回填 Telegram 账号唯一归属；若历史数据中同一 Telegram ID 已绑定多个用户，迁移会拒绝继续启动，需先消除歧义。
- 数据库迁移会为 Session 签发计数和分批清理新增索引；已有 `user_sessions` 很大时应为首次启动预留维护窗口。
- `user_sessions.previous_refresh_hash` 会从定长 `char(64)` 迁移为 `varchar(64)`。应用会兼容读取历史定长字段留下的空格填充；迁移后的目标结构必须保持幂等，连续启动不应反复执行列类型变更。
- 仅 master 节点定时清理过期登录会话、超过配置保留期的 revoked 会话和已过保留期的 AuthFlow。
- `TRUSTED_PROXIES` 未配置时的默认**与上游一致**：信任回环、RFC1918 与 `fc00::/7`，并在启动日志里打一条 WARNING。本仓曾短暂把这一档改成「谁都不信」，已撤回——那是一次行为变更，而上游从未做过这个强制。装在反代后面、从没配过这个变量的部署不需要为此做任何事。
- 客户端 IP 的取法已收敛到 `common.ClientIP(c)` 一处，`X-Forwarded-For` 支持多值请求头、带端口条目与 IPv4-mapped 归一化。归一化会让**已有的按 IP 限流桶键与台账值**在 `::ffff:` 形态上发生一次性变化（折回点分十进制），滚动升级期间同一个客户端可能短暂占用两个限流桶。
- Redis 限流从近似滑动窗口改为原子固定窗口，存在明确的边界双倍突发语义。
- 用户级模型成功请求限流的 UTC 时间戳在滚动升级期间存在一个窗口的混合格式过渡，期间可能临时误放行或误拒绝。
- 自建客户端应按新的 AuthBundle、`flow_token` 和 Security Proof 契约升级；PAT 客户端可直接移除 `New-Api-User`。
