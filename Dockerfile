# Alcove — multi-stage build to a distroless, non-root, static image.
#
# Hard constraint 9 (CLAUDE.md): non-root, read-only rootfs, all capabilities
# dropped, base images pinned BY DIGEST. `scripts/guards.sh digest-pin` fails CI if
# any FROM line below loses its @sha256: — a floating base tag is an unreviewed
# change to what ships (principle 1).
#
# To update a pin, resolve the new digest deliberately and record why in the
# changelog; never replace a digest with a bare tag "temporarily".

# --- build stage -------------------------------------------------------------
FROM golang:1.26-bookworm@sha256:dc40a13b61304647ed7e5d581d246b86413dfc29d63c6d018310feb93cd98fc0 AS build

WORKDIR /src

# Dependency layer first, so source edits do not re-download the module graph.
COPY go.mod go.sum* ./
RUN go mod download

COPY . .

# CGO_ENABLED=0 is load-bearing: it keeps the binary static so the runtime stage
# can be distroless/static. It is also why storage uses a pure-Go SQLite
# (modernc.org/sqlite) sealed to disk instead of SQLCipher (ADR D-16).
ARG VERSION=0.0.0-dev
RUN CGO_ENABLED=0 GOOS=linux go build \
      -trimpath \
      -ldflags="-s -w -X main.version=${VERSION}" \
      -o /out/alcove ./cmd/alcove

# --- runtime stage -----------------------------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab

# distroless :nonroot is uid/gid 65532. Stated explicitly so `docker inspect` can
# assert it in CI rather than trusting the base image's default.
USER 65532:65532

COPY --from=build /out/alcove /usr/local/bin/alcove

# Only the HTTP surface. LAN only (ADR D-11): publishing this port to the
# internet is a hard-constraint violation, not a deployment choice.
EXPOSE 8080

# In the image (not just compose) so any runtime gets the same health signal.
# distroless has no shell or curl; the binary probes itself.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD ["/usr/local/bin/alcove", "-healthcheck"]

ENTRYPOINT ["/usr/local/bin/alcove"]
