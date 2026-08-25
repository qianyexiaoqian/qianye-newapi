package common

import (
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// client_ip.go —— 全站**唯一**的「这个请求的客户端 IP 是谁」实现。
//
// # 为什么必须只有一份
//
// 客户端 IP 在本仓是四类判据的取值来源,它们必须看到同一个字符串:
//
//	令牌 allow_ips     middleware/auth.go 的 TokenAuth / TokenAuthReadOnly。
//	                   密钥泄漏之后用户唯一的自助止损手段。
//	按 IP 的限流       GlobalAPIRateLimit / CriticalRateLimit /
//	                   EmailVerificationRateLimit,桶键就是它。
//	审计与资金台账     qy_request_audits.ip、划转/提现/工单/违规的 client_ip。
//	                   事后取证的来源。
//	风控与去重         抽奖 dedup_ip、Turnstile 的 remoteip。
//
// 任何一处走了别的取法,就会出现「限流按真实 IP、台账记伪造 IP」这种自相矛盾:
// 两边都不报错,只有真出事的时候才发现取证数据是假的。因此仓库里
// **除了本文件之外禁止调用 gin 的 c.ClientIP()**,由
// common/client_ip_single_source_guard_test.go 逐文件校验。
//
// # 与 gin 自带 ClientIP() 的差异
//
// gin 的实现方向是对的(只在直连对端属于可信代理时解析转发头,并从右往左剥离),
// 但有四处在真实部署里会出错:
//
//	多值请求头   gin 用 Header.Get(),只取**第一个** X-Forwarded-For 头。
//	             客户端自带一个 XFF、反代再追加一个,Go 会保留成两个头,
//	             Get() 拿到的恰好是客户端伪造的那个。这里按 Header.Values()
//	             全部按序拼起来,与 nginx `$http_x_forwarded_for` 的语义一致。
//	归一化       gin 原样返回请求头里的字符串。`::ffff:203.0.113.5` 与
//	             `203.0.113.5` 是同一个客户端,却会落进**两个限流桶**、
//	             在台账里显示成两个来源。这里统一 Unmap + 去 zone + 规范化。
//	带端口       Azure App Service 与部分云 LB 会写成 `1.2.3.4:56789`。
//	             gin 的 net.ParseIP 解析失败后直接放弃整条链。
//	厂商头       CF-Connecting-IP / True-Client-IP / Fastly-Client-IP。
//	             gin 只有 TrustedPlatform 这一档,而它是**无条件**采信的 ——
//	             任何能直连到端口的东西加一个头就能伪造。这里把请求头绑定到
//	             「它是从哪一档可信来源进来的」,CF 的头只有 CF 的网段说了才算。
//
// # 信任链
//
// 判据只有一条:**只有当直连对端本身是受信代理时,才看它带来的转发头**。
// 直连对端(TCP 层的 RemoteAddr)是唯一无法被 HTTP 客户端伪造的东西。
//
// 采信之后,X-Forwarded-For 要**从右往左**读:每一跳代理把「连上自己的那个地址」
// 追加到最右边,所以最右边是离本进程最近的一跳,最左边是客户端自己写的、
// 谁都能编的前缀。从右往左跳过所有落在受信网段里的地址,第一个不受信的地址
// 就是真实客户端 —— 再往左的内容一律作废。
//
// 反过来(从左往右取第一个)是最常见的错误实现:客户端只要自带
// `X-Forwarded-For: 1.2.3.4` 就能把结果指成任意值。

// ---------------------------------------------------------------------------
// 取值结果
// ---------------------------------------------------------------------------

// 解析结局。管理端诊断页直接展示这些值,所以它们是对外契约,不要随手改名。
const (
	// ClientIPReasonDirectPeer 直连对端不是受信代理,转发头全部忽略。
	ClientIPReasonDirectPeer = "direct_peer"
	// ClientIPReasonPeerUnparsable RemoteAddr 解析不出 IP。只可能出现在
	// 非 TCP 监听(unix socket)或测试里构造的请求上。
	ClientIPReasonPeerUnparsable = "peer_unparsable"
	// ClientIPReasonForwardedChain 从转发链上从右往左剥出来的地址。
	ClientIPReasonForwardedChain = "forwarded_chain"
	// ClientIPReasonChainAllTrusted 转发链上每一跳都落在受信网段里,
	// 取了最左端。内网自测或代理网段配得过宽时会出现。
	ClientIPReasonChainAllTrusted = "forwarded_chain_all_trusted"
	// ClientIPReasonForwardedHeader 从单值请求头(CF-Connecting-IP 等)取到。
	ClientIPReasonForwardedHeader = "forwarded_header"
	// ClientIPReasonTrustedNoHeader 对端受信,但一个可用的转发头都没带。
	// 反代配置漏了 proxy_set_header 时就是这一档。
	ClientIPReasonTrustedNoHeader = "trusted_peer_no_header"
)

// 策略档位。启动日志与诊断页都按这个值说话。
const (
	// ClientIPStrategyExplicit 运维显式配了 TRUSTED_PROXIES。
	ClientIPStrategyExplicit = "explicit"
	// ClientIPStrategyNone 显式配了 TRUSTED_PROXIES=none。
	ClientIPStrategyNone = "none"
	// ClientIPStrategyDefaultPrivate 未配置 TRUSTED_PROXIES,用上游的默认
	// 网段(回环 + RFC1918 + fc00::/7),并按上游的口径打一条 WARNING。
	// 这一档等价于显式写 `TRUSTED_PROXIES=private`,只是没人写过。
	ClientIPStrategyDefaultPrivate = "default_private"
)

// ClientIPIgnoredHeader 是一条**被忽略**的转发头,只用于诊断。
type ClientIPIgnoredHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// ClientIPResolution 是一次取值的完整过程,管理端诊断页直接渲染它。
//
// 存在的理由:「我的 IP 被识别成了什么」在出问题时永远不够 ——
// 真正要回答的是「**为什么**是这个值」。少了 Peer / TrustSource / Header
// 三个字段,运维只能在 allow_ips 里瞎试。
type ClientIPResolution struct {
	// IP 最终结论。判据、限流桶、台账用的都是它。
	IP string `json:"ip"`
	// Peer TCP 直连对端,已归一化。唯一不可伪造的事实。
	Peer string `json:"peer"`
	// PeerTrusted 对端是否落在某一档受信网段里。false 时转发头全部作废。
	PeerTrusted bool `json:"peer_trusted"`
	// TrustSource 命中的受信来源名(explicit / private / loopback / cloudflare)。
	TrustSource string `json:"trust_source,omitempty"`
	// Header 结论是从哪个请求头取的。空串表示结论就是 Peer。
	Header string `json:"header,omitempty"`
	// Chain 转发链原文(未归一化),从左到右。
	Chain []string `json:"chain,omitempty"`
	// Ignored 因为对端不受信而被丢弃的转发头。这一栏非空 + PeerTrusted=false
	// 就是「装在反代后面但没配 TRUSTED_PROXIES」的确诊信号。
	Ignored []ClientIPIgnoredHeader `json:"ignored_headers,omitempty"`
	Reason  string                  `json:"reason"`
	// Conflicts 是**排在结论之后、但同样有值且给出不同答案**的受信请求头。
	//
	// 它专为一种被文档祝福过的部署形态而存在:只配了 X-Real-IP 的 nginx
	// (`proxy_set_header X-Real-IP $remote_addr;`,不配 XFF)。那种 nginx 会把
	// 客户端**自带的** X-Forwarded-For **原样透传**给上游(要删掉必须显式写
	// `proxy_set_header X-Forwarded-For "";`),而本仓的默认头顺序是
	// [X-Forwarded-For, X-Real-IP] —— 于是客户端只要自己加一个 XFF,
	// 就能顶掉反代诚实写下的 X-Real-IP。实测:令牌 allow_ips 从 403 变成 200;
	// 按 IP 的限流从 360 次触顶变成打满 420 次一条 429 都没有。
	//
	// 这一栏就是那种错配的确诊信号:反代写了 X-Real-IP=A、客户端塞了 XFF=B,
	// 而我们用了 B。正确的配置是显式声明 CLIENT_IP_HEADERS=X-Real-IP
	// (声明之后 XFF 根本不进判据),文档表格里那一行已经补上这个键。
	//
	// 只记录、不改变结论:按声明的顺序取值是这一层的契约,悄悄换一个头会让
	// 另一批正确配置的站点结果跟着变。诊断页把它显示出来,由人去改配置。
	Conflicts []ClientIPHeaderValue `json:"conflicts,omitempty"`
	// Strategy 当前生效的策略档,见 ClientIPStrategy* 常量。
	Strategy string `json:"strategy"`
}

// ClientIPHeaderValue 是一个请求头及它归一化之后的取值。
type ClientIPHeaderValue struct {
	Header string `json:"header"`
	IP     string `json:"ip"`
}

// ---------------------------------------------------------------------------
// 归一化
// ---------------------------------------------------------------------------

// NormalizeIPString 把「各种形态里写着的一个 IP」归一成规范文本。
//
// 认这些形态,因为它们全都在真实部署里出现过:
//
//	203.0.113.5              直连 RemoteAddr(无端口)、多数反代写的 XFF 条目
//	203.0.113.5:41234        RemoteAddr、Azure App Service 的 XFF
//	2001:db8::1              IPv6
//	[2001:db8::1]:41234      IPv6 RemoteAddr、RFC 7239 的 for= 值
//	[2001:db8::1]            少数反代写的带括号无端口形态
//	::ffff:203.0.113.5       IPv4-mapped IPv6。**必须折回 203.0.113.5**,
//	                         否则同一个客户端会占两个限流桶、在台账里是两个来源。
//	fe80::1%eth0             带 zone 的链路本地地址,zone 一律丢掉:
//	                         它是**本机**的接口名,跨进程没有意义。
//	"203.0.113.5"            RFC 7239 里带引号的值
//
// 归一化本身也是安全判据的一部分:两处判据(allow_ips 与限流桶)拿到不同写法
// 的同一个地址会得到不同结论,而这不需要攻击者做任何事就会自然发生。
func NormalizeIPString(raw string) (string, bool) {
	s := strings.TrimSpace(raw)
	s = strings.Trim(s, `"`)
	s = strings.TrimSpace(s)
	if s == "" {
		return "", false
	}
	if addr, err := netip.ParseAddr(s); err == nil {
		return canonicalClientIP(addr), true
	}
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return canonicalClientIP(ap.Addr()), true
	}
	if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		if addr, err := netip.ParseAddr(s[1 : len(s)-1]); err == nil {
			return canonicalClientIP(addr), true
		}
	}
	return "", false
}

func canonicalClientIP(addr netip.Addr) string {
	return addr.Unmap().WithZone("").String()
}

// ---------------------------------------------------------------------------
// 策略
// ---------------------------------------------------------------------------

// ClientIPTrustSource 是一档受信来源:一组网段,加上「这一档的对端可以用哪些
// 请求头声明客户端 IP」。
//
// 请求头必须绑定到来源而不是全局,这是本文件与 gin TrustedPlatform 的关键差别。
// CF-Connecting-IP 只有当对端真的是 Cloudflare 边缘节点时才可信:普通 nginx
// **默认原样透传**客户端带来的任意请求头,全局采信 CF-Connecting-IP 等于把
// 判据交还给客户端。
type ClientIPTrustSource struct {
	Name    string         `json:"name"`
	CIDRs   []netip.Prefix `json:"-"`
	Headers []string       `json:"headers"`
}

// CIDRStrings 供诊断页与 gin SetTrustedProxies 使用。
func (s ClientIPTrustSource) CIDRStrings() []string {
	out := make([]string, 0, len(s.CIDRs))
	for _, p := range s.CIDRs {
		out = append(out, p.String())
	}
	return out
}

// ClientIPPolicy 是一次启动内不变的取值策略。
type ClientIPPolicy struct {
	Strategy string                `json:"strategy"`
	Sources  []ClientIPTrustSource `json:"sources"`
	// Raw 原始 TRUSTED_PROXIES 取值,诊断页原样回显。
	Raw string `json:"raw"`
	// Notice 启动时该说的一句话(为什么是这个策略 / 怎么改)。
	Notice string `json:"notice"`
	// Warning 非空表示这个策略有已知代价,启动日志按 WARNING 打。
	Warning string `json:"warning,omitempty"`
}

// 默认转发头。顺序与 gin 一致:先看 X-Forwarded-For 链,链上取不出来才退到
// X-Real-IP(nginx 的 `proxy_set_header X-Real-IP $remote_addr` 是**覆盖**写,
// 不会把客户端带来的值透传过来)。
var defaultClientIPHeaders = []string{"X-Forwarded-For", "X-Real-IP"}

// cloudflareClientIPHeaders 只在对端落在 Cloudflare 网段时生效。
// CF-Connecting-IP 由 CF 边缘覆盖写入,且恒为**单个**客户端地址,
// 比 XFF 链更不容易出错,所以排在前面。
var cloudflareClientIPHeaders = []string{"CF-Connecting-IP", "X-Forwarded-For"}

// privateTrustedProxyPrefixes 是 `private` 这一档,同时是**未配置时的默认**
// (ClientIPStrategyDefaultPrivate)。与上游 defaultTrustedProxyCIDRs 逐条相同,
// 只把 `::1` 写成等价的 `::1/128`(netip.Prefix 需要位数,Contains 语义一致)。
//
// 刻意**不含** 100.64.0.0/10(CGNAT)与 169.254.0.0/16 / fe80::/10(链路本地):
// 前者被部分云厂商的 CNI 与部分 ISP 同时使用,后者是链路作用域。把它们放进
// 一个「一行搞定」的档位里,等于在运维不知情的前提下把信任面扩大到运营商网络。
// 需要它们的部署显式写 CIDR。
var privateTrustedProxyPrefixes = []string{
	"127.0.0.0/8",
	"::1/128",
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
	"fc00::/7",
}

var loopbackTrustedProxyPrefixes = []string{"127.0.0.0/8", "::1/128"}

// sourceFor 返回对端命中的受信来源;nil 表示对端不受信。
//
// 按声明顺序取第一个命中的。网段重叠时靠顺序决定用哪一档的请求头列表,
// 这一点写进了配置文档。
func (p *ClientIPPolicy) sourceFor(addr netip.Addr) *ClientIPTrustSource {
	if p == nil || !addr.IsValid() {
		return nil
	}
	addr = addr.Unmap()
	for i := range p.Sources {
		for _, prefix := range p.Sources[i].CIDRs {
			if prefix.Contains(addr) {
				return &p.Sources[i]
			}
		}
	}
	return nil
}

// isTrustedHop 判断转发链上的某一跳是否属于**任意**一档受信来源。
//
// 用并集而不是「命中对端那一档」是必须的:CF → nginx 的部署里,对端是 nginx
// (private 档),而链上要跳过的是 Cloudflare 边缘地址(cloudflare 档)。
// 只按对端那一档判,剥离会在 CF 边缘地址上停下,全站客户端 IP 变成 CF 的边缘节点。
func (p *ClientIPPolicy) isTrustedHop(addr netip.Addr) bool {
	return p.sourceFor(addr) != nil
}

// AllCIDRStrings 是全部受信网段,交给 gin SetTrustedProxies。
//
// gin 自己的 ClientIP() 仍会被它内部的日志中间件调用,让它和本文件看到同一份
// 网段,可以避免访问日志里的 IP 与台账里的 IP 对不上。
func (p *ClientIPPolicy) AllCIDRStrings() []string {
	if p == nil {
		return nil
	}
	out := make([]string, 0, 8)
	for _, s := range p.Sources {
		out = append(out, s.CIDRStrings()...)
	}
	return out
}

// ---------------------------------------------------------------------------
// 解析
// ---------------------------------------------------------------------------

func clientIPHeaderIsChain(name string) bool {
	return strings.EqualFold(name, "X-Forwarded-For")
}

func clientIPHeaderIsForwarded(name string) bool {
	return strings.EqualFold(name, "Forwarded")
}

// forwardedHeaderNames 是「出现即说明前面有代理」的请求头全集,只用于诊断:
// 对端不受信时把它们收进 Ignored,好让运维一眼看出「确实有反代,只是没配」。
var forwardedHeaderNames = []string{
	"X-Forwarded-For",
	"X-Real-IP",
	"Forwarded",
	"CF-Connecting-IP",
	"True-Client-IP",
	"Fastly-Client-IP",
	"X-Client-IP",
	"X-Cluster-Client-IP",
	"X-Appengine-Remote-Addr",
	"Fly-Client-IP",
}

// ResolveClientIP 按策略解析一次请求的客户端 IP。纯函数,不碰全局状态。
func ResolveClientIP(policy *ClientIPPolicy, r *http.Request) ClientIPResolution {
	res := ClientIPResolution{Reason: ClientIPReasonPeerUnparsable}
	if policy != nil {
		res.Strategy = policy.Strategy
	}
	if r == nil {
		return res
	}
	peer, ok := NormalizeIPString(r.RemoteAddr)
	if !ok {
		return res
	}
	res.Peer = peer
	res.IP = peer
	res.Reason = ClientIPReasonDirectPeer

	peerAddr, err := netip.ParseAddr(peer)
	if err != nil {
		res.Reason = ClientIPReasonPeerUnparsable
		res.IP = ""
		return res
	}
	source := policy.sourceFor(peerAddr)
	if source == nil {
		res.Ignored = collectIgnoredForwardedHeaders(r)
		return res
	}
	res.PeerTrusted = true
	res.TrustSource = source.Name

	decided := false
	for _, header := range source.Headers {
		values := r.Header.Values(header)
		if len(values) == 0 {
			continue
		}
		// 结论已经定了,后面的头只用来**对账**:同样有值却给出另一个答案时
		// 记一条冲突(理由见 ClientIPResolution.Conflicts)。
		if decided {
			if ip := firstUsableForwardedIP(policy, header, values); ip != "" && ip != res.IP {
				res.Conflicts = append(res.Conflicts, ClientIPHeaderValue{Header: header, IP: ip})
			}
			continue
		}
		switch {
		case clientIPHeaderIsChain(header):
			chain := splitForwardedChain(values)
			if len(chain) == 0 {
				continue
			}
			res.Chain = chain
			if ip, reason, ok := policy.walkForwardedChain(chain); ok {
				res.IP, res.Header, res.Reason = ip, header, reason
				decided = true
				continue
			}
		case clientIPHeaderIsForwarded(header):
			chain := parseRFC7239Chain(values)
			if len(chain) == 0 {
				continue
			}
			res.Chain = chain
			if ip, reason, ok := policy.walkForwardedChain(chain); ok {
				res.IP, res.Header, res.Reason = ip, header, reason
				decided = true
				continue
			}
		default:
			// 单值头。多个同名头只认第一个:CF / Akamai / Fastly 都是覆盖写,
			// 出现第二个就说明有人在往里塞东西,拼起来只会让结果更不可预测。
			if ip, ok := NormalizeIPString(values[0]); ok {
				res.IP, res.Header, res.Reason = ip, header, ClientIPReasonForwardedHeader
				decided = true
				continue
			}
		}
	}
	if !decided {
		res.Reason = ClientIPReasonTrustedNoHeader
	}
	return res
}

// firstUsableForwardedIP 按 header 的类型取出它能给出的那个客户端地址。
//
// 只用于**对账**(填 ClientIPResolution.Conflicts),不参与结论 ——
// 所以它对失败一律返回空串,而不是让调用方去分辨"没有值"与"值不可用"。
func firstUsableForwardedIP(policy *ClientIPPolicy, header string, values []string) string {
	switch {
	case clientIPHeaderIsChain(header):
		if chain := splitForwardedChain(values); len(chain) > 0 {
			if ip, _, ok := policy.walkForwardedChain(chain); ok {
				return ip
			}
		}
	case clientIPHeaderIsForwarded(header):
		if chain := parseRFC7239Chain(values); len(chain) > 0 {
			if ip, _, ok := policy.walkForwardedChain(chain); ok {
				return ip
			}
		}
	default:
		if ip, ok := NormalizeIPString(values[0]); ok {
			return ip
		}
	}
	return ""
}

// splitForwardedChain 把可能出现的**多个** X-Forwarded-For 头按序拼成一条链。
//
// Go 的 net/http 会把重复的请求头保留成 Header.Values() 的多个元素,而
// Header.Get() 只返回第一个。客户端自带 `X-Forwarded-For: 1.2.3.4`、
// 反代再 add 一个自己的,Get() 拿到的正好是客户端伪造的那条 —— 而伪造的那条
// 里最右边的地址就是攻击者随便写的值。按序全部拼起来之后,反代追加的那一段
// 恒在最右,从右往左剥离依然成立。
func splitForwardedChain(values []string) []string {
	out := make([]string, 0, len(values)+2)
	for _, value := range values {
		for _, item := range strings.Split(value, ",") {
			item = strings.TrimSpace(item)
			if item == "" {
				continue
			}
			out = append(out, item)
		}
	}
	return out
}

// parseRFC7239Chain 解析 `Forwarded: for=1.2.3.4;proto=https, for="[2001:db8::1]:8080"`。
//
// 只取 for= 参数,其余(by / host / proto)与客户端 IP 无关。
func parseRFC7239Chain(values []string) []string {
	out := make([]string, 0, len(values)+2)
	for _, value := range values {
		for _, element := range strings.Split(value, ",") {
			for _, pair := range strings.Split(element, ";") {
				key, val, found := strings.Cut(pair, "=")
				if !found || !strings.EqualFold(strings.TrimSpace(key), "for") {
					continue
				}
				val = strings.TrimSpace(val)
				if val == "" {
					continue
				}
				out = append(out, val)
			}
		}
	}
	return out
}

// walkForwardedChain 从右往左剥离转发链。
//
// 右端是离本进程最近的一跳(每一跳把「连上自己的地址」追加到最右),左端是
// 客户端自己写的、谁都能编的前缀。跳过所有落在受信网段里的地址,第一个不受信的
// 就是真实客户端。
//
// 遇到解析不出来的条目就**整条放弃**,而不是继续往左找:一条从中间就坏掉的链
// 说明左侧内容来源不明,继续往左取等于采信一段来历不明的文本。放弃之后调用方
// 会退到下一个请求头,最终退到直连对端 —— 那是保守的一侧。
func (p *ClientIPPolicy) walkForwardedChain(chain []string) (string, string, bool) {
	for i := len(chain) - 1; i >= 0; i-- {
		ip, ok := NormalizeIPString(chain[i])
		if !ok {
			return "", "", false
		}
		addr, err := netip.ParseAddr(ip)
		if err != nil {
			return "", "", false
		}
		if !p.isTrustedHop(addr) {
			return ip, ClientIPReasonForwardedChain, true
		}
		if i == 0 {
			return ip, ClientIPReasonChainAllTrusted, true
		}
	}
	return "", "", false
}

func collectIgnoredForwardedHeaders(r *http.Request) []ClientIPIgnoredHeader {
	var out []ClientIPIgnoredHeader
	for _, name := range forwardedHeaderNames {
		values := r.Header.Values(name)
		if len(values) == 0 {
			continue
		}
		out = append(out, ClientIPIgnoredHeader{Name: name, Value: strings.Join(values, ", ")})
	}
	return out
}

// ---------------------------------------------------------------------------
// 进程内生效的策略
// ---------------------------------------------------------------------------

var (
	clientIPPolicyMu sync.RWMutex
	clientIPPolicy   *ClientIPPolicy
)

// SetClientIPPolicy 装载策略。只在启动时调用一次(测试里会换)。
func SetClientIPPolicy(p *ClientIPPolicy) {
	clientIPPolicyMu.Lock()
	clientIPPolicy = p
	clientIPPolicyMu.Unlock()
}

// ActiveClientIPPolicy 返回当前策略。nil 表示还没装载 —— 此时一律按
// 「谁都不信」处理(ResolveClientIP 对 nil 策略返回直连对端)。
func ActiveClientIPPolicy() *ClientIPPolicy {
	clientIPPolicyMu.RLock()
	defer clientIPPolicyMu.RUnlock()
	return clientIPPolicy
}

// clientIPContextKey 是解析结果在 gin.Context 里的键。
//
// 每个请求只解析一次:限流、鉴权、台账、日志在一条链上会各取一次,
// 重复解析除了浪费之外还会让「同一请求内两次取值不一致」变成可能
// (中间件如果改了请求头的话)。
const clientIPContextKey = "qy_client_ip_resolution"

// ClientIP 是**全站唯一**的客户端 IP 取法。
//
// 仓库里所有原本写 c.ClientIP() 的地方都改成了这里,理由见文件头。
func ClientIP(c *gin.Context) string {
	return ClientIPResolutionOf(c).IP
}

// ClientIPResolutionOf 返回完整的取值过程(诊断用),并在 gin.Context 上缓存。
func ClientIPResolutionOf(c *gin.Context) ClientIPResolution {
	if c == nil || c.Request == nil {
		return ClientIPResolution{Reason: ClientIPReasonPeerUnparsable}
	}
	if v, ok := c.Get(clientIPContextKey); ok {
		if res, ok := v.(ClientIPResolution); ok {
			return res
		}
	}
	res := ResolveClientIP(ActiveClientIPPolicy(), c.Request)
	c.Set(clientIPContextKey, res)
	return res
}

// ClientIPForAccessLog 返回 gin 访问日志那一列该写的客户端 IP —— 也就是
// **本仓解析出来的**那一个,与审计台账逐字同源。
//
// # 为什么访问日志不能用 gin 自己的那个值
//
// gin.LoggerWithFormatter 传进来的 param.ClientIP 就是 gin 的 c.ClientIP()
// (gin@v1.9.1 logger.go:254),而本文件存在的全部理由就是那个实现在四处给出
// 不同的答案。实测同一条请求上两者的分叉:
//
//	多值 XFF(两个同名头)      台账 203.0.113.5   访问日志 1.2.3.4  ← 客户端伪造的那条
//	链上带端口(host:56789)    台账 203.0.113.5   访问日志 127.0.0.1
//	CLIENT_IP_HEADERS 生效时   台账 203.0.113.44  访问日志 6.6.6.6  ← 客户端自带的 XFF
//	::ffff: 与大写 IPv6        归一化前后不同
//
// 后果不是风格问题:访问日志是运维排查滥用/攻击时最先看的东西,而它与审计台账
// 在同一条请求上给出两个不同来源 IP,且日志那一侧是**攻击者可控**的。
// TRUSTED_PROXIES 让两边看到同一份网段并不能抹平这件事 —— 同一份网段统一的是
// 信任面,不是解析口径。
//
// 走 Keys 而不是再解析一遍:ClientIPResolutionOf 已经把结果缓存在 gin.Context 上,
// 日志格式化器拿到的正是同一个 map,读缓存既省一次解析也保证与台账**逐字**同源。
// 回落参数是 gin 自己那个值:一个日志格式化器不该因为拿不到某个键就让整行
// 日志少一列(中间件还没跑到、或非 HTTP 路径时确实拿不到)。回落写在这里而不是
// 调用方,是为了让 middleware/logger.go 里**一次都不出现** gin 的那个字段名 ——
// 「全站唯一取法」那条守卫因此可以把 param.ClientIP 也一并扫掉,而不需要给
// 访问日志开一个例外(开了例外,回归就再也不会被发现)。
func ClientIPForAccessLog(param gin.LogFormatterParams) string {
	if param.Keys != nil {
		if res, ok := param.Keys[clientIPContextKey].(ClientIPResolution); ok && res.IP != "" {
			return res.IP
		}
	}
	return param.ClientIP
}

// ---------------------------------------------------------------------------
// 「有反代但没配」的观测台
// ---------------------------------------------------------------------------

// ClientIPForwardObservation 是一个**不受信**却带着转发头的直连对端。
//
// 它是诊断,不是判据:结论一个字都不会因为这张表而改变。
// 「反代在公网地址上(CDN 回源、独立 LB 主机)、而 TRUSTED_PROXIES 没配」的站点
// 会静默地把所有人算成反代自己 —— 默认的私网网段覆盖不到那种对端。这张表把
// 那件事变成可见的、带着**可直接粘贴的 CIDR** 的诊断结论,而不是让运维去猜。
type ClientIPForwardObservation struct {
	Peer      string   `json:"peer"`
	Headers   []string `json:"headers"`
	Count     int64    `json:"count"`
	FirstSeen int64    `json:"first_seen"`
	LastSeen  int64    `json:"last_seen"`
	// Suggestion 可以直接写进 TRUSTED_PROXIES 的值。
	Suggestion string `json:"suggestion"`
}

// clientIPObservationCap 上限。观测台是诊断辅助,不是审计:超过上限之后只计
// 丢弃数,不再新增条目,免得被构造流量撑爆内存。
const clientIPObservationCap = 32

var (
	clientIPObserveMu      sync.Mutex
	clientIPObservations   = map[string]*ClientIPForwardObservation{}
	clientIPObserveDropped int64
)

// RecordClientIPObservation 记一条「对端不受信但带了转发头」。
//
// 只在这一种情况下记录:配置正确的站点这里恒为 0 次调用,不上锁;
// 配置错误的站点才会进锁,而那时候本来就需要有人来看这张表。
func RecordClientIPObservation(res ClientIPResolution) {
	if res.PeerTrusted || len(res.Ignored) == 0 || res.Peer == "" {
		return
	}
	names := make([]string, 0, len(res.Ignored))
	for _, h := range res.Ignored {
		names = append(names, h.Name)
	}
	now := time.Now().Unix()

	clientIPObserveMu.Lock()
	defer clientIPObserveMu.Unlock()
	if existing, ok := clientIPObservations[res.Peer]; ok {
		existing.Count++
		existing.LastSeen = now
		existing.Headers = names
		return
	}
	if len(clientIPObservations) >= clientIPObservationCap {
		clientIPObserveDropped++
		return
	}
	clientIPObservations[res.Peer] = &ClientIPForwardObservation{
		Peer:       res.Peer,
		Headers:    names,
		Count:      1,
		FirstSeen:  now,
		LastSeen:   now,
		Suggestion: suggestTrustedProxyCIDR(res.Peer),
	}
}

// suggestTrustedProxyCIDR 把一个观测到的对端变成可以直接粘贴的 TRUSTED_PROXIES 值。
//
// 给的是 /32(或 /128)单机地址,不是它所在的网段:建议一个网段等于替运维
// 决定「这一整段里的东西都可信」,而那是运维自己才做得了的判断。地址不固定的部署
// 自己往上放宽,那是一次显式的决定。
func suggestTrustedProxyCIDR(peer string) string {
	addr, err := netip.ParseAddr(peer)
	if err != nil {
		return ""
	}
	if addr.Is4() {
		return peer + "/32"
	}
	return peer + "/128"
}

// ClientIPObservations 返回观测台快照,按出现次数降序。
func ClientIPObservations() ([]ClientIPForwardObservation, int64) {
	clientIPObserveMu.Lock()
	defer clientIPObserveMu.Unlock()
	out := make([]ClientIPForwardObservation, 0, len(clientIPObservations))
	for _, v := range clientIPObservations {
		out = append(out, *v)
	}
	dropped := clientIPObserveDropped
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Peer < out[j].Peer
	})
	return out, dropped
}

// ResetClientIPObservations 只给测试用。
func ResetClientIPObservations() {
	clientIPObserveMu.Lock()
	clientIPObservations = map[string]*ClientIPForwardObservation{}
	clientIPObserveDropped = 0
	clientIPObserveMu.Unlock()
}

// ---------------------------------------------------------------------------
// 从环境变量构建策略
// ---------------------------------------------------------------------------

// BuildClientIPPolicy 读环境变量构建策略。
//
// 优先级:
//
//  1. 显式的 TRUSTED_PROXIES —— 永远最高优先级,不做任何"聪明"的补充。
//  2. 未配置 —— 用**上游那份默认**:回环 + RFC1918 + fc00::/7,并打一条
//     WARNING。见下。
//
// # 未配置时的默认与上游逐条一致
//
// 上游 new-api 的 middleware/trusted_proxies.go 在未配置时装的是
// {127.0.0.0/8, ::1, 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, fc00::/7}
// 并打一条 WARNING;TRUSTED_PROXIES=none 才谁都不信。**上游没有任何强制**:
// 不拒绝启动,也不因为"看起来装在反代后面"而报错。
//
// 这一档曾经被本仓改成 fail-closed(谁都不信),理由是「私网对端能自带
// X-Forwarded-For 伪造客户端 IP」。那条观察本身没错,但它是**上游的默认取舍**,
// 不是本仓可以替部署者改掉的东西:改掉它的代价是每一个装在反代后面、从没配过
// 这个变量的部署在升级那一秒全站客户端 IP 变成反代地址。
// 现在回到上游默认,风险改由**一条 WARNING + 一个诊断页**表达 —— 也就是上游
// 自己选的那条路,加上一份能照着抄的诊断。
func BuildClientIPPolicy() (*ClientIPPolicy, error) {
	raw := strings.TrimSpace(os.Getenv("TRUSTED_PROXIES"))
	headers, err := parseClientIPHeaders(os.Getenv("CLIENT_IP_HEADERS"))
	if err != nil {
		return nil, err
	}

	if raw == "" {
		source, err := newTrustSource("private", privateTrustedProxyPrefixes, headers)
		if err != nil {
			return nil, err
		}
		return &ClientIPPolicy{
			Strategy: ClientIPStrategyDefaultPrivate,
			Sources:  []ClientIPTrustSource{source},
			Raw:      raw,
			Notice: "TRUSTED_PROXIES is unset: loopback, RFC 1918 and IPv6 ULA peers are trusted " +
				"as reverse proxies for compatibility, and their forwarding headers are honoured.",
			Warning: "TRUSTED_PROXIES is unset or blank; trusting loopback, RFC 1918, and IPv6 ULA " +
				"proxy addresses for compatibility. Anything that can reach this port from those " +
				"ranges can therefore pick its own client IP through X-Forwarded-For, which is what " +
				"token IP allowlists and per-IP rate limits are compared against. " +
				"Set TRUSTED_PROXIES to the proxy IP/CIDR (see GET /api/qy/admin/client-ip for the " +
				"exact value observed on live traffic), or TRUSTED_PROXIES=none to trust no proxies.",
		}, nil
	}

	if strings.EqualFold(raw, "none") {
		return &ClientIPPolicy{
			Strategy: ClientIPStrategyNone,
			Raw:      raw,
			Notice:   "TRUSTED_PROXIES=none: no proxy is trusted, client IP is the direct TCP peer address.",
		}, nil
	}

	policy := &ClientIPPolicy{Strategy: ClientIPStrategyExplicit, Raw: raw}
	var explicitPrefixes []string
	seenKeyword := map[string]bool{}

	for _, part := range strings.Split(raw, ",") {
		token := strings.TrimSpace(part)
		if token == "" {
			continue
		}
		switch {
		case strings.EqualFold(token, "none"):
			return nil, errors.New("TRUSTED_PROXIES=none must be used alone")
		case strings.EqualFold(token, "private"), strings.EqualFold(token, "loopback"), strings.EqualFold(token, "cloudflare"):
			keyword := strings.ToLower(token)
			if seenKeyword[keyword] {
				return nil, fmt.Errorf("TRUSTED_PROXIES lists %q twice", keyword)
			}
			seenKeyword[keyword] = true
			source, err := newKeywordTrustSource(keyword, headers)
			if err != nil {
				return nil, err
			}
			policy.Sources = append(policy.Sources, source)
		default:
			if _, err := parseClientIPPrefix(token); err != nil {
				return nil, fmt.Errorf("invalid TRUSTED_PROXIES entry %q: %w", token, err)
			}
			explicitPrefixes = append(explicitPrefixes, token)
		}
	}

	if len(explicitPrefixes) > 0 {
		source, err := newTrustSource("explicit", explicitPrefixes, headers)
		if err != nil {
			return nil, err
		}
		// 显式网段排在关键字档之前:运维逐条写出来的地址是最强的意图表达,
		// 网段重叠时该由它决定用哪一组请求头。
		policy.Sources = append([]ClientIPTrustSource{source}, policy.Sources...)
	}

	if len(policy.Sources) == 0 {
		return nil, errors.New("TRUSTED_PROXIES does not contain an IP address or CIDR")
	}

	for _, source := range policy.Sources {
		for _, prefix := range source.CIDRs {
			if prefix.Bits() == 0 {
				policy.Warning = fmt.Sprintf(
					"TRUSTED_PROXIES trusts %s, which covers every address: any client can then set "+
						"its own IP through X-Forwarded-For, and token IP allowlists plus per-IP rate "+
						"limits stop being enforceable.", prefix.String())
			}
		}
	}
	policy.Notice = "TRUSTED_PROXIES is set explicitly; forwarding headers are honoured only for peers in the listed ranges."
	return policy, nil
}

func newKeywordTrustSource(keyword string, headers []string) (ClientIPTrustSource, error) {
	switch keyword {
	case "private":
		return newTrustSource("private", privateTrustedProxyPrefixes, headers)
	case "loopback":
		return newTrustSource("loopback", loopbackTrustedProxyPrefixes, headers)
	case "cloudflare":
		prefixes, err := CloudflarePrefixes()
		if err != nil {
			return ClientIPTrustSource{}, err
		}
		// Cloudflare 档忽略 CLIENT_IP_HEADERS:CF-Connecting-IP 是这一档
		// 唯一由边缘覆盖写入、恒为单个客户端地址的头,让它被一个全局配置
		// 顶掉只会制造沉默的错误。
		return newTrustSource("cloudflare", prefixes, cloudflareClientIPHeaders)
	}
	return ClientIPTrustSource{}, fmt.Errorf("unknown TRUSTED_PROXIES keyword %q", keyword)
}

func newTrustSource(name string, prefixes []string, headers []string) (ClientIPTrustSource, error) {
	source := ClientIPTrustSource{Name: name, Headers: headers}
	if len(source.Headers) == 0 {
		source.Headers = defaultClientIPHeaders
	}
	for _, raw := range prefixes {
		prefix, err := parseClientIPPrefix(raw)
		if err != nil {
			return ClientIPTrustSource{}, fmt.Errorf("invalid trusted proxy entry %q: %w", raw, err)
		}
		source.CIDRs = append(source.CIDRs, prefix)
	}
	return source, nil
}

// parseClientIPPrefix 接受 CIDR 与裸 IP 两种写法,裸 IP 按单机地址处理。
func parseClientIPPrefix(raw string) (netip.Prefix, error) {
	token := strings.TrimSpace(raw)
	if strings.Contains(token, "/") {
		prefix, err := netip.ParsePrefix(token)
		if err != nil {
			return netip.Prefix{}, err
		}
		// Masked() 去掉主机位。不这么做的话 10.0.0.5/8 这种写法在
		// netip.Prefix.Contains 下仍然可用,但 String() 回显会带着主机位,
		// 诊断页上看起来像是配错了。
		return prefix.Masked(), nil
	}
	addr, err := netip.ParseAddr(token)
	if err != nil {
		return netip.Prefix{}, err
	}
	addr = addr.Unmap()
	return netip.PrefixFrom(addr, addr.BitLen()), nil
}

// parseClientIPHeaders 解析 CLIENT_IP_HEADERS。
//
// 空值 = 用默认的 X-Forwarded-For, X-Real-IP。
// 显式配置**完全替代**默认值(而不是追加):追加语义会让人以为自己在收紧,
// 实际上在放宽。
func parseClientIPHeaders(raw string) ([]string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	var out []string
	for _, part := range strings.Split(trimmed, ",") {
		name := strings.TrimSpace(part)
		if name == "" {
			continue
		}
		if !isHTTPHeaderToken(name) {
			return nil, fmt.Errorf("invalid CLIENT_IP_HEADERS entry %q: not a valid HTTP header name", name)
		}
		// 原样保留运维写的大小写,不做 http.CanonicalHeaderKey。
		// 查找不需要它(Header.Values 内部就会规范化),而规范化之后
		// `CF-Connecting-IP` 会在诊断页上显示成 `Cf-Connecting-Ip` ——
		// 与厂商文档里的写法对不上,排障的人得先怀疑自己是不是配错了名字。
		out = append(out, name)
	}
	if len(out) == 0 {
		return nil, errors.New("CLIENT_IP_HEADERS does not contain a header name")
	}
	return out, nil
}

func isHTTPHeaderToken(name string) bool {
	if name == "" {
		return false
	}
	const extra = "!#$%&'*+-.^_`|~"
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case strings.ContainsRune(extra, r):
		default:
			return false
		}
	}
	return true
}
