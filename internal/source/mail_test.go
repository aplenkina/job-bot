package source

import (
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"

	"github.com/anastasia/job-bot/internal/config"
)

const hhHTML = `<html><body>
<table><tr><td><a href="https://hh.ru/vacancy/123456?hhtmFrom=email_alert">Senior PHP-разработчик</a></td></tr>
<tr><td>Acme Group</td></tr>
<tr><td>от 280 000 до 350 000 ₽</td></tr>
<tr><td>Москва, можно удалённо</td></tr>
<tr><td><a href="https://hh.ru/vacancy/123456?x=1">Откликнуться</a></td></tr></table>
<table><tr><td><a href="https://hh.ru/vacancy/777">Go-разработчик</a></td></tr>
<tr><td>ООО Ромашка</td></tr><tr><td>Минск</td></tr></table>
<a href="https://hh.ru/applicant/resumes">Мои резюме</a>
</body></html>`

func hhSender() MailSender { return DefaultMailSenders()[0] }

func TestParseMailHTML(t *testing.T) {
	vs := parseMailHTML(hhHTML, hhSender())
	if len(vs) != 2 {
		t.Fatalf("want 2 vacancies (без «Мои резюме»), got %d: %+v", len(vs), vs)
	}
	a, b := vs[0], vs[1]
	if a.ExternalID != "123456" || a.URL != "https://hh.ru/vacancy/123456" || a.Title != "Senior PHP-разработчик" ||
		a.Company != "Acme Group" || a.SalaryFrom != 280000 || a.SalaryTo != 350000 || a.Currency != "RUR" ||
		!a.Remote || a.Source != "mail:hh.ru" || !strings.Contains(a.Location, "Москва") {
		t.Errorf("unexpected first vacancy: %+v", a)
	}
	if strings.Contains(a.Title, "Откликнуться") {
		t.Errorf("button text must not become the title: %q", a.Title)
	}
	if b.ExternalID != "777" || b.Title != "Go-разработчик" || b.Company != "ООО Ромашка" || b.Location != "Минск" || b.Remote {
		t.Errorf("unexpected second vacancy: %+v", b)
	}
}

func TestParseMailText(t *testing.T) {
	body := "Новые вакансии\r\n\r\nSenior PHP Developer\r\nhttps://hh.ru/vacancy/555?from=x\r\n\r\nGo Developer\r\nhttps://hh.ru/vacancy/556\r\nhttps://hh.ru/vacancy/556\r\n"
	vs := parseMailText(body, hhSender())
	if len(vs) != 2 || vs[0].Title != "Senior PHP Developer" || vs[0].ExternalID != "555" || vs[1].Title != "Go Developer" {
		t.Fatalf("unexpected: %+v", vs)
	}
}

// rawMail собирает письмо multipart/alternative с base64-HTML и текстовой частью.
func rawMail(from, subject, htmlBody string) []byte {
	enc := base64.StdEncoding.EncodeToString([]byte(htmlBody))
	var lines []string
	for len(enc) > 76 {
		lines = append(lines, enc[:76])
		enc = enc[76:]
	}
	lines = append(lines, enc)
	return []byte(fmt.Sprintf("From: %s\r\nTo: me@example.test\r\nSubject: %s\r\nDate: %s\r\nMIME-Version: 1.0\r\n"+
		"Content-Type: multipart/alternative; boundary=BND\r\n\r\n"+
		"--BND\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nтекстовая версия без ссылок\r\n"+
		"--BND\r\nContent-Type: text/html; charset=utf-8\r\nContent-Transfer-Encoding: base64\r\n\r\n%s\r\n--BND--\r\n",
		from, subject, time.Now().Format(time.RFC1123Z), strings.Join(lines, "\r\n")))
}

func TestParseMailMessageMIME(t *testing.T) {
	vs, err := parseMailMessage(rawMail("HH <noreply@hh.ru>", "Вакансии", hhHTML), hhSender())
	if err != nil || len(vs) != 2 || vs[0].Title != "Senior PHP-разработчик" {
		t.Fatalf("err=%v vs=%+v", err, vs)
	}
	if _, err := parseMailMessage([]byte("это не письмо"), hhSender()); err != nil {
		t.Logf("мусор вместо письма: %v (допустимо)", err) // главное — без паники
	}
}

// startIMAP поднимает тестовый IMAP-сервер в памяти и кладёт в INBOX письма.
func startIMAP(t *testing.T, messages ...[]byte) (addr string) {
	t.Helper()
	mem := imapmemserver.New()
	user := imapmemserver.NewUser("me@example.test", "app-password")
	if err := user.Create("INBOX", nil); err != nil {
		t.Fatal(err)
	}
	mem.AddUser(user)
	srv := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return mem.NewSession(), nil, nil
		},
		Caps:         imap.CapSet{imap.CapIMAP4rev1: {}},
		InsecureAuth: true,
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	c, err := imapclient.DialInsecure(ln.Addr().String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Login("me@example.test", "app-password").Wait(); err != nil {
		t.Fatal(err)
	}
	for _, m := range messages {
		cmd := c.Append("INBOX", int64(len(m)), &imap.AppendOptions{Time: time.Now()})
		if _, err := cmd.Write(m); err != nil {
			t.Fatal(err)
		}
		if err := cmd.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := cmd.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	return ln.Addr().String()
}

func TestMailFetchOverIMAP(t *testing.T) {
	addr := startIMAP(t,
		rawMail("HH.ru <noreply@hh.ru>", "Новые вакансии по вашему поиску", hhHTML),
		rawMail("Банк <spam@bank.example>", "Кредит", `<a href="https://hh.ru/vacancy/999">Чужое письмо</a>`),
	)
	m := &Mail{Addr: addr, User: "me@example.test", Password: "app-password", insecure: true}
	vs, err := m.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 2 {
		t.Fatalf("письмо от чужого отправителя читать не должны; got %d: %+v", len(vs), vs)
	}
	for _, v := range vs {
		if v.PublishedAt.IsZero() || time.Since(v.PublishedAt) > time.Hour {
			t.Errorf("дата вакансии должна быть датой письма: %v", v.PublishedAt)
		}
	}
	// повторный опрос возвращает то же (письма не помечаются прочитанными и не удаляются)
	again, err := m.Fetch(context.Background())
	if err != nil || len(again) != 2 {
		t.Fatalf("повторное чтение: %v, %d", err, len(again))
	}
}

func TestMailWrongPassword(t *testing.T) {
	addr := startIMAP(t)
	m := &Mail{Addr: addr, User: "me@example.test", Password: "wrong", insecure: true}
	_, err := m.Fetch(context.Background())
	if err == nil || !strings.Contains(err.Error(), "пароль приложения") {
		t.Fatalf("want a hint about the app password, got: %v", err)
	}
}

func TestNewMailValidatesSenders(t *testing.T) {
	creds := config.IMAP{Addr: "x:993", User: "u", Password: "p"}
	if _, err := NewMail(config.Mail{Senders: []config.MailSender{{Domain: "wantapply.com", Link: `wantapply\.com/job/(\d+)`}}}, creds); err != nil {
		t.Fatalf("valid sender rejected: %v", err)
	}
	for name, s := range map[string]config.MailSender{
		"без домена":   {Link: `x/(\d+)`},
		"плохой regex": {Domain: "a.com", Link: `([`},
		"без группы":   {Domain: "a.com", Link: `a\.com/job/\d+`},
	} {
		if _, err := NewMail(config.Mail{Senders: []config.MailSender{s}}, creds); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	m, _ := NewMail(config.Mail{}, creds)
	if len(m.Senders) != 2 || !m.QuietOK() {
		t.Errorf("defaults: %+v", m.Senders)
	}
}

func TestIMAPFromEnv(t *testing.T) {
	t.Setenv("IMAP_USER", "")
	t.Setenv("IMAP_PASSWORD", "")
	if c := config.IMAPFromEnv(); c.User != "" {
		t.Fatalf("без логина и пароля почта выключена: %+v", c)
	}
	t.Setenv("IMAP_USER", "u@example.test")
	t.Setenv("IMAP_PASSWORD", "p")
	if c := config.IMAPFromEnv(); c.Addr != "imap.gmail.com:993" || c.User != "u@example.test" {
		t.Fatalf("defaults: %+v", c)
	}
	t.Setenv("IMAP_HOST", "imap.yandex.ru")
	if c := config.IMAPFromEnv(); c.Addr != "imap.yandex.ru:993" {
		t.Fatalf("host without port: %+v", c)
	}
}
