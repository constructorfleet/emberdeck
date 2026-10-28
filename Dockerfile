# Build with BuildKit/buildx for the host or for multiple target architectures.
ARG GO_VERSION=1.27.2
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS build
ARG TARGETOS=linux
ARG TARGETARCH=amd64
ARG TARGETVARIANT
ARG VERSION=dev
WORKDIR /src
COPY go.mod ./
COPY . .
RUN export GOARM=${TARGETVARIANT#v}; export GOARM=${GOARM:-7}; CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/emberdeck ./cmd/emberdeck \
    && mkdir -p /out/data \
    && chmod 0750 /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build --chown=65532:65532 /out/emberdeck /usr/local/bin/emberdeck
COPY --from=build --chown=65532:65532 /out/data /data
COPY LICENSE THIRD_PARTY_NOTICES.txt /usr/share/licenses/emberdeck/
VOLUME ["/data"]
EXPOSE 8080
ENV EMBERDECK_API_KEY=""
ENTRYPOINT ["/usr/local/bin/emberdeck"]
CMD ["serve", "-listen", "0.0.0.0:8080", "-data", "/data"]
