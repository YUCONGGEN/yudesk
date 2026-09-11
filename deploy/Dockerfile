FROM golang:1.27.1 AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY third_party/anet ./third_party/anet
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/yudesk-relay ./cmd/yudesk-relay

FROM alpine:3.20
RUN addgroup -S yudesk && adduser -S -G yudesk -h /data yudesk
COPY --from=build /out/yudesk-relay /usr/local/bin/yudesk-relay
USER yudesk
WORKDIR /data
VOLUME ["/data"]
EXPOSE 8233
EXPOSE 8233/udp
EXPOSE 8254/tcp
EXPOSE 8254/udp
EXPOSE 20200-20295/udp
EXPOSE 8235
ENTRYPOINT ["yudesk-relay"]
CMD ["-listen", ":8233", "-http", ":8235", "-device-licenses", "-admin-key-file", "/data/admin.key", "-public-relay", "www.yucg.cn:8233", "-accounts", "/data/yudesk.db", "-cert", "/data/relay.crt", "-key", "/data/relay.key", "-downloads", "/data/downloads", "-conference-turn", ":8254", "-conference-turn-public", "www.yucg.cn:8254", "-conference-turn-secret-file", "/data/conference-turn.secret", "-conference-turn-relay-min-port", "20200", "-conference-turn-relay-max-port", "20295", "-conference-direct-timeout-ms", "3000"]
