#!/usr/bin/env bash
# Выкладка бота на Linux-сервер по SSH. Docker не нужен.
#
#   ./deploy/deploy.sh user@host [amd64|arm64] [ssh-порт]
#
# Что делает:
#   1. собирает статический бинарник под архитектуру сервера (по умолчанию amd64);
#   2. при первом запуске создаёт пользователя jobbot и папки в /opt/jobbot;
#   3. копирует бинарник, config.yaml, candidate.yaml и службу systemd;
#   4. при первом запуске копирует .env (токены) с правами 600; потом .env на сервере не трогает;
#   5. перезапускает службу и показывает её статус.
#
# Нужен доступ по SSH-ключу и пользователь с правом sudo.
set -euo pipefail

TARGET="${1:?Использование: $0 user@host [amd64|arm64] [ssh-порт]}"
ARCH="${2:-amd64}"
PORT="${3:-22}"
cd "$(dirname "$0")/.."

for f in config.yaml candidate.yaml .env; do
	[[ -f "$f" ]] || { echo "Нет файла $f в папке проекта" >&2; exit 1; }
done
case "$ARCH" in amd64|arm64) ;; *) echo "Архитектура: amd64 или arm64" >&2; exit 1 ;; esac

# На сервере нет локальной модели: письма пишутся по шаблону. Предупредим, а не будем молча менять конфиг.
if grep -Eq '^[[:space:]]*provider:[[:space:]]*ollama' config.yaml; then
	echo "ВНИМАНИЕ: в config.yaml включена Ollama (llm.provider: ollama), а на сервере её нет." >&2
	echo "Бот будет писать письма по шаблону и при каждом запуске присылать предупреждение в Telegram." >&2
	echo "Поставьте provider: none в серверном конфиге. Продолжить? [y/N]" >&2
	read -r answer
	[[ "$answer" == "y" || "$answer" == "Y" ]] || exit 1
fi

SSH=(ssh -p "$PORT" -o StrictHostKeyChecking=accept-new "$TARGET")
SCP=(scp -P "$PORT" -o StrictHostKeyChecking=accept-new)

echo "==> Тесты"
go vet ./... && go test ./...

echo "==> Сборка linux/$ARCH"
mkdir -p bin
CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" go build -trimpath -ldflags="-s -w" -o "bin/jobbot-linux-$ARCH" ./cmd/jobbot

echo "==> Подготовка сервера"
"${SSH[@]}" 'sudo bash -s' <<'REMOTE'
set -euo pipefail
id jobbot >/dev/null 2>&1 || useradd --system --home /opt/jobbot --shell /usr/sbin/nologin jobbot
mkdir -p /opt/jobbot/data /opt/jobbot/upload
chown -R jobbot:jobbot /opt/jobbot/data
chown "${SUDO_USER:-root}" /opt/jobbot/upload
REMOTE

echo "==> Копирование файлов"
"${SCP[@]}" "bin/jobbot-linux-$ARCH" "$TARGET:/opt/jobbot/upload/jobbot"
"${SCP[@]}" config.yaml candidate.yaml deploy/jobbot.service deploy/notify-stop.sh "$TARGET:/opt/jobbot/upload/"
HAS_ENV=$("${SSH[@]}" 'test -f /opt/jobbot/.env && echo yes || echo no')
if [[ "$HAS_ENV" == "no" ]]; then
	echo "    первый запуск: копирую .env (без DB_PATH: путь к базе задан в службе)"
	TMP_ENV="$(mktemp)"
	trap 'rm -f "$TMP_ENV"' EXIT
	grep -v '^DB_PATH=' .env > "$TMP_ENV" || true
	"${SCP[@]}" "$TMP_ENV" "$TARGET:/opt/jobbot/upload/.env"
fi

echo "==> Установка и перезапуск"
"${SSH[@]}" 'sudo bash -s' <<'REMOTE'
set -euo pipefail
cd /opt/jobbot
install -m 755 -o root -g root upload/jobbot jobbot
install -m 640 -o root -g jobbot upload/config.yaml config.yaml
install -m 640 -o root -g jobbot upload/candidate.yaml candidate.yaml
if [[ -f upload/.env ]]; then install -m 640 -o root -g jobbot upload/.env .env; fi
install -m 755 -o root -g root upload/notify-stop.sh notify-stop.sh
install -m 644 upload/jobbot.service /etc/systemd/system/jobbot.service
rm -f upload/jobbot upload/config.yaml upload/candidate.yaml upload/.env upload/jobbot.service upload/notify-stop.sh
systemctl daemon-reload
systemctl enable jobbot >/dev/null
systemctl restart jobbot
sleep 3
systemctl --no-pager --lines=15 status jobbot || true
REMOTE

echo "==> Готово. Логи: ssh $TARGET 'sudo journalctl -u jobbot -f'"
