package common

import (
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net/smtp"
	"slices"
	"strings"
	"time"
)

func generateMessageID(from string) (string, error) {
	split := strings.Split(from, "@")
	if len(split) < 2 {
		return "", fmt.Errorf("invalid SMTP account")
	}
	domain := split[1]
	return fmt.Sprintf("<%d.%s@%s>", time.Now().UnixNano(), GetRandomString(12), domain), nil
}

func shouldUseSMTPLoginAuth(account SMTPAccountConfig) bool {
	if account.ForceAuthLogin {
		return true
	}
	return isOutlookServer(account.Account) || slices.Contains(EmailLoginAuthServerList, account.Server)
}

func getSMTPAuth(account SMTPAccountConfig) smtp.Auth {
	return AutoSMTPAuth(account)
}

func shouldAuthenticateSMTP(account SMTPAccountConfig) bool {
	return account.Account != "" && account.Token != ""
}

func smtpTLSConfig(account SMTPAccountConfig) *tls.Config {
	return &tls.Config{
		ServerName:         account.Server,
		InsecureSkipVerify: account.InsecureSkipVerify, // #nosec G402 -- admin-controlled SMTP compatibility option.
	}
}

func newSMTPClient(addr string, account SMTPAccountConfig) (*smtp.Client, error) {
	if account.SSLEnabled || (account.Port == 465 && !account.StartTLSEnabled) {
		conn, err := tls.Dial("tcp", addr, smtpTLSConfig(account))
		if err != nil {
			return nil, err
		}
		client, err := smtp.NewClient(conn, account.Server)
		if err != nil {
			_ = conn.Close()
			return nil, err
		}
		return client, nil
	}

	client, err := smtp.Dial(addr)
	if err != nil {
		return nil, err
	}

	if account.StartTLSEnabled {
		startTLSSupported, _ := client.Extension("STARTTLS")
		if !startTLSSupported {
			_ = client.Close()
			return nil, fmt.Errorf("SMTP server does not support STARTTLS")
		}
		if err := client.StartTLS(smtpTLSConfig(account)); err != nil {
			_ = client.Close()
			return nil, err
		}
	}

	return client, nil
}

// SendEmail 发一封邮件。
//
// 账号由 ResolveSMTPAccount 按当前发件模式挑出(固定 / 随机 / 依次);
// 账号表为空时它返回由老全局变量拼出的隐式账号,行为与多账号功能上线前
// 逐位一致。每一次尝试 —— 无论成败 —— 都落一条发件台账。
func SendEmail(subject string, receiver string, content string) error {
	account, err := ResolveSMTPAccount()
	if err != nil {
		return err
	}
	started := time.Now()
	err = sendEmailWith(account, subject, receiver, content)

	// 台账在**返回之前**落,且失败也要落:运维排查"为什么这个人没收到"时,
	// 最需要的恰恰是那些失败记录,而它们在旧实现里只在 Quit 失败那一种情况下
	// 进过一次系统日志。SMTPRecordSend 的实现体自己吞掉一切错误(见其注释),
	// 台账写不进去绝不影响本次发件的返回值。
	rec := SMTPSendRecord{
		AccountID:   account.ID,
		AccountName: account.Name,
		From:        account.FromAddress(),
		Receiver:    receiver,
		Subject:     subject,
		Success:     err == nil,
		DurationMs:  time.Since(started).Milliseconds(),
	}
	if err != nil {
		rec.ErrorMsg = err.Error()
	}
	SMTPRecordSend(rec)
	return err
}

func sendEmailWith(account SMTPAccountConfig, subject string, receiver string, content string) error {
	from := account.FromAddress()
	if account.Server == "" && account.Account == "" {
		return fmt.Errorf("SMTP 服务器未配置")
	}
	id, err := generateMessageID(from)
	if err != nil {
		return err
	}
	encodedSubject := fmt.Sprintf("=?UTF-8?B?%s?=", base64.StdEncoding.EncodeToString([]byte(subject)))
	mail := []byte(fmt.Sprintf("To: %s\r\n"+
		"From: %s <%s>\r\n"+
		"Subject: %s\r\n"+
		"Date: %s\r\n"+
		"Message-ID: %s\r\n"+ // 添加 Message-ID 头
		"Content-Type: text/html; charset=UTF-8\r\n\r\n%s\r\n",
		receiver, SystemName, from, encodedSubject, time.Now().Format(time.RFC1123Z), id, content))
	auth := getSMTPAuth(account)
	addr := fmt.Sprintf("%s:%d", account.Server, account.Port)
	to := strings.Split(receiver, ";")
	client, err := newSMTPClient(addr, account)
	if err != nil {
		return err
	}
	defer client.Close()
	if shouldAuthenticateSMTP(account) {
		if err = client.Auth(auth); err != nil {
			return err
		}
	}
	if err = client.Mail(from); err != nil {
		return err
	}
	for _, rcpt := range to {
		if err = client.Rcpt(rcpt); err != nil {
			return err
		}
	}
	w, err := client.Data()
	if err != nil {
		return err
	}
	_, err = w.Write(mail)
	if err != nil {
		return err
	}
	err = w.Close()
	if err != nil {
		return err
	}
	err = client.Quit()
	if err != nil {
		SysError(fmt.Sprintf("failed to send email to %s: %v", receiver, err))
	}
	return err
}
