package smtp_test

import (
	"bufio"
	"context"
	"encoding/base64"
	"io"
	"mime"
	"net"
	netmail "net/mail"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-tenon/tenon/mail"
	"github.com/go-tenon/tenon/mail/smtp"
)

// session 是假服务器收到的一次 SMTP 会话。
type session struct {
	auth string
	from string
	rcpt []string
	data string
}

// server 是只支持明文与 AUTH PLAIN 的最小 SMTP 服务器。
type server struct {
	ln       net.Listener
	rejectTo string
	mu       sync.Mutex
	sessions []session
}

func newServer(t *testing.T) *server {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &server{ln: ln}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(conn)
		}
	}()
	return s
}

func (s *server) port() int { return s.ln.Addr().(*net.TCPAddr).Port }

func (s *server) serve(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	reply := func(line string) { io.WriteString(conn, line+"\r\n") }
	var cur session
	reply("220 test ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		verb := strings.ToUpper(strings.SplitN(line, " ", 2)[0])
		switch verb {
		case "EHLO", "HELO":
			reply("250-test")
			reply("250-AUTH PLAIN")
			reply("250 8BITMIME")
		case "AUTH":
			parts := strings.Fields(line)
			raw, _ := base64.StdEncoding.DecodeString(parts[len(parts)-1])
			cur.auth = string(raw)
			reply("235 ok")
		case "MAIL":
			cur.from = line
			reply("250 ok")
		case "RCPT":
			if s.rejectTo != "" && strings.Contains(line, s.rejectTo) {
				reply("550 no such user")
				continue
			}
			cur.rcpt = append(cur.rcpt, line)
			reply("250 ok")
		case "DATA":
			reply("354 go ahead")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				b.WriteString(strings.TrimPrefix(l, "."))
			}
			cur.data = b.String()
			s.mu.Lock()
			s.sessions = append(s.sessions, cur)
			s.mu.Unlock()
			reply("250 queued")
		case "RSET":
			cur = session{auth: cur.auth}
			reply("250 ok")
		case "QUIT":
			reply("221 bye")
			return
		default:
			reply("250 ok")
		}
	}
}

func (s *server) received() []session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]session(nil), s.sessions...)
}

func config(port int) smtp.Config {
	var cfg smtp.Config
	cfg.SetDefaults()
	cfg.From = mail.Address{Name: "Blog", Email: "no-reply@example.com"}
	cfg.Host = "127.0.0.1"
	cfg.Port = port
	cfg.TLS = smtp.TLSNone
	cfg.Timeout = 5 * time.Second
	return cfg
}

func TestSend(t *testing.T) {
	srv := newServer(t)
	m, err := smtp.New(config(srv.port()))
	if err != nil {
		t.Fatal(err)
	}
	err = m.Send(context.Background(), mail.Message{
		To:          []mail.Address{{Name: "Ada", Email: "ada@example.com"}},
		Cc:          []mail.Address{{Email: "cc@example.com"}},
		Bcc:         []mail.Address{{Email: "bcc@example.com"}},
		ReplyTo:     mail.Address{Email: "support@example.com"},
		Subject:     "欢迎",
		HTML:        "<p>Hi</p>",
		Text:        "Hi",
		Attachments: []mail.Attachment{{Filename: "a.txt", ContentType: "text/plain", Content: []byte("hello")}},
		Headers:     map[string]string{"X-Campaign": "welcome"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := srv.received()
	if len(got) != 1 {
		t.Fatalf("sessions = %d", len(got))
	}
	s := got[0]
	if s.auth != "" {
		t.Errorf("auth without username: %q", s.auth)
	}
	if !strings.Contains(s.from, "<no-reply@example.com>") || len(s.rcpt) != 3 {
		t.Errorf("envelope from=%q rcpt=%q", s.from, s.rcpt)
	}
	parsed, err := netmail.ReadMessage(strings.NewReader(s.data))
	if err != nil {
		t.Fatal(err)
	}
	h := parsed.Header
	subject, _ := new(mime.WordDecoder).DecodeHeader(h.Get("Subject"))
	if subject != "欢迎" {
		t.Errorf("subject = %q", subject)
	}
	for key, want := range map[string]string{
		"To": "ada@example.com", "Cc": "cc@example.com", "Reply-To": "support@example.com",
		"From": "no-reply@example.com", "X-Campaign": "welcome",
	} {
		if !strings.Contains(h.Get(key), want) {
			t.Errorf("%s = %q", key, h.Get(key))
		}
	}
	if h.Get("Bcc") != "" {
		t.Errorf("Bcc header leaked: %q", h.Get("Bcc"))
	}
	body, _ := io.ReadAll(parsed.Body)
	for _, want := range []string{"multipart/alternative", "text/html", "a.txt", base64.StdEncoding.EncodeToString([]byte("hello"))} {
		if !strings.Contains(string(body), want) {
			t.Errorf("body missing %q", want)
		}
	}
}

func TestSendErrors(t *testing.T) {
	srv := newServer(t)
	srv.rejectTo = "nobody@example.com"
	m, err := smtp.New(config(srv.port()))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	msg := mail.Message{To: []mail.Address{{Email: "nobody@example.com"}}, Subject: "s", Text: "t"}
	if err := m.Send(ctx, msg); err == nil {
		t.Error("rejected recipient accepted")
	}
	if err := m.Send(ctx, mail.Message{}); err == nil {
		t.Error("invalid message accepted")
	}

	cfg := config(srv.port())
	cfg.TLS = smtp.TLSStartTLS
	strict, _ := smtp.New(cfg)
	msg.To[0].Email = "ada@example.com"
	if err := strict.Send(ctx, msg); err == nil {
		t.Error("starttls not enforced")
	}
	// go-mail 不会在未加密的连接上发送明文密码。
	cfg = config(srv.port())
	cfg.Username, cfg.Password = "user", "secret"
	plain, _ := smtp.New(cfg)
	if err := plain.Send(ctx, msg); err == nil {
		t.Error("plain auth over unencrypted connection")
	}
	if len(srv.received()) != 0 {
		t.Errorf("unexpected deliveries: %d", len(srv.received()))
	}
}

func TestConfig(t *testing.T) {
	var cfg smtp.Config
	cfg.SetDefaults()
	if cfg.Port != 587 || cfg.TLS != smtp.TLSStartTLS || cfg.Timeout != 10*time.Second {
		t.Fatalf("defaults = %+v", cfg)
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("empty host and from accepted")
	}
	cfg = config(25)
	cfg.TLS = "ssl"
	if _, err := smtp.New(cfg); err == nil || !strings.Contains(err.Error(), "tls must be") {
		t.Fatalf("bad tls err = %v", err)
	}
	cfg = config(70000)
	if _, err := smtp.New(cfg); err == nil || !strings.Contains(err.Error(), strconv.Itoa(65535)) {
		t.Fatalf("bad port err = %v", err)
	}
}
