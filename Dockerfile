FROM golang:1.26-alpine AS builder
RUN apk add --no-cache gcc musl-dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 go build -trimpath -o /centersurvey ./cmd/server

FROM alpine:3.23
RUN apk add --no-cache ca-certificates && addgroup -S survey && adduser -S survey -G survey && mkdir /data && chown survey:survey /data
COPY --from=builder /centersurvey /usr/local/bin/centersurvey
USER survey
VOLUME ["/data"]
EXPOSE 8000
ENTRYPOINT ["centersurvey"]
CMD ["--database", "/data/centersurvey.db", "--address", "0.0.0.0:8000"]
