# Use minimal Alpine Linux image
FROM dhi.io/alpine-base:3.23-alpine3.23-dev

# Install ca-certificates for HTTPS support
RUN apk add --no-cache ca-certificates

# Create the flomation user and group, pinned to an explicit uid/gid.
# Pinning matters: `adduser -S` with no -u takes the first free system id, which
# collides with package-provided accounts — that is how the runner image ended
# up on uid 101, because `apk add clamav` claimed 100 first. 10001 is outside
# the range apk allocates from and is free in every base image we use.
RUN addgroup -g 10001 -S flomation && \
    adduser  -u 10001 -S flomation -G flomation

# Copy the binary into the container.
# Owned by root and mode 0555: the application cannot rewrite its own
# executable, and one COPY replaces COPY + chmod + chown, which previously
# rewrote every byte of the binary into a second image layer.
ARG BINARY_FILE
COPY --chown=root:root --chmod=0555 ${BINARY_FILE} /usr/local/bin/flomation-api

# gencerts — the internal-mTLS CA and certificate minting tool from tools/gencerts.
#
# It ships here so a Kubernetes install can provision its own mTLS material without
# depending on cert-manager, which a customer cluster may not have. The Helm chart
# runs it from a one-shot, create-once provisioning Job; the api process itself
# never invokes it.
#
# It has to be a SEPARATE, SHARED CA for the whole mesh, which is why this is not
# something each service can do for itself at startup: api, launch and runner must
# all trust one CA, and three components minting independently produce three CAs
# and a mesh where nothing trusts anything.
#
# CI already supplies this. The shared .build-docker-dhi-buildx-template globs
# BINARY_FILE_PATTERN and assigns BINARY_FILE, BINARY_FILE_2, ... in `ls` order, so
# widening the pattern in .gitlab-ci.yml is the whole change. That ordering is
# positional and therefore fragile — "flomation-api" sorts before
# "flomation-gencerts", which is the only reason the two land the right way round.
# The RUN below asserts it rather than trusting it, because a silent swap would
# produce an image whose entrypoint is a certificate tool.
ARG BINARY_FILE_2
COPY --chown=root:root --chmod=0555 ${BINARY_FILE_2} /usr/local/bin/flomation-gencerts
RUN /usr/local/bin/flomation-gencerts -h 2>&1 | grep -q '\-ca-cert' \
    && /usr/local/bin/flomation-api -version >/dev/null 2>&1 \
    || { echo "FATAL: binaries are the wrong way round — BINARY_FILE must be the api binary and BINARY_FILE_2 gencerts"; exit 1; }

# Numeric rather than a name: with `runAsNonRoot: true` the kubelet refuses an
# image whose USER is a name, because it cannot verify the name is not root.
# The account is still called `flomation`, so `ps` and `ls -l` stay readable.
USER 10001:10001

# Expose any ports if needed (adjust as necessary)
EXPOSE 8888

# Set the binary as entrypoint
ENTRYPOINT ["/usr/local/bin/flomation-api"]