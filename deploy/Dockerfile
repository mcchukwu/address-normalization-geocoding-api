# One image with every binary: the API, the probe, and the operator tools (keys, gazetteer, geoeval).
# Set GO_VERSION to the version in go.mod.
ARG GO_VERSION=1.27.0
FROM golang:${GO_VERSION}-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN for c in geocoded probe keys gazetteer geoeval; do \
      CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/$c ./cmd/$c || exit 1; \
    done

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/ /
COPY --from=build /src/testdata/ /testdata/
EXPOSE 8080
USER nonroot
ENTRYPOINT ["/geocoded"]
