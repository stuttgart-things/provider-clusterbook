FROM golang:1.27-alpine AS builder

RUN apk add --no-cache git

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -o /provider ./cmd/provider

FROM gcr.io/distroless/static@sha256:15f8637509792e05b97d83451b0b6ccfa4abd548f997e406ac1e72c87fe1fa78

COPY --from=builder /provider /usr/local/bin/crossplane-clusterbook-provider

USER 65532
ENTRYPOINT ["crossplane-clusterbook-provider"]
