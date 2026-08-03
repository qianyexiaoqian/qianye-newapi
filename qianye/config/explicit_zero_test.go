package config

import (
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gopkg.in/yaml.v3"
)

// zeroExemptions 列出"显式写 0 会被 validate 直接拒绝启动"的数值字段,
// 因此不能进入下面那份全 0 配置。
//
// 逐条列出而不是静默跳过:静默跳过等于把漏网之鱼藏起来 —— 某个字段哪天
// 悄悄接错了默认值,只要它同时也被跳过,就再也没有任何东西会红。
// 这份清单同时也是对外承诺的"填 0 会起不来的字段全集",示例配置的升级提示
// 直接引用它,所以它必须既不多也不少:多了由 TestZeroExemptionsAreAllReallyRejected
// 抓(每一条都必须真的被拒绝),少了由主测试抓(漏掉的字段会以断言失败现身)。
var zeroExemptions = map[string]string{
	"database.max_idle_conns":     "validateDatabase 要求 > 0,连接池为 0 等于数据库不可用",
	"database.max_open_conns":     "不得小于 max_idle_conns,而后者被豁免后取默认值 20",
	"runtime.lease_ttl_seconds":   "与 lease_renew_seconds 有 2 倍关系,两者同时为 0 必然失败;豁免 TTL 才能让 renew=0 仍受断言保护",
	"runtime.hot_hook_queue_size": "validateRuntime 要求 > 0,队列长度为 0 的 worker 收不到任何任务",
	"runtime.hot_hook_workers":    "validateRuntime 要求 > 0,同上",

	"two_phase.pending_grace_seconds":       "0 会让补偿任务在主库事务尚未提交时就介入探针",
	"two_phase.max_probe_attempts":          "0 让第一次退避就把资金单转人工裁决",
	"two_phase.manual_review_after_seconds": "0 让存活超过一秒的 pending 单被判 failed,主库随后提交即两库分叉",

	"commission.levels":           "当前只支持恰好 1 级,0 会被当成多级返佣拒绝",
	"commission.min_settle_quota": "必须 > 0,否则佣金按 decimal 累计后会被截断归零",

	"withdraw.remark_max_runes": "必须在 1..2000,0 不是「不限制」而是「一个字都不许填」",
	"withdraw.proof_max_bytes":  "必须在 1..8MiB,凭证要整张读进内存,0 等于把堆交给上传者",

	"availability.bucket_seconds":         "必须是 3600 的因数,0 会让小时级汇总跨桶错位",
	"availability.flush_interval_seconds": "validateAvailability 要求 > 0",

	"group_pricing.rule_cache_seconds":            "validateGroupPricing 要求 > 0",
	"group_pricing.max_stale_seconds":             "不得小于 rule_cache_seconds,而后者被豁免后取默认值 60",
	"group_pricing.shadow_flush_interval_seconds": "validateGroupPricing 要求 > 0",
	"group_pricing.shadow_retention_days":         "validateGroupPricing 要求 > 0",
	"group_pricing.max_rules":                     "validateGroupPricing 要求 > 0",
}

// TestExplicitZeroIsNeverReplacedByDefault 守的是"显式配置必须生效"这个契约本身,
// 而不是某几个字段:反射遍历 Config 里全部带 yaml tag 的 int/int64 字段,
// 生成一份把它们统统显式写成 0 的 YAML,加载后逐个断言仍是 0。
//
// 缺陷原型是 commission.holding_days: 0 被静默补成 7(佣金要多等 8 天才结算,
// 而配置文件上仍写着 0)。单点测试挡不住这类问题 —— 十几个字段里漏接一个,
// 没有任何东西会发现。以后任何人新增一个数值配置项并给它接默认值,
// 只要判据接错就会在这里红。
func TestExplicitZeroIsNeverReplacedByDefault(t *testing.T) {
	all := numericLeaves(reflect.ValueOf(Config{}), "")

	// 前置断言:遍历本身必须有效。如果反射只找到个位数字段,说明递归写错了,
	// 主断言会在一份几乎空的清单上空转通过 —— 那是最典型的假回归。
	require.GreaterOrEqual(t, len(all), 60,
		"反射只扫到 %d 个数值字段,遍历必然写错了(Config 至少有几十个)", len(all))
	for path := range zeroExemptions {
		require.Contains(t, all, path,
			"豁免清单里的 %q 在 Config 上不存在(字段改名或路径拼错),这条豁免正在白白放行一个字段", path)
	}

	c, _, err := parseFile(writeTemp(t, allZeroYAML(t, all)))
	require.NoError(t, err, "全 0 配置必须能通过校验,否则本测试断的是别的东西")

	got := numericLeaves(reflect.ValueOf(*c), "")
	require.Len(t, got, len(all), "解析前后扫到的字段数必须一致")
	for path, v := range got {
		if reason, exempt := zeroExemptions[path]; exempt {
			assert.NotEqual(t, int64(0), v, "%s 被豁免的理由是不能为 0(%s),它却成了 0", path, reason)
			continue
		}
		assert.Equal(t, int64(0), v,
			"%s 在 YAML 里显式写了 0,却被替换成 %d —— 配置层正在替运维改主意", path, v)
	}
}

// TestZeroExemptionsAreAllReallyRejected 把每条豁免单独放回全 0 配置,
// 要求它真的让启动失败。
//
// 没有这一条,豁免清单就是一个只进不出的口袋:谁把某个字段的默认值接错了,
// 只要顺手往清单里加一行"这个不能填 0",主测试立刻变绿,而缺陷原样留在代码里。
// 有了这一条,加一行豁免就必须先真的存在一条拒绝规则。
func TestZeroExemptionsAreAllReallyRejected(t *testing.T) {
	all := numericLeaves(reflect.ValueOf(Config{}), "")
	for path, reason := range zeroExemptions {
		t.Run(path, func(t *testing.T) {
			raw := allZeroYAML(t, all, path)
			_, _, err := parseFile(writeTemp(t, raw))
			require.Error(t, err,
				"%q 被豁免的理由是 %s,但它填 0 时配置照常加载成功 —— 这条豁免是多余的,"+
					"把它删掉让主测试真正覆盖这个字段", path, reason)
		})
	}
}

// TestUnsetNumberStillGetsDefault 是主测试的反面:换了判据之后,
// "没写这个键"仍然必须拿到默认值,否则这次修复就把默认值机制整个弄丢了。
func TestUnsetNumberStillGetsDefault(t *testing.T) {
	c, _, err := parseFile(writeTemp(t, minimalValid))
	require.NoError(t, err)

	assert.Equal(t, 7, c.Commission.HoldingDays)
	assert.Equal(t, 60, c.Withdraw.CooldownSecs)
	assert.Equal(t, int64(500000), c.Transfer.MinQuota)
	assert.Equal(t, 30, c.TwoPhase.OutboxRetentionDays)

	// 没有默认值的字段必须落回 0,而不是带着哨兵进入业务代码。
	assert.Equal(t, 0, c.Transfer.FeeBps)
	assert.Equal(t, 0, c.Audit.RetentionDays)
	assert.Equal(t, 0, c.Violation.AutoBanThreshold)
	assert.Equal(t, 0, c.Runtime.ConfigReloadSeconds)
}

// TestEveryNumericFieldIsReachableBySentinel 守 markNumbersUnset 的递归假设。
//
// 它只认直接嵌套的结构体。有人照现有风格加一段 `Foo *FooCfg` 或 `[]FooCfg`,
// 哨兵就到不了那一段,该段所有默认值会静默失效(闸门全变 0)—— 与本次修的
// 缺陷同一种形状,而上面两条测试同样看不见它(numericLeaves 也不递归指针)。
// 因此这里不查值,查类型:Config 里出现任何"藏着数值字段的间接类型"就红。
func TestEveryNumericFieldIsReachableBySentinel(t *testing.T) {
	c := &Config{}
	markNumbersUnset(reflect.ValueOf(c).Elem())
	for path, v := range numericLeaves(reflect.ValueOf(*c), "") {
		assert.True(t, v == int64(missingInt) || v == missingInt64,
			"%s 打完哨兵之后仍是 %d,markNumbersUnset 没走到它", path, v)
	}

	var unreachable []string
	var walk func(t reflect.Type, prefix string)
	walk = func(rt reflect.Type, prefix string) {
		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)
			name := strings.Split(f.Tag.Get("yaml"), ",")[0]
			if name == "" || name == "-" {
				continue
			}
			path := name
			if prefix != "" {
				path = prefix + "." + name
			}
			if f.Type.Kind() == reflect.Struct {
				walk(f.Type, path)
				continue
			}
			// 间接类型背后是一整个配置段时才有问题。*int(已废弃的 *_rate_bps)
			// 与 map[int]string 不在此列:指针本来就能区分"写了 0"和"没写",
			// 那正是哨兵要给普通 int 补齐的能力。
			if f.Type.Kind() != reflect.Struct && baseType(f.Type).Kind() == reflect.Struct {
				unreachable = append(unreachable, path)
			}
		}
	}
	walk(reflect.TypeOf(Config{}), "")

	require.Empty(t, unreachable,
		"这些字段把数值项藏在指针/切片/映射后面,markNumbersUnset 走不进去,"+
			"它们的默认值会静默失效:%v。要么改成直接嵌套的结构体,要么先扩展 "+
			"markNumbersUnset / clearUnsetNumbers / numericLeaves 三处递归", unreachable)
}

// baseType 剥掉指针/切片/映射,得到最里层的元素类型。
func baseType(rt reflect.Type) reflect.Type {
	for {
		switch rt.Kind() {
		case reflect.Ptr, reflect.Slice, reflect.Array, reflect.Map:
			rt = rt.Elem()
		default:
			return rt
		}
	}
}

// allZeroYAML 生成一份把 fields 里全部数值字段显式写成 0 的配置文本,
// 豁免字段整键省略(从而取默认值);alsoZero 里的路径反过来强行写回 0。
//
// 每一段的 enabled 都打开:validateWithdraw / validateAvailability /
// validateViolation / validateGroupPricing 都以 `if !Enabled { return nil }`
// 开头,只写顶层 enabled 的话九个校验器里有四个一进门就返回,那四段配置
// 根本没被校验过,而豁免清单会因此显得比实际短。
func allZeroYAML(t *testing.T, fields map[string]int64, alsoZero ...string) string {
	t.Helper()
	tree := map[string]any{}
	for path := range fields {
		if _, exempt := zeroExemptions[path]; !exempt {
			setYAMLPath(tree, path, 0)
		}
	}
	for _, path := range alsoZero {
		setYAMLPath(tree, path, 0)
	}
	setYAMLPath(tree, "enabled", true)
	setYAMLPath(tree, "database.dsn", "u:p@tcp(127.0.0.1:3306)/qy")
	for _, section := range []string{
		"transfer", "commission", "withdraw", "availability", "violation", "group_pricing",
	} {
		setYAMLPath(tree, section+".enabled", true)
	}
	// 只留 quota 一种提现方式:带上 fiat 就要求 pii_key / digest_key 两把真钥匙,
	// 而本测试断的是数值字段,不该被密钥格式校验拖着走。
	setYAMLPath(tree, "withdraw.methods", []string{WithdrawMethodQuota})

	raw, err := yaml.Marshal(tree)
	require.NoError(t, err)
	return string(raw)
}

// numericLeaves 收集 v 中全部 int/int64 叶子的 yaml 路径与当前取值。
// 与 leafFields 同样只递归结构体:*bool、[]string、map 都是运维直接填的一个值。
func numericLeaves(v reflect.Value, prefix string) map[string]int64 {
	out := map[string]int64{}
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		name := strings.Split(t.Field(i).Tag.Get("yaml"), ",")[0]
		if name == "" || name == "-" {
			continue
		}
		path := name
		if prefix != "" {
			path = prefix + "." + name
		}
		f := v.Field(i)
		switch f.Kind() {
		case reflect.Struct:
			for k, nested := range numericLeaves(f, path) {
				out[k] = nested
			}
		case reflect.Int, reflect.Int64:
			out[path] = f.Int()
		}
	}
	return out
}

// setYAMLPath 按点分路径往嵌套 map 里写一个值,供 yaml.Marshal 生成配置文本。
func setYAMLPath(tree map[string]any, path string, val any) {
	parts := strings.Split(path, ".")
	node := tree
	for _, p := range parts[:len(parts)-1] {
		sub, ok := node[p].(map[string]any)
		if !ok {
			sub = map[string]any{}
			node[p] = sub
		}
		node = sub
	}
	node[parts[len(parts)-1]] = val
}
