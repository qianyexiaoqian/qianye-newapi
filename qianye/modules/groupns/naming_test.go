package groupns

import (
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGroupNamesAreNeverTruncated 锁住两张登记表的主键**永不裁剪**。
//
// ── 这条测试保护的是一次能让整个功能静默失效的失败 ──
//
// 此前 newUserGroup / newModelGroup 走一个按**字节**裁剪的 truncate(name, 64)。
// users.group / abilities.group / channels.group 都是 varchar(64) 即 64 个**字符**,
// 所以一个 22 个汉字的合法分组名(66 字节)完全放得下,却会被砍成 21 个字 +
// 半个 UTF-8 序列。后果分两档:
//
//	多数情况 MySQL 在 STRICT_TRANS_TABLES 下以 Error 1366 拒绝**整条** INSERT,
//	         而全部新行在同一条 CreateInBatches 里 ⇒ 整次回填失败并 return ⇒
//	         两张登记表永远是空的,管理端两页显示「一个分组都没有」,
//	         adminSetDefaultModelGroup 对所有分组返回「还没有登记」——
//	         整个「默认模型分组」功能不可用,唯一的线索是一行 SysError。
//	切在字符边界上时静默写入一条**幽灵登记**:主键与源名字永远不相等,
//	         haveModel[全名] 恒 false ⇒ 每次回填都重新尝试插入(不幂等),
//	         而 snapshot.UserGroups[全名] 与 Where("name = ?", 全名) 又永远 miss
//	         ⇒ 那一行既配不上也读不到。
func TestGroupNamesAreNeverTruncated(t *testing.T) {
	// 26 个汉字 / 78 字节:在 users.group varchar(64) 里完全合法。
	long := "浅夜梦专属号池促销限时体验版分组二零二六年度特别企划"
	require.Equal(t, 26, utf8.RuneCountInString(long))
	require.Equal(t, 78, len(long))

	for _, name := range []string{long, "default", "浅夜の梦专属号池"} {
		assert.Equal(t, name, newUserGroup(name, 1, 0).Name,
			"用户分组登记行的 name 必须与源名字逐位相同")
		assert.Equal(t, name, newModelGroup(name, 1, 0).Name,
			"模型分组登记行的 name 必须与源名字逐位相同")
		assert.True(t, utf8.ValidString(newUserGroup(name, 1, 0).Name),
			"登记行的 name 必须是合法 UTF-8")
	}
}

// TestRegistrableGroupNameCountsRunesNotBytes 锁住「能不能登记」的判据是字符数。
//
// 判据写成字节数时,26 个汉字(78 字节)会被判成"超限"并跳过登记 —— 一个完全合法、
// 正在被使用的中文分组名从此在管理端两页上看不到,而运营会把它读成"这个分组不存在"。
func TestRegistrableGroupNameCountsRunesNotBytes(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"空串不是分组", "", false},
		{"ASCII 短名", "default", true},
		{"26 个汉字 78 字节,在 varchar(64) 字符限内", "浅夜梦专属号池促销限时体验版分组二零二六年度特别企划", true},
		{"恰好 64 个字符", string(make([]rune, 64)), true},
		{"65 个字符,超出主键宽度", string(make([]rune, 65)), false},
		{"64 个汉字(192 字节)仍然合法", repeatRune('夜', 64), true},
		{"65 个汉字超限", repeatRune('夜', 65), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, registrableGroupName(tc.in))
		})
	}
}

func repeatRune(r rune, n int) string {
	out := make([]rune, n)
	for i := range out {
		out[i] = r
	}
	return string(out)
}

// TestDropUnregistrableRemovesFromEverySet 断言超长名字被从**全部**观测集合里剔除。
//
// 只从其中一个集合里剔除的表现是 seen 与 added 永远对不上,而对不上的原因不可见:
// 运维盯着「登记回填新增 1 个模型分组」这条每次启动都出现的日志,而表里的行数一直不变。
func TestDropUnregistrableRemovesFromEverySet(t *testing.T) {
	tooLong := repeatRune('夜', 65)
	users := map[string]bool{"default": true, tooLong: true}
	models := map[string]bool{"免费の渠道": true, tooLong: true}
	routed := map[string]bool{tooLong: true}
	rated := map[string]bool{"免费の渠道": true, tooLong: true}

	skipped := dropUnregistrable(users, models, routed, rated)

	assert.Equal(t, []string{tooLong}, skipped)
	assert.Equal(t, map[string]bool{"default": true}, users)
	assert.Equal(t, map[string]bool{"免费の渠道": true}, models)
	assert.Empty(t, routed)
	assert.Equal(t, map[string]bool{"免费の渠道": true}, rated)
}
