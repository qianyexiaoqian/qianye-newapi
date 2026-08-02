package transfer

import (
	"encoding/json" // 仅取 RawMessage 类型;编解码一律走 common.*
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/audit"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// api_admin_config.go —— 划转门槛的管理端读写接口。
//
// 形状照抄 commission/api_admin.go(已过四轮审计),四条硬性约束逐条对齐:
//
//  1. 校验**全部前置**,再统一落库 —— 一半写进去一半 400,会留下一组
//     谁都没批准过的门槛;
//  2. 整批包进**一个事务**,键名**排序后写入** —— 两个管理员同时保存互相
//     交叉的键集时,乱序写正是死锁的配方;
//  3. **失败也要写审计**,且 after 取**回滚后重读库**的真实值 ——
//     "有人在这一刻试图改门槛、失败了"正是最需要留痕的事实;
//  4. 白名单之外的键让整个请求 400,而不是静默忽略。

// adminGetTransferConfig 返回生效门槛 + 运营覆盖 + 取值区间 + YAML 只读快照。
//
// 分成两块是刻意的:YAML 段(总闸、收款人解析口径、保留期)涉及安全与启动行为,
// 只能改文件后重载;门槛类运营参数才允许在这里改(裁决 5)。
func adminGetTransferConfig(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagTransfer) {
		return
	}
	ctx := c.Request.Context()
	overrides, err := loadOverrides(ctx)
	if err != nil {
		respondErr(c, err)
		return
	}
	// 用 resolveSettings 而不是 effectiveCtx:库里现存的组合非法时,这一页
	// 恰恰是修复它的唯一界面,不能跟着一起打不开。valid 一并下发,让界面
	// 明确告诉运营"划转当前是停的,请先修好这组值"。
	s, valid, err := resolveSettings(ctx)
	if err != nil {
		respondErr(c, err)
		return
	}
	cfg := config.Get().Transfer
	respondOK(c, gin.H{
		"effective":       settingsSnapshot(s),
		"effective_valid": valid,
		"overrides":       overrides,
		"editable_keys":   editableKeys,
		// 取值区间由后端下发,前端不再自己抄一份:两份区间迟早会漂移成
		// "界面允许、后端 400"或者更糟的"界面拒绝、后端放行"。
		"bounds": boundsView(),
		"yaml_readonly": gin.H{
			"enabled":                  cfg.Enabled,
			"recipient_lookup":         cfg.RecipientLookup,
			"require_receiver_enabled": cfg.ReceiverMustBeEnabled(),
			"lookup_log_retain_days":   cfg.LookupLogRetainDays,
		},
		// YAML 里那份门槛的原值。运营需要知道"清掉覆盖之后会回到哪里",
		// 否则删除覆盖这个动作等于闭眼跳。
		"yaml_defaults": settingsSnapshot(opSettings{
			Transfer:          cfg,
			PayPwdMaxAttempts: defaultPayPwdMaxAttempts,
			PayPwdLockMinutes: defaultPayPwdLockMinutes,
		}),
	})
}

// boundsView 把 settingBounds 摊成 {key: {min, max}} 的下发形状。
func boundsView() map[string]gin.H {
	out := make(map[string]gin.H, len(editableKeys))
	for _, key := range editableKeys {
		b := settingBounds[key]
		out[key] = gin.H{"min": b.Lo, "max": b.Hi}
	}
	return out
}

// adminPutTransferConfig 修改划转门槛。
//
// 每一次改动都必须写审计:门槛直接决定"一个账号一天能转走多少",
// "谁在什么时候把日额度从 2 亿改成 20 亿"事后必须能查到人。
func adminPutTransferConfig(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagTransfer) {
		return
	}
	// 用 RawMessage 而不是 map[string]int64:前端发字符串最安全,但工具、
	// 脚本和历史客户端习惯发数字,让它们直接 400 只会制造无谓的故障。
	var req map[string]json.RawMessage
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErr(c, errInvalidParam)
		return
	}
	if len(req) == 0 {
		respondErr(c, errConfigEmptyPatch)
		return
	}

	ctx := c.Request.Context()
	// 同 adminGetTransferConfig:库里现存的组合非法时也必须放行到这里,
	// 否则管理员再也无法通过接口把它改回来。candidate 那一步的校验保证
	// 本次写入的结果一定是自洽的。
	before, _, err := resolveSettings(ctx)
	if err != nil {
		respondErr(c, err)
		return
	}

	// 先把全部取值校验并规范化,再统一落库。candidate 从**当前生效值**出发
	// 而不是从零值:跨字段校验必须看到"这次没改的那些字段现在是多少",
	// 否则单独把 min_quota 调到 max_per_tx_quota 之上会一路放行。
	candidate := before
	normalized := make(map[string]string, len(req))
	for k, raw := range req {
		if !editable(k) {
			respondErr(c, errConfigKeyNotEditable)
			return
		}
		v, err := parseSetting(k, jsonScalarLiteral(raw))
		if err != nil {
			respondErr(c, configValueError(k, err.Error()))
			return
		}
		assignSetting(&candidate, k, v)
		normalized[k] = strconv.FormatInt(v, 10)
	}
	if err := config.ValidateTransfer(&candidate.Transfer); err != nil {
		respondErr(c, configValueError("", err.Error()))
		return
	}

	operatorId := c.GetInt("id")
	gdb := db.Get()
	if gdb == nil {
		respondErr(c, db.ErrNotReady)
		return
	}
	// 按键名排序后写入。两个理由:失败留痕可复现;更要紧的是给并发的两次保存
	// 一个固定的加锁顺序 —— 两个管理员同时保存互相交叉的键集时,乱序写正是
	// 死锁的配方,而死锁恰好是这段代码最怕的那种失败。
	keys := make([]string, 0, len(normalized))
	for k := range normalized {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// 整批包进一个事务。前面的校验只挡住了"一半写进去一半 400";写库过程中
	// 第二个键失败同样会留下一组谁都没有批准的门槛组合,而且更糟 ——
	// 那时接口已经改过库了,却只回了个 500,运营重试一次就可能再改一遍。
	txErr := gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, k := range keys {
			if err := writeSetting(tx, k, normalized[k], operatorId); err != nil {
				return err
			}
		}
		return nil
	})
	if txErr != nil {
		// 审计必须落在事务之外,否则它会跟着一起回滚 —— 而"有人在这一刻试图
		// 改门槛、失败了"正是最需要留痕的事实。
		//
		// after 取的是**回滚之后重新读库**的真实值,不是这次想写的值:
		// 万一回滚没有生效(连接被掐、DDL 混进来),这条记录就是唯一能看出
		// "库里已经变了"的地方。事实本身必须留痕,哪怕它与预期不符。
		invalidateSettings()
		writeConfigUpdateAudit(c, qymodel.ResultFail,
			"修改划转门槛失败(事务已回滚): "+txErr.Error(), before, reReadSettings(c, before))
		respondErr(c, txErr)
		return
	}

	invalidateSettings()
	after, _, err := resolveSettings(ctx)
	if err != nil {
		// 写已经成功了,只是回读失败。审计仍然要写,after 用"这次批准的值",
		// 并在原因里点明它是推算值而不是回读值 —— 否则事后翻审计的人会把
		// 一个没有被证实过的快照当成库里的事实。
		writeConfigUpdateAudit(c, qymodel.ResultOK,
			"修改划转门槛(写入成功,回读失败,after 为本次批准值): "+err.Error(), before, candidate)
		respondErr(c, err)
		return
	}
	writeConfigUpdateAudit(c, qymodel.ResultOK, "修改划转门槛", before, after)
	respondOK(c, gin.H{"effective": settingsSnapshot(after)})
}

// reReadSettings 在写入失败后重新读一次库,用于审计的 after 快照。
// 连回读都失败时退回调用方给的兜底值 —— 审计本身绝不能因此写不出来。
func reReadSettings(c *gin.Context, fallback opSettings) opSettings {
	s, _, err := resolveSettings(c.Request.Context())
	if err != nil {
		return fallback
	}
	return s
}

// writeConfigUpdateAudit 落一条门槛变更审计,成功与失败共用。
//
// 失败那条同样带前后快照 —— 它回答的是"有人在这个时刻想把门槛改成什么、
// 库里现在实际是什么"。
//
// 组装与写入住在 audit.WriteConfigUpdate,与 commission 共用同一份实现;
// 这里只剩 Action 与快照视图。operatorId 不再往下传 —— 共享实现直接读
// context,与这里的 c.GetInt("id") 是同一个来源。
func writeConfigUpdateAudit(c *gin.Context, result, reason string, before, after opSettings) {
	audit.WriteConfigUpdate(c, audit.ConfigChange{
		Action: "transfer.config.update",
		Result: result,
		Reason: reason,
		Before: settingsSnapshot(before),
		After:  settingsSnapshot(after),
	})
}

func editable(key string) bool {
	for _, k := range editableKeys {
		if k == key {
			return true
		}
	}
	return false
}

// 管理端配置接口的错误。
//
// 刻意不把键名与区间拼进对外文案:那属于给运营看的字段级提示,由前端按
// bounds 渲染;后端这一层只需要一个稳定的 code。唯一的例外是
// configValueError,它带上服务端的判定原因,否则运营面对 400 无从下手。
var (
	errConfigEmptyPatch     = newBizError("qy_invalid_param", "没有需要修改的配置项", http.StatusBadRequest)
	errConfigKeyNotEditable = newBizError("qy_transfer_config_key_not_editable",
		"包含不可在线修改的配置项", http.StatusBadRequest)
)

// configValueError 构造一条带原因的取值错误。key 为空表示这是跨字段校验的失败。
func configValueError(key, reason string) *bizError {
	msg := "配置项取值不合法: " + reason
	if key != "" {
		msg = "配置项 " + key + " 取值不合法: " + reason
	}
	return newBizError("qy_transfer_config_value_invalid", msg, http.StatusBadRequest)
}

// jsonScalarLiteral 取出一个 JSON 标量的十进制字面量。
//
// 数字原样返回,字符串脱掉引号。两种写法都收是刻意的:前端发字符串最安全,
// 但工具与历史客户端习惯发数字。真正的解析与区间校验统一在 parseSetting 里做,
// 这里只负责剥掉 JSON 那一层皮。
func jsonScalarLiteral(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		var out string
		if err := common.Unmarshal(raw, &out); err == nil {
			return strings.TrimSpace(out)
		}
	}
	return s
}
