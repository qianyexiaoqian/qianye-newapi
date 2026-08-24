package audit

// middleware.go —— 写请求的统一兜底留痕。
//
// # 为什么必须有中间件,而不是继续加手写埋点
//
// 资金审计(qy_audit_logs)全靠调用点手写 audit.Write。它在**每一处写对了的
// 地方**都很好,问题在于漏掉的地方 —— 漏一处就是永久盲区,而且这个盲区无法
// 被任何测试发现:没有埋点的接口不会报错,只会在事故复盘时安静地什么都查不到。
// 实测本扩展有 31 处手写埋点,而至少 8 类写操作(新增/删除收款账号、上传打款
// 凭证、申诉审核、索取支付密码重置码……)一处埋点都没有。
//
// 中间件把默认值从"没有留痕"翻成"有留痕":新加的接口只要挂在 /api/qy 下,
// 天然进台账。手写埋点仍然不可替代 —— 它记的是**判定**(按什么费率、
// 前后快照、冻结汇率),中间件记的是**调用**(谁、什么时候、成没成功)。
//
// # 挂载位置
//
// 挂在 /api/qy 根组上,即 UserAuth/AdminAuth **之前**。
// 于是它 c.Next() 之后既能读到认证中间件写进 context 的身份,
// 又能记下被认证挡掉的 401/403 —— 后者正是越权探测的形状,
// 挂在认证之后就会把这类请求整个漏掉。

import (
	"bytes"
	"io"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/gin-gonic/gin"
)

// sensitiveReads 是需要留痕的 GET 读取(键为 "METHOD 路由模板")。
//
// 默认不记 GET:列表与详情每天几千次,记下来只会把台账稀释到无法扫读。
// 进这张表的判据有两条:
//
//  1. **这次读取本身泄露了受保护的明文**;
//  2. 或者它不是一次读取,而是一次**站点对外的行为**。
//
// 第二条是为 check-update 加的。它把本站的一次出站请求发给 github.com,而正是
// "这是一次站点行为,不是一次数据读取"这句话把它提到了超级管理员。可是提档之后
// 台账只留下**被拒**的越权尝试(RootActionGate 拒绝时写审计),成功的那一次
// 一行都不写 —— 实测该路径下 401 有 4 行、403 有 28 行、200 有 **0** 行,
// 而离线/内网部署事后最想追问的恰恰是"是谁、在什么时候替这台机器连了 github.com"。
// gin 的访问日志有时间和 IP,但没有任何身份列,答不出"是谁"。
// 这条路由天然稀少(手动点击 + 关键操作限流),不会稀释台账。
var sensitiveReads = map[string]bool{
	"GET /api/qy/admin/withdraw/:id/payee":   true, // 收款信息明文解密
	"GET /api/qy/admin/withdraw/:id/proof":   true, // 打款凭证原图
	"GET /api/qy/admin/withdraw/pii-audits":  true, // 谁查过明文,这份名册本身也要留痕
	"GET /api/qy/admin/version/check-update": true, // 站点替自己向 github.com 开一次出站连接
}

// credentialBodyRoutes 列出请求体**整体**由凭证构成的路由。
//
// 键级脱敏在这些路由上不够用:支付密码接口的 body 除了密码几乎没有别的字段,
// 而收款账号接口的开户行、备注这类自由文本里就嵌着账号本身。
// 与其赌清单能覆盖每一个字段名,不如整体不入库 —— 这些接口的"调用事实"
// (谁、何时、成没成功)才是台账要的,body 本身没有排障价值。
var credentialBodyRoutes = map[string]bool{
	"POST /api/qy/pay-password":                      true,
	"PUT /api/qy/pay-password":                       true,
	"POST /api/qy/pay-password/recover/reset":        true,
	"POST /api/qy/withdraw/payees":                   true,
	"POST /api/qy/admin/pay-password/:user_id/reset": true,
}

// targetUserParams 是"这次操作针对哪个用户"的路径参数名。
//
// 三种写法并存不是疏忽,是既有路由的事实(:user_id 在 paypass、:userId 在
// violation)。在这里统一,好过去改那些路由 —— 改路由会破坏前端已有的调用。
var targetUserParams = []string{"user_id", "userId", "uid"}

// Middleware 返回写请求台账中间件。
func Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !config.Get().Audit.RequestOn() {
			c.Next()
			return
		}
		plan := planRequestAudit(c.Request.Method, c.FullPath())
		if !plan.wanted() {
			c.Next()
			return
		}

		body := captureBody(c, c.Request.Method+" "+c.FullPath())
		start := time.Now()
		c.Next()
		if row := plan.rowFor(c, body, time.Since(start)); row != nil {
			Record(row)
		}
	}
}

// requestAuditPlan 是「这次请求要不要进台账」的决定。
//
// 单独拆出来不是为了少写几行:测试夹具要让请求真的经过 gin 路由才验得到
// c.FullPath() 与 c.Params,而夹具若自己抄一份判据,生产代码这边改错了
// 测试照样全绿 —— 那正是这份台账最不能出的一类错。两边共用这一个函数。
type requestAuditPlan struct {
	// always:无条件留痕(全部写方法 + 白名单内的敏感读取)。
	always bool
	// onlyIfDenied:成功就不记、被 401/403 挡下才记。
	onlyIfDenied bool
}

func (p requestAuditPlan) wanted() bool { return p.always || p.onlyIfDenied }

// rowFor 在 c.Next() 之后决定这一行到底写不写,返回 nil 表示不写。
func (p requestAuditPlan) rowFor(c *gin.Context, body string, latency time.Duration) *qymodel.RequestAudit {
	if !p.always && p.onlyIfDenied && !isAuthDenial(c.Writer.Status()) {
		return nil
	}
	if !p.wanted() {
		return nil
	}
	return buildRequestAudit(c, body, latency)
}

func planRequestAudit(method, path string) requestAuditPlan {
	always := shouldRecord(method, method+" "+path)
	// 被挡下来的管理端读取也要留痕。「挨个戳一遍管理端接口」里有一多半是 GET,
	// 原先一条都不记,于是最该被抓的那个形状在台账里只剩下写接口那几行。
	// 只在 401/403 时才记:成功的列表/详情每天几千次,记下来会把台账稀释到
	// 无法扫读,而「有人在探边界」天然稀少。
	//
	// 刻意只覆盖 /api/qy/admin/:受限账号的浏览器会持续轮询若干用户端只读接口,
	// 把它们也记下来只会把真正的信号淹掉(与 admitRestrictedUser 同一条理由)。
	return requestAuditPlan{
		always:       always,
		onlyIfDenied: !always && method == "GET" && strings.HasPrefix(path, "/api/qy/admin/"),
	}
}

// shouldRecord 判断这条路由要不要无条件进台账:全部写方法 + 白名单内的敏感读取。
func shouldRecord(method, routeKey string) bool {
	switch method {
	case "POST", "PUT", "PATCH", "DELETE":
		return true
	case "GET":
		return sensitiveReads[routeKey]
	default:
		return false
	}
}

// isAuthDenial 只认鉴权层的两个状态码。404/500 是业务与故障,不属于探测信号。
func isAuthDenial(status int) bool {
	return status == 401 || status == 403
}

// captureBody 读出请求体前缀做脱敏,并把它原样回填,让 handler 仍能读到完整 body。
//
// 回填走 io.MultiReader(已读前缀, 原 body) 而不是把整个 body 读进内存再包回去:
// 一次导入请求可能有几 MB,全量复制两份是白白的内存放大。多读一个字节是为了
// 让 RedactBody 能分辨"恰好等于上限"和"超过上限"。
func captureBody(c *gin.Context, routeKey string) string {
	if credentialBodyRoutes[routeKey] {
		return "<credential-bearing body omitted>"
	}
	if c.Request.Body == nil || c.Request.Method == "GET" {
		return ""
	}
	orig := c.Request.Body
	raw, err := io.ReadAll(io.LimitReader(orig, BodyCaptureLimit+1))
	if err != nil {
		// 读失败时绝不能把 Body 换掉:那会让 handler 读到一个空 body,
		// 于是一次读取故障被放大成一次业务失败。台账丢一条 body 就够了。
		return "<body capture failed>"
	}
	c.Request.Body = &restoredBody{
		Reader: io.MultiReader(bytes.NewReader(raw), orig),
		closer: orig,
	}
	return RedactBody(raw, c.GetHeader("Content-Type"))
}

// restoredBody 把按上限读出的前缀与未读完的原始 body 拼回去;Close 委托给原始 body。
type restoredBody struct {
	io.Reader
	closer io.Closer
}

func (b *restoredBody) Close() error { return b.closer.Close() }

// buildRequestAudit 组装一行台账。
//
// 导出给测试的是行为而不是这个函数:它接 *gin.Context,而 gin 的
// httptest 上下文构造在测试里比在生产代码里更容易读。
func buildRequestAudit(c *gin.Context, body string, latency time.Duration) *qymodel.RequestAudit {
	path := c.FullPath()
	if path == "" {
		// 理论上走不到:gin 的 NoRoute 不会经过路由组中间件。留着是为了
		// 万一将来这个中间件被挂到别处,台账里也不至于是一列空路径。
		path = c.Request.URL.Path
	}
	status := c.Writer.Status()
	// 身份优先取鉴权放行后写进 context 的那一组;取不到时回落到
	// common.DeniedActor*(凭据验过了、随后被 status/role 挡掉的那个人)。
	//
	// 没有这条回落,一个已登录的 role=1 账号挨个戳管理端接口留下的 403 行,
	// 与真匿名扫描的 401 行在 actor_user_id / actor_role / actor_type /
	// actor_name / auth_method 五列上逐列相同(全空),只剩一个可伪造的 IP;
	// 而 actorType 又会把这批行读成「匿名探测」,方向性地误导仲裁人。
	// 这个中间件被挂在鉴权之前的全部理由就是要抓这一类,不能只剩下 IP。
	actorUserId := c.GetInt("id")
	actorName := c.GetString("username")
	actorRole := c.GetInt("role")
	useAccessToken := c.GetBool("use_access_token")
	if actorUserId <= 0 {
		if deniedId := c.GetInt(common.DeniedActorIdKey); deniedId > 0 {
			actorUserId = deniedId
			actorName = c.GetString(common.DeniedActorNameKey)
			actorRole = c.GetInt(common.DeniedActorRoleKey)
			useAccessToken = c.GetBool(common.DeniedActorAccessTokenKey)
		}
	}
	row := &qymodel.RequestAudit{
		Action:      DeriveAction(c.Request.Method, path),
		Method:      c.Request.Method,
		Path:        Truncate(path, 256),
		StatusCode:  status,
		Success:     status < 400,
		LatencyMs:   latency.Milliseconds(),
		ActorUserId: actorUserId,
		ActorName:   Truncate(actorName, 64),
		ActorRole:   actorRole,
		Params:      Truncate(redactParams(c), 512),
		Query:       RedactQuery(c.Request.URL.RawQuery),
		Body:        body,
		RequestId:   Truncate(c.GetString(common.RequestIdKey), 64),
		NodeName:    common.NodeName,
		CreatedAt:   common.GetTimestamp(),
	}

	if useAccessToken {
		row.AuthMethod = qymodel.AuthMethodAccessToken
	} else if row.ActorUserId > 0 {
		row.AuthMethod = qymodel.AuthMethodSession
	}
	row.ActorType = actorType(row.ActorUserId, row.ActorRole)
	row.TargetUserId = targetUserId(c)

	if config.Get().Audit.ShouldRecordIP() {
		row.IP = Truncate(common.ClientIP(c), 64)
		row.UserAgent = Truncate(c.Request.UserAgent(), 256)
	}
	return row
}

// actorType 复用 qy_audit_logs 的三值口径。
//
// 未认证请求(被 401 挡掉的)记 system 是错的 —— system 的含义是"补偿任务/
// 结算任务干的",把匿名探测算进去会污染"这是人干的还是程序干的"这个判定。
// 所以匿名一律留空:空值本身就是"不知道是谁",比一个错的分类诚实。
//
// # 分类只看**身份**,不看路径
//
// 这里曾经写成 `role >= RoleAdminUser || strings.HasPrefix(path, "/api/qy/admin/")`。
// 那个 || 的右半边在**鉴权通过**的行上恒为冗余(/api/qy/admin 整组都挂着
// AdminAuth,能走到 handler 的必然 role>=10),它唯一能改变结果的场合恰恰是
// **被拒的越权探测** —— 一个 role=1 的普通账号挨个戳管理端接口留下的 403,
// 在 actor_type 列上与真管理员的操作完全同色。
//
// 实测线上台账:actor_type='admin' 的行里有 810 行 actor_role=1,且这 810 行
// 100% 是 401/403。后果是仲裁人按 actor_type='admin' 过滤会把普通用户的越权
// 探测当成管理员操作读(过采),按 actor_type='user' 过滤则把这批行整体漏掉
// (漏采);而筛选清单里没有 actor_role,这条歧义补偿不了。
//
// 本表的字段注释还写明"两张表的『操作者』必须是同一个概念",而 qy_audit_logs
// 那一侧的 ActorAdmin 全部由 admin handler 用 c.GetInt("id") 手写,那里的
// 操作者必然 role>=10 —— 按路径判会让两张表的同名列指两件事。
//
// "碰的是哪个面"由 path 列与 DeriveAction 的前缀(admin.violation.rules.update)
// 完整回答,不需要 actor_type 再表达一遍。
func actorType(userId, role int) string {
	if userId <= 0 {
		return ""
	}
	if role >= common.RoleAdminUser {
		return qymodel.ActorAdmin
	}
	return qymodel.ActorUser
}

// targetUserId 从路径参数里取"这次操作针对谁"。
func targetUserId(c *gin.Context) int {
	for _, name := range targetUserParams {
		if v := c.Param(name); v != "" {
			// 走 httpq 那套上界解析的兄弟形态:这里的输入是路径参数而不是
			// 查询参数,且失败即当作"没有目标用户",不需要再报错给调用方。
			if id, ok := parsePositiveInt(v); ok {
				return id
			}
		}
	}
	return 0
}

// parsePositiveInt 解析一个不超过 int32 的正整数,失败返回 false。
//
// 手工按字符累加而不是 strconv:请求参数的解析必须走 qianye/httpq 那一份,
// 而 httpq 只服务查询参数与分页;这里是台账的旁路补充信息,解析失败的唯一
// 后果是 target_user_id 记 0。上界卡在 int32 是因为目标列是 int。
func parsePositiveInt(s string) (int, bool) {
	if s == "" || len(s) > 10 {
		return 0, false
	}
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int(r-'0')
		if n > 2147483647 {
			return 0, false
		}
	}
	if n <= 0 {
		return 0, false
	}
	return n, true
}

// redactParams 把路径参数序列化成 JSON,并按同一套键名判据脱敏。
func redactParams(c *gin.Context) string {
	if len(c.Params) == 0 {
		return ""
	}
	params := make(map[string]string, len(c.Params))
	for _, p := range c.Params {
		if IsSensitiveKey(p.Key) {
			params[p.Key] = redactedPlaceholder
			continue
		}
		params[p.Key] = p.Value
	}
	encoded, err := common.Marshal(params)
	if err != nil {
		return ""
	}
	return string(encoded)
}

// DeriveAction 由 method + 路由模板推导稳定的动作名。
//
//	POST /api/qy/admin/withdraw/:id/approve → admin.withdraw.approve.create
//
// 动词一律后缀,即使读起来别扭(approve.create)。理由是可预测性:
// 只要规则里出现"某些情况下不加动词",这条规则就会在下一个接口上被重新解释,
// 而台账最怕的就是同一个接口在两次改动之间换了名字 —— 那会让按 action
// 做的历史查询悄悄漏掉前半段。method 列本身已经能区分动作。
func DeriveAction(method, fullPath string) string {
	path := strings.TrimPrefix(fullPath, "/api/qy/")
	path = strings.Trim(path, "/")
	parts := make([]string, 0, 8)
	for _, seg := range strings.Split(path, "/") {
		if seg == "" || strings.HasPrefix(seg, ":") || strings.HasPrefix(seg, "*") {
			continue
		}
		parts = append(parts, strings.ReplaceAll(seg, "-", "_"))
	}
	verb := strings.ToLower(method)
	switch method {
	case "POST":
		verb = "create"
	case "PUT", "PATCH":
		verb = "update"
	case "DELETE":
		verb = "delete"
	case "GET":
		verb = "read"
	}
	if len(parts) == 0 {
		return verb
	}
	return Truncate(strings.Join(parts, ".")+"."+verb, 128)
}
