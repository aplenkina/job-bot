// Package bot — Telegram-интерфейс: команды управления и уведомления.
package bot

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/anastasia/job-bot/internal/config"
	"github.com/anastasia/job-bot/internal/model"
	"github.com/anastasia/job-bot/internal/store"
)

type Bot struct {
	api    *tgbot.Bot
	chatID int64
	store  *store.Store
	prof   config.Profile
}

func New(token string, chatID int64, st *store.Store, prof config.Profile) (*Bot, error) {
	b := &Bot{chatID: chatID, store: st, prof: prof}
	api, err := tgbot.New(token, tgbot.WithDefaultHandler(b.onUnknown))
	if err != nil {
		return nil, fmt.Errorf("telegram init: %w", err)
	}
	b.api = api
	api.RegisterHandler(tgbot.HandlerTypeMessageText, "/start", tgbot.MatchTypeExact, b.guard(b.onStart))
	api.RegisterHandler(tgbot.HandlerTypeMessageText, "/status", tgbot.MatchTypeExact, b.guard(b.onStatus))
	api.RegisterHandler(tgbot.HandlerTypeMessageText, "/pause", tgbot.MatchTypeExact, b.guard(b.onPause))
	api.RegisterHandler(tgbot.HandlerTypeMessageText, "/resume", tgbot.MatchTypeExact, b.guard(b.onResume))
	api.RegisterHandler(tgbot.HandlerTypeMessageText, "/unpark", tgbot.MatchTypePrefix, b.guard(b.onUnpark))
	api.RegisterHandler(tgbot.HandlerTypeCallbackQueryData, cbApplied, tgbot.MatchTypePrefix, b.onCallback)
	api.RegisterHandler(tgbot.HandlerTypeCallbackQueryData, cbSkip, tgbot.MatchTypePrefix, b.onCallback)
	return b, nil
}

// Run блокируется до отмены ctx.
func (b *Bot) Run(ctx context.Context) { b.api.Start(ctx) }

// Notify отправляет сообщение владельцу.
func (b *Bot) Notify(ctx context.Context, text string) error {
	_, err := b.api.SendMessage(ctx, &tgbot.SendMessageParams{ChatID: b.chatID, Text: text})
	return err
}

const (
	cbApplied = "ap:" // «Откликнулась»
	cbSkip    = "sk:" // «Пропустить»
)

// SendVacancy отправляет карточку вакансии с кнопками.
func (b *Bot) SendVacancy(ctx context.Context, r store.Row) error {
	_, err := b.api.SendMessage(ctx, &tgbot.SendMessageParams{
		ChatID:             b.chatID,
		Text:               FormatVacancy(r),
		ParseMode:          models.ParseModeHTML,
		LinkPreviewOptions: &models.LinkPreviewOptions{IsDisabled: tgbot.True()},
		ReplyMarkup: &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
			{Text: "Откликнулась", CallbackData: fmt.Sprintf("%s%d", cbApplied, r.ID)},
			{Text: "Пропустить", CallbackData: fmt.Sprintf("%s%d", cbSkip, r.ID)},
		}}},
	})
	if err != nil {
		return err
	}
	if r.Letter != "" { // письмо отдельным сообщением, чтобы его было удобно скопировать
		_, err = b.api.SendMessage(ctx, &tgbot.SendMessageParams{
			ChatID:    b.chatID,
			Text:      FormatLetter(r.Letter),
			ParseMode: models.ParseModeHTML,
		})
	}
	return err
}

// onCallback обрабатывает нажатия кнопок под карточкой.
func (b *Bot) onCallback(ctx context.Context, _ *tgbot.Bot, upd *models.Update) {
	cq := upd.CallbackQuery
	if cq == nil || cq.Message.Message == nil || cq.Message.Message.Chat.ID != b.chatID {
		return
	}
	var st model.Status
	var prefix, answer string
	switch {
	case strings.HasPrefix(cq.Data, cbApplied):
		st, prefix, answer = model.StatusApplied, cbApplied, "Отмечено: откликнулись"
	case strings.HasPrefix(cq.Data, cbSkip):
		st, prefix, answer = model.StatusSkipped, cbSkip, "Пропущено"
	default:
		return
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(cq.Data, prefix), 10, 64)
	if err == nil {
		err = b.store.SetStatusByID(ctx, id, st)
	}
	if err != nil {
		slog.Error("callback failed", "err", err, "data", cq.Data)
		answer = "Ошибка, попробуйте ещё раз"
	}
	if _, err := b.api.AnswerCallbackQuery(ctx, &tgbot.AnswerCallbackQueryParams{CallbackQueryID: cq.ID, Text: answer}); err != nil {
		slog.Warn("answer callback", "err", err)
	}
	// убираем кнопки, чтобы не нажать дважды
	if _, err := b.api.EditMessageReplyMarkup(ctx, &tgbot.EditMessageReplyMarkupParams{
		ChatID: b.chatID, MessageID: cq.Message.Message.ID,
		ReplyMarkup: &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{}},
	}); err != nil {
		slog.Warn("edit markup", "err", err)
	}
}

type handler func(ctx context.Context, upd *models.Update)

// guard пропускает только сообщения из нашего чата — бот не отвечает чужим.
func (b *Bot) guard(h handler) tgbot.HandlerFunc {
	return func(ctx context.Context, _ *tgbot.Bot, upd *models.Update) {
		if upd.Message == nil || upd.Message.Chat.ID != b.chatID {
			return
		}
		h(ctx, upd)
	}
}

func (b *Bot) reply(ctx context.Context, text string) {
	if err := b.Notify(ctx, text); err != nil {
		slog.Error("telegram send failed", "err", err)
	}
}

func (b *Bot) onUnknown(ctx context.Context, _ *tgbot.Bot, upd *models.Update) {
	if upd.Message == nil || upd.Message.Chat.ID != b.chatID {
		return
	}
	b.reply(ctx, "Не понял. Команды: /status, /pause, /resume, /unpark <компания>")
}

func (b *Bot) onStart(ctx context.Context, _ *models.Update) {
	b.reply(ctx, "Привет! Я ищу вакансии PHP/Go и присылаю их сюда.\n\n"+
		"/status — состояние и счётчики\n/pause, /resume — пауза\n"+
		"/unpark <компания> — начать показывать отложенную компанию (или all)")
}

func (b *Bot) onStatus(ctx context.Context, _ *models.Update) {
	paused, err := b.store.Paused(ctx)
	if err != nil {
		b.reply(ctx, "Ошибка: "+err.Error())
		return
	}
	counts, err := b.store.Counts(ctx)
	if err != nil {
		b.reply(ctx, "Ошибка: "+err.Error())
		return
	}
	state := "работает"
	if paused {
		state = "на паузе"
	}
	b.reply(ctx, fmt.Sprintf(
		"Статус: %s\nНовых: %d\nОтправлено вам: %d\nОткликов: %d\nПропущено: %d\nОтсеяно фильтрами: %d\nОтложено (компании в списке): %d",
		state, counts[model.StatusNew], counts[model.StatusNotified], counts[model.StatusApplied],
		counts[model.StatusSkipped], counts[model.StatusFiltered], counts[model.StatusParked]))
}

func (b *Bot) onPause(ctx context.Context, _ *models.Update) {
	if err := b.store.SetPaused(ctx, true); err != nil {
		b.reply(ctx, "Ошибка: "+err.Error())
		return
	}
	b.reply(ctx, "Пауза включена. Сбор продолжается, уведомления не приходят.")
}

func (b *Bot) onResume(ctx context.Context, _ *models.Update) {
	if err := b.store.SetPaused(ctx, false); err != nil {
		b.reply(ctx, "Ошибка: "+err.Error())
		return
	}
	b.reply(ctx, "Продолжаю работу.")
}

func (b *Bot) onUnpark(ctx context.Context, upd *models.Update) {
	arg := strings.TrimSpace(strings.TrimPrefix(upd.Message.Text, "/unpark"))
	if arg == "" {
		b.reply(ctx, "Укажите компанию: /unpark ozon (или /unpark all)")
		return
	}
	targets := []string{arg}
	if strings.EqualFold(arg, "all") {
		targets = b.prof.ParkedCompanies
	}
	var total int64
	for _, c := range targets {
		n, err := b.store.Unpark(ctx, c)
		if err != nil {
			b.reply(ctx, "Ошибка: "+err.Error())
			return
		}
		total += n
	}
	b.reply(ctx, fmt.Sprintf("Вернул в работу вакансий: %d", total))
}
