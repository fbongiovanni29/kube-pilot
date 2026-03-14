FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /web-gateway .

FROM alpine:3.19
RUN apk add --no-cache ca-certificates
COPY --from=build /web-gateway /usr/local/bin/web-gateway
EXPOSE 8080
ENTRYPOINT ["web-gateway"]
