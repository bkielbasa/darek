# syntax=docker/dockerfile:1.7

FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build \
        -trimpath -ldflags="-s -w" \
        -o /out/darek ./cmd/darek

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/darek /usr/local/bin/darek
EXPOSE 7777
ENTRYPOINT ["/usr/local/bin/darek"]
