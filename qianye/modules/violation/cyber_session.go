package violation

// cyber_session.go —— cyber 会话自动屏蔽。
//
// 对齐参照实现 sub2api 的一条独立机制,它与 AI 内容审核**不是一回事**:
//
//	AI 审核        判**请求内容**违不违规(护栏模型 / 提示词模型)。
//	本文件         认**上游的拒绝码**(如 OpenAI Codex 后端的 cyber_policy),
//	              命中一次就把**这条会话**在本地拉黑,该会话后续任何请求
//	              (哪怕正常内容)进网关即 403「请开启新会话」,不再打上游。
//
// # 为什么要在本地拦,而不是靠上游每次各自拒
//
// 命中过 cyber_policy 的会话继续发请求时,每一条都要真的打到上游、由上游再拒
// 一次 —— 那既烧上游配额,也给探测方留了一整条可以无限重试的通道。sub2api 的
// 做法(也是本文件的做法)是:第一条被上游拒的请求照常把上游错误回给用户,
// 同时把这条会话记进本地黑名单;从第二条起,本地直接 403,不再打上游。
//
// # 会话身份从哪来
//
// 优先取请求体 prompt_cache_key(codex CLI 每条会话一个 UUID,与
// service/channel_affinity.go 的"codex cli trace"取的是同一个字段),回落到
// session_id / conversation_id 等会话头。混入 user_id 做哈希,屏蔽因此严格
// 隔离在单个账号内 —— 换一条新会话(新 UUID)即可继续,这正是设计意图。
//
// # fail-open,与本模块其它一切一致
//
// 取不到会话身份、Redis 抖动、进程内兜底表满 —— 一律放行。风控是附加物,
// 转发是主业务,拉黑设施故障绝不能把正常请求也挡在门外。存储走 Redis 优先、
// 进程内兜底,形状与 reqrate.go 逐行对应。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/audit"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// ctxKeyCyberSession 缓存本次请求算好的会话哈希:PreRelayGuard 读一次请求体
// 算出来,PostRelayGuard 在 defer 里直接复用 —— 那时请求体可能已被释放,
// 再读一次要么拿到空、要么串到下一个请求的身上。
const ctxKeyCyberSession = constant.ContextKey("qy_violation_cyber_session")

// cyberBlockErrorCode / cyberBlockMessage 是本地拉黑后回给客户端的 403。
//
// 文案刻意中英双语且不含任何内部细节:它会直接出现在 API 响应里,而"为什么被
// 屏蔽"不该由这一行解释(解释等于告诉探测方触发了哪条策略)。
const (
	cyberBlockErrorCode = "session_blocked_by_cyber_policy"
	cyberBlockMessage   = "此会话已被安全策略屏蔽,请开启新会话 / This session is blocked by the security policy; please start a new session"
	// cyberBlockPublicReason 是同一句话在**使用记录**里的形态。
	//
	// 单独一个常量而不是复用 cyberBlockMessage:后者要同时服务国际客户端,
	// 所以是中英双语的一整行;日志那一列前面已经有「会话已被安全策略屏蔽:」
	// 的抬头,再拼一遍双语只会让那一格读不下去。两处口径必须一致 ——
	// 用户先看到 403、再来翻使用记录,说法不一样会让他以为是两回事。
	cyberBlockPublicReason = "此会话已被安全策略屏蔽,请开启新会话"
)

// cyberKeyPrefix 与 reqrate 的命名空间刻意分开,理由同 rateKeyPrefix。
const cyberKeyPrefix = "qy:vio:cyberblk:"

// cyberLogKeyPrefix 是"这条会话的屏蔽已经写过一行使用记录"的节流键。
//
// 与黑名单本身分开的命名空间:两者语义不同(一个是"还拦不拦",一个是"还写不写"),
// 而且管理员手工清掉黑名单时不该顺带让日志重新开始刷。
const cyberLogKeyPrefix = "qy:vio:cyberlog:"

// cyberLogLocalNS 是同一个节流键在**进程内兜底表**里的前缀。
// 兜底表与黑名单共用一张(自带过期清扫与条目上界),靠这个前缀分开。
const cyberLogLocalNS = "log:"

// cyberLogMinWindowSeconds 是两行屏蔽日志之间的最小间隔。
//
// 节流窗口正常取屏蔽 TTL 本身("一个屏蔽周期一行"),这个下界只兜住把 TTL 配得
// 极短的站点:TTL=5 秒 + 客户端硬重试,那就是每 5 秒一行。
const cyberLogMinWindowSeconds = 60

// cyberRedisTimeout 是拉黑读写允许占用 relay 热路径的时间上界,同 rateRedisTimeout。
const cyberRedisTimeout = 200 * time.Millisecond

// cyberLocalMax 是进程内兜底黑名单的条目上界,防止异常流量撑成内存泄漏。
const cyberLocalMax = 100_000

// cyberSessionPrecheck 在转发前判定这条会话是否已被拉黑。
//
// 命中返回 403 拦截错误(由 relay.go 按 relayFormat 序列化);未命中返回 nil。
// 顺带把算好的会话哈希塞进 ctx,供 PostRelayGuard 复用。
//
// rt 非 nil(总开关开着)由调用方保证;这里只做**作用分组**这道闸与会话判定。
func cyberSessionPrecheck(c *gin.Context, info *relaycommon.RelayInfo, rt *cyberRuntime) error {
	defer recoverHot("cyber_session_precheck")

	// 作用分组:这一档流量不受屏蔽就直接放行,连会话身份都不取。比对的是
	// **模型分组**(info.UsingGroup),与同页 AI 审核作用域同口径。
	if !rt.groupManaged(info.UsingGroup) {
		return nil
	}
	raw := cyberSessionRawKey(c)
	if raw == "" {
		// 没有会话身份 = 无法按会话拉黑,也无法命中黑名单。放行。
		return nil
	}
	h := cyberSessionHash(info.UserId, raw)
	common.SetContextKey(c, ctxKeyCyberSession, h)

	if cyberBlocked(c, h) {
		// ── 被屏蔽的请求同样要在「使用记录」里留一行 ──
		//
		// 这里一度什么都不写,理由是"一条被拉黑的会话可能高频重试,会把日志刷爆"。
		// 那条理由只站在运维一侧:站在用户一侧,他刚刚收到一个 403、而使用记录页
		// 一片空白,与规则命中被拦时的处境一模一样(见 usagelog.go 顶部)。
		// 现在两件事都要:写,但**按会话节流**,同一条会话一个屏蔽周期只写一行。
		// 用户要的是"我这次为什么失败了"的解释,一行就够;而"它一直在被拦"
		// 这件事在拉黑那一刻的审计里另有完整记录。
		if claimCyberBlockLog(c, h, rt.ttlSeconds) {
			recordBlockedUsageLog(cyberBlockLogRecord(captureRecordCtx(c, info)))
		}
		return cyberSessionBlockError()
	}
	return nil
}

// cyberBlockLogRecord 把一次"会话已被屏蔽"折成一条**只用于写使用记录**的记录。
//
// 它不会被 persist,也没有对应的 qy_violation_record —— 屏蔽期内的重复请求本来
// 就不该各留一条违规记录(计数只在拉黑那一刻推进一次,见 recordCyberHit)。
// 借 Record 这个形状是因为 blockedUsageLogRow 的入参就是它,而这里要填的字段
// (谁、什么模型、哪个分组、对外文案)与规则命中那条路径逐格相同 ——
// 两条路径写出来的日志行必须长得一样,否则前端得认两种形状。
//
// RecNo 刻意留空:只有开了「计入自动封号计数」时那条 qy_violation_record 才存在,
// 写一个查不到的记录号比不写更糟。管理员要定位靠 admin_info 里的 phase。
func cyberBlockLogRecord(rc recordCtx) *Record {
	return &Record{
		UserId:   rc.UserId,
		Username: rc.Username,
		TokenId:  rc.TokenId, TokenName: rc.TokenName,
		RuleId: 0, RuleName: "cyber 会话屏蔽",
		// 对外文案与 403 响应体里那句话同源:用户先看到响应、再来翻使用记录,
		// 两处说法不一致会让他以为是两回事。
		PublicReason: cyberBlockPublicReason,
		Phase:        PhaseCyberBlock,
		Action:       ActionBlock,
		Blocked:      true,
		ModelName:    rc.ModelName,
		UsingGroup:   rc.UsingGroup,
		ChannelId:    rc.ChannelId,
		RelayFormat:  rc.RelayFormat,
		RequestId:    truncate(rc.RequestId, 64),
		Ip:           rc.Ip,
		Status:       RecordActive,
		FeeStatus:    FeeStatusNone,
		CreatedAt:    common.GetTimestamp(),
	}
}

// claimCyberBlockLog 抢一次"这条会话的屏蔽由我来写进使用记录"的资格。
//
// 每一次被拦的请求都写一行是不行的:客户端并不知道自己被本地拦了,重试往往是
// 几十上百次,那会把使用记录页刷成一整屏同一句话 —— 对用户和运营都是纯噪音。
//
// 节流窗口取**屏蔽本身的 TTL**(下界 cyberLogMinWindowSeconds),不另立一个魔数:
// 这样"一个屏蔽周期一行"是字面意思 —— 会话被拉黑写一行,TTL 到期后若再次被拉黑,
// 又是一行。窗口独立于黑名单键(命名空间不同),因此管理员手工清掉黑名单也不会
// 让日志重新开始刷。
//
// 存储形状与 cyberMark / cyberBlocked 逐行对应:Redis 优先(SetNX 是原子的),
// 抖动时回落进程内那张表。回落路径上"查完再写"不是原子的,多节点或高并发下
// 可能多写一两行 —— 那个方向是安全的(多一行解释),不值得为它加锁。
func claimCyberBlockLog(c *gin.Context, hash string, ttl int) bool {
	window := ttl
	if window < cyberLogMinWindowSeconds {
		window = cyberLogMinWindowSeconds
	}
	if common.RedisEnabled && common.RDB != nil {
		ctx, cancel := context.WithTimeout(cyberCtx(c), cyberRedisTimeout)
		defer cancel()
		ok, err := common.RDB.SetNX(ctx, cyberLogKeyPrefix+hash, "1",
			time.Duration(window)*time.Second).Result()
		if err == nil {
			return ok
		}
	}
	// 进程内兜底复用同一张表,靠命名空间前缀与黑名单本身分开 ——
	// 它自带过期清扫与条目上界,不必再维护第二张。
	key := cyberLogLocalNS + hash
	now := common.GetTimestamp()
	if cyberBlockedLocal(key, now) {
		return false
	}
	cyberMarkLocal(key, now+int64(window))
	return true
}

// maybeBlockCyberSession 在事后判定这次上游拒绝是不是 cyber 命中,是则拉黑本会话。
//
// 触发码走 YAML(violation.cyber_session_block_trigger_codes),它是"上游返回什么
// 算命中"的底层判据;开关/TTL/作用分组走 rt(DB)。
func maybeBlockCyberSession(c *gin.Context, info *relaycommon.RelayInfo, apiErr *types.NewAPIError, rt *cyberRuntime) {
	defer recoverHot("cyber_session_block")

	if !rt.groupManaged(info.UsingGroup) {
		return
	}
	matched := matchesCyberTrigger(rt.triggers, apiErr)
	if matched == "" {
		return
	}
	h := common.GetContextKeyString(c, ctxKeyCyberSession)
	if h == "" {
		// 事后没有会话身份(没带 prompt_cache_key / 会话头):记不下拉黑,
		// 但上游那次拒绝仍然原样回给了用户,不影响本次请求的结果。
		return
	}
	cyberMark(c, h, rt.ttlSeconds)
	common.SysLog(fmt.Sprintf(
		"qianye/violation: 命中 cyber 触发规则 %q,拉黑该会话 %ds(user=%d, group=%s, model=%s, request=%s)",
		matched, rt.ttlSeconds, info.UserId, info.UsingGroup, info.OriginModelName, info.RequestId))
	// 写一条带上下文的管理端审计:运营在「审计日志」页能查到这次拉黑的
	// 谁/哪个模型/哪个分组/哪条规则命中/上游原话,据此做人工复核与针对性配置防御。
	writeCyberBlockAudit(c, info, rt.ttlSeconds, h, matched, apiErr)

	// 计入自动封号计数(可选):落一条违规记录 → 推进该用户的账号总量线与
	// 类型线 → 达阈值即自动受限/封号。复用违规模块现成的计数+封禁闸。
	if rt.countTowardBan {
		recordCyberHit(c, info, rt.categoryId, matched, apiErr)
	}
}

// recordCyberHit 把一次 cyber 命中做成违规记录并交给 persist —— persist 会推进
// 计数并触发 maybeAutoBan。它不是"规则命中"(RuleId=0、有一个稳定的合成规则名),
// 但走的是与规则命中完全相同的持久化+计数+封号链路。
//
// RecNo 用会话哈希:一条会话只会被拉黑一次(之后在 precheck 就被 403 挡下,不再
// 到这里),所以它天然幂等 —— persist 的 RecNo 唯一索引把任何重入路径也一并兜住,
// 计数因此不会重复推进。
func recordCyberHit(c *gin.Context, info *relaycommon.RelayInfo, categoryId int64, matched string, apiErr *types.NewAPIError) {
	rc := captureRecordCtx(c, info)
	sessionHash := common.GetContextKeyString(c, ctxKeyCyberSession)
	snap := Snapshot()
	cat := categoryForRule(snap, categoryId)
	// ── cyber 的「不指定」与规则的「不指定」不是同一件事 ──
	//
	// 规则那边没选类型就是"只拦不罚,不计数"(见 categoryForRule)。cyber 这边
	// 另有一个显式开关 count_toward_ban:管理员打开它,说的就是"这一档要计入
	// 自动封号计数"。此时把类型绑定留空只表示"没挑具体哪一类",不表示"别计数"——
	// 按前者处理会让那个开关变成一个打开了也不起作用的摆设。所以这里显式回落兜底桶。
	if cat.Id <= 0 && snap != nil {
		cat = snap.catFallback
	}
	persist(buildCyberRecord(rc, sessionHash, cat, matched, apiErr), nil)
}

// buildCyberRecord 组装一条 cyber 命中的违规记录。纯函数(不碰 ctx / 库),
// 便于直接断言字段。cat 由调用方用 categoryForRule 解析好再传进来。
func buildCyberRecord(rc recordCtx, sessionHash string, cat Category, matched string, apiErr *types.NewAPIError) *Record {
	return &Record{
		// 一条会话只会被拉黑一次(之后在 precheck 就被 403 挡下),会话哈希天然幂等;
		// RecNo 唯一索引再把任何重入路径兜住,计数不会重复推进。
		RecNo:    truncate("cyber_"+sessionHash, 64),
		UserId:   rc.UserId,
		Username: rc.Username,
		TokenId:  rc.TokenId, TokenName: rc.TokenName,
		RuleId: 0, RuleName: "cyber 会话屏蔽",

		CategoryId:          cat.Id,
		CategoryName:        truncate(cat.Name, 64),
		CategoryPublicTitle: truncate(cat.PublicTitle, 64),

		Phase:  PhaseCyberBlock,
		Action: ActionRecord,
		// 不是影子:我们确实拦了这条会话。Blocked=true 与之呼应。
		Shadow: false, Blocked: true,

		ModelName:   rc.ModelName,
		UsingGroup:  rc.UsingGroup,
		ChannelId:   rc.ChannelId,
		RelayFormat: rc.RelayFormat,
		RequestId:   truncate(rc.RequestId, 64),
		Ip:          rc.Ip,

		MatchedTerms: truncate("cyber:"+matched, 1024),
		MatchSnippet: truncate(redactSnippet(apiErr.Error()), 2048),

		// 计数权重恒为 1:cyber 命中就是"这个用户又踩了一次网络安全线",
		// 一次算一次,与规则的 CountWeight 语义一致。
		CountWeight: 1,
		Status:      RecordActive,
		FeeStatus:   FeeStatusNone,
		CreatedAt:   common.GetTimestamp(),
	}
}

// writeCyberBlockAudit 落一条 cyber 会话拉黑的审计,Category=violation、
// Action=cyber_session_blocked,可在管理端「审计日志」页按类型筛出来。
//
// 触发者是**用户的这次请求**(上游拒绝的),所以 ActorType=user、主体是该用户。
// IP / UA / request_id 由 audit.fillFromContext 自动补齐。上游原话经 redactSnippet
// 脱敏再入库(它常把请求内容原样回抄,可能带邮箱/手机号/密钥)。
func writeCyberBlockAudit(c *gin.Context, info *relaycommon.RelayInfo, ttl int, sessionHash, matched string, apiErr *types.NewAPIError) {
	shortHash := sessionHash
	if len(shortHash) > 16 {
		shortHash = shortHash[:16]
	}
	snap := map[string]any{
		"model":           info.OriginModelName,
		"using_group":     info.UsingGroup,
		"token_id":        info.TokenId,
		"token_name":      c.GetString("token_name"),
		"session_hash":    sessionHash, // 已是 sha256(user|会话原文),不含明文会话
		"ttl_seconds":     ttl,
		"matched_trigger": matched,
		// 上游原话:命中"为什么"的唯一证据。脱敏 + 截断后入库。
		"upstream_error": truncate(redactSnippet(apiErr.Error()), 512),
		"status_code":    apiErr.StatusCode,
	}
	audit.Write(c, audit.Entry{
		Category:    qymodel.AuditCategoryViolation,
		Action:      "cyber_session_blocked",
		ActorType:   qymodel.ActorUser,
		ActorUserId: info.UserId,
		ActorName:   c.GetString("username"),
		Result:      qymodel.ResultOK,
		Reason:      fmt.Sprintf("命中触发规则 %q,会话已拉黑 %d 秒(后续请求 403)", matched, ttl),
		TraceNo:     "cyber:" + shortHash,
		AfterSnap:   common.MapToJsonStr(snap),
	})
}

// cyberSessionRawKey 抽取这次请求的会话身份原文。取不到返回空串。
func cyberSessionRawKey(c *gin.Context) string {
	if c == nil {
		return ""
	}
	// 1) 请求体 prompt_cache_key —— codex CLI 每条会话一个 UUID。
	if storage, err := common.GetBodyStorage(c); err == nil && storage != nil {
		if body, err := storage.Bytes(); err == nil && len(body) > 0 {
			if res := gjson.GetBytes(body, "prompt_cache_key"); res.Exists() {
				if s := strings.TrimSpace(res.String()); s != "" {
					return s
				}
			}
		}
	}
	// 2) 会话头。覆盖常见几种写法(下划线 / 连字符),与 channel_affinity 的口径一致。
	if c.Request != nil {
		for _, name := range []string{"session_id", "session-id", "conversation_id", "x-session-id"} {
			if s := strings.TrimSpace(c.Request.Header.Get(name)); s != "" {
				return s
			}
		}
	}
	return ""
}

// cyberSessionHash 把 (user_id, 会话原文) 折成一个定长键。
//
// 混入 user_id:拉黑严格隔离在单个账号内,一个用户的被封会话不会撞到另一个
// 用户碰巧相同的 session id 上。哈希也顺带让键长恒定、且不把会话原文直接
// 铺进 Redis key。
func cyberSessionHash(userId int, raw string) string {
	sum := sha256.Sum256([]byte(strconv.Itoa(userId) + "|" + raw))
	return hex.EncodeToString(sum[:])
}

// cyberSessionBlockError 构造返回给客户端的 403。两个 ErrOption 都是必须的,
// 理由与 violationBlockError 逐字相同:漏 SkipRetry 会把一次拉黑放大成 N 次
// 上游调用,漏 NoRecordErrorLog 会把它算进渠道错误统计触发渠道自动禁用。
func cyberSessionBlockError() error {
	return types.NewErrorWithStatusCode(
		errors.New(cyberBlockMessage),
		types.ErrorCode(cyberBlockErrorCode),
		http.StatusForbidden,
		types.ErrOptionWithSkipRetry(),
		types.ErrOptionWithNoRecordErrorLog(),
	)
}

// matchesCyberTrigger 判定这次上游拒绝是否算 cyber 命中。
//
// 子串匹配、大小写不敏感,比对**四处**:
//   - apiErr 的错误码:标准 OpenAI 形状 {"error":{"code":"cyber_policy"}} 下,
//     RelayErrorHandler 会把 error.code 原样搬进这里(见 WithOpenAIError),这是最准的一处;
//   - ToOpenAIError().Code / .Message:结构化字段的兜底;
//   - Error():showBodyWhenFail 打开时,上游整段响应体拼在这里 —— 非标准渠道
//     把 cyber_policy 包成别的形状时靠它兜住。
//
// codes 已在 parseCyberTriggers 里折成小写;这里再对 hay 折一次即可。
//
// 返回**命中的那一条规则**(空串 = 没命中)。带出来是为了写进审计上下文:
// 运营复核时要知道"这次是被哪条过滤规则拦下的",才好据此调规则。
func matchesCyberTrigger(codes []string, apiErr *types.NewAPIError) string {
	if apiErr == nil || len(codes) == 0 {
		return ""
	}
	oai := apiErr.ToOpenAIError()
	hay := strings.ToLower(strings.Join([]string{
		string(apiErr.GetErrorCode()),
		fmt.Sprintf("%v", oai.Code),
		oai.Message,
		apiErr.Error(),
	}, " "))
	for _, code := range codes {
		if code != "" && strings.Contains(hay, code) {
			return code
		}
	}
	return ""
}

// ───────────────────────────── 拉黑存储 ─────────────────────────────
//
// Redis 优先、进程内兜底,形状与 reqrate.go 一致。两点与计数器不同,写在这里:
//
//   - 进程内兜底是**每节点各存各的**:N 节点部署下,在节点 A 被拉黑的会话打到
//     节点 B 仍会放行。这比计数器的每节点误差更肉眼可见,但方向仍是 fail-open,
//     且 Redis 才是这功能的正经存储 —— 进程内只是 Redis 抖动时的降级。
//   - 到期即自动解封:Redis 靠 TTL,进程内靠读时判过期 + 写时顺手清扫。

var (
	cyberMu      sync.Mutex
	cyberLocal   = make(map[string]int64) // hash → 过期 unix 秒
	cyberSweptAt int64
)

// cyberMark 把一条会话哈希写进黑名单,存活 ttl 秒。
func cyberMark(c *gin.Context, hash string, ttl int) {
	if ttl <= 0 {
		return
	}
	if common.RedisEnabled && common.RDB != nil {
		ctx, cancel := context.WithTimeout(cyberCtx(c), cyberRedisTimeout)
		defer cancel()
		if err := common.RDB.Set(ctx, cyberKeyPrefix+hash, "1", time.Duration(ttl)*time.Second).Err(); err == nil {
			return
		}
		// Redis 抖动:回落进程内(每节点各存各的),方向仍是 fail-safe 偏保守 ——
		// 顶多某节点上的拉黑在另一节点看不到,不会把正常请求误挡。
	}
	cyberMarkLocal(hash, common.GetTimestamp()+int64(ttl))
}

// cyberBlocked 报告一条会话哈希此刻是否在黑名单里。
func cyberBlocked(c *gin.Context, hash string) bool {
	if common.RedisEnabled && common.RDB != nil {
		ctx, cancel := context.WithTimeout(cyberCtx(c), cyberRedisTimeout)
		defer cancel()
		n, err := common.RDB.Exists(ctx, cyberKeyPrefix+hash).Result()
		if err == nil {
			return n > 0
		}
		// Redis 抖动:回落进程内。查不到即放行(fail-open),与本模块一致。
	}
	return cyberBlockedLocal(hash, common.GetTimestamp())
}

func cyberCtx(c *gin.Context) context.Context {
	if c != nil && c.Request != nil && c.Request.Context() != nil {
		return c.Request.Context()
	}
	return context.Background()
}

func cyberMarkLocal(hash string, expireAt int64) {
	cyberMu.Lock()
	defer cyberMu.Unlock()
	cyberSweepLocked(common.GetTimestamp())
	if _, exists := cyberLocal[hash]; !exists && len(cyberLocal) >= cyberLocalMax {
		// 兜底表满:不再为新会话建条目(等于该会话在本节点放行)。上界只是防
		// 异常流量把它撑成内存泄漏,正常情况下表大小就等于最近一个 TTL 窗口内
		// 被拉黑的会话数。
		return
	}
	cyberLocal[hash] = expireAt
}

func cyberBlockedLocal(hash string, now int64) bool {
	cyberMu.Lock()
	defer cyberMu.Unlock()
	exp, ok := cyberLocal[hash]
	if !ok {
		return false
	}
	if now >= exp {
		delete(cyberLocal, hash)
		return false
	}
	return true
}

// cyberSweepLocked 清掉已过期项。调用方必须持有 cyberMu。
func cyberSweepLocked(now int64) {
	if now-cyberSweptAt < 60 && len(cyberLocal) < cyberLocalMax {
		return
	}
	for h, exp := range cyberLocal {
		if now >= exp {
			delete(cyberLocal, h)
		}
	}
	cyberSweptAt = now
}
