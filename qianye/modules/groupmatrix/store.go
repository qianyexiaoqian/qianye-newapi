package groupmatrix

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// store.go —— 扩展库的清单读写,以及**上游倍率**的读改写。
//
// ══════════════ 倍率为什么写在上游 options 而不是扩展库 ══════════════
//
// 分组倍率的唯一真相源是上游 options.GroupGroupRatio。它不是新功能,是上游既有
// 设置项,本轮改的是它的**使用强度与界面**,数据形状一个字节不变 —— 不加表、
// 不加列,不违反"新增功能走新库"的铁律。
//
// 反过来,把倍率搬进扩展库要同时挂 3 个计费 hook(relay/helper/price.go 预扣、
// service/quota.go 的 WSS/音频结算、service/task_billing.go 的 Task 差额结算,
// 三处各写各的 if、不共用函数),横跨预扣与结算两侧,每一个都要重新证明
// "预扣与结算必须同口径"。漏一条的表现正是:某条路径按旧倍率扣费,而管理界面
// 完全看不出来。本仓 service/qy_pricing_export.go 的注释里已经记着一次同形事故。
//
// 可卸载性也在这里:扩展整个删掉时计费行为逐位不变,只有可见性回退。

// ratioMu 串行化"读 GroupGroupRatio → 改 → UpdateOption"这整段。
//
// 上游对这张整表的读改写没有任何保护,而本轮的写入频率会显著高于上游预期
// (两个管理员同屏编辑矩阵)。进程内互斥挡住单节点并发;多节点部署时
// 主要防线是请求体里的 base_ratio_hash(不匹配返回 409)。
var ratioMu sync.Mutex

// ratioMatrix 是 GroupGroupRatio 的解码形态:用户分组 → 模型分组 → 倍率。
type ratioMatrix map[string]map[string]float64

// loadRatioMatrix 从上游内存快照解出当前倍率矩阵。
//
// 直接读 ratio_setting 而不是查 options 表:热路径乘的就是这份内存快照,
// 管理端必须与它同源,否则又会出现"界面显示 A、实际乘 B"。
func loadRatioMatrix() (ratioMatrix, string, error) {
	raw := ratio_setting.GroupGroupRatio2JSONString()
	m := ratioMatrix{}
	if raw != "" {
		if err := common.UnmarshalJsonStr(raw, &m); err != nil {
			return nil, "", fmt.Errorf("上游 GroupGroupRatio 不是合法 JSON: %w", err)
		}
	}
	h, err := hashRatioMatrix(m)
	if err != nil {
		return nil, "", err
	}
	return m, h, nil
}

// hashRatioMatrix 给出矩阵的规范化指纹。
//
// 走 common.Marshal 的 map 序列化(encoding/json 对 map 键排序),因此同一份
// 内容永远得到同一个字节串,与它当初被写进 options 时的字面量无关 ——
// 用原始字符串做哈希会让"上游页面重排了一次键"被误报成"数据被改过"。
func hashRatioMatrix(m ratioMatrix) (string, error) {
	b, err := common.Marshal(m)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// cloneRatioMatrix 深拷一份矩阵,供审计的 before 快照使用。
//
// 必须深拷:applyRatioCells 会就地改内层 map,浅拷出来的 before 会跟着变,
// 于是审计里 before 与 after 一模一样 —— 一条看起来齐全、实际什么都没记的记录。
func cloneRatioMatrix(m ratioMatrix) ratioMatrix {
	out := make(ratioMatrix, len(m))
	for ug, row := range m {
		dst := make(map[string]float64, len(row))
		for mg, v := range row {
			dst[mg] = v
		}
		out[ug] = dst
	}
	return out
}

// publishRatioMatrix 把矩阵写回上游 options。
//
// 走 model.UpdateOption 而不是自己拼 SQL:那条路径会落库 + 更新内存快照 +
// 广播给其它节点,与上游"系统设置-分组倍率"页完全同一条路径。因此扩展关掉之后,
// 运营仍然能在原地改倍率 —— 这是回退能力的一部分,那个入口刻意不锁死。
//
// ══════════════ 为什么写完还要回查一次 options 表 ══════════════
//
// 上游 model.UpdateOption 对 DB.FirstOrCreate / DB.Save 的返回值**一个都不检查**,
// 只把内存写的错误返回出来。库写失败(磁盘满 / 只读 / 行锁超时)时它照样返回 nil,
// 于是本节点内存里是新值、库里是旧值、审计记成功、管理端回读也"证实"生效 ——
// 而其余节点永远拿不到,本节点重启后也回到旧值。
// 表现是「一部分请求 0.3、一部分 1.0,界面显示 0.3,日志无任何异常」,对账时无解。
//
// 回查是这条链上唯一非自证的一环:它读的是 options 表本身,不是我们刚写进去的内存。
func publishRatioMatrix(m ratioMatrix) error {
	payload, err := marshalRatioMatrix(m)
	if err != nil {
		return err
	}
	if err := model.UpdateOption("GroupGroupRatio", payload); err != nil {
		return err
	}
	return verifyOptionPersisted("GroupGroupRatio", payload)
}

// verifyOptionPersisted 确认一条 option 真的落到了库里。
//
// 主库不可用时返回错误而不是放行:这条路径只在管理端写入后跑一次,
// 宁可让运营看到一次"保存失败,请重试",也不能让他相信一个只存在于本进程内存里的倍率。
func verifyOptionPersisted(key, want string) error {
	if model.DB == nil {
		return errors.New("主库未就绪,无法确认分组倍率是否真的落库 —— " +
			"上游 UpdateOption 不检查库写错误,不回查就等于没写")
	}
	// 条件走**结构体**而不是 "key = ?":key 是 MySQL 的保留字,
	// 裸 SQL 片段在三种方言上的引号规则不同;交给 GORM 的 schema 渲染才跨库正确。
	var got model.Option
	if err := model.DB.Where(&model.Option{Key: key}).Take(&got).Error; err != nil {
		return fmt.Errorf("回查 options.%s 失败,无法确认倍率是否落库: %w", key, err)
	}
	if got.Value != want {
		return fmt.Errorf("options.%s 回查值与写入值不一致 —— 库写很可能失败了"+
			"(上游 UpdateOption 不检查库写错误),本节点内存已是新值而库里不是,请重试", key)
	}
	return nil
}

// marshalRatioMatrix 给出要写进 options 的 JSON。
//
// inherit 的格子**不写 key**(而不是写 null 或 0):上游 GetGroupGroupRatio 靠
// key 存在与否返回 (ratio, false),"没有这个 key"就是"继承兜底倍率"。
// 写一个 0 进去是**显式免费**,它会命中 relay/helper/price.go 的整体替换分支 ——
// 与继承是两件完全不同的事,差别是这个分组这个模型收不收钱。
//
// 抽出来是为了让 round-trip 测试能在不碰主库的前提下断言这条语义。
func marshalRatioMatrix(m ratioMatrix) (string, error) {
	cleaned := make(ratioMatrix, len(m))
	for ug, row := range m {
		if len(row) == 0 {
			// 整行为空时连用户分组这一层的 key 都不写:留一个空对象在里面,
			// 上游 GetGroupGroupRatio 的第一层查找就会命中,第二层再落空 ——
			// 结果一样,但它会让 options 里堆满永远不会被读到的空壳。
			continue
		}
		dst := make(map[string]float64, len(row))
		for mg, v := range row {
			dst[mg] = v
		}
		cleaned[ug] = dst
	}
	b, err := common.Marshal(cleaned)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// ─────────────────────────── 扩展库读写 ───────────────────────────

func loadScopes(gdb *gorm.DB) (map[string]Scope, error) {
	var rows []Scope
	if err := gdb.Order("user_group asc").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[string]Scope, len(rows))
	for _, r := range rows {
		out[r.UserGroup] = r
	}
	return out, nil
}

func loadGrants(gdb *gorm.DB) (map[string]map[string]struct{}, error) {
	var rows []Grant
	if err := gdb.Order("user_group asc, model_group asc").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[string]map[string]struct{}, len(rows))
	for _, r := range rows {
		set, ok := out[r.UserGroup]
		if !ok {
			set = map[string]struct{}{}
			out[r.UserGroup] = set
		}
		set[r.ModelGroup] = struct{}{}
	}
	return out, nil
}

// listWriteDenies 读出影子期的写入拒绝计数,按次数降序。
//
// 读失败只记日志、返回空切片:这是观测数据,不能让它挡住整个矩阵页 ——
// 而矩阵页打不开的时刻,恰恰是最需要看它的时刻。
func listWriteDenies(gdb *gorm.DB) []WriteDeny {
	out := make([]WriteDeny, 0)
	if gdb == nil {
		return out
	}
	if err := gdb.Order("count desc, id asc").Limit(200).Find(&out).Error; err != nil {
		common.SysError("qianye/groupmatrix: 读取影子写入拒绝失败(矩阵页其余部分不受影响): " + err.Error())
		return make([]WriteDeny, 0)
	}
	return out
}

// groupByRaw 以**原样表达式**做 GROUP BY。
//
// ══════════════ 为什么不能直接用 gdb.Group(col) ══════════════
//
// `group` 是三种数据库里的保留字,列名一律走 model.QyCommonGroupCol(),
// 而它返回的是**已经带引号**的字符串。gorm.DB.Group(name) 只有在 name 被
// utils.IsValidDBNameChar 切出多于一个字段时才按 Raw 处理;单个已引用列名
// (以及 "users.`group`" 这种带库前缀的)会被判成普通列名再引一次,
// 生成 SET/GROUP BY “group“ —— 在 SQLite 上是直接的语法错误。
//
// 更糟的是这两处失败都被上层吞成"数字是 0"而不是"接口报错":
// 孤儿探测器会永远报"干净",矩阵行轴会整行消失。所以必须显式声明 Raw。
func groupByRaw(gdb *gorm.DB, expr string) *gorm.DB {
	return gdb.Clauses(clause.GroupBy{
		Columns: []clause.Column{{Name: expr, Raw: true}},
	})
}

func countGrants(gdb *gorm.DB) (int64, error) {
	var n int64
	err := gdb.Model(&Grant{}).Count(&n).Error
	return n, err
}

// upsertWriteDeny 累加一条影子期写入拒绝。
//
// 先 Update 再 Create:唯一索引保证并发下最多有一个 Create 成功,
// 失败的那次退回 Update。不用 ON DUPLICATE KEY —— 扩展库固定 MySQL,
// 但这段逻辑也会在 SQLite 上跑测试。
func upsertWriteDeny(ctx context.Context, userGroup, modelGroup string, userId int) error {
	gdb := db.Get()
	if gdb == nil {
		return db.ErrNotReady
	}
	gdb = gdb.WithContext(ctx)
	now := common.GetTimestamp()

	res := gdb.Model(&WriteDeny{}).
		Where("user_group = ? AND model_group = ?", userGroup, modelGroup).
		Updates(map[string]any{
			"count":          gorm.Expr("count + 1"),
			"last_seen":      now,
			"sample_user_id": userId,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected > 0 {
		return nil
	}
	err := gdb.Create(&WriteDeny{
		UserGroup: userGroup, ModelGroup: modelGroup,
		Count: 1, FirstSeen: now, LastSeen: now, SampleUserId: userId,
	}).Error
	if err == nil {
		return nil
	}
	// 并发下另一个请求抢先建了行:退回累加。
	return gdb.Model(&WriteDeny{}).
		Where("user_group = ? AND model_group = ?", userGroup, modelGroup).
		Updates(map[string]any{
			"count":     gorm.Expr("count + 1"),
			"last_seen": now,
		}).Error
}

// ─────────────────────────── 草稿与指纹 ───────────────────────────

// cellAction 是矩阵单元格的**显式动作**。
//
// 刻意不做整表 diff:「传 null 表示清除」与「不传表示不动」必须分开,
// 否则前端一次局部保存会把没在屏幕上的格子全清掉 —— 而"全清掉"在这里的
// 后果是一批用户立刻 403。
const (
	ActionGrant      = "grant"
	ActionRevoke     = "revoke"
	ActionSetRatio   = "set_ratio"
	ActionClearRatio = "clear_ratio"
)

// Cell 是一次矩阵改动里的一个格子。
//
// Ratio 必须是 *float64 + omitempty:运营填的 0 是**显式免费**,
// 用非指针 float64 会让它在序列化时消失,一个本该免费的分组静默变成兜底价。
// 这是 AGENTS.md 已有的同一条规则。
type Cell struct {
	UserGroup  string   `json:"user_group"`
	ModelGroup string   `json:"model_group"`
	Action     string   `json:"action"`
	Ratio      *float64 `json:"ratio,omitempty"`
}

// normalizeCells 校验并去重动作列表,返回按 (用户分组, 模型分组, 动作) 排序的结果。
//
// 排序不只是为了好看:draft_hash 必须与提交顺序无关,否则前端调换两次点击的
// 顺序就会拿到不同的指纹,预览与保存永远对不上。
func normalizeCells(cells []Cell) ([]Cell, error) {
	out := make([]Cell, 0, len(cells))
	for _, c := range cells {
		if c.UserGroup == "" {
			return nil, errors.New("user_group 不能为空")
		}
		if c.ModelGroup == "" {
			return nil, errors.New("model_group 不能为空")
		}
		if len(c.UserGroup) > maxGroupNameLen || len(c.ModelGroup) > maxGroupNameLen {
			return nil, fmt.Errorf("分组名超过 %d 字节", maxGroupNameLen)
		}
		if c.ModelGroup == autoGroup {
			return nil, errors.New("清单里不能出现 auto —— 它是伪分组," +
				"上游的 IsUserSelectableGroup 本来就显式拒绝它;是否放行 auto 由 scope.allow_auto 控制")
		}
		switch c.Action {
		case ActionGrant, ActionRevoke, ActionClearRatio:
			c.Ratio = nil
		case ActionSetRatio:
			if c.Ratio == nil {
				return nil, errors.New("set_ratio 必须带 ratio;要清除请用 clear_ratio —— " +
					"「传 null 表示清除」与「不传表示不动」必须分开")
			}
			if err := validateRatio(*c.Ratio); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("action %q 非法(可选 grant|revoke|set_ratio|clear_ratio)", c.Action)
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UserGroup != out[j].UserGroup {
			return out[i].UserGroup < out[j].UserGroup
		}
		if out[i].ModelGroup != out[j].ModelGroup {
			return out[i].ModelGroup < out[j].ModelGroup
		}
		return out[i].Action < out[j].Action
	})
	return out, nil
}

// maxGroupRatio 是倍率上界。上游对倍率本身没有上界,但它会被直接乘进每一笔账单:
// 一次手滑把倍率填成 1e18,配合大 token 数就会把中间结果推过 int32,
// 而饱和之后的账单没有任何意义(见 AGENTS.md 计费安全不变量)。
const maxGroupRatio = 1000000

func validateRatio(v float64) error {
	if v != v { // NaN
		return errors.New("倍率不是合法数值")
	}
	if v < 0 {
		return fmt.Errorf("倍率不得为负数,收到 %v —— 负倍率会把扣费变成给用户充值", v)
	}
	if v > maxGroupRatio {
		return fmt.Errorf("倍率不得超过 %d,收到 %v", maxGroupRatio, v)
	}
	return nil
}

// hashCells 是草稿的规范化指纹,覆盖排序后的动作列表。
func hashCells(cells []Cell) (string, error) {
	b, err := common.Marshal(cells)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}
