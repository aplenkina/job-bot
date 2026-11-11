#!/usr/bin/env bash
# Сторож: systemd вызывает его после остановки службы (ExecStopPost).
# Если бот завершился НЕ штатно (падение, kill -9, нехватка памяти, ошибка запуска) —
# присылает сообщение в Telegram. Сам бот в этот момент уже не может ничего отправить.
# При штатной остановке/перезапуске молчит: бот сам пишет «остановлен».
#
# Токен и chat_id приходят из окружения службы (EnvironmentFile=.env).
# NOTIFY_DRY_RUN=1 — только напечатать сообщение, ничего не отправлять (для проверки).
set -u

RESULT="${SERVICE_RESULT:-unknown}"
CODE="${EXIT_STATUS:-}"
[[ "$RESULT" == "success" ]] && exit 0

# Не чаще одного сообщения в 10 минут: при цикле падений systemd перезапускает бота каждые 10 секунд.
STAMP="${NOTIFY_STAMP:-/opt/jobbot/data/.last-crash-alert}"
if [[ -f "$STAMP" && -n "$(find "$STAMP" -mmin -10 2>/dev/null)" ]]; then
	exit 0
fi

case "$RESULT" in
	oom-kill)  WHY="не хватило памяти (его убил OOM-killer)" ;;
	signal)    WHY="процесс убит сигналом${CODE:+ ($CODE)}" ;;
	core-dump) WHY="аварийное завершение (core dump)" ;;
	timeout)   WHY="процесс не остановился вовремя" ;;
	exit-code) WHY="процесс завершился с ошибкой${CODE:+, код $CODE}" ;;
	*)         WHY="результат: $RESULT${CODE:+, код $CODE}" ;;
esac
MSG="❗ Бот остановился аварийно: $WHY.
systemd перезапустит его автоматически. Если сообщение повторится — смотрите журнал: journalctl -u jobbot -n 50"

if [[ "${NOTIFY_DRY_RUN:-}" == "1" ]]; then
	echo "$MSG"
	exit 0
fi
[[ -n "${TELEGRAM_TOKEN:-}" && -n "${TELEGRAM_CHAT_ID:-}" ]] || exit 0

# Токен передаём через stdin, а не в аргументах: так он не виден в списке процессов.
CODE_HTTP=$(printf 'url = "https://api.telegram.org/bot%s/sendMessage"\n' "$TELEGRAM_TOKEN" |
	curl -sS -m 15 -K - --data-urlencode "chat_id=$TELEGRAM_CHAT_ID" --data-urlencode "text=$MSG" \
		-o /dev/null -w '%{http_code}' 2>/dev/null || true)
[[ "$CODE_HTTP" == "200" ]] && touch "$STAMP"
exit 0
