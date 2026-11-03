# syntax=docker/dockerfile:1
# Build the static binary outside Docker:
#   CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
#     -o dist/amd64/uroboros-binary-broker ./cmd/uroboros-binary-broker
FROM scratch
ARG UROBOROS_SOURCE_REVISION=unknown
LABEL org.opencontainers.image.source="https://github.com/nobanic/UroborOS" \
      org.opencontainers.image.revision="${UROBOROS_SOURCE_REVISION}" \
      org.opencontainers.image.title="Uroboros private binary artifact broker" \
      org.opencontainers.image.licenses="AGPL-3.0-only"
COPY --chmod=0555 dist/amd64/uroboros-binary-broker /uroboros-binary-broker
COPY --chmod=0444 LICENSE /usr/share/licenses/uroboros/LICENSE
USER 65531:65531
ENTRYPOINT ["/uroboros-binary-broker"]
