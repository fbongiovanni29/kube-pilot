FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /notifications-worker .

FROM alpine:3.19
RUN apk add --no-cache ca-certificates
COPY --from=build /notifications-worker /usr/local/bin/notifications-worker
ENTRYPOINT ["notifications-worker"]
