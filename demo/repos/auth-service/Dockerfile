FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /auth-service .

FROM alpine:3.19
RUN apk add --no-cache ca-certificates
COPY --from=build /auth-service /usr/local/bin/auth-service
EXPOSE 8081
ENTRYPOINT ["auth-service"]
