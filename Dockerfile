FROM --platform=$BUILDPLATFORM golang:1.26.2@sha256:b54cbf583d390341599d7bcbc062425c081105cc5ef6d170ced98ef9d047c716 AS builder

WORKDIR /workspace

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Inherit BuildKit's target instead of overriding ARM64 with an AMD64 default.
ARG TARGETOS
ARG TARGETARCH
ARG TARGETPLATFORM
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w" -o /manager ./app/operator/cmd/manager \
    && go run ./scripts/verify-controller-architecture /manager "${TARGETPLATFORM}" \
    && CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w" -o /hosted-server ./app/hosted-server/cmd/server \
    && go run ./scripts/verify-controller-architecture /hosted-server "${TARGETPLATFORM}"

FROM gcr.io/distroless/static:nonroot@sha256:aafe993665bf6200db38a02c11a9351e782a84b6a45e7f33b6695638c88e3931

WORKDIR /
COPY --from=builder /manager /manager
COPY --from=builder /hosted-server /hosted-server

USER 65532:65532
ENTRYPOINT ["/manager"]
