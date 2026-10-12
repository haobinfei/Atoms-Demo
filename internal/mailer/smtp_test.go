package mailer_test

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"strconv"
	"strings"
	"testing"

	"github.com/haobinfei/Atoms-Demo/internal/mailer"
)

func TestSendToLocalMailCapture(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	message := make(chan string, 1)
	serverError := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverError <- err
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		write := func(line string) error { _, err := fmt.Fprintf(conn, "%s\r\n", line); return err }
		if err := write("220 local test SMTP"); err != nil {
			serverError <- err
			return
		}
		var data strings.Builder
		inData := false
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				serverError <- err
				return
			}
			line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
			if inData {
				if line == "." {
					message <- data.String()
					inData = false
					if err := write("250 queued"); err != nil {
						serverError <- err
						return
					}
					continue
				}
				data.WriteString(line)
				data.WriteString("\r\n")
				continue
			}
			switch {
			case strings.HasPrefix(line, "EHLO "):
				err = write("250 local")
			case strings.HasPrefix(line, "MAIL FROM:<noreply@example.com>"):
				err = write("250 sender accepted")
			case strings.HasPrefix(line, "RCPT TO:<alice@example.com>"):
				err = write("250 recipient accepted")
			case line == "DATA":
				inData = true
				err = write("354 continue")
			case line == "QUIT":
				serverError <- write("221 bye")
				return
			default:
				serverError <- fmt.Errorf("unexpected SMTP command: %s", line)
				return
			}
			if err != nil {
				serverError <- err
				return
			}
		}
	}()

	port, _ := strconv.Atoi(strings.TrimPrefix(listener.Addr().String(), "127.0.0.1:"))
	sender, err := mailer.NewSMTP(mailer.Config{Host: "127.0.0.1", Port: port, From: "noreply@example.com", Security: "none"})
	if err != nil {
		t.Fatal(err)
	}
	if err := sender.Send(context.Background(), "alice@example.com", "验证邮箱", "请点击验证链接：https://example.com/verify?token=test"); err != nil {
		t.Fatal(err)
	}
	raw := <-message
	if !strings.Contains(raw, "From: noreply@example.com\r\nTo: alice@example.com\r\n") {
		t.Fatalf("missing envelope headers: %q", raw)
	}
	if !strings.Contains(raw, "Subject: =?UTF-8?") {
		t.Fatalf("subject should be MIME encoded: %q", raw)
	}
	parts := strings.SplitN(raw, "\r\n\r\n", 2)
	if len(parts) != 2 {
		t.Fatalf("missing message body: %q", raw)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(parts[1], "\r\n", ""))
	if err != nil {
		t.Fatal(err)
	}
	if string(decoded) != "请点击验证链接：https://example.com/verify?token=test" {
		t.Fatalf("unexpected body: %s", decoded)
	}
	if err := <-serverError; err != nil {
		t.Fatal(err)
	}
}

func TestPlaintextSMTPOnlyForLocalMailCapture(t *testing.T) {
	for _, host := range []string{"smtp.163.com", "smtp.example.com"} {
		if _, err := mailer.NewSMTP(mailer.Config{Host: host, Port: 25, From: "noreply@example.com", Security: "none"}); err == nil {
			t.Fatalf("insecure SMTP to %s was accepted", host)
		}
	}
	for _, host := range []string{"localhost", "127.0.0.1", "mailpit"} {
		if _, err := mailer.NewSMTP(mailer.Config{Host: host, Port: 1025, From: "noreply@localhost", Security: "none"}); err != nil {
			t.Fatalf("local SMTP to %s was rejected: %v", host, err)
		}
	}
	if _, err := mailer.NewSMTP(mailer.Config{Host: "mailpit", Port: 1025, From: "noreply@example.com", Username: "user", Password: "secret", Security: "none"}); err == nil {
		t.Fatal("credentials were accepted over unencrypted SMTP")
	}
}

func TestConfigFromEnvDefaultsToImplicitTLS(t *testing.T) {
	t.Setenv("SMTP_HOST", "smtp.163.com")
	t.Setenv("SMTP_PORT", "")
	t.Setenv("SMTP_SECURITY", "")
	config, err := mailer.ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if config.Host != "smtp.163.com" || config.Port != 465 || config.Security != "tls" {
		t.Fatalf("unexpected 163 SMTP defaults: %+v", config)
	}
}

func TestSendRejectsHeaderInjectionBeforeConnecting(t *testing.T) {
	sender, err := mailer.NewSMTP(mailer.Config{Host: "127.0.0.1", Port: 1, From: "noreply@example.com", Security: "none"})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ to, subject string }{
		{"alice@example.com\r\nBcc: attacker@example.com", "verify"},
		{"alice@example.com", "verify\r\nBcc: attacker@example.com"},
	} {
		if err := sender.Send(context.Background(), test.to, test.subject, "body"); err == nil || strings.Contains(err.Error(), "connect SMTP") {
			t.Fatalf("header injection was not rejected before dialing: %v", err)
		}
	}
}
