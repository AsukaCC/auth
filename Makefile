.PHONY: dev web build test run docker-up docker-down docker-logs

dev:
	go run ./cmd/server
web:
	cd web && npm run dev
build:
	cd web && npm ci && npm run build
	go build -o auth-server ./cmd/server
test:
	go test ./...
	go vet ./...
	cd web && npm run build
run:
	AUTH_PUBLIC_URL=http://127.0.0.1:8080 ./auth-server
docker-up:
	docker compose up -d --build --wait
docker-down:
	docker compose down
docker-logs:
	docker compose logs -f --tail=100 auth
