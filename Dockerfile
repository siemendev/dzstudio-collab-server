FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY protocol ./protocol
COPY core ./core
COPY adapters ./adapters
COPY httpapi ./httpapi
COPY config ./config
COPY reference ./reference
COPY cmd ./cmd
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/dzcollab ./cmd/dzcollab && mkdir -p /out/data

FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /out/dzcollab /dzcollab
COPY --from=build --chown=nonroot:nonroot /out/data /data
COPY config.example.json /config.example.json
ENV DZCOLLAB_DATA_DIR=/data
VOLUME /data
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s CMD ["/dzcollab", "health"]
ENTRYPOINT ["/dzcollab"]
