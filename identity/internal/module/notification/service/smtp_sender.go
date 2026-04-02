package service

import (
	"bytes"
	"fmt"
	"html/template"
	"log/slog"
	"net/smtp"

	"mallow/identity/internal/config"
	"mallow/identity/internal/module/notification/domain"
)

var verificationTmpl = template.Must(template.New("verification").Parse(`<!DOCTYPE html>
<html><body>
<h2>Xác thực tài khoản</h2>
<p>Xin chào <strong>{{.Name}}</strong>,</p>
<p>Nhấp vào link bên dưới để xác thực email của bạn:</p>
<p><a href="{{.Link}}">{{.Link}}</a></p>
<p>Link có hiệu lực trong 24 giờ.</p>
</body></html>`))

var passwordResetTmpl = template.Must(template.New("password_reset").Parse(`<!DOCTYPE html>
<html><body>
<h2>Đặt lại mật khẩu</h2>
<p>Xin chào <strong>{{.Name}}</strong>,</p>
<p>Nhấp vào link bên dưới để đặt lại mật khẩu:</p>
<p><a href="{{.Link}}">{{.Link}}</a></p>
<p>Link có hiệu lực trong 1 giờ.</p>
</body></html>`))

// SMTPSender sends emails via SMTP.
type SMTPSender struct {
	cfg    config.SMTPConfig
	logger *slog.Logger
}

func NewSMTPSender(cfg *config.Config, logger *slog.Logger) *SMTPSender {
	return &SMTPSender{cfg: cfg.SMTP, logger: logger.With("component", "smtp")}
}

// Send dispatches the correct email template based on job type.
func (s *SMTPSender) Send(job domain.EmailJob) {
	var err error
	switch job.Type {
	case domain.EmailJobVerification:
		err = s.send(job.To, "Xác thực tài khoản", verificationTmpl, map[string]string{
			"Name": job.Name,
			"Link": job.Token,
		})
	case domain.EmailJobPasswordReset:
		err = s.send(job.To, "Đặt lại mật khẩu", passwordResetTmpl, map[string]string{
			"Name": job.Name,
			"Link": job.Token,
		})
	default:
		s.logger.Warn("unknown email job type", "type", string(job.Type))
		return
	}
	if err != nil {
		s.logger.Error("failed to send email", "to", job.To, "error", err)
	} else {
		s.logger.Debug("email sent", "to", job.To, "type", string(job.Type))
	}
}

func (s *SMTPSender) send(to, subject string, tmpl *template.Template, data map[string]string) error {
	var body bytes.Buffer
	if err := tmpl.Execute(&body, data); err != nil {
		return fmt.Errorf("template execute: %w", err)
	}

	from := fmt.Sprintf("%s <%s>", s.cfg.FromName, s.cfg.From)
	msg := fmt.Sprintf(
		"From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/html; charset=UTF-8\r\n\r\n%s",
		from, to, subject, body.String(),
	)

	addr := fmt.Sprintf("%s:%d", s.cfg.Host, s.cfg.Port)
	auth := smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, s.cfg.Host)
	return smtp.SendMail(addr, auth, s.cfg.From, []string{to}, []byte(msg))
}
