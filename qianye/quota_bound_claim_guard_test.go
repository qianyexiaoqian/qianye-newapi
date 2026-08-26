package qianye

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNoCodeClaimsQuotaColumnsAre32Bit 守住一句已经被证伪的话。
//
// common.MaxQuota 原先取 math.MaxInt32,理由写着「额度列是 32 位整数」。
// 那个前提是假的:users.quota / tokens.remain_quota / logs.quota 在
// SQLite / MySQL / PostgreSQL 上都是 64 位(model.TestQuotaColumnsAre64BitOnEveryDialect
// 在全新空库上跑真实迁移逐一举证),`gorm:"type:int"` 选中的是 GORM 的通用
// schema.Int 种类,方言仍按 64 位 Go int 定型;而线上真实数据早已超出 int32
// 四个数量级(tokens.remain_quota 最大 49,998,247,982,612)。
//
// 上界抬到 2^43 时,common/quota_math.go 与 AGENTS.md 里的那句话改掉了,
// 而**同一句话在生产代码注释里还活着十几处**,多数还是以"这道闸门存在的理由"
// 出现的:钱最敏感的六个模块(twophase / transfer / violation / lottery /
// overdraft / relay 计费)各有一份。fork 于是自相矛盾,谁先读到哪一处就信哪一句;
// 而下一个人按注释去推「那我把它换成 int64 列就能去掉这道闸」会拆错东西。
//
// 这条守卫让那句话再也回不来。它只管**断言列宽**的说法,不管
// `int32(x)` 这类真实的类型转换,也不管测试里对这段历史的引用。
func TestNoCodeClaimsQuotaColumnsAre32Bit(t *testing.T) {
	// 每一条都是「主语是额度列 + 谓语是 32 位」的说法。
	banned := []string{
		"quota 是 int32",
		"quota 列是 int32",
		"额度列是 int32",
		"额度列本身就是 32 位",
		"额度列是 32 位",
		"quota 是 32 位列",
		"MaxQuota = math.MaxInt32",
	}
	root := repoRoot(t)
	var offenders []string
	require.NoError(t, filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "web", "worktrees", ".claude":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		text := string(raw)
		for _, claim := range banned {
			if strings.Contains(text, claim) {
				rel, _ := filepath.Rel(root, path)
				offenders = append(offenders, filepath.ToSlash(rel)+" 里写着 “"+claim+"”")
			}
		}
		return nil
	}))
	assert.Emptyf(t, offenders,
		"额度列在三种方言上都是 64 位,common.MaxQuota 是一条**算术**上界而不是列宽。"+
			"下面这些地方还在把它说成列宽:\n%s", strings.Join(offenders, "\n"))
}

// repoRoot 从本包(qianye/)回到仓库根。
func repoRoot(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs("..")
	require.NoError(t, err)
	return abs
}
