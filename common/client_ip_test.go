package common

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// client_ip_test.go —— 「各种部署形态下都能拿到正确的 IP」这条要求的机器校验。
//
// 每一条用例都对应一种真实部署形态,注释写清那个形态下代理**实际**会发什么头。
// 断言的是最终 IP、命中的受信来源与结论来源,三者一起才说明"取对了而且是按
// 正确的理由取对的" —— 只断言 IP 的话,一条"凑巧右端就是答案"的错误实现
// (例如无条件取 XFF 最右)能全绿通过。

// newClientIPRequest 造一条请求。headers 里同一个键可以给多个值,
// 那正是「客户端自带一个 XFF、反代再追加一个」的形状。
func newClientIPRequest(remoteAddr string, headers map[string][]string) *http.Request {
	request := httptest.NewRequest(http.MethodGet, "/anything", nil)
	request.RemoteAddr = remoteAddr
	for name, values := range headers {
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}
	return request
}

func buildPolicyForTest(t *testing.T, trustedProxies, clientIPHeaders, bindAddress string) *ClientIPPolicy {
	t.Helper()
	t.Setenv("TRUSTED_PROXIES", trustedProxies)
	t.Setenv("CLIENT_IP_HEADERS", clientIPHeaders)
	t.Setenv("BIND_ADDRESS", bindAddress)
	t.Setenv("TRUSTED_PROXIES_CLOUDFLARE_FILE", "")
	policy, err := BuildClientIPPolicy()
	require.NoError(t, err)
	return policy
}

// TestResolveClientIPDeploymentShapes 逐一覆盖任务里点名的部署形态。
func TestResolveClientIPDeploymentShapes(t *testing.T) {
	testCases := []struct {
		name string
		// trustedProxies / clientIPHeaders / bindAddress 是这套部署的环境变量。
		trustedProxies  string
		clientIPHeaders string
		bindAddress     string
		remoteAddr      string
		headers         map[string][]string
		wantIP          string
		wantSource      string
		wantHeader      string
		wantReason      string
	}{
		{
			// 直连,没有任何反代,客户端自己塞一个 XFF 想冒充别人。
			// 未配置 TRUSTED_PROXIES → 用上游默认(回环 + RFC1918 + fc00::/7),
			// 公网对端不在里面,转发头一律作废。
			name:       "direct exposure ignores a self-supplied X-Forwarded-For",
			remoteAddr: "203.0.113.5:41234",
			headers:    map[string][]string{"X-Forwarded-For": {"10.0.0.9"}},
			wantIP:     "203.0.113.5",
			wantReason: ClientIPReasonDirectPeer,
		},
		{
			// 同机 nginx。未配置 TRUSTED_PROXIES 时上游默认就信任回环对端,
			// 所以这一档开箱即用 —— 不需要 BIND_ADDRESS,也不需要任何配置。
			// nginx 默认 `proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for`。
			name:       "same-host nginx needs no configuration under the upstream default",
			remoteAddr: "127.0.0.1:52100",
			headers:    map[string][]string{"X-Forwarded-For": {"203.0.113.5"}},
			wantIP:     "203.0.113.5",
			wantSource: "private",
			wantHeader: "X-Forwarded-For",
			wantReason: ClientIPReasonForwardedChain,
		},
		{
			// **上游默认的代价,写出来而不是藏着。**
			// 未配置时 RFC1918 一律受信,于是任何能从容器网桥 / K8s Pod 网段 /
			// 同 VPC 主机打到这个端口的东西,都可以用一个 X-Forwarded-For 决定
			// 自己在令牌 allow_ips 与限流桶里的取值。
			// 这是上游选的取舍,本仓照抄:堵法是 TRUSTED_PROXIES=none 或写死网段,
			// 而不是替部署者改掉默认。
			name:       "the upstream default lets any RFC1918 peer choose its own client IP",
			remoteAddr: "172.17.0.9:52100",
			headers:    map[string][]string{"X-Forwarded-For": {"203.0.113.5"}},
			wantIP:     "203.0.113.5",
			wantSource: "private",
			wantHeader: "X-Forwarded-For",
			wantReason: ClientIPReasonForwardedChain,
		},
		{
			// 标准 Nginx / Caddy / Traefik:客户端自带一段伪造前缀,反代把
			// 「连上自己的那个地址」追加在最右。从右往左剥离,伪造前缀作废。
			name:           "nginx appends the real client to a forged prefix",
			trustedProxies: "10.8.0.2/32",
			remoteAddr:     "10.8.0.2:41000",
			headers:        map[string][]string{"X-Forwarded-For": {"1.2.3.4, 203.0.113.5"}},
			wantIP:         "203.0.113.5",
			wantSource:     "explicit",
			wantHeader:     "X-Forwarded-For",
			wantReason:     ClientIPReasonForwardedChain,
		},
		{
			// Docker / K8s 多层转发:ingress → sidecar → 应用。中间两跳都在
			// 受信网段里,要一路跳过去才能拿到真实客户端。
			name:           "kubernetes ingress plus sidecar strips both internal hops",
			trustedProxies: "10.42.0.0/16,10.43.0.0/16",
			remoteAddr:     "10.43.0.7:33000",
			headers: map[string][]string{
				"X-Forwarded-For": {"198.51.100.7, 10.42.0.3, 10.43.0.9"},
			},
			wantIP:     "198.51.100.7",
			wantSource: "explicit",
			wantHeader: "X-Forwarded-For",
			wantReason: ClientIPReasonForwardedChain,
		},
		{
			// Cloudflare 直连回源。CF-Connecting-IP 由边缘覆盖写入,恒为单个地址。
			name:           "cloudflare edge is read from CF-Connecting-IP",
			trustedProxies: "cloudflare",
			remoteAddr:     "172.68.1.1:44000",
			headers: map[string][]string{
				"CF-Connecting-IP": {"203.0.113.77"},
				"X-Forwarded-For":  {"1.2.3.4, 203.0.113.77"},
			},
			wantIP:     "203.0.113.77",
			wantSource: "cloudflare",
			wantHeader: "CF-Connecting-IP",
			wantReason: ClientIPReasonForwardedHeader,
		},
		{
			// Cloudflare → 自建 nginx → 应用。对端是 nginx(private 档),
			// 而链上要跳过的是 **cloudflare 档** 的边缘地址。
			// 跳过判据必须用全部受信网段的并集,只按对端那一档判会停在 CF 边缘上。
			name:           "cloudflare behind nginx skips the edge hop through the union of trusted ranges",
			trustedProxies: "private,cloudflare",
			remoteAddr:     "172.20.0.9:41000",
			headers: map[string][]string{
				"X-Forwarded-For": {"203.0.113.88, 172.68.1.1"},
			},
			wantIP:     "203.0.113.88",
			wantSource: "private",
			wantHeader: "X-Forwarded-For",
			wantReason: ClientIPReasonForwardedChain,
		},
		{
			// **这一条是厂商头必须绑定来源的理由。**
			// nginx 默认原样透传客户端带来的任意请求头,所以 CF-Connecting-IP
			// 在「没有 Cloudflare、只有 nginx」的部署里是客户端可控的。
			// private 档的请求头列表里没有它,于是它必须被忽略,结论只能来自 XFF。
			name:           "a forged CF-Connecting-IP through plain nginx is ignored",
			trustedProxies: "private",
			remoteAddr:     "172.20.0.9:41000",
			headers: map[string][]string{
				"CF-Connecting-IP": {"9.9.9.9"},
				"X-Forwarded-For":  {"203.0.113.5"},
			},
			wantIP:     "203.0.113.5",
			wantSource: "private",
			wantHeader: "X-Forwarded-For",
			wantReason: ClientIPReasonForwardedChain,
		},
		{
			// 同上,但连对端都不受信:CF-Connecting-IP 连看都不该看一眼。
			name:       "a forged CF-Connecting-IP from an untrusted peer is ignored",
			remoteAddr: "198.51.100.30:41000",
			headers:    map[string][]string{"CF-Connecting-IP": {"9.9.9.9"}},
			wantIP:     "198.51.100.30",
			wantReason: ClientIPReasonDirectPeer,
		},
		{
			// 其它 CDN(Akamai True-Client-IP / Fastly-Client-IP)。
			// 必须显式配 CLIENT_IP_HEADERS 才生效 —— 默认不认,理由同上一条。
			name:            "akamai True-Client-IP is honoured only when configured",
			trustedProxies:  "23.32.0.0/11",
			clientIPHeaders: "True-Client-IP,X-Forwarded-For",
			remoteAddr:      "23.45.67.89:41000",
			headers: map[string][]string{
				"True-Client-IP":  {"203.0.113.44"},
				"X-Forwarded-For": {"1.2.3.4, 203.0.113.44"},
			},
			wantIP:     "203.0.113.44",
			wantSource: "explicit",
			wantHeader: "True-Client-IP",
			wantReason: ClientIPReasonForwardedHeader,
		},
		{
			// **多值请求头。** 客户端自带一个 XFF、反代 add 了第二个,
			// Go 会把它们保留成两个头。gin 的 Header.Get() 只取第一个,
			// 也就是客户端伪造的那条,于是"最右端"变成了攻击者写的值。
			// 这里按顺序全部拼起来,反代追加的那一段恒在最右。
			name:           "a second X-Forwarded-For header appended by the proxy still wins",
			trustedProxies: "10.8.0.2/32",
			remoteAddr:     "10.8.0.2:41000",
			headers: map[string][]string{
				"X-Forwarded-For": {"1.2.3.4", "203.0.113.5"},
			},
			wantIP:     "203.0.113.5",
			wantSource: "explicit",
			wantHeader: "X-Forwarded-For",
			wantReason: ClientIPReasonForwardedChain,
		},
		{
			// IPv4-mapped IPv6。归一化必须折回点分十进制,否则同一个客户端
			// 会占两个限流桶、在台账里显示成两个来源。
			name:           "IPv4-mapped IPv6 collapses to the dotted form",
			trustedProxies: "10.8.0.2/32",
			remoteAddr:     "10.8.0.2:41000",
			headers:        map[string][]string{"X-Forwarded-For": {"::ffff:203.0.113.5"}},
			wantIP:         "203.0.113.5",
			wantSource:     "explicit",
			wantHeader:     "X-Forwarded-For",
			wantReason:     ClientIPReasonForwardedChain,
		},
		{
			// 带端口的 XFF 条目(Azure App Service、部分云 LB)。
			// gin 的 net.ParseIP 在这里解析失败并放弃整条链。
			name:           "an X-Forwarded-For entry carrying a port is still parsed",
			trustedProxies: "10.8.0.2/32",
			remoteAddr:     "10.8.0.2:41000",
			headers:        map[string][]string{"X-Forwarded-For": {"203.0.113.5:56789"}},
			wantIP:         "203.0.113.5",
			wantSource:     "explicit",
			wantHeader:     "X-Forwarded-For",
			wantReason:     ClientIPReasonForwardedChain,
		},
		{
			// IPv6 客户端 + 带括号带端口的写法,并且大小写/缩写都要归一。
			name:           "a bracketed IPv6 client with a port is canonicalised",
			trustedProxies: "fd00:dead:beef::/48",
			remoteAddr:     "[fd00:dead:beef::9]:41000",
			headers:        map[string][]string{"X-Forwarded-For": {"[2001:DB8:0000:0000::1]:9999"}},
			wantIP:         "2001:db8::1",
			wantSource:     "explicit",
			wantHeader:     "X-Forwarded-For",
			wantReason:     ClientIPReasonForwardedChain,
		},
		{
			// 带 zone 的地址。zone 是**本机**的接口名,跨进程没有意义,必须丢掉,
			// 否则同一个地址会因为不同的 zone 后缀分裂成多个限流桶。
			name:           "an IPv6 zone suffix is dropped",
			trustedProxies: "10.8.0.2/32",
			remoteAddr:     "10.8.0.2:41000",
			headers:        map[string][]string{"X-Forwarded-For": {"fe80::1%eth0"}},
			wantIP:         "fe80::1",
			wantSource:     "explicit",
			wantHeader:     "X-Forwarded-For",
			wantReason:     ClientIPReasonForwardedChain,
		},
		{
			// 只配了 X-Real-IP 的 nginx(`proxy_set_header X-Real-IP $remote_addr`,
			// 没配 XFF)。这是覆盖写,客户端带来的值到不了这里。
			name:           "X-Real-IP is used when no X-Forwarded-For arrives",
			trustedProxies: "10.8.0.2/32",
			remoteAddr:     "10.8.0.2:41000",
			headers:        map[string][]string{"X-Real-IP": {"203.0.113.5"}},
			wantIP:         "203.0.113.5",
			wantSource:     "explicit",
			wantHeader:     "X-Real-IP",
			wantReason:     ClientIPReasonForwardedHeader,
		},
		{
			// 两个头都在时以 XFF 链为准:它带着完整的跳数信息,X-Real-IP 没有。
			name:           "X-Forwarded-For wins over X-Real-IP",
			trustedProxies: "10.8.0.2/32",
			remoteAddr:     "10.8.0.2:41000",
			headers: map[string][]string{
				"X-Real-IP":       {"9.9.9.9"},
				"X-Forwarded-For": {"203.0.113.5"},
			},
			wantIP:     "203.0.113.5",
			wantSource: "explicit",
			wantHeader: "X-Forwarded-For",
			wantReason: ClientIPReasonForwardedChain,
		},
		{
			// RFC 7239 Forwarded(部分企业代理只发这个头)。
			name:            "RFC 7239 Forwarded is parsed when configured",
			trustedProxies:  "10.8.0.2/32",
			clientIPHeaders: "Forwarded",
			remoteAddr:      "10.8.0.2:41000",
			headers: map[string][]string{
				"Forwarded": {`for=1.2.3.4;proto=https, for="[2001:db8::5]:8080";proto=https`},
			},
			wantIP:     "2001:db8::5",
			wantSource: "explicit",
			wantHeader: "Forwarded",
			wantReason: ClientIPReasonForwardedChain,
		},
		{
			// 反代受信但一个转发头都没配(漏了 proxy_set_header)。
			// 结论只能是对端自己,而 reason 必须能把这件事说出来 ——
			// 否则运维看到「全站 IP 都是 10.8.0.2」会先去怀疑受信网段配错了。
			name:           "a trusted proxy that forwards nothing is reported as such",
			trustedProxies: "10.8.0.2/32",
			remoteAddr:     "10.8.0.2:41000",
			wantIP:         "10.8.0.2",
			wantSource:     "explicit",
			wantReason:     ClientIPReasonTrustedNoHeader,
		},
		{
			// 受信网段配得过宽(把客户端网段也算进去了):链上每一跳都受信。
			// 取最左端并用一个**不同的** reason 报出来,这是"网段配宽了"的信号。
			name:           "an all-trusted chain falls back to the leftmost entry with its own reason",
			trustedProxies: "10.0.0.0/8",
			remoteAddr:     "10.8.0.2:41000",
			headers:        map[string][]string{"X-Forwarded-For": {"10.1.1.1, 10.2.2.2"}},
			wantIP:         "10.1.1.1",
			wantSource:     "explicit",
			wantHeader:     "X-Forwarded-For",
			wantReason:     ClientIPReasonChainAllTrusted,
		},
		{
			// 链从中间就坏掉(垃圾条目)。右侧已经确认全是受信跳,再往左的内容
			// 来历不明 —— 整条链作废,退到下一个头(这里是 X-Real-IP)。
			name:           "a corrupt chain entry voids the chain and falls through to the next header",
			trustedProxies: "10.8.0.0/16",
			remoteAddr:     "10.8.0.2:41000",
			headers: map[string][]string{
				"X-Forwarded-For": {"not-an-ip, 10.8.0.3"},
				"X-Real-IP":       {"203.0.113.5"},
			},
			wantIP:     "203.0.113.5",
			wantSource: "explicit",
			wantHeader: "X-Real-IP",
			wantReason: ClientIPReasonForwardedHeader,
		},
		{
			// 同上但没有兜底的头:必须退到直连对端,而不是采信半条链。
			name:           "a corrupt chain with no fallback header lands on the peer",
			trustedProxies: "10.8.0.0/16",
			remoteAddr:     "10.8.0.2:41000",
			headers:        map[string][]string{"X-Forwarded-For": {"not-an-ip, 10.8.0.3"}},
			wantIP:         "10.8.0.2",
			wantSource:     "explicit",
			wantReason:     ClientIPReasonTrustedNoHeader,
		},
		{
			// TRUSTED_PROXIES=none:显式声明前面没有反代。
			name:           "none ignores every forwarding header",
			trustedProxies: "none",
			remoteAddr:     "127.0.0.1:41000",
			headers: map[string][]string{
				"X-Forwarded-For":  {"203.0.113.5"},
				"CF-Connecting-IP": {"203.0.113.6"},
				"X-Real-IP":        {"203.0.113.7"},
			},
			wantIP:     "127.0.0.1",
			wantReason: ClientIPReasonDirectPeer,
		},
		{
			// 显式清单**替代**而不是叠加:配了 10.8.0.2 之后,回环不再自动受信。
			name:           "an explicit list replaces rather than extends the defaults",
			trustedProxies: "10.8.0.2/32",
			remoteAddr:     "127.0.0.1:41000",
			headers:        map[string][]string{"X-Forwarded-For": {"203.0.113.5"}},
			wantIP:         "127.0.0.1",
			wantReason:     ClientIPReasonDirectPeer,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			policy := buildPolicyForTest(t, testCase.trustedProxies, testCase.clientIPHeaders, testCase.bindAddress)
			resolution := ResolveClientIP(policy, newClientIPRequest(testCase.remoteAddr, testCase.headers))

			assert.Equal(t, testCase.wantIP, resolution.IP, "resolved client IP")
			assert.Equal(t, testCase.wantSource, resolution.TrustSource, "trust source")
			assert.Equal(t, testCase.wantHeader, resolution.Header, "header the answer came from")
			assert.Equal(t, testCase.wantReason, resolution.Reason, "reason")
			assert.Equal(t, testCase.wantSource != "", resolution.PeerTrusted, "peer trusted")
		})
	}
}

// TestResolveClientIPUntrustedPeerNeverYieldsAForgedAddress 是这一组里的 blocker。
//
// 上一轮的实测形状:令牌 allow_ips=203.0.113.5/32,从本机加一个
// `X-Forwarded-For: 203.0.113.5` 请求 /v1/models,从 403 变 200。
// 这条断言把「对端不受信时,任何转发头都不能改变结论」钉死成一条一眼看得懂的
// 表格 —— 覆盖全部已知的客户端 IP 请求头,不只是 XFF。
func TestResolveClientIPUntrustedPeerNeverYieldsAForgedAddress(t *testing.T) {
	policy := buildPolicyForTest(t, "", "", "")

	for _, header := range forwardedHeaderNames {
		t.Run(header, func(t *testing.T) {
			request := newClientIPRequest("198.51.100.30:41000", map[string][]string{
				header: {"203.0.113.5"},
			})
			resolution := ResolveClientIP(policy, request)
			assert.Equal(t, "198.51.100.30", resolution.IP,
				"%s 来自不受信对端时绝不能变成客户端 IP —— 那等于让任何能直连到本端口的"+
					"东西自己决定令牌 IP 白名单与限流桶的取值", header)
			assert.False(t, resolution.PeerTrusted)
			assert.Equal(t, ClientIPReasonDirectPeer, resolution.Reason)
			assert.Len(t, resolution.Ignored, 1, "被丢掉的头必须进诊断,否则这件事没有任何一处看得见")
		})
	}
}

// TestNormalizeIPString 单独锁归一化。
//
// 它是判据的一部分而不是显示层:allow_ips 与限流桶拿到同一个地址的不同写法
// 会得到不同结论,而这不需要攻击者做任何事就会自然发生。
func TestNormalizeIPString(t *testing.T) {
	testCases := []struct {
		name  string
		raw   string
		want  string
		valid bool
	}{
		{name: "plain IPv4", raw: "203.0.113.5", want: "203.0.113.5", valid: true},
		{name: "IPv4 with port", raw: "203.0.113.5:41234", want: "203.0.113.5", valid: true},
		{name: "IPv4-mapped IPv6", raw: "::ffff:203.0.113.5", want: "203.0.113.5", valid: true},
		{name: "IPv4-mapped IPv6 with port", raw: "[::ffff:203.0.113.5]:80", want: "203.0.113.5", valid: true},
		{name: "IPv6", raw: "2001:db8::1", want: "2001:db8::1", valid: true},
		{name: "IPv6 uppercase and padded", raw: "2001:0DB8:0000:0000:0000:0000:0000:0001", want: "2001:db8::1", valid: true},
		{name: "IPv6 bracketed with port", raw: "[2001:db8::1]:443", want: "2001:db8::1", valid: true},
		{name: "IPv6 bracketed without port", raw: "[2001:db8::1]", want: "2001:db8::1", valid: true},
		{name: "IPv6 with zone", raw: "fe80::1%eth0", want: "fe80::1", valid: true},
		{name: "quoted RFC 7239 value", raw: `"203.0.113.5"`, want: "203.0.113.5", valid: true},
		{name: "surrounding whitespace", raw: "  203.0.113.5  ", want: "203.0.113.5", valid: true},
		{name: "empty", raw: "", valid: false},
		{name: "hostname", raw: "proxy.example.com", valid: false},
		{name: "unknown RFC 7239 token", raw: "_hidden", valid: false},
		{name: "CIDR is not an address", raw: "203.0.113.0/24", valid: false},
		{name: "bare number", raw: "12345", valid: false},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			got, ok := NormalizeIPString(testCase.raw)
			require.Equal(t, testCase.valid, ok)
			if testCase.valid {
				assert.Equal(t, testCase.want, got)
			}
		})
	}
}

// TestBuildClientIPPolicyRejectsInvalidConfiguration 守「配错了就起不来」。
//
// 这条配置的两个失败方向都是沉默的,所以它的语法错误必须**响亮**:
// 一个被静默忽略的错字会让运维以为自己配好了,而实际生效的是 fail-closed
// (或者更糟,上一档的宽松值)。
func TestBuildClientIPPolicyRejectsInvalidConfiguration(t *testing.T) {
	testCases := []struct {
		name            string
		trustedProxies  string
		clientIPHeaders string
	}{
		{name: "no usable entry", trustedProxies: ", ,"},
		{name: "not an address", trustedProxies: "not-an-ip"},
		{name: "one bad entry among good ones", trustedProxies: "127.0.0.1, not-an-ip"},
		{name: "none mixed with an address", trustedProxies: "none,127.0.0.1"},
		{name: "address mixed with none", trustedProxies: "127.0.0.1,NONE"},
		{name: "hostname instead of address", trustedProxies: "proxy.example.com"},
		{name: "duplicate keyword", trustedProxies: "private,private"},
		{name: "bad prefix length", trustedProxies: "10.0.0.0/33"},
		{name: "header name with a space", trustedProxies: "private", clientIPHeaders: "X Forwarded For"},
		{name: "empty header list", trustedProxies: "private", clientIPHeaders: " , "},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv("TRUSTED_PROXIES", testCase.trustedProxies)
			t.Setenv("CLIENT_IP_HEADERS", testCase.clientIPHeaders)
			t.Setenv("BIND_ADDRESS", "")
			_, err := BuildClientIPPolicy()
			assert.Error(t, err)
		})
	}
}

// TestBuildClientIPPolicyStrategies 锁默认值的决策表。
//
// 这一张表就是「默认值方案」本身,而它现在**逐条等于上游**:
// 显式配置最高优先级;未配置就用上游那份私网默认并打 WARNING;
// TRUSTED_PROXIES=none 才谁都不信。BIND_ADDRESS 不再参与这个判断 ——
// 上游没有那一档,本仓也不再自己加。
func TestBuildClientIPPolicyStrategies(t *testing.T) {
	testCases := []struct {
		name           string
		trustedProxies string
		bindAddress    string
		wantStrategy   string
		wantSources    []string
		wantWarning    bool
	}{
		{
			// 未配置 = 上游默认 + WARNING。这是本轮撤回的核心那一行。
			name:         "unset falls back to the upstream private default and warns",
			wantStrategy: ClientIPStrategyDefaultPrivate,
			wantSources:  []string{"private"},
			wantWarning:  true,
		},
		{
			// BIND_ADDRESS 是监听地址,不是信任判据 —— 上游从不读它。
			// 这三条钉死「回环 bind 不再改变策略」,防止那个自动档被重新加回来。
			name:         "a loopback BIND_ADDRESS no longer changes the strategy",
			bindAddress:  "127.0.0.1",
			wantStrategy: ClientIPStrategyDefaultPrivate,
			wantSources:  []string{"private"},
			wantWarning:  true,
		},
		{
			name:         "an IPv6 loopback BIND_ADDRESS no longer changes the strategy",
			bindAddress:  "::1",
			wantStrategy: ClientIPStrategyDefaultPrivate,
			wantSources:  []string{"private"},
			wantWarning:  true,
		},
		{
			name:         "a routable BIND_ADDRESS no longer changes the strategy",
			bindAddress:  "10.0.0.5",
			wantStrategy: ClientIPStrategyDefaultPrivate,
			wantSources:  []string{"private"},
			wantWarning:  true,
		},
		{
			// 显式配置压过默认:运维写了什么就是什么,不做任何"聪明"的补充。
			name:           "an explicit list replaces the default",
			trustedProxies: "10.8.0.2/32",
			bindAddress:    "127.0.0.1",
			wantStrategy:   ClientIPStrategyExplicit,
			wantSources:    []string{"explicit"},
		},
		{
			name:           "none replaces the default",
			trustedProxies: "none",
			bindAddress:    "127.0.0.1",
			wantStrategy:   ClientIPStrategyNone,
		},
		{
			// 显式网段排在关键字档之前:网段重叠时由逐条写出来的地址决定用哪组头。
			name:           "explicit ranges are matched before keyword ranges",
			trustedProxies: "private,10.8.0.2/32",
			wantStrategy:   ClientIPStrategyExplicit,
			wantSources:    []string{"explicit", "private"},
		},
		{
			// 0.0.0.0/0 是合法配置(有人真的要),但必须带警告:
			// 它让任何客户端都能自己决定自己的 IP。
			name:           "trusting every address is allowed but warns",
			trustedProxies: "0.0.0.0/0",
			wantStrategy:   ClientIPStrategyExplicit,
			wantSources:    []string{"explicit"},
			wantWarning:    true,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			policy := buildPolicyForTest(t, testCase.trustedProxies, "", testCase.bindAddress)
			assert.Equal(t, testCase.wantStrategy, policy.Strategy)

			names := make([]string, 0, len(policy.Sources))
			for _, source := range policy.Sources {
				names = append(names, source.Name)
			}
			assert.Equal(t, testCase.wantSources, nilIfEmpty(names))
			assert.Equal(t, testCase.wantWarning, policy.Warning != "", "warning presence")
			assert.NotEmpty(t, policy.Notice, "每一档都必须能说出自己是什么档 —— 启动日志靠它")
		})
	}
}

// TestUnsetTrustedProxiesMatchesUpstreamDefaultsExactly 是本轮撤回的**判据**。
//
// 上游 middleware/trusted_proxies.go 在 TRUSTED_PROXIES 未配置时装的是
// defaultTrustedProxyCIDRs = {127.0.0.0/8, ::1, 10.0.0.0/8, 172.16.0.0/12,
// 192.168.0.0/16, fc00::/7},并打一条 WARNING。本仓前两轮把这一档改成了
// 「谁都不信」——那是一次行为变更,现在撤回。
//
// 这条测试逐条比对那份清单(`::1` 写成等价的 `::1/128`,netip.Prefix 需要位数),
// 并要求:未配置时必须有 WARNING、必须只有一档来源、且不因为 BIND_ADDRESS
// 或 CLIENT_IP_HEADERS 而改变网段。
func TestUnsetTrustedProxiesMatchesUpstreamDefaultsExactly(t *testing.T) {
	// 逐字抄自上游 defaultTrustedProxyCIDRs,只把 `::1` 补成 `::1/128`。
	upstreamDefaults := []string{
		"127.0.0.0/8",
		"::1/128",
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"fc00::/7",
	}

	for _, bindAddress := range []string{"", "127.0.0.1", "0.0.0.0", "10.0.0.5"} {
		t.Run("BIND_ADDRESS="+bindAddress, func(t *testing.T) {
			policy := buildPolicyForTest(t, "", "", bindAddress)

			require.Len(t, policy.Sources, 1, "未配置时只有一档来源")
			assert.Equal(t, "private", policy.Sources[0].Name)
			assert.Equal(t, upstreamDefaults, policy.Sources[0].CIDRStrings(),
				"未配置 TRUSTED_PROXIES 时信任的网段必须与上游 defaultTrustedProxyCIDRs 逐条相同")
			assert.Equal(t, upstreamDefaults, policy.AllCIDRStrings(),
				"交给 gin SetTrustedProxies 的那一份也必须是同一份")
			assert.NotEmpty(t, policy.Warning,
				"上游在这一档打的是 WARNING,本仓必须也打得出来 —— 那是这条默认唯一常驻的信号")
			assert.Equal(t, []string{"X-Forwarded-For", "X-Real-IP"}, policy.Sources[0].Headers)
		})
	}
}

func nilIfEmpty(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	return values
}

// TestRecordClientIPObservationSuggestsTheExactCIDR 守那条**提示**。
//
// 未配置时用的是上游默认(回环 + RFC1918 + fc00::/7),它覆盖不到反代坐在
// 公网地址上的部署(CDN 回源、独立 LB 主机):那种站点一切照常 200,只是所有人
// 的 IP 都成了反代的地址。观测台就是这一档的解药,它必须给出**可以直接粘贴**
// 的值 —— 只说"配置有问题"等于把人推回去猜。它只提示,不改变任何结论。
func TestRecordClientIPObservationSuggestsTheExactCIDR(t *testing.T) {
	ResetClientIPObservations()
	t.Cleanup(ResetClientIPObservations)

	policy := buildPolicyForTest(t, "", "", "")
	for i := 0; i < 3; i++ {
		RecordClientIPObservation(ResolveClientIP(policy, newClientIPRequest("198.51.100.5:41000",
			map[string][]string{"X-Forwarded-For": {"203.0.113.5"}})))
	}
	RecordClientIPObservation(ResolveClientIP(policy, newClientIPRequest("[2001:db8::9]:41000",
		map[string][]string{"X-Real-IP": {"203.0.113.6"}})))
	// 没带转发头的直连请求不该进观测台:它不是"有反代却没配",它就是直连。
	RecordClientIPObservation(ResolveClientIP(policy, newClientIPRequest("203.0.113.9:41000", nil)))
	// 私网对端在上游默认下**是受信的**,它没有任何错配可报 —— 进了观测台
	// 就是噪声,而噪声会让这张表失去它唯一的作用。
	RecordClientIPObservation(ResolveClientIP(policy, newClientIPRequest("172.18.0.5:41000",
		map[string][]string{"X-Forwarded-For": {"203.0.113.7"}})))

	observations, dropped := ClientIPObservations()
	require.Len(t, observations, 2)
	assert.Zero(t, dropped)

	assert.Equal(t, "198.51.100.5", observations[0].Peer)
	assert.EqualValues(t, 3, observations[0].Count)
	assert.Equal(t, "198.51.100.5/32", observations[0].Suggestion)
	assert.Equal(t, []string{"X-Forwarded-For"}, observations[0].Headers)

	assert.Equal(t, "2001:db8::9", observations[1].Peer)
	assert.Equal(t, "2001:db8::9/128", observations[1].Suggestion)
}

// TestRecordClientIPObservationIsBounded 守观测台不会被构造流量撑爆内存。
func TestRecordClientIPObservationIsBounded(t *testing.T) {
	ResetClientIPObservations()
	t.Cleanup(ResetClientIPObservations)

	policy := buildPolicyForTest(t, "", "", "")
	for i := 1; i <= clientIPObservationCap+5; i++ {
		request := newClientIPRequest(
			// 每条一个不同的对端,正是"被构造流量撑爆"的形状。
			fmt.Sprintf("198.51.100.%d:41000", i),
			map[string][]string{"X-Forwarded-For": {"203.0.113.5"}})
		RecordClientIPObservation(ResolveClientIP(policy, request))
	}

	observations, dropped := ClientIPObservations()
	assert.Len(t, observations, clientIPObservationCap)
	assert.EqualValues(t, 5, dropped, "超出上限的条数必须计数,否则运维会以为自己看到的是全部")
}

// 只配了 X-Real-IP 的 nginx 是文档表格里明确列出的一种部署形态
// (`proxy_set_header X-Real-IP $remote_addr;`,不配 XFF)。那种 nginx 会把
// 客户端**自带的** X-Forwarded-For **原样透传**给上游 —— 要删掉必须显式写
// `proxy_set_header X-Forwarded-For "";`。而默认头顺序是
// [X-Forwarded-For, X-Real-IP],于是客户端只要自己加一个 XFF,就能顶掉反代
// 诚实写下的 X-Real-IP。
//
// 实测(隔离实例 + 真实令牌):allow_ips=203.0.113.5/32 时,只带反代写的
// X-Real-IP: 198.51.100.7 → 403;再加一个客户端自造的
// X-Forwarded-For: 203.0.113.5 → **200**。限流侧同形:带诚实 X-Real-IP +
// 轮换 XFF 打 420 次,一条 429 都没有;不带头的对照组第 361 次就 429。
//
// 结论**不**在这一层改(按声明顺序取值是这层的契约,悄悄换一个头会让另一批
// 配置正确的站点结果跟着变)。这里钉的是两件事:
//   - 正确的配置(显式声明 CLIENT_IP_HEADERS=X-Real-IP)必须真的免疫;
//   - 错配时必须留下**确诊信号**,让诊断页说得出"反代写了 A、客户端塞了 B、
//     我们用了 B",而不是让运维对着一个看起来正常的结果查一天。
func TestXRealIPOnlyProxyIsDiagnosableAndFixableByDeclaringTheHeader(t *testing.T) {
	const (
		honest = "198.51.100.7" // 反代写进 X-Real-IP 的真实客户端
		forged = "203.0.113.5"  // 客户端自己塞进 XFF 的值
	)

	newReq := func() *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		r.RemoteAddr = "127.0.0.1:40001"
		r.Header.Set("X-Real-IP", honest)
		r.Header.Set("X-Forwarded-For", forged)
		return r
	}

	t.Run("默认头顺序:客户端的 XFF 赢,但必须留下冲突信号", func(t *testing.T) {
		policy := buildPolicyForTest(t, "loopback", "", "")
		res := ResolveClientIP(policy, newReq())

		require.Equal(t, forged, res.IP, "这是当前契约(按声明顺序取值),先把它钉住")
		assert.Equal(t, "X-Forwarded-For", res.Header)
		require.Len(t, res.Conflicts, 1,
			"反代写的 X-Real-IP 与客户端塞的 XFF 给出两个不同的答案,"+
				"这是「只配了 X-Real-IP 的 nginx」错配唯一的确诊信号 —— 必须记下来")
		assert.Equal(t, "X-Real-IP", res.Conflicts[0].Header)
		assert.Equal(t, honest, res.Conflicts[0].IP)
	})

	t.Run("显式声明 CLIENT_IP_HEADERS=X-Real-IP:客户端的 XFF 再也够不着", func(t *testing.T) {
		policy := buildPolicyForTest(t, "loopback", "X-Real-IP", "")
		res := ResolveClientIP(policy, newReq())

		assert.Equal(t, honest, res.IP,
			"这就是那种部署的正确配置:声明之后 XFF 根本不进判据")
		assert.Equal(t, "X-Real-IP", res.Header)
		assert.Empty(t, res.Conflicts, "只声明了一个头,没有第二个答案可冲突")
	})

	t.Run("两个头一致时不算冲突", func(t *testing.T) {
		policy := buildPolicyForTest(t, "loopback", "", "")
		r := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		r.RemoteAddr = "127.0.0.1:40001"
		r.Header.Set("X-Real-IP", honest)
		r.Header.Set("X-Forwarded-For", honest)
		res := ResolveClientIP(policy, r)

		assert.Equal(t, honest, res.IP)
		assert.Empty(t, res.Conflicts,
			"标准 nginx(两个头都写、且写的是同一个人)是最常见的形态,"+
				"它绝不能天天报冲突 —— 那会让这个信号变成噪声")
	})
}
