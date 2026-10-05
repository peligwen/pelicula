# Pelicula server image: API, auto-wiring and import pipeline in one binary.
# Built with the repository root as context (see compose/docker-compose.yml).

FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -ldflags "-X main.version=${VERSION}" -o /out/pelicula-server ./cmd/pelicula-server

# ffmpeg provides ffprobe, which the import pipeline uses to validate files.
FROM alpine:3.20
RUN apk add --no-cache ca-certificates ffmpeg
COPY --from=build /out/pelicula-server /usr/local/bin/pelicula-server
EXPOSE 8181
CMD ["pelicula-server"]
