FROM --platform=$BUILDPLATFORM golang:1.26.2 AS builder

WORKDIR /workspace

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG BUILDPLATFORM
ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w" -o /manager ./app/operator/cmd/manager

FROM gcr.io/distroless/static:nonroot

WORKDIR /
COPY --from=builder /manager /manager

USER 65532:65532
ENTRYPOINT ["/manager"]
