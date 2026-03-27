FROM golang:1.26-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /mailr ./cmd/mailr

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=builder /mailr /usr/local/bin/mailr
EXPOSE 4802 2525
ENTRYPOINT ["mailr"]
CMD ["serve", "--db=/data/mailr.db", "--http=:4802", "--smtp=:2525"]
