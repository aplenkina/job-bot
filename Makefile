.PHONY: test build build-linux run deploy-check

test:
	go vet ./... && go test ./...

build:
	go build -o bin/jobbot ./cmd/jobbot

# локальный запуск: нужны config.yaml и переменные из .env
run:
	set -a && . ./.env && set +a && go run ./cmd/jobbot -config config.yaml

# проверка доступности источников и Telegram — запускать НА СЕРВЕРЕ перед выкладкой
deploy-check:
	@for u in https://api.telegram.org https://t.me/s/rabotaphp "https://career.habr.com/vacancies/rss?q=php" https://boards-api.greenhouse.io/v1/boards/canonical/jobs; do \
		printf "%s -> " "$$u"; curl -s -o /dev/null -m 10 -w "%{http_code}\n" "$$u" || echo fail; done

# статические бинарники под Linux (для сервера), Docker не нужен
build-linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o bin/jobbot-linux-amd64 ./cmd/jobbot
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o bin/jobbot-linux-arm64 ./cmd/jobbot
