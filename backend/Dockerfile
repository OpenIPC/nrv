FROM golang:1.22-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -o /server ./cmd/server

FROM alpine:3.20

# openssh-client + sshpass — для управления камерами OpenIPC (restart Majestic, reboot).
# ffmpeg — для получения кадра из HLS-потока, когда камера не отдаёт JPEG напрямую
# (например, OpenIPC при занятых аппаратных скейлерах возвращает 503 на /image.jpg).
RUN apk --no-cache add ca-certificates tzdata openssh-client sshpass ffmpeg

# Корневой сертификат Минцифры нужен для работы с российскими сервисами:
# без него запросы к API MAX падают с «certificate signed by unknown authority»,
# потому что TLS-соединение проверяется по стандартному набору, где его нет.
COPY certs/russian_trusted_root.crt /usr/local/share/ca-certificates/russian_trusted_root.crt
RUN update-ca-certificates

WORKDIR /app

COPY --from=builder /server .
COPY migrations/ ./migrations/

EXPOSE 8080

ENTRYPOINT ["./server"]