# qisqa as a container image. The links, accounts and domains are kept in
# the volume at /data:
#
#   docker build --build-arg VERSION=1.2.3 -t qisqa .
#   docker run -d --name qisqa -p 127.0.0.1:8080:8080 \
#     -v "$PWD/config.yaml:/etc/qisqa/config.yaml:ro" -v qisqa-data:/data qisqa
#   docker exec qisqa /qisqa -config /etc/qisqa/config.yaml domain add short.example

# Stage 1: build the static binary.
FROM golang:1.27 AS build
ARG VERSION=dev
WORKDIR /src
# Dependencies first, so they are downloaded again only when they change.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN make build VERSION=$VERSION
# The data folder: the final image has no shell to make it with.
RUN mkdir /data

# Stage 2: the binary alone, on an image with no shell, run as a non-root user.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /src/qisqa /qisqa
# For the image's user alone, so that a new volume mounted there starts out
# writable by qisqa and closed to everyone else.
COPY --from=build --chown=nonroot:nonroot --chmod=700 /data /data
VOLUME /data
# Paths in the config are taken from /, so the example's data_dir, ./data,
# is the volume.
WORKDIR /
EXPOSE 8080
ENTRYPOINT ["/qisqa"]
CMD ["-config", "/etc/qisqa/config.yaml"]
