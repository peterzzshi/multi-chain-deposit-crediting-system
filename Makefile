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
	# -p 1: integration packages share one Postgres; parallel package runs
	# would let one package's cleanup delete another's rows mid-test.
	go test -race -tags=integration -p 1 ./internal/...

generate:
	go run -mod=mod entgo.io/ent/cmd/ent generate --feature sql/lock --target ./internal/store/ent ./internal/store/ent/schema

mock:
	GOPROXY=https://goproxy.cn,direct go run github.com/vektra/mockery/v3@v3.8.0
