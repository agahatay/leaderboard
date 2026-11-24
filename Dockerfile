# syntax=docker/dockerfile:1

# --- build stage: full Go toolchain ---
FROM golang:1.27 AS build
WORKDIR /src

# Download modules first so this layer is cached until go.mod/go.sum change.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# The git commit shown by /healthz. CI passes --build-arg COMMIT=<sha>; .git is
# not in the build context, so without it the server falls back to
# RENDER_GIT_COMMIT at runtime, or "unknown".
ARG COMMIT=""
# CGO_ENABLED=0 gives a static binary that runs on distroless/static.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.commit=${COMMIT}" -o /out/server ./cmd/server

# --- runtime stage: no shell, no package manager, runs as non-root ---
FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/server /server
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/server"]
