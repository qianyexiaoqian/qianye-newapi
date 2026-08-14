package controller

// smtp_account.go —— SMTP 发件账号的管理端 CRUD。
//
// 单条增删改,不做整表覆盖:整表覆盖时两个管理员同屏各改一个账号,
// 后保存的会把先保存的静默盖掉,而界面上两次都提示成功。

import (
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
)

// smtpAccountView 是下发给管理端的账号形态。
//
// **它没有 Token 字段。** 回显密码等于把全部发件凭据发给每一个打开设置页的
// 管理员,而这些凭据在很多站点是同一套邮箱的主密码。前端提交时留空即表示
// 「不改密码」(model.UpdateSmtpAccount 保留库里原值)。
type smtpAccountView struct {
	Id                 int    `json:"id"`
	AccountId          string `json:"account_id"`
	Name               string `json:"name"`
	Enabled            bool   `json:"enabled"`
	Server             string `json:"server"`
	Port               int    `json:"port"`
	Account            string `json:"account"`
	FromAddr           string `json:"from_addr"`
	SSLEnabled         bool   `json:"ssl_enabled"`
	StartTLSEnabled    bool   `json:"start_tls_enabled"`
	InsecureSkipVerify bool   `json:"insecure_skip_verify"`
	ForceAuthLogin     bool   `json:"force_auth_login"`
	HourlyLimit        int    `json:"hourly_limit"`
	SortOrder          int    `json:"sort_order"`
	// HasToken 让界面能区分「这个号还没配密码」与「配了但不给你看」。
	// 少了它,两种状态在页面上长得一模一样,而前者发一封信就会认证失败。
	HasToken bool `json:"has_token"`
}

func toSmtpAccountView(a *model.SmtpAccount) smtpAccountView {
	return smtpAccountView{
		Id:                 a.Id,
		AccountId:          a.AccountId,
		Name:               a.Name,
		Enabled:            a.Enabled,
		Server:             a.Server,
		Port:               a.Port,
		Account:            a.Account,
		FromAddr:           a.FromAddr,
		SSLEnabled:         a.SSLEnabled,
		StartTLSEnabled:    a.StartTLSEnabled,
		InsecureSkipVerify: a.InsecureSkipVerify,
		ForceAuthLogin:     a.ForceAuthLogin,
		HourlyLimit:        a.HourlyLimit,
		SortOrder:          a.SortOrder,
		HasToken:           a.Token != "",
	}
}

func GetSmtpAccounts(c *gin.Context) {
	rows, err := model.GetAllSmtpAccounts()
	if err != nil {
		common.ApiError(c, err)
		return
	}
	out := make([]smtpAccountView, 0, len(rows))
	for _, r := range rows {
		out = append(out, toSmtpAccountView(r))
	}
	common.ApiSuccess(c, out)
}

func CreateSmtpAccount(c *gin.Context) {
	var account model.SmtpAccount
	if err := c.ShouldBindJSON(&account); err != nil {
		common.ApiErrorMsg(c, "无效的参数")
		return
	}
	if err := model.CreateSmtpAccount(&account); err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, toSmtpAccountView(&account))
}

func UpdateSmtpAccount(c *gin.Context) {
	var account model.SmtpAccount
	if err := c.ShouldBindJSON(&account); err != nil {
		common.ApiErrorMsg(c, "无效的参数")
		return
	}
	if account.Id <= 0 {
		common.ApiErrorMsg(c, "缺少 id")
		return
	}
	if err := model.UpdateSmtpAccount(&account); err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, nil)
}

func DeleteSmtpAccount(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		common.ApiErrorMsg(c, "无效的 id")
		return
	}
	if err := model.DeleteSmtpAccount(id); err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, nil)
}
