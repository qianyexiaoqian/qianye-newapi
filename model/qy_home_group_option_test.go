package model

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 首屏群聊卡片的两个选项由管理员填、渲染给**未登录访客**:入群链接会进
// <a href> 并被编成二维码,群号原样印在页面上。这一份钉的是三件事:
//
//  1. 判据本身放行什么、拒绝什么;
//  2. 被拒的值**不会先落库**(UpdateOption 的顺序是「先 DB.Save,后写内存」,
//     只在 controller 挡的话坏值会持久化而内存停在旧值,重启也不自愈);
//  3. 读取侧那道闸能把绕过写入校验进来的坏值再滤掉一次。
//
// 三件事共用同一批样本,所以放在同一个文件里,不在 controller / setting 各建一份。
func TestQyHomeGroupOptionsAreValidatedBeforeTheyReachTheDatabase(t *testing.T) {
	longURL := "https://qm.qq.com/q/" + strings.Repeat("a", 600)

	for _, tc := range []struct {
		name    string
		key     string
		value   string
		wantErr bool
	}{
		// 清空等于关掉这块卡片。挡住空值就等于管理员再也删不掉一个填错的地址。
		{"链接留空", operation_setting.QyHomeGroupJoinUrlOptionKey, "", false},
		{"链接纯空白也算清空", operation_setting.QyHomeGroupJoinUrlOptionKey, "   ", false},
		// 群邀请链接几乎一定带 query。这一条钉的是「不要照抄禁 query 的那份判据」。
		{"链接带 query", operation_setting.QyHomeGroupJoinUrlOptionKey, "https://qm.qq.com/q/abc?k=xyz", false},
		{"链接走 http", operation_setting.QyHomeGroupJoinUrlOptionKey, "http://10.0.0.5:8080/join", false},

		{"伪协议 javascript", operation_setting.QyHomeGroupJoinUrlOptionKey, "javascript:alert(1)", true},
		{"伪协议大小写变体", operation_setting.QyHomeGroupJoinUrlOptionKey, "JaVaScript:alert(1)", true},
		{"伪协议 data", operation_setting.QyHomeGroupJoinUrlOptionKey, "data:text/html;base64,PHM+", true},
		{"缺协议头", operation_setting.QyHomeGroupJoinUrlOptionKey, "qm.qq.com/q/abc", true},
		{"相对路径", operation_setting.QyHomeGroupJoinUrlOptionKey, "/join", true},
		{"空主机", operation_setting.QyHomeGroupJoinUrlOptionKey, "http://", true},
		// 超长不是洁癖:前端把这个值编成二维码,编码器在超出容量时抛的是渲染期
		// 异常,而路由根挂着错误页 —— 一个选项值能把整个前端外壳顶掉。
		{"超长", operation_setting.QyHomeGroupJoinUrlOptionKey, longURL, true},
		// url.Parse 放行双向排版符,这一条补的就是那个缺口。
		{"含双向排版符", operation_setting.QyHomeGroupJoinUrlOptionKey, "https://qm.qq.com/‮abc", true},
		// 浏览器的 URL 解析会**静默剥掉**换行并判为合法,后端必须比前端严。
		{"含换行", operation_setting.QyHomeGroupJoinUrlOptionKey, "https://qm.qq.com/\nabc", true},

		{"群号留空", operation_setting.QyHomeGroupNumberOptionKey, "", false},
		{"群号纯数字", operation_setting.QyHomeGroupNumberOptionKey, "1013106587", false},
		{"群号非数字标识", operation_setting.QyHomeGroupNumberOptionKey, "qianye_group-01", false},

		{"群号过短", operation_setting.QyHomeGroupNumberOptionKey, "ab", true},
		{"群号过长", operation_setting.QyHomeGroupNumberOptionKey, strings.Repeat("1", 33), true},
		{"群号带空格", operation_setting.QyHomeGroupNumberOptionKey, "101 310", true},
		{"群号是整句说明", operation_setting.QyHomeGroupNumberOptionKey, "浅夜群 1013106587", true},
		{"群号用全角数字", operation_setting.QyHomeGroupNumberOptionKey, "１０１３１０６５８７", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateOptionValue(tc.key, tc.value)
			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
		})
	}
}

func TestRejectedQyHomeGroupOptionNeverReachesTheDatabase(t *testing.T) {
	db := useFrontendOptionMigrationDB(t)
	// 合法值那一支会往下走到 updateOptionMap,它无条件写 OptionMap ——
	// 包级默认值是 nil,不换成空 map 就是写 nil map。
	previousMap := common.OptionMap
	common.OptionMap = map[string]string{}
	t.Cleanup(func() { common.OptionMap = previousMap })

	require.Error(t, UpdateOption(operation_setting.QyHomeGroupJoinUrlOptionKey, "javascript:alert(1)"))
	requireOptionMissing(t, db, operation_setting.QyHomeGroupJoinUrlOptionKey)

	// 对照组:闸门不能顺手把正常保存一起挡掉。
	const good = "https://qm.qq.com/q/abc?k=xyz"
	require.NoError(t, UpdateOption(operation_setting.QyHomeGroupJoinUrlOptionKey, good))
	assert.Equal(t, good, requireOptionValue(t, db, operation_setting.QyHomeGroupJoinUrlOptionKey))
}

// 读取侧那道闸守的是「经手改库 / 恢复备份 / 日后某条不走 UpdateOption 的写入
// 进来的值,不会出现在 /api/status 的匿名 payload 里」。
func TestQyHomeGroupOptionsAreFilteredAgainOnTheWayOut(t *testing.T) {
	for _, bad := range []string{"javascript:alert(1)", "data:text/html,x", "qm.qq.com/q/abc", "https://qm.qq.com/‮abc"} {
		assert.Empty(t, operation_setting.SanitizedQyHomeGroupJoinUrl(bad), bad)
	}
	assert.Equal(t, "https://qm.qq.com/q/abc?k=xyz",
		operation_setting.SanitizedQyHomeGroupJoinUrl("  https://qm.qq.com/q/abc?k=xyz  "))

	for _, bad := range []string{"101 310", "浅夜群", "ab", strings.Repeat("1", 33)} {
		assert.Empty(t, operation_setting.SanitizedQyHomeGroupNumber(bad), bad)
	}
	assert.Equal(t, "1013106587", operation_setting.SanitizedQyHomeGroupNumber("  1013106587  "))
}
