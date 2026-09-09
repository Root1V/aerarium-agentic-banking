# Servicios en Go: API de socio, canal del titular y alta de integraciones.
#
# Una sola imagen con los tres binarios. Son pequeños y comparten dependencias;
# tres imágenes solo multiplicarían el tiempo de build y la superficie a mantener.
#
# El contexto de build es la RAÍZ del repositorio: go.work liga los módulos
# locales y sin él los imports no resuelven.
FROM golang:1.27-alpine AS build

WORKDIR /src
COPY go.work go.work.sum ./
COPY clients/go ./clients/go
COPY adapters ./adapters
COPY services ./services

ENV CGO_ENABLED=0
RUN go build -trimpath -o /out/ \
      ./services/mercatus/cmd/mercatus \
      ./services/mercatus/cmd/bootstrap \
      ./services/mercatus/cmd/demo \
      ./services/bff/cmd/bff

FROM alpine:3.21
RUN apk add --no-cache ca-certificates \
 && adduser -S -u 10001 aibank
COPY --from=build /out/ /usr/local/bin/
USER aibank
CMD ["mercatus"]
