# syntax=docker/dockerfile:1

FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/nycu ./cmd/nycu

# distroless/static ships CA certificates and tzdata, and runs as a non-root user.
FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /out/nycu /nycu
ENV TZ=Asia/Taipei PORT=8080
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 CMD ["/nycu", "healthcheck"]
ENTRYPOINT ["/nycu"]
CMD ["serve"]
