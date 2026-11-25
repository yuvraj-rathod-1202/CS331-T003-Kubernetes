FROM golang:1.23-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /iotsim .

FROM alpine:3.20
COPY --from=build /iotsim /iotsim
ENTRYPOINT ["/iotsim"]
