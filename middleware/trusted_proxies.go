package middleware

import (
	"fmt"
	"log"
	"strings"

	"github.com/QuantumNous/new-api/common"

	"github.com/gin-gonic/gin"
)

// ConfigureTrustedProxies 装载全站的客户端 IP 取值策略。
//
// 取值逻辑本身住在 common/client_ip.go(那里是全站唯一的实现,理由见该文件头)。
// 这里只做三件事:
//
//  1. 从环境变量构建策略并装载到进程里;
//  2. 把同一份受信网段交给 gin 的 SetTrustedProxies —— gin 自带的访问日志
//     中间件仍然会调用它自己的 ClientIP(),让两边看到同一份网段,可以避免
//     访问日志里的 IP 与台账里的 IP 对不上;
//  3. **把当前用的是哪一档策略打进启动日志**。
//
// 第 3 条不是装饰。这个配置的失败模式全都是沉默的:配窄了,全站客户端 IP 变成
// 反代地址,令牌 allow_ips 开始挡人而没有任何一处报错;配宽了,任何能打到端口
// 的东西都能伪造来源 IP,同样没有任何一处报错。启动日志里一行「现在用的是哪档、
// 信任哪些网段、代价是什么」,是这条配置唯一常驻的信号。
func ConfigureTrustedProxies(engine *gin.Engine) error {
	policy, err := common.BuildClientIPPolicy()
	if err != nil {
		return err
	}
	common.SetClientIPPolicy(policy)

	if err := engine.SetTrustedProxies(policy.AllCIDRStrings()); err != nil {
		return fmt.Errorf("invalid TRUSTED_PROXIES: %w", err)
	}

	logClientIPPolicy(policy)
	return nil
}

func logClientIPPolicy(policy *common.ClientIPPolicy) {
	descriptions := make([]string, 0, len(policy.Sources))
	for _, source := range policy.Sources {
		descriptions = append(descriptions, fmt.Sprintf("%s(%d ranges via %s)",
			source.Name, len(source.CIDRs), strings.Join(source.Headers, "/")))
	}
	trusted := "nothing"
	if len(descriptions) > 0 {
		trusted = strings.Join(descriptions, ", ")
	}
	line := fmt.Sprintf("client IP policy: strategy=%s trusting=%s. %s",
		policy.Strategy, trusted, policy.Notice)
	if policy.Warning != "" {
		log.Print("WARNING: " + line + " " + policy.Warning)
		return
	}
	log.Print(line)
}

// ClientIPResolver 在请求链最前面解析一次客户端 IP。
//
// 必须挂在**全部**其他中间件之前:限流、鉴权、台账都要读这个值,而它们各自
// 取一次会重复解析同一个请求头(还给"同一请求内两次取值不一致"留了缝)。
// 这里解析一次并缓存进 gin.Context,之后所有 common.ClientIP(c) 都读缓存。
//
// 它同时是「装在反代后面但没配 TRUSTED_PROXIES」的观测点:对端不受信却带着
// 转发头的请求会被记进观测台,管理端 GET /api/qy/admin/client-ip 会把该填哪个
// CIDR 直接给出来。这是提示,不是判据 —— 结论一个字都不会因为观测台而改变。
func ClientIPResolver() gin.HandlerFunc {
	return func(c *gin.Context) {
		common.RecordClientIPObservation(common.ClientIPResolutionOf(c))
		c.Next()
	}
}
