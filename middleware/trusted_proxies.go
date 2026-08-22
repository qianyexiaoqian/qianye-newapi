package middleware

import (
	"errors"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
)

// privateTrustedProxyCIDRs 是 `TRUSTED_PROXIES=private` 这一档的取值。
//
// 它曾经是**未配置时的默认**。改掉的理由见 ConfigureTrustedProxies。
var privateTrustedProxyCIDRs = []string{
	"127.0.0.0/8",
	"::1",
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
	"fc00::/7",
}

// ConfigureTrustedProxies 决定 gin 的 ClientIP() 认不认 X-Forwarded-For。
//
// ═══════════════ 未配置时为什么必须是「谁都不信」 ═══════════════
//
// 这不是一个日志字段的配置,ClientIP() 是**两处安全判据**的取值来源:
//
//	令牌的 allow_ips   middleware/auth.go 的 TokenAuth 与 TokenAuthReadOnly 都直接
//	                   用 c.ClientIP() 比对白名单。而 allow_ips 是用户在密钥泄漏
//	                   之后**唯一**的自助止损手段。
//	按 IP 计的全部限流  GlobalAPIRateLimit / CriticalRateLimit 等等,桶键就是
//	                   ClientIP()。
//
// 此前未配置时的默认是「信任 127.0.0.0/8 + 全部 RFC1918 + fc00::/7」。
// 只要调用方的**直连对端**落在这些网段里(容器网段、K8s Pod 网段、同一 VPC 的
// 其他主机、以及本机),它就能自带一个 X-Forwarded-For 头把 ClientIP() 指成
// 任意值。备份库实测:
//
//   - 令牌 allow_ips=203.0.113.5/32,从 127.0.0.1 直接请求 GET /v1/models
//     不带 XFF → 403;加上 `X-Forwarded-For: 203.0.113.5` → **200**。
//     /api/usage/token/ 与 /api/log/token 同样从 403 变 200。
//   - GlobalAPIRateLimit(360 次/180 秒/IP):不带 XFF 打 /api/status,第 343 条
//     起 429;把 XFF 换成逐条轮换的 10.20.x.x 打 915 次,**一条 429 都没有**。
//   - qy_request_audits.ip 记下的就是伪造的那个值,事后取证的来源 IP 全是假的。
//
// 「默认信任私网」这条默认的问题不在于它宽松,而在于它**默默地**把一个安全判据
// 的取值交给了调用方,而部署者不知道自己需要做任何事。fail-closed 的表现相反且
// 可见:反代后面的部署会看到所有人的 IP 都是反代的 IP,allow_ips 立刻挡人、
// 限流立刻粒度变粗 —— 运维当天就会去配 TRUSTED_PROXIES,而不是三年后才发现
// 白名单一直形同虚设。
//
// 三档取值,都要显式写:
//
//	未设置 / 空       谁都不信。ClientIP() == 直连对端地址,XFF 一律忽略。
//	none              同上,显式表达「我确认前面没有反代」。
//	private           恢复旧默认(回环 + RFC1918 + IPv6 ULA)。给「反代确实在
//	                  私网里、但地址不固定」的部署(容器编排)一条一行的出路。
//	<IP/CIDR 列表>    只信这些。反代地址固定时这才是正确答案。
func ConfigureTrustedProxies(engine *gin.Engine) error {
	rawTrustedProxies := strings.TrimSpace(os.Getenv("TRUSTED_PROXIES"))
	if rawTrustedProxies == "" {
		log.Print("WARNING: TRUSTED_PROXIES is unset; trusting NO proxies. " +
			"X-Forwarded-For is ignored and ClientIP() is the direct peer address. " +
			"If this app sits behind a reverse proxy, token IP allowlists and per-IP rate limits " +
			"will see the proxy's address until you set TRUSTED_PROXIES to the proxy IPs/CIDRs " +
			"(or TRUSTED_PROXIES=private to trust loopback + RFC1918 + IPv6 ULA, the old default). " +
			"Set TRUSTED_PROXIES=none to silence this warning when there is no proxy.")
		return engine.SetTrustedProxies(nil)
	}
	if strings.EqualFold(rawTrustedProxies, "none") {
		return engine.SetTrustedProxies(nil)
	}
	if strings.EqualFold(rawTrustedProxies, "private") {
		// 显式选择的宽松档:任何能直连到本进程、且对端落在私网/回环的东西都能
		// 伪造 X-Forwarded-For。只有当这些网段里除了自家反代之外没有别的东西
		// 能打到这个端口时,它才是安全的。
		log.Print("TRUSTED_PROXIES=private: trusting loopback, RFC 1918 and IPv6 ULA peers. " +
			"Anything that can reach this port from those ranges can forge the client IP " +
			"used by token IP allowlists and per-IP rate limits.")
		return engine.SetTrustedProxies(privateTrustedProxyCIDRs)
	}

	parts := strings.Split(rawTrustedProxies, ",")
	trustedProxies := make([]string, 0, len(parts))
	for _, part := range parts {
		trustedProxy := strings.TrimSpace(part)
		if trustedProxy == "" {
			continue
		}
		if strings.EqualFold(trustedProxy, "none") {
			return errors.New("TRUSTED_PROXIES=none must be used alone")
		}
		if strings.EqualFold(trustedProxy, "private") {
			return errors.New("TRUSTED_PROXIES=private must be used alone")
		}
		trustedProxies = append(trustedProxies, trustedProxy)
	}
	if len(trustedProxies) == 0 {
		return errors.New("TRUSTED_PROXIES does not contain an IP address or CIDR")
	}
	if err := engine.SetTrustedProxies(trustedProxies); err != nil {
		return fmt.Errorf("invalid TRUSTED_PROXIES: %w", err)
	}
	return nil
}
