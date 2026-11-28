# Tiny test image for the Floci seeding helpers: it only sleeps. MARKER makes
# the content (and so the digest) differ between builds.
FROM busybox:1.37.0@sha256:a7dbc319b1438115a207e11930dc4bb3e643dcb3ec0bdd694bb36cd8fad289ec
ARG MARKER=default
RUN echo "$MARKER" > /marker
CMD ["sleep", "3600"]
