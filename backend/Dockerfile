FROM golang:1.27.1-bookworm@sha256:648f440f42a0958804efb24df176f806f9d353b41f1c0627f666428e40310f6b AS build
WORKDIR /src
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/cmd ./cmd
COPY backend/internal ./internal
COPY backend/gen ./gen
ARG SERVICE=identity
ARG REVISION=development
RUN --mount=type=cache,target=/root/.cache/go-build CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X github.com/KDZZZZZZ/human-worth/backend/internal/platform.Revision=${REVISION}" -o /service ./cmd/${SERVICE}

FROM scratch AS runtime
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /service /service
COPY public/brand/logo.png /assets/logo.png
ENV SITE_LOGO_FILE=/assets/logo.png
USER 65532:65532
ENTRYPOINT ["/service"]

# The trusted worker alone creates fixed Jobs via the Kubernetes CLI. Reuse the
# same pinned Kubernetes distribution as the lab; executors receive no CLI or credentials.
FROM kindest/node:v1.36.4@sha256:099e049362a1526b2db71494e1947aae99bd16290d7c895f2b7ea312e3cbfaed AS kubectl
FROM runtime AS worker
COPY --from=kubectl /usr/bin/kubectl /usr/local/bin/kubectl
ENV PATH=/usr/local/bin

# Keep the ordinary application image the default build target.
FROM runtime AS service
