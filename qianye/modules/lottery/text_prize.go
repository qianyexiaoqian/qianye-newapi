package lottery

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	"github.com/QuantumNous/new-api/qianye/httpq"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/audit"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// text_prize.go —— 文本奖(兑换码 / CDK / 实物说明)的履行与可见性。
//
// # 分层:奖品是什么 vs 你的那一份具体是什么
//
// 这一刀必须划清楚,否则兑换码会顺着匿名证据链流出去。
//
//	公示层(Prize.TextDesc):这一档发的是什么、叫什么、有几份、公开的履行说明。
//	              **进 spec 原像 → spec_hash → commit_hash**,发布时钉死,
//	              事后改一个字都开不了奖。它本来就要展示给所有人,
//	              所以明文进原像 —— 不需要哈希,也不需要盐。
//	私密层(Payout.Secret*):中奖者手里那一串实际兑换码。
//	              **没有任何承诺**,不进证据链的任何一层,连哈希都不进。
//
// # 为什么兑换码没有承诺(必须诚实说清的不对称)
//
// 项目方已拍板"管理员手动履行":那么发布时那串码**根本不存在**,无从承诺。
// 第三方能验的是"奖品的名称、说明、数量在发布时钉死且事后没被增发没被掉包";
// "我拿到的这串码是不是当初承诺的那一串"在本轮**不可验证**。
// 这句话原样写进 proof 的 notice 与活动规则页,不粉饰。
//
// 顺带说明为什么不做"发布时先承诺一批码":那需要一整个库存子系统(导入去重、
// 开奖事务内的原子分配、"码不够了"这个发生在最坏时刻的失败态),
// 以及一个新的明文泄漏面 —— 而它只是把"管理员填码"这件事提前了。
//
// # 为什么连哈希都不进证据链
//
// proof 是**匿名可访问**的,act_no / tier / 域前缀全部公开。一个 8 位兑换码约
// 40 位熵 —— 公开一个可复现原像的哈希等于给了离线爆破面,与直接公开只差几分钟
// 算力。所谓"用 act_no 当盐"一个比特的保护都没有,因为 act_no 就印在同一份
// 文件里。
//
// # 不自动发放带来的技术红利
//
// **正因为文本奖不自动发放,混合奖档才根本不存在"码发了钱没发"的问题。**
// 跨库的那一半(额度)有完整的两阶段 + 探针 + 代次,不跨库的那一半(文本)
// 根本不需要。一旦以后要加自动发码,就会凭空多出一整套跨系统两阶段。

// maxFulfillSecretRunes / maxFulfillNoteRunes 是履行时两个输入的长度上限。
const (
	maxFulfillSecretRunes = 500
	maxFulfillNoteRunes   = 200
	maxUnfulfillReason    = 200
	// minRevealReason 与提现揭示收款账号同一个数:看明文必须交代为什么。
	minRevealReason = 4
)

// ─────────────────────────── 密文封装 ───────────────────────────

// sealPrizeSecret 封装中奖者的实际兑换码。
//
// # 为什么这一列必须是真密文
//
// 它存的是管理员为中奖者填进去的**实际兑换码**,而同一个扩展库里性质相同的
// 两处 —— qy_withdrawal_payee_accounts(收款账号)与 qy_violation_ai_channel
// (渠道密钥)—— 都是 AES-256-GCM 真密文。此前这一列是明文直存
// (key_version 恒 0、nonce 恒 NULL),于是列名叫 cipher、内容任何拿到库备份、
// 只读报表账号或离线 dump 的人都能直接读走,而在线侧那一整套控制
// (json:"-" 不下发、列表只回 maskSecret、reveal 强制事由 + 双写审计)
// 一条都拦不住他们。
//
// # 未配置密钥时仍然是明文,这是刻意的
//
// lottery.prize_secret_key 为空 → 走 keyVersion=0 的明文分支。强制要求它会让
// 每一个现存部署在升级那一刻 FATAL 退出,而那是破坏性变更;自检里会为此报一条
// 告警。配上之后**新写入**的码即刻加密,历史的 v0 行仍然读得出来。
//
// aad 绑定 payout_no:密文若被搬到另一条记录上,GCM 校验会直接失败,
// 而不是安静地解出一份属于别人的兑换码。
//
// 本函数与 openPrizeSecret 是**包内唯一**允许触碰 Secret* 三列的地方,
// 由 secret_guard_test.go 的 AST 断言守住。
func sealPrizeSecret(plain, aad string) (nonce, cipher []byte, keyVersion int, err error) {
	key, version, ok, err := activePrizeSecretKey()
	if err != nil {
		return nil, nil, 0, err
	}
	if !ok {
		// 未配置密钥:明文直存,与改造前逐字节相同。
		return nil, []byte(plain), 0, nil
	}
	nonce, ciphertext, err := sealAESGCM(key, []byte(plain), aad)
	if err != nil {
		return nil, nil, 0, err
	}
	return nonce, ciphertext, version, nil
}

// openPrizeSecret 取回中奖者的实际兑换码。
//
// keyVersion 是那一列存在的全部理由:按**行上记录的版本**选密钥,
// 而不是一律用当前密钥。轮换 prize_secret_key 时若少了这一层,
// 已履行的兑换码会全部变成不可读。
func openPrizeSecret(nonce, cipher []byte, aad string, keyVersion int) (string, error) {
	if keyVersion <= 0 {
		// 历史行(以及未配置密钥时写下的行)是明文。nonce 必须为空 ——
		// 有 nonce 却标 v0 说明数据被改过,不猜。
		if len(nonce) != 0 {
			return "", errPrizeSecretUnreadable
		}
		return string(cipher), nil
	}
	key, err := prizeSecretKeyForVersion(keyVersion)
	if err != nil {
		return "", err
	}
	plain, err := openAESGCM(key, nonce, cipher, aad)
	if err != nil {
		return "", errPrizeSecretUnreadable
	}
	return string(plain), nil
}

// activePrizeSecretKey 返回当前启用的密钥与版本;ok=false 表示没配,走明文。
//
// 密钥与版本取自**同一份**配置快照:config.Get() 每次返回当前快照指针,
// 两次分开取恰好落在热更新两侧时,会把 v1 的密文标成 v2 —— 那一行从此
// 永远解不开,而且没有任何迹象(与 withdraw.activePIIKey 同一条理由)。
func activePrizeSecretKey() ([]byte, int, bool, error) {
	l := config.Get().Lottery
	raw := strings.TrimSpace(l.PrizeSecretKey)
	if raw == "" {
		return nil, 0, false, nil
	}
	key, err := decodePrizeSecretKey(raw)
	if err != nil {
		return nil, 0, false, err
	}
	return key, activePrizeSecretKeyVersion(l), true, nil
}

func activePrizeSecretKeyVersion(l config.Lottery) int {
	if l.PrizeSecretKeyVersion <= 0 {
		return 1
	}
	return l.PrizeSecretKeyVersion
}

// prizeSecretKeyForVersion 按密文行上的版本号选密钥。
func prizeSecretKeyForVersion(version int) ([]byte, error) {
	l := config.Get().Lottery
	if version == activePrizeSecretKeyVersion(l) {
		if strings.TrimSpace(l.PrizeSecretKey) == "" {
			common.SysError(fmt.Sprintf(
				"qianye/lottery: 有 key_version=%d 的兑换码密文,但 lottery.prize_secret_key 未配置", version))
			return nil, errPrizeSecretUnreadable
		}
		return decodePrizeSecretKey(l.PrizeSecretKey)
	}
	raw, ok := l.PrizeSecretKeysRetired[version]
	if !ok {
		// 轮换时忘了把旧钥搬进 prize_secret_keys_retired,是最典型的一种运维
		// 事故:表现是"全部已履行的兑换码突然都看不到了"。错误必须直指配置。
		common.SysError(fmt.Sprintf(
			"qianye/lottery: 兑换码使用的密钥版本 %d 未在 lottery.prize_secret_keys_retired 中登记,"+
				"该行无法解密(轮换 prize_secret_key 时必须把旧密钥连同版本号一并保留)", version))
		return nil, errPrizeSecretUnreadable
	}
	return decodePrizeSecretKey(raw)
}

// decodePrizeSecretKey 解出 32 字节的 AES-256 密钥。规格与 withdraw.pii_key 相同。
func decodePrizeSecretKey(raw string) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("qianye/lottery: prize_secret_key 必须是 base64 编码的 32 字节密钥")
	}
	return key, nil
}

func sealAESGCM(key, plain []byte, aad string) (nonce, ciphertext []byte, err error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, err
	}
	nonce = make([]byte, gcm.NonceSize())
	// 必须是 crypto/rand:GCM 在同一密钥下 nonce 重用会直接泄漏明文异或值。
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, err
	}
	return nonce, gcm.Seal(nil, nonce, plain, []byte(aad)), nil
}

func openAESGCM(key, nonce, ciphertext []byte, aad string) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(nonce) != gcm.NonceSize() || len(ciphertext) == 0 {
		return nil, errPrizeSecretUnreadable
	}
	return gcm.Open(nil, nonce, ciphertext, []byte(aad))
}

// archivePrizeSecret 把一份**即将被顶替**的兑换码整体搬进只增不改的履历。
//
// 调用点只有一个:再次履行之前(unfulfill 之后)。撤销履行刻意不清密文,
// 因此再次履行的 CAS 会把上一串码整列覆盖 —— 而 Event 与审计快照按设计
// 都不含明文,不归档的话第一串码在系统里彻底消失。
//
// 与 sealPrizeSecret / openPrizeSecret 同一档纪律:这是包内第三个、
// 也是最后一个允许触碰 Secret* 三列的地方(secret_guard_test.go 守住)。
func archivePrizeSecret(tx *gorm.DB, p *Payout, adminId int) error {
	if len(p.SecretCipher) == 0 {
		return nil
	}
	var seq int64
	if err := tx.Model(&PrizeSecretHist{}).
		Where("payout_no = ?", p.PayoutNo).Count(&seq).Error; err != nil {
		return err
	}
	return tx.Create(&PrizeSecretHist{
		PayoutNo:         p.PayoutNo,
		Seq:              int(seq) + 1,
		SecretNonce:      p.SecretNonce,
		SecretCipher:     p.SecretCipher,
		SecretKeyVersion: p.SecretKeyVersion,
		FulfilledAt:      p.FulfilledAt,
		FulfilledBy:      p.FulfilledBy,
		FulfillNote:      p.FulfillNote,
		SupersededAt:     common.GetTimestamp(),
		SupersededBy:     adminId,
	}).Error
}

// supersededSecret 是 reveal 接口下发的一条历史明文。
type supersededSecret struct {
	Seq          int    `json:"seq"`
	Secret       string `json:"secret"`
	Note         string `json:"note"`
	FulfilledAt  int64  `json:"fulfilled_at"`
	FulfilledBy  int    `json:"fulfilled_by"`
	SupersededAt int64  `json:"superseded_at"`
	SupersededBy int    `json:"superseded_by"`
}

// loadSupersededSecrets 取回被顶替过的历史明文。
//
// 只在写审计的 reveal 接口里调用 —— 归档如果读不出来,它就只是一张让人心安的
// 表,回答不了"用户当初拿到的是哪一串"这个它唯一存在的理由。
func loadSupersededSecrets(ctx context.Context, gdb *gorm.DB, payoutNo string) ([]supersededSecret, error) {
	var rows []PrizeSecretHist
	if err := gdb.WithContext(ctx).Where("payout_no = ?", payoutNo).
		Order("seq asc").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]supersededSecret, 0, len(rows))
	for i := range rows {
		plain, err := openPrizeSecret(rows[i].SecretNonce, rows[i].SecretCipher,
			rows[i].PayoutNo, rows[i].SecretKeyVersion)
		if err != nil {
			return nil, err
		}
		out = append(out, supersededSecret{
			Seq: rows[i].Seq, Secret: plain, Note: rows[i].FulfillNote,
			FulfilledAt: rows[i].FulfilledAt, FulfilledBy: rows[i].FulfilledBy,
			SupersededAt: rows[i].SupersededAt, SupersededBy: rows[i].SupersededBy,
		})
	}
	return out, nil
}

// maskSecret 把兑换码压成一个**只表示"填了没填"**的掩码。
//
// 一个字符都不露。曾经的写法是首 2 + 末 2,那对短码等于泄漏:6 位数字码露出
// 首末各一段后剩余搜索空间只有 100,而管理端列表(handleAdminListTextPrizes)
// 只需要只读管理权限、**不写任何审计** —— 那条写审计的 reveal 路会被完全绕过。
//
// "认出是哪一条"由 payout_no 负责,它就在同一行里,并且不可枚举。
// 掩码要回答的只是"这一条有没有内容",长度本身就够了。
func maskSecret(secret string) string {
	n := utf8.RuneCountInString(secret)
	if n == 0 {
		return ""
	}
	if n > 12 {
		// 长度也不必精确外泄:超过 12 就统一压平,避免"内容有多长"变成一条
		// 可用来区分码型的侧信道。
		n = 12
	}
	return strings.Repeat("*", n)
}

// ─────────────────────────── 用户侧 ───────────────────────────

type myPrizeView struct {
	PayoutNo string `json:"payout_no"`
	ActNo    string `json:"act_no"`
	Title    string `json:"title"`
	Tier     int    `json:"tier"`
	Name     string `json:"name"`
	// TextDesc 是公示层的履行说明,发布时就已进承诺。
	TextDesc string `json:"text_desc"`
	// Status 是 pending(等管理员履行)或 fulfilled。
	Status      string `json:"status"`
	FulfilledAt int64  `json:"fulfilled_at"`
	// Secret 只在已履行时非空。
	Secret string `json:"secret,omitempty"`
	Note   string `json:"note,omitempty"`
	// Notice 是那条诚实边界:这串码没有承诺。
	Notice string `json:"notice"`
}

const textPrizeNotice = "本奖品的公开说明与份数在活动发布时就已进入承诺哈希,事后不可更改;" +
	"但你拿到的这串具体内容是开奖之后由管理员填入的,**它没有进入承诺**," +
	"因为在发布那一刻它还不存在。"

// handleGetMyPrize 逐条返回中奖者自己的文本奖。
//
// **刻意不做批量列表**:一个列表接口返回全部正文,意味着一次越权 bug 就是
// 全量泄漏。payout_no 由 crypto/rand 生成,不可枚举。
func handleGetMyPrize(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagLottery) {
		return
	}
	gdb := db.Get()
	if gdb == nil {
		respondErr(c, db.ErrNotReady)
		return
	}
	ctx := c.Request.Context()

	var p Payout
	err := gdb.WithContext(ctx).
		Where("payout_no = ? AND kind = ?", c.Param("payout_no"), PayoutText).
		Take(&p).Error
	if err != nil {
		// 不区分"不存在"与"不是你的":两者回同一个 404,否则这个接口就成了
		// 一个可以逐个试探他人单号的存在性预言机。
		respondErr(c, errPayoutNotFound)
		return
	}
	if p.UserId != c.GetInt("id") {
		respondErr(c, errPayoutNotFound)
		return
	}

	view := myPrizeView{
		PayoutNo: p.PayoutNo, Tier: p.Tier, Status: "pending",
		FulfilledAt: p.FulfilledAt, Notice: textPrizeNotice,
	}
	var act Activity
	if err := gdb.WithContext(ctx).Select("act_no, title").
		Where("id = ?", p.ActId).Take(&act).Error; err == nil {
		view.ActNo, view.Title = act.ActNo, act.Title
	}
	var prize Prize
	if err := gdb.WithContext(ctx).Select("name, text_desc").
		Where("act_id = ? AND tier = ?", p.ActId, p.Tier).Take(&prize).Error; err == nil {
		view.Name, view.TextDesc = prize.Name, prize.TextDesc
	}

	if p.FulfilledAt > 0 {
		secret, err := openPrizeSecret(p.SecretNonce, p.SecretCipher, p.PayoutNo, p.SecretKeyVersion)
		if err != nil {
			respondErr(c, err)
			return
		}
		view.Status = "fulfilled"
		view.Secret = secret
		view.Note = p.FulfillNote
	}
	respondOK(c, view)
}

// ─────────────────────────── 管理端 ───────────────────────────

type textPrizeRow struct {
	PayoutNo string `json:"payout_no"`
	ActNo    string `json:"act_no"`
	Title    string `json:"title"`
	Tier     int    `json:"tier"`
	UserId   int    `json:"user_id"`
	Username string `json:"username"`
	// SecretMask 是掩码。**明文只走 reveal 那一个写审计的接口。**
	SecretMask  string `json:"secret_mask"`
	Fulfilled   bool   `json:"fulfilled"`
	FulfilledAt int64  `json:"fulfilled_at"`
	FulfilledBy int    `json:"fulfilled_by"`
	FulfillNote string `json:"fulfill_note"`
	CreatedAt   int64  `json:"created_at"`
}

// handleAdminListTextPrizes 返回跨活动的文本奖履行队列。
//
// 列表页顶部那个"待履行"红点直读它的 total(?fulfilled=0)。
// 它与 text_prize_stale 异常同源 —— 一个盯人的机制必须有一个人会看的落点。
func handleAdminListTextPrizes(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	page, size := httpq.Paginate(c, listPaging)
	gdb := db.Get()
	if gdb == nil {
		respondErr(c, db.ErrNotReady)
		return
	}
	gdb = gdb.WithContext(c.Request.Context())

	q := gdb.Model(&Payout{}).Where("kind = ?", PayoutText)
	switch c.Query("fulfilled") {
	case "1":
		q = q.Where("fulfilled_at > 0")
	case "0":
		q = q.Where("fulfilled_at = 0")
	}
	if v := c.Query("act_no"); v != "" {
		var act Activity
		if err := gdb.Select("id").Where("act_no = ?", v).Take(&act).Error; err != nil {
			respondErr(c, errActivityNotFound)
			return
		}
		q = q.Where("act_id = ?", act.Id)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, wrapInternal("统计文本奖", err))
		return
	}
	rows := make([]Payout, 0, size)
	err := q.Order("fulfilled_at asc, id asc").
		Offset(httpq.Offset(page, size)).Limit(size).Find(&rows).Error
	if err != nil {
		db.MarkFailure(err)
		respondErr(c, wrapInternal("查询文本奖", err))
		return
	}

	// 下发给前端的数组一律显式初始化,理由见 qianye/json_array_guard_test.go。
	out := make([]textPrizeRow, 0, len(rows))
	acts := make(map[int64]Activity, 8)
	entries := make(map[int64]Entry, len(rows))
	for _, p := range rows {
		if _, ok := acts[p.ActId]; !ok {
			var a Activity
			if err := gdb.Select("act_no, title").Where("id = ?", p.ActId).Take(&a).Error; err == nil {
				acts[p.ActId] = a
			}
		}
		if _, ok := entries[p.EntryId]; !ok {
			var e Entry
			if err := gdb.Select("username").Where("id = ?", p.EntryId).Take(&e).Error; err == nil {
				entries[p.EntryId] = e
			}
		}
		secret, _ := openPrizeSecret(p.SecretNonce, p.SecretCipher, p.PayoutNo, p.SecretKeyVersion)
		out = append(out, textPrizeRow{
			PayoutNo: p.PayoutNo, ActNo: acts[p.ActId].ActNo, Title: acts[p.ActId].Title,
			Tier: p.Tier, UserId: p.UserId, Username: entries[p.EntryId].Username,
			SecretMask: maskSecret(secret),
			Fulfilled:  p.FulfilledAt > 0, FulfilledAt: p.FulfilledAt, FulfilledBy: p.FulfilledBy,
			FulfillNote: p.FulfillNote, CreatedAt: p.CreatedAt,
		})
	}
	respondOK(c, gin.H{"items": out, "total": total, "p": page, "page_size": size})
}

// handleRevealPrizeSecret 向管理员展示一份文本奖的明文。
//
// 独立成一个接口而不是让列表直接带明文:看明文是一个**二次确认 + 写审计**的
// 动作,与提现页揭示收款账号同一套姿势。写两条痕迹 ——
// Event(用户可见的履历)与全局审计(跨模块台账)。
func handleRevealPrizeSecret(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	payoutNo := c.Param("payout_no")
	// 事由是**服务端强制**的,与提现揭示收款账号逐字同一套:前端那个"必填 ≥4
	// 字符"的输入框只是提示,谁都可以绕过它直接 POST。没有这一段,"你为什么看了
	// 这 50 个中奖者的码"在台账里无法回答 —— 审计行的 reason 恒为空。
	var req revealRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeAdminAudit(c, "lottery.prize.reveal", payoutNo, qymodel.ResultFail, "请求体解析失败", "", "")
		respondErr(c, errBadRequest("请求参数不合法"))
		return
	}
	reason := strings.TrimSpace(req.Reason)
	if utf8.RuneCountInString(reason) < minRevealReason || utf8.RuneCountInString(reason) > maxUnfulfillReason {
		writeAdminAudit(c, "lottery.prize.reveal", payoutNo, qymodel.ResultFail, "未填写查看事由", "", "")
		respondErr(c, errBadRequest("必须填写查看明文的事由(至少 4 个字),它会进入审计与不可删除的履历"))
		return
	}

	ctx, cancel := guard.ColdContext(context.Background())
	defer cancel()

	p, err := loadTextPayout(ctx, payoutNo)
	if err != nil {
		writeAdminAudit(c, "lottery.prize.reveal", payoutNo, qymodel.ResultFail, auditReason(err), "", "")
		respondErr(c, err)
		return
	}
	if p.FulfilledAt == 0 {
		writeAdminAudit(c, "lottery.prize.reveal", payoutNo, qymodel.ResultFail, "尚未履行,没有可展示的内容", "", "")
		respondErr(c, errPrizeNotFulfilled)
		return
	}
	secret, err := openPrizeSecret(p.SecretNonce, p.SecretCipher, p.PayoutNo, p.SecretKeyVersion)
	if err != nil {
		writeAdminAudit(c, "lottery.prize.reveal", payoutNo, qymodel.ResultFail, auditReason(err), "", "")
		respondErr(c, err)
		return
	}

	gdb := db.Get()
	if gdb == nil {
		respondErr(c, db.ErrNotReady)
		return
	}
	// 被顶替过的历史明文也一并下发。归档如果读不出来,它就只是一张让人心安的表:
	// 争议的原话永远是"我用的那串码失效了",而回答它需要看到当初发出去的那一串。
	history, err := loadSupersededSecrets(ctx, gdb, p.PayoutNo)
	if err != nil {
		db.MarkFailure(err)
		writeAdminAudit(c, "lottery.prize.reveal", payoutNo, qymodel.ResultFail, auditReason(err), "", "")
		respondErr(c, wrapInternal("读取文本奖履历", err))
		return
	}
	// 履历与审计都**只记"谁在什么时候看了哪一条"**,绝不记内容本身 ——
	// 否则明文会在事件流与审计表里各留一份,而那两张表的读者范围比这个接口宽。
	err = gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := writeActivityEvent(tx, p.ActId, "", "", ActionPrizeReveal,
			qymodel.ActorAdmin, c.GetInt("id"), map[string]any{
				"payout_no": p.PayoutNo, "tier": p.Tier, "reason": reason,
			}); err != nil {
			return err
		}
		return audit.WriteTx(tx, audit.Entry{
			TraceNo:     p.PayoutNo,
			Category:    auditCategory,
			Action:      "lottery.prize.reveal",
			ActorType:   qymodel.ActorAdmin,
			ActorUserId: c.GetInt("id"),
			ActorName:   c.GetString("username"),
			Result:      qymodel.ResultOK,
			Reason:      audit.Truncate(reason, 500),
			AfterSnap:   snapText(map[string]any{"payout_no": p.PayoutNo, "tier": p.Tier}),
		})
	})
	if err != nil {
		db.MarkFailure(err)
		writeAdminAudit(c, "lottery.prize.reveal", payoutNo, qymodel.ResultFail, auditReason(err), "", "")
		respondErr(c, wrapInternal("揭示文本奖", err))
		return
	}
	respondOK(c, gin.H{
		"payout_no": p.PayoutNo, "secret": secret, "note": p.FulfillNote,
		"superseded": history,
	})
}

// revealRequest 是揭示明文的请求体。事由必填,服务端强制。
type revealRequest struct {
	Reason string `json:"reason"`
}

type fulfillRequest struct {
	Secret string `json:"secret"`
	Note   string `json:"note"`
}

// handleFulfillPrize 标记一份文本奖已履行,并存入要发给中奖者的内容。
//
// 幂等靠 CAS 的 `fulfilled_at = 0`:重复提交第二次 RowsAffected==0,
// 返回"已履行过"而不是覆盖掉第一次填的内容 —— 覆盖会让用户先看到 A 再看到 B,
// 而争议时没人说得清他到底用掉了哪一个。
func handleFulfillPrize(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	payoutNo := c.Param("payout_no")
	var req fulfillRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeAdminAudit(c, "lottery.prize.fulfill", payoutNo, qymodel.ResultFail, "请求体解析失败", "", "")
		respondErr(c, errBadRequest("请求参数不合法"))
		return
	}
	secret := strings.TrimSpace(req.Secret)
	note := strings.TrimSpace(req.Note)
	if secret == "" || utf8.RuneCountInString(secret) > maxFulfillSecretRunes {
		writeAdminAudit(c, "lottery.prize.fulfill", payoutNo, qymodel.ResultFail, "未填写要发给中奖者的内容", "", "")
		respondErr(c, errBadRequest("必须填写要发给中奖者的内容,且不超过 500 个字"))
		return
	}
	if utf8.RuneCountInString(note) > maxFulfillNoteRunes {
		writeAdminAudit(c, "lottery.prize.fulfill", payoutNo, qymodel.ResultFail, "备注过长", "", "")
		respondErr(c, errBadRequest("备注不超过 200 个字"))
		return
	}

	ctx, cancel := guard.ColdContext(context.Background())
	defer cancel()
	p, err := loadTextPayout(ctx, payoutNo)
	if err != nil {
		writeAdminAudit(c, "lottery.prize.fulfill", payoutNo, qymodel.ResultFail, auditReason(err), "", "")
		respondErr(c, err)
		return
	}

	nonce, cipher, keyVersion, err := sealPrizeSecret(secret, p.PayoutNo)
	if err != nil {
		writeAdminAudit(c, "lottery.prize.fulfill", payoutNo, qymodel.ResultFail, auditReason(err), "", "")
		respondErr(c, wrapInternal("封装文本奖内容", err))
		return
	}

	gdb := db.Get()
	if gdb == nil {
		respondErr(c, db.ErrNotReady)
		return
	}
	now := common.GetTimestamp()
	adminId := c.GetInt("id")
	err = gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 覆盖之前先归档。走到这里且行上已有密文,只可能是
		// 「履行 → 撤销履行 → 再次履行」:撤销刻意不清密文,而下面那条 CAS
		// 只判 fulfilled_at = 0,会把上一串码整列覆盖。
		// 事务内重读一次,而不是用函数开头那份快照 —— 两次读之间可能有别的
		// 管理员刚履行过,归档必须归的是**即将被覆盖的那一份**。
		var cur Payout
		if err := tx.Where("payout_no = ? AND kind = ?", payoutNo, PayoutText).
			Take(&cur).Error; err != nil {
			return err
		}
		if err := archivePrizeSecret(tx, &cur, adminId); err != nil {
			return err
		}
		res := tx.Model(&Payout{}).
			Where("payout_no = ? AND kind = ? AND fulfilled_at = 0", payoutNo, PayoutText).
			Updates(map[string]any{
				"fulfilled_at":       now,
				"fulfilled_by":       adminId,
				"fulfill_note":       note,
				"secret_nonce":       nonce,
				"secret_cipher":      cipher,
				"secret_key_version": keyVersion,
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return errPrizeAlreadyFulfilled
		}
		if err := writeActivityEvent(tx, p.ActId, "", "", ActionPrizeFulfill,
			qymodel.ActorAdmin, adminId, map[string]any{
				"payout_no": payoutNo, "tier": p.Tier,
			}); err != nil {
			return err
		}
		return audit.WriteTx(tx, audit.Entry{
			TraceNo:     payoutNo,
			Category:    auditCategory,
			Action:      "lottery.prize.fulfill",
			ActorType:   qymodel.ActorAdmin,
			ActorUserId: adminId,
			ActorName:   c.GetString("username"),
			Result:      qymodel.ResultOK,
			BeforeSnap:  snapText(map[string]any{"payout_no": payoutNo, "fulfilled": false}),
			// 快照里**绝不放 secret**:审计表的保留期与读者范围都不归本模块管。
			AfterSnap: snapText(map[string]any{"payout_no": payoutNo, "fulfilled": true, "note": note}),
		})
	})
	if err != nil {
		if err != errPrizeAlreadyFulfilled {
			db.MarkFailure(err)
			err = wrapInternal("履行文本奖", err)
		}
		writeAdminAudit(c, "lottery.prize.fulfill", payoutNo, qymodel.ResultFail, auditReason(err), "", "")
		respondErr(c, err)
		return
	}
	respondOK(c, gin.H{"payout_no": payoutNo, "fulfilled_at": now})
}

type unfulfillRequest struct {
	Reason string `json:"reason"`
}

// handleUnfulfillPrize 撤销一次误标的履行。
//
// **不清空密文。** 用户可能已经看到并用掉了那串码,抹掉记录等于抹掉争议时唯一的
// 证据。撤销只是账面纠错:明文本身撤不回来,这句话原样写在管理端的二次确认上。
//
// 只对 kind='text' 开放:额度奖的 paid 是**资金终态**,永远不可撤。
func handleUnfulfillPrize(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	payoutNo := c.Param("payout_no")
	var req unfulfillRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeAdminAudit(c, "lottery.prize.unfulfill", payoutNo, qymodel.ResultFail, "请求体解析失败", "", "")
		respondErr(c, errBadRequest("请求参数不合法"))
		return
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" || utf8.RuneCountInString(reason) > maxUnfulfillReason {
		writeAdminAudit(c, "lottery.prize.unfulfill", payoutNo, qymodel.ResultFail, "未填写撤销原因", "", "")
		respondErr(c, errBadRequest("必须填写撤销原因,它会进入不可删除的履历"))
		return
	}

	ctx, cancel := guard.ColdContext(context.Background())
	defer cancel()
	p, err := loadTextPayout(ctx, payoutNo)
	if err != nil {
		writeAdminAudit(c, "lottery.prize.unfulfill", payoutNo, qymodel.ResultFail, auditReason(err), "", "")
		respondErr(c, err)
		return
	}

	gdb := db.Get()
	if gdb == nil {
		respondErr(c, db.ErrNotReady)
		return
	}
	adminId := c.GetInt("id")
	err = gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&Payout{}).
			Where("payout_no = ? AND kind = ? AND fulfilled_at > 0", payoutNo, PayoutText).
			Updates(map[string]any{
				"fulfilled_at": 0,
				"fulfilled_by": 0,
				"fulfill_note": "",
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return errPrizeNotFulfilled
		}
		// Event 是 append-only:误标的痕迹永远在,消失的只是那个错误的当前态。
		if err := writeActivityEvent(tx, p.ActId, "", "", ActionPrizeUnfulfill,
			qymodel.ActorAdmin, adminId, map[string]any{
				// 备注在这一步被清掉,而它不是秘密(它就是给用户看的领取说明)。
				// 不留在履历里的话,"当初那份奖是怎么交代领取方式的"随撤销一起消失。
				"payout_no": payoutNo, "tier": p.Tier, "reason": reason,
				"cleared_note": p.FulfillNote,
			}); err != nil {
			return err
		}
		return audit.WriteTx(tx, audit.Entry{
			TraceNo:     payoutNo,
			Category:    auditCategory,
			Action:      "lottery.prize.unfulfill",
			ActorType:   qymodel.ActorAdmin,
			ActorUserId: adminId,
			ActorName:   c.GetString("username"),
			Result:      qymodel.ResultOK,
			Reason:      reason,
			BeforeSnap: snapText(map[string]any{
				"payout_no": payoutNo, "fulfilled": true, "note": p.FulfillNote}),
			AfterSnap: snapText(map[string]any{"payout_no": payoutNo, "fulfilled": false}),
		})
	})
	if err != nil {
		if err != errPrizeNotFulfilled {
			db.MarkFailure(err)
			err = wrapInternal("撤销文本奖履行", err)
		}
		writeAdminAudit(c, "lottery.prize.unfulfill", payoutNo, qymodel.ResultFail, auditReason(err), "", "")
		respondErr(c, err)
		return
	}
	respondOK(c, gin.H{"payout_no": payoutNo, "fulfilled_at": 0})
}

// loadTextPayout 取出一条文本奖出款行。
//
// kind 不是 text 时回 errNotTextPrize 而不是 404:履行/撤销这三个接口对额度奖
// **必须明确拒绝**,因为额度奖的终态是资金终态,永远不可撤 ——
// 回 404 会让人以为只是单号打错了,然后去找一个"正确"的单号再试一次。
func loadTextPayout(ctx context.Context, payoutNo string) (*Payout, error) {
	if strings.TrimSpace(payoutNo) == "" {
		return nil, errBadRequest("缺少出款单号")
	}
	gdb := db.Get()
	if gdb == nil {
		return nil, db.ErrNotReady
	}
	var p Payout
	if err := gdb.WithContext(ctx).Where("payout_no = ?", payoutNo).Take(&p).Error; err != nil {
		return nil, errPayoutNotFound
	}
	if p.Kind != PayoutText {
		return nil, errNotTextPrize
	}
	return &p, nil
}
