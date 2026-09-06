FROM --platform=$BUILDPLATFORM golang:1.26.2 AS builder

WORKDIR /workspace

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Inherit BuildKit's target instead of overriding ARM64 with an AMD64 default.
ARG TARGETOS
ARG TARGETARCH
ARG TARGETPLATFORM
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w" -o /manager ./app/operator/cmd/manager \
    && go run ./scripts/verify-controller-architecture /manager "${TARGETPLATFORM}"

FROM gcr.io/distroless/static:nonroot

WORKDIR /
COPY --from=builder /manager /manager

USER 65532:65532
ENTRYPOINT ["/manager"]
