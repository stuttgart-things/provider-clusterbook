FROM golang:1.27-alpine AS builder

RUN apk add --no-cache git

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -o /provider ./cmd/provider

FROM gcr.io/distroless/static@sha256:4c2cd1dee442c9a99924a378c7c126fac8fef14771579ef567c3f2bdd7a5122a

COPY --from=builder /provider /usr/local/bin/crossplane-clusterbook-provider

USER 65532
ENTRYPOINT ["crossplane-clusterbook-provider"]
