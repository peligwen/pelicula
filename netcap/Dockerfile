FROM golang:1.25-alpine AS build
WORKDIR /app
COPY go.mod ./
RUN go mod download
COPY . ./
RUN CGO_ENABLED=0 go build -o netcap .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=build /app/netcap /usr/local/bin/
EXPOSE 9191
CMD ["netcap"]
