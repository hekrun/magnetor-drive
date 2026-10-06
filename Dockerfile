FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/magnetor-drive ./cmd/magnetor-drive

FROM alpine:3.20
RUN adduser -D -u 10001 magnetor && mkdir /data && chown magnetor /data
COPY --from=build /out/magnetor-drive /usr/local/bin/magnetor-drive
USER magnetor
ENV MAGNETOR_DATA_DIR=/data MAGNETOR_LISTEN_ADDR=:8080
VOLUME /data
EXPOSE 8080 42069
ENTRYPOINT ["magnetor-drive"]
