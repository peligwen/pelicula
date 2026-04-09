FROM golang:1.25-alpine AS build
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 go build -o pelicula-api .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=build /app/pelicula-api /usr/local/bin/
EXPOSE 8181
CMD ["pelicula-api"]
