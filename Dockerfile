FROM golang:1.27.3-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /openmerge ./cmd/openmerge

FROM debian:bookworm-slim
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
