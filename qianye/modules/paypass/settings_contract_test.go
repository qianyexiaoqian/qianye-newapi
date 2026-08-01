package paypass

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// settings_contract_test.go —— 盯住"同一概念的两份定义"。
//
// # 为什么会有两份
//
// `pay_pwd_max_attempts` / `pay_pwd_lock_minutes` 的**写入侧**在
// qianye/modules/transfer/settings.go(管理端配置页、白名单、区间校验都在那儿),
// **读取侧**在本包的 settings.go。两边各有一份默认值与取值区间。
//
// 本来应该只有一份。做不到的原因是包依赖方向:划转的执行入口要调
// paypass.Require,也就是 transfer → paypass;paypass 再去 import transfer
// 就成环了。把它抽到第三个包属于跨模块改动,不在本轮的改动预算里。
//
// # 于是用这条断言顶上
//
// 两份定义不一致的后果非常隐蔽:管理端页面上写着"最多 20 次",而验密侧按
// 自己那份区间把 20 判成越界、回落成 5 —— 运营改了一个不生效的值,而且
// 没有任何报错。这条断言让那种漂移在测试里立刻现形。
//
// 判据用源码文本而不是 AST:要比对的是"字面上写了哪几个数",而 AST 取值还得
// 处理 iota、类型转换、常量表达式,反而更容易在无关改动上误判。
// 代价是 transfer 那边改了常量名或换了排版会让这条变红 —— 那时候正确做法是
// **核对数值之后**更新这里的字符串,而不是把断言删掉。
func TestPayPasswordPolicyMatchesTransferSettings(t *testing.T) {
	path := filepath.Join("..", "transfer", "settings.go")
	raw, err := os.ReadFile(path)
	require.NoError(t, err, "读不到划转的运营配置文件 —— 键的写入侧在那里,"+
		"本包只是消费方;文件被挪走的话这条一致性断言必须跟着挪")
	src := string(raw)

	expected := map[string]string{
		`settingScope = "` + settingScope + `"`:                       "qy_settings 的命名空间必须两边一致",
		`"` + keyMaxAttempts + `"`:                                    "错误次数阈值的键名必须两边一致",
		`"` + keyLockMinutes + `"`:                                    "锁定时长的键名必须两边一致",
		"defaultPayPwdMaxAttempts = " + itoaConst(defaultMaxAttempts): "默认错误次数阈值必须两边一致",
		"defaultPayPwdLockMinutes = " + itoaConst(defaultLockMinutes): "默认锁定时长必须两边一致",
		"keyPayPwdMaxAttempts: {" + itoaConst(minMaxAttempts) + ", " +
			itoaConst(maxMaxAttempts) + "}": "错误次数阈值的取值区间必须两边一致",
		"keyPayPwdLockMinutes: {" + itoaConst(minLockMinutes) + ", " +
			itoaConst(maxLockMinutes) + "}": "锁定时长的取值区间必须两边一致",
	}
	for snippet, why := range expected {
		assert.Contains(t, src, snippet,
			"%s。在 %s 里找不到 %q —— 两份定义已经漂移,"+
				"或者那边改了常量名/排版。核对数值之后再更新本断言,不要直接删掉它。",
			why, filepath.ToSlash(path), snippet)
	}
}

func itoaConst(v int) string { return strconv.Itoa(v) }
