.PHONY: fmt vet test build generate db-up db-down

fmt:
	go fmt ./...

vet:
	go vet ./...

test:
	go test ./...

build:
	go build ./...

generate:
	go run -mod=mod entgo.io/ent/cmd/ent generate --feature sql/lock --target ./internal/store/ent ./internal/store/ent/schema

db-up:
	docker compose up -d postgres

db-down:
	docker compose down
