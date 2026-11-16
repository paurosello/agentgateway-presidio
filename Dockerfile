FROM --platform=$BUILDPLATFORM golang:1.27 AS build
WORKDIR /src
COPY . ./
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags='-s -w' -o /app .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /app /app
EXPOSE 8000
ENTRYPOINT ["/app"]
