package controller

import (
	"github.com/QuantumNous/new-api/common"

	"github.com/gin-gonic/gin"
)

// client_ip.go —— 管理端「客户端 IP 识别」诊断端点。
//
// # 这一页回答的问题
//
// 客户端 IP 是四类判据的取值来源(令牌 allow_ips、按 IP 的限流、审计台账、
// 风控去重),而它的失败模式**全是沉默的**:
//
//	配窄了(该信的代理没信)  全站客户端 IP 变成反代自己的地址。令牌 allow_ips
//	                        开始挡人,按 IP 的限流把所有人算成一个人,台账里
//	                        每一行来源都一样。没有任何一处报错。
//	配宽了(不该信的信了)    任何能打到端口的东西加一个 X-Forwarded-For 就能
//	                        把这四个判据指成任意值。同样没有任何一处报错。
//
// 排障时真正要回答的不是「我被识别成了什么 IP」——那个值在台账里就有——而是
// **「为什么是这个值」**:直连对端是谁、它落在哪一档受信网段里、结论是从哪个
// 请求头取的、转发链长什么样、哪些头被丢掉了。少了这几项,运维只能在
// TRUSTED_PROXIES 里试错,而每次试错都要重启进程。
//
// # 为什么还要下发观测台
//
// 未配置 TRUSTED_PROXIES 时默认是 fail-closed(谁都不信)。这是安全的一侧,
// 但它的代价落在「装在反代后面又从没配过」的站点上:一切照常 200,只是所有人
// 的 IP 都成了反代的地址。observations 就是这一档的解药 —— 它记下「直连对端
// 不受信、却带着转发头」的那些对端,并直接给出可以粘进 TRUSTED_PROXIES 的
// CIDR。把一个沉默的错配变成一条带着答案的诊断。
//
// # 为什么是管理端
//
// 响应里有完整的受信网段清单(信任面本身),那是攻击者最想知道的一件事:
// 知道哪些网段被信任,就知道从哪里打过来可以伪造来源 IP。所以这条挂在
// AdminAuth 后面,而不是做成人人可查的「查我的 IP」。
func AdminClientIP(c *gin.Context) {
	policy := common.ActiveClientIPPolicy()
	observations, dropped := common.ClientIPObservations()

	sources := make([]gin.H, 0, 4)
	strategy := ""
	raw := ""
	notice := ""
	warning := ""
	if policy != nil {
		strategy = policy.Strategy
		raw = policy.Raw
		notice = policy.Notice
		warning = policy.Warning
		for _, source := range policy.Sources {
			sources = append(sources, gin.H{
				"name":    source.Name,
				"headers": source.Headers,
				"cidrs":   source.CIDRStrings(),
			})
		}
	}

	ok(c, gin.H{
		// request 是**这一条请求**的完整取值过程。管理员自己就是一个真实样本:
		// 他从浏览器点开这一页,看到的 peer / trust_source / header 就是线上
		// 每一条业务请求会走的同一条路径。
		"request": common.ClientIPResolutionOf(c),
		"policy": gin.H{
			"strategy": strategy,
			"raw":      raw,
			"notice":   notice,
			"warning":  warning,
			"sources":  sources,
		},
		"cloudflare_source": common.CloudflareSnapshotSource,
		// observations 恒为数组而不是 null:前端 .map 不需要再判一次空,
		// 而 nil slice 在 Go 里会被 encoding/json 写成 null。
		"observations":         append([]common.ClientIPForwardObservation{}, observations...),
		"observations_dropped": dropped,
	})
}
