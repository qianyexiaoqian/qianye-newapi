package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// useSmtpMigrationDB 给一次迁移测试准备一个干净的库 + 干净的进程状态。
//
// 老的单账号配置在生产里是「options 行 → InitOptionMap → common.SMTP* 全局变量」
// 这条链装载进来的,而测试直接换 DB 绕过了 InitOptionMap,所以两头都要显式摆好:
// 库里的行决定「配过吗」,全局变量决定「迁进去的值长什么样」。
func useSmtpMigrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	previousDB := DB
	previousType := common.MainDatabaseType()
	previousOptionMap := common.OptionMap
	previousServer, previousPort := common.SMTPServer, common.SMTPPort
	previousAccount, previousToken, previousFrom := common.SMTPAccount, common.SMTPToken, common.SMTPFrom

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Option{}, &SmtpAccount{}))
	DB = db
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	common.OptionMap = map[string]string{}

	t.Cleanup(func() {
		DB = previousDB
		common.SetMainDatabaseType(previousType)
		common.OptionMap = previousOptionMap
		common.SMTPServer, common.SMTPPort = previousServer, previousPort
		common.SMTPAccount, common.SMTPToken, common.SMTPFrom = previousAccount, previousToken, previousFrom
	})
	return db
}

// seedLegacySingleAccountSMTP 摆出「升级前配过单账号 SMTP」的现场。
func seedLegacySingleAccountSMTP(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.Create(&[]Option{
		{Key: "SMTPServer", Value: "smtp.example.com"},
		{Key: "SMTPPort", Value: "465"},
		{Key: "SMTPAccount", Value: "no-reply@example.com"},
		{Key: "SMTPToken", Value: "legacy-secret-placeholder"},
		{Key: "SMTPFrom", Value: "no-reply@example.com"},
	}).Error)
	common.SMTPServer = "smtp.example.com"
	common.SMTPPort = 465
	common.SMTPAccount = "no-reply@example.com"
	common.SMTPToken = "legacy-secret-placeholder"
	common.SMTPFrom = "no-reply@example.com"
}

// TestMigrateLegacySMTPAccountDoesNotResurrectDeletedAccount 钉死迁移的幂等判据。
//
// 判据如果是「账号表里有没有 account_id = legacy 这一行」,运营在界面上删掉那个号
// 之后判据就又变成了"没迁过",下次重启原样插回去 —— 表现是这个号删不掉,
// 而它带着一套已经被弃用的凭据继续参与轮转发信。
//
// 这条同时守住迁移原本的目的:首次升级的老站点必须自动迁得过来。
func TestMigrateLegacySMTPAccountDoesNotResurrectDeletedAccount(t *testing.T) {
	db := useSmtpMigrationDB(t)
	seedLegacySingleAccountSMTP(t, db)

	require.NoError(t, MigrateLegacySMTPAccount())

	var migrated []*SmtpAccount
	require.NoError(t, db.Find(&migrated).Error)
	require.Len(t, migrated, 1, "首次升级必须把老的单账号配置自动迁进账号表")
	assert.Equal(t, "legacy", migrated[0].AccountId)
	assert.Equal(t, "smtp.example.com", migrated[0].Server)
	assert.Equal(t, 465, migrated[0].Port)
	assert.Equal(t, "legacy-secret-placeholder", migrated[0].Token)
	assert.True(t, migrated[0].Enabled)

	// 迁移必须把源消费掉 —— 那才是"迁过了"这件事唯一的记号。
	var leftover []Option
	require.NoError(t, db.Where(map[string]any{"key": retiredSMTPOptionKeys}).Find(&leftover).Error)
	assert.Empty(t, leftover, "老配置那几行迁完就该没了,留着就等于永远「还没迁过」")

	require.NoError(t, DeleteSmtpAccount(migrated[0].Id))

	// 重启:同一份进程内全局变量还在(它们只在 InitOptionMap 时装载一次),
	// 迁移必须凭"源已经没了"认出这是「迁过了但被人删了」。
	require.NoError(t, MigrateLegacySMTPAccount())

	var afterRestart []*SmtpAccount
	require.NoError(t, db.Find(&afterRestart).Error)
	assert.Empty(t, afterRestart, "被删掉的 legacy 账号不能在下次启动时复活")
}

// TestMigrateLegacySMTPAccountConsumesSourceWhenLegacyRowAlreadyExists 覆盖
// 「上一版代码已经迁过、但没消费源」的存量站点 —— 那是所有正在跑当前构建的站点
// 升级时的现场:账号表里已经有 legacy 那一行,而老配置那几行还在库里。
//
// 此时必须只消费源、不重复插入:重复插入会撞 account_id 的唯一索引,整个事务回滚,
// 源就永远消费不掉,于是「删了又复活」的缺陷在这批站点上原样保留。
func TestMigrateLegacySMTPAccountConsumesSourceWhenLegacyRowAlreadyExists(t *testing.T) {
	db := useSmtpMigrationDB(t)
	seedLegacySingleAccountSMTP(t, db)
	alreadyMigrated := &SmtpAccount{
		AccountId: "legacy",
		Name:      "默认(单账号配置)",
		Enabled:   true,
		Server:    "smtp.example.com",
		Port:      465,
		Account:   "no-reply@example.com",
		Token:     "legacy-secret-placeholder",
	}
	require.NoError(t, CreateSmtpAccount(alreadyMigrated))

	require.NoError(t, MigrateLegacySMTPAccount())

	var accounts []*SmtpAccount
	require.NoError(t, db.Find(&accounts).Error)
	require.Len(t, accounts, 1, "已经迁过的站点不能再插一条")
	assert.Equal(t, alreadyMigrated.Id, accounts[0].Id)

	var leftover []Option
	require.NoError(t, db.Where(map[string]any{"key": retiredSMTPOptionKeys}).Find(&leftover).Error)
	assert.Empty(t, leftover, "源必须在这一次就被消费掉,否则这批站点的账号仍然删不掉")
}

// TestMigrateLegacySMTPAccountPurgesRetiredAccountsOption 钉死那块含明文密码的
// SMTPAccounts 被清掉、且不会被当成账号导进轮转。
//
// 它是「多账号存成一块 JSON」那一版留下的孤儿:账号改存独立表之后一个读取方都没有,
// 而 loadOptionsFromDatabase 会把库里的任何键装进 OptionMap,GetOptions 又只按后缀
// 判敏感 —— 于是整块含密码的 JSON 原样下发给了设置页。
func TestMigrateLegacySMTPAccountPurgesRetiredAccountsOption(t *testing.T) {
	db := useSmtpMigrationDB(t)
	const retiredAccounts = `[{"id":"smtp_a","name":"a","enabled":true,"server":"8","port":587,"account":"7","token":"json-secret-placeholder"}]`
	require.NoError(t, db.Create(&Option{Key: retiredSMTPAccountsOptionKey, Value: retiredAccounts}).Error)
	common.OptionMap[retiredSMTPAccountsOptionKey] = retiredAccounts

	require.NoError(t, MigrateLegacySMTPAccount())

	var leftover []Option
	require.NoError(t, db.Where(map[string]any{"key": retiredSMTPAccountsOptionKey}).Find(&leftover).Error)
	assert.Empty(t, leftover, "含明文密码的 SMTPAccounts 必须从 options 表里清掉")

	_, stillInMemory := common.OptionMap[retiredSMTPAccountsOptionKey]
	assert.False(t, stillInMemory, "本进程内存里的那份副本也要清掉,否则要等下次重启才不下发")

	var accounts []*SmtpAccount
	require.NoError(t, db.Find(&accounts).Error)
	assert.Empty(t, accounts,
		"那块 JSON 里的账号从没经过写入侧校验(server 可以是 \"8\"),自动导进来只会让轮转固定比例失败")
}
