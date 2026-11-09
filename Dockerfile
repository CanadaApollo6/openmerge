FROM golang:1.27.3-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
# .dockerignore drops .git, so Go cannot stamp the commit itself. Pass
# REVISION only when the build context is a clean checkout of that commit:
# the binary attests to whatever revision it is given.
ARG REVISION=
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags="-s -w -X main.version=${VERSION} -X github.com/CanadaApollo6/openmerge/internal/authority.BuildRevision=${REVISION}" \
    -o /openmerge ./cmd/openmerge

FROM debian:bookworm-slim
LABEL org.opencontainers.image.source="https://github.com/CanadaApollo6/openmerge" \
      org.opencontainers.image.licenses="Apache-2.0"
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates git \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 1000 openmerge \
    && useradd --uid 1000 --gid openmerge --create-home openmerge \
    && mkdir -p /var/lib/openmerge/git \
    && chown -R openmerge:openmerge /var/lib/openmerge
COPY --from=build /openmerge /usr/local/bin/openmerge
USER 1000:1000
WORKDIR /var/lib/openmerge
ENTRYPOINT ["openmerge"]
CMD ["serve"]
