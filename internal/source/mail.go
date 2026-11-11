package source

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message/mail"

	"github.com/anastasia/job-bot/internal/config"
	"github.com/anastasia/job-bot/internal/model"
)

// Mail читает письма с рассылками вакансий (hh.ru, geekjob и др.) по IMAP.
// Почтовый ящик открывается только на чтение: письма не помечаются прочитанными и не удаляются.
// Повторы отсекает общая база (идентификатор вакансии из ссылки).
type Mail struct {
	Addr         string // host:port, например imap.gmail.com:993
	User         string
	Password     string
	Folder       string // по умолчанию INBOX
	LookbackDays int    // за сколько дней искать письма, по умолчанию 3
	Senders      []MailSender
	MaxMessages  int // не больше столько писем за цикл, по умолчанию 50

	insecure bool // без TLS — только для тестов с локальным сервером
}

// NewMail собирает источник из настроек и доступа к почте. Дополнительные отправители из настроек
// добавляются к hh.ru и geekjob.ru.
func NewMail(cfg config.Mail, creds config.IMAP) (*Mail, error) {
	senders := DefaultMailSenders()
	for _, s := range cfg.Senders {
		if s.Domain == "" || s.Link == "" {
			return nil, fmt.Errorf("mail.senders: нужны domain и link")
		}
		re, err := regexp.Compile(s.Link)
		if err != nil {
			return nil, fmt.Errorf("mail.senders %s: регулярка ссылки: %w", s.Domain, err)
		}
		if re.NumSubexp() < 1 {
			return nil, fmt.Errorf("mail.senders %s: в регулярке ссылки нужна группа (...) с идентификатором вакансии", s.Domain)
		}
		senders = append(senders, MailSender{Domain: s.Domain, Link: re, URL: s.URL})
	}
	return &Mail{Addr: creds.Addr, User: creds.User, Password: creds.Password,
		Folder: cfg.Folder, LookbackDays: cfg.LookbackDays, Senders: senders}, nil
}

func (m *Mail) Name() string { return "mail" }

// QuietOK: тишина в почте — нормальное состояние (новых писем может не быть часами), алерт «0 вакансий» не нужен.
func (m *Mail) QuietOK() bool { return true }

func (m *Mail) Fetch(ctx context.Context) ([]model.Vacancy, error) {
	if m.Folder == "" {
		m.Folder = "INBOX"
	}
	if m.LookbackDays <= 0 {
		m.LookbackDays = 3
	}
	if m.MaxMessages <= 0 {
		m.MaxMessages = 50
	}
	if len(m.Senders) == 0 {
		m.Senders = DefaultMailSenders()
	}

	c, err := m.dial()
	if err != nil {
		return nil, fmt.Errorf("подключение к %s: %w", m.Addr, err)
	}
	defer func() { _ = c.Close() }()
	if err := c.Login(m.User, m.Password).Wait(); err != nil {
		return nil, fmt.Errorf("вход в почту не удался (проверьте IMAP_USER и пароль приложения): %w", err)
	}
	defer func() { _ = c.Logout().Wait() }()
	if _, err := c.Select(m.Folder, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		return nil, fmt.Errorf("папка %q: %w", m.Folder, err)
	}

	since := time.Now().AddDate(0, 0, -m.LookbackDays)
	var all []model.Vacancy
	var errs []string
	for _, sender := range m.Senders {
		vs, err := m.fetchSender(ctx, c, sender, since)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", sender.Domain, err))
			continue
		}
		all = append(all, vs...)
	}
	return all, joinErrs(errs)
}

func (m *Mail) dial() (*imapclient.Client, error) {
	if m.insecure {
		return imapclient.DialInsecure(m.Addr, nil)
	}
	return imapclient.DialTLS(m.Addr, nil)
}

func (m *Mail) fetchSender(ctx context.Context, c *imapclient.Client, sender MailSender, since time.Time) ([]model.Vacancy, error) {
	crit := &imap.SearchCriteria{
		Since:  since,
		Header: []imap.SearchCriteriaHeaderField{{Key: "From", Value: sender.Domain}},
	}
	found, err := c.UIDSearch(crit, nil).Wait()
	if err != nil {
		return nil, fmt.Errorf("поиск писем: %w", err)
	}
	uids := found.AllUIDs()
	if len(uids) == 0 {
		return nil, nil
	}
	if len(uids) > m.MaxMessages { // самые новые письма имеют большие UID
		uids = uids[len(uids)-m.MaxMessages:]
	}

	section := &imap.FetchItemBodySection{Peek: true} // Peek: не ставить флаг «прочитано»
	msgs, err := c.Fetch(imap.UIDSetNum(uids...), &imap.FetchOptions{
		UID: true, InternalDate: true, Envelope: true, RFC822Size: true,
		BodySection: []*imap.FetchItemBodySection{section},
	}).Collect()
	if err != nil {
		return nil, fmt.Errorf("загрузка писем: %w", err)
	}

	var out []model.Vacancy
	for _, msg := range msgs {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		if msg.RFC822Size > 3<<20 {
			continue // слишком большое письмо (вложения) — пропускаем
		}
		var raw []byte
		for _, b := range msg.BodySection {
			raw = b.Bytes
			break
		}
		when := msg.InternalDate
		if msg.Envelope != nil && !msg.Envelope.Date.IsZero() {
			when = msg.Envelope.Date
		}
		vs, err := parseMailMessage(raw, sender)
		if err != nil {
			slog.Warn("письмо не разобрано", "err", err, "uid", msg.UID)
			continue
		}
		for _, v := range vs {
			v.PublishedAt = when
			out = append(out, v)
		}
	}
	return out, nil
}

// parseMailMessage разбирает письмо (RFC 822) и достаёт вакансии: из HTML-части, а при её отсутствии из текстовой.
func parseMailMessage(raw []byte, sender MailSender) ([]model.Vacancy, error) {
	mr, err := mail.CreateReader(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("разбор письма: %w", err)
	}
	var htmlBody, textBody string
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			// часть с неизвестной кодировкой не должна ломать остальное письмо
			if htmlBody != "" || textBody != "" {
				break
			}
			return nil, fmt.Errorf("часть письма: %w", err)
		}
		h, ok := p.Header.(*mail.InlineHeader)
		if !ok {
			continue // вложение
		}
		ct, _, _ := h.ContentType()
		b, err := io.ReadAll(io.LimitReader(p.Body, 2<<20))
		if err != nil {
			continue
		}
		switch strings.ToLower(ct) {
		case "text/html":
			if htmlBody == "" {
				htmlBody = string(b)
			}
		case "text/plain":
			if textBody == "" {
				textBody = string(b)
			}
		}
	}
	if htmlBody != "" {
		if vs := parseMailHTML(htmlBody, sender); len(vs) > 0 {
			return vs, nil
		}
	}
	if textBody != "" {
		return parseMailText(textBody, sender), nil
	}
	return nil, nil
}
