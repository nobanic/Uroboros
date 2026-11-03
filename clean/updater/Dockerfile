FROM golang:1.26.10-bookworm@sha256:632dff7351799a5a9954a48dc06670ade72ee44e8c351cdc0e5a57af2fea21af AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY stackupdate/ ./stackupdate/
COPY cmd/uroboros-updater/ ./cmd/uroboros-updater/
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/uroboros-updater ./cmd/uroboros-updater

FROM docker:29.9.2-cli@sha256:5e6baf67325a79f64ed3bed6863954d5e05d0f3d4e57cbdb14809346814bbfdb
RUN apk add --no-cache bash grep
COPY --from=build /out/uroboros-updater /usr/local/bin/uroboros-updater
ENTRYPOINT ["/usr/local/bin/uroboros-updater"]
