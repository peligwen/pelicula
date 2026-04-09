FROM golang:1.25-alpine AS build
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN go build -o procula .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates ffmpeg
WORKDIR /
COPY --from=build /app/procula /usr/local/bin/
EXPOSE 8282
CMD ["procula"]
