# syntax=docker/dockerfile:1

FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/planner ./cmd/planner

FROM scratch
COPY --from=build /out/planner /planner
COPY examples/manifest.json /manifest.json
USER 65534:65534
ENTRYPOINT ["/planner", "/manifest.json"]
