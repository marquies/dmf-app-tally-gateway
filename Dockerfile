FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /tally-gateway ./cmd/tally-gateway
# distroless has no shell to create the registry directory with.
RUN mkdir /data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /tally-gateway /tally-gateway
COPY --from=build --chown=nonroot:nonroot /data /data
EXPOSE 8080
ENTRYPOINT ["/tally-gateway"]
