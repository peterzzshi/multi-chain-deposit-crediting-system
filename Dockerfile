FROM golang:1.27-bookworm AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

ARG SERVICE
RUN test -n "$SERVICE" && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /out/app ./cmd/$SERVICE

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/app /app
COPY configs /configs
ENTRYPOINT ["/app"]
