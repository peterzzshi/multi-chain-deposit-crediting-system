.PHONY: check test itest generate mock

check:
	gofmt -w .
	go vet ./...
	go build ./...
	go test -race ./...

test:
	go test -race ./...

itest:
	docker compose up -d postgres
	go test -race -tags=integration ./internal/store/...

generate:
	go run -mod=mod entgo.io/ent/cmd/ent generate --feature sql/lock --target ./internal/store/ent ./internal/store/ent/schema

mock:
	go run github.com/vektra/mockery/v3@v3.8.0
