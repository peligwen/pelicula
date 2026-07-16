FROM golang:1.25-alpine AS build
ARG VERSION=dev
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . ./
RUN CGO_ENABLED=0 go build -ldflags="-X pelicula-api/internal/app/services.Version=${VERSION}" -o pelicula-api ./cmd/pelicula-api/

FROM alpine:3.20
# Re-declared: ARGs don't cross stage boundaries. The label is read by the
# CLI's image-staleness check (pelicula up / doctor) via docker image inspect.
ARG VERSION=dev
LABEL org.pelicula.version="${VERSION}"
RUN apk add --no-cache ca-certificates
COPY --from=build /app/pelicula-api /usr/local/bin/
EXPOSE 8181
CMD ["pelicula-api"]
