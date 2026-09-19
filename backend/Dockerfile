FROM golang:1.27.1-bookworm@sha256:648f440f42a0958804efb24df176f806f9d353b41f1c0627f666428e40310f6b AS build
WORKDIR /src
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/cmd ./cmd
COPY backend/internal ./internal
COPY backend/gen ./gen
ARG SERVICE=identity
ARG REVISION=development
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X github.com/KDZZZZZZ/human-worth/backend/internal/platform.Revision=${REVISION}" -o /service ./cmd/${SERVICE}

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /service /service
COPY public/brand/logo.png /assets/logo.png
ENV SITE_LOGO_FILE=/assets/logo.png
USER 65532:65532
ENTRYPOINT ["/service"]
