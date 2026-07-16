FROM golang:1.25-alpine AS build
ARG VERSION=dev
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
COPY cmd/ ./cmd/
RUN go build -ldflags="-X procula.Version=${VERSION}" -o procula ./cmd/procula/

FROM alpine:3.20
# Re-declared: ARGs don't cross stage boundaries. The label is read by the
# CLI's image-staleness check (pelicula up / doctor) via docker image inspect.
ARG VERSION=dev
LABEL org.pelicula.version="${VERSION}"
RUN apk add --no-cache ca-certificates ffmpeg
WORKDIR /
COPY --from=build /app/procula /usr/local/bin/
EXPOSE 8282
CMD ["procula"]
