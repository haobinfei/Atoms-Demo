package mailer

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"os"
	"strconv"
	"strings"
	"time"
)

// Sender is the outbound mail boundary used by account and workspace workflows.
type Sender interface {
	Send(ctx context.Context, to, subject, body string) error
}

type Config struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
	Security string // tls (implicit TLS), starttls, or none (local mail capture only).
}

type SMTP struct {
	config Config
	from   string
}

func ConfigFromEnv() (Config, error) {
	security := os.Getenv("SMTP_SECURITY")
	if security == "" {
		security = "tls"
	}
	portText := os.Getenv("SMTP_PORT")
	if portText == "" {
		switch security {
		case "tls":
			portText = "465"
		case "starttls":
			portText = "587"
		case "none":
			portText = "1025"
		}
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return Config{}, fmt.Errorf("invalid SMTP_PORT: %w", err)
	}
	return Config{
		Host: os.Getenv("SMTP_HOST"), Port: port,
		Username: os.Getenv("SMTP_USERNAME"), Password: os.Getenv("SMTP_PASSWORD"),
		From: os.Getenv("SMTP_FROM"), Security: security,
	}, nil
}

func NewSMTP(config Config) (*SMTP, error) {
	if config.Host == "" || strings.ContainsAny(config.Host, "\r\n\t ") {
		return nil, errors.New("SMTP_HOST is required and must be a hostname")
	}
	if config.Port < 1 || config.Port > 65535 {
		return nil, errors.New("SMTP_PORT must be between 1 and 65535")
	}
	from, err := address(config.From)
	if err != nil {
		return nil, fmt.Errorf("invalid SMTP_FROM: %w", err)
	}
	if (config.Username == "") != (config.Password == "") {
		return nil, errors.New("SMTP_USERNAME and SMTP_PASSWORD must be configured together")
	}
	switch config.Security {
	case "tls", "starttls":
	case "none":
		if config.Username != "" {
			return nil, errors.New("SMTP credentials require TLS")
		}
		ip := net.ParseIP(config.Host)
		if config.Host != "localhost" && config.Host != "mailpit" && (ip == nil || !ip.IsLoopback()) {
			return nil, errors.New("unencrypted SMTP is restricted to local mail capture")
		}
	default:
		return nil, errors.New("SMTP_SECURITY must be tls, starttls, or none")
	}
	return &SMTP{config: config, from: from}, nil
}

func (s *SMTP) Send(ctx context.Context, to, subject, body string) error {
	recipient, err := address(to)
	if err != nil {
		return fmt.Errorf("invalid mail recipient: %w", err)
	}
	if subject == "" || strings.ContainsAny(subject, "\r\n") {
		return errors.New("mail subject must be nonempty and contain no newlines")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	addr := net.JoinHostPort(s.config.Host, strconv.Itoa(s.config.Port))
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("connect SMTP: %w", err)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			return fmt.Errorf("set SMTP deadline: %w", err)
		}
	}
	if s.config.Security == "tls" {
		secure := tls.Client(conn, &tls.Config{ServerName: s.config.Host, MinVersion: tls.VersionTLS12})
		if err := secure.HandshakeContext(ctx); err != nil {
			return fmt.Errorf("SMTP TLS handshake: %w", err)
		}
		conn = secure
	}
	client, err := smtp.NewClient(conn, s.config.Host)
	if err != nil {
		return fmt.Errorf("SMTP greeting: %w", err)
	}
	defer client.Close()
	if s.config.Security == "starttls" {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return errors.New("SMTP server does not support STARTTLS")
		}
		if err := client.StartTLS(&tls.Config{ServerName: s.config.Host, MinVersion: tls.VersionTLS12}); err != nil {
			return fmt.Errorf("SMTP STARTTLS: %w", err)
		}
	}
	if s.config.Username != "" {
		if ok, _ := client.Extension("AUTH"); !ok {
			return errors.New("SMTP server does not support authentication")
		}
		if err := client.Auth(smtp.PlainAuth("", s.config.Username, s.config.Password, s.config.Host)); err != nil {
			return fmt.Errorf("SMTP authentication: %w", err)
		}
	}
	if err := client.Mail(s.from); err != nil {
		return fmt.Errorf("SMTP sender: %w", err)
	}
	if err := client.Rcpt(recipient); err != nil {
		return fmt.Errorf("SMTP recipient: %w", err)
	}
	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("SMTP data: %w", err)
	}
	if _, err := io.WriteString(writer, message(s.from, recipient, subject, body)); err != nil {
		writer.Close()
		return fmt.Errorf("write SMTP data: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("finish SMTP data: %w", err)
	}
	if err := client.Quit(); err != nil {
		return fmt.Errorf("finish SMTP session: %w", err)
	}
	return nil
}

func address(value string) (string, error) {
	if value == "" || strings.ContainsAny(value, "\r\n") {
		return "", errors.New("email address is required")
	}
	parsed, err := mail.ParseAddress(value)
	if err != nil || parsed.Address != value {
		return "", errors.New("expected one plain email address")
	}
	return parsed.Address, nil
}

func message(from, to, subject, body string) string {
	encoded := base64.StdEncoding.EncodeToString([]byte(body))
	var out strings.Builder
	fmt.Fprintf(&out, "From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: base64\r\n\r\n", from, to, mime.QEncoding.Encode("UTF-8", subject))
	for len(encoded) > 76 {
		out.WriteString(encoded[:76])
		out.WriteString("\r\n")
		encoded = encoded[76:]
	}
	out.WriteString(encoded)
	out.WriteString("\r\n")
	return out.String()
}
