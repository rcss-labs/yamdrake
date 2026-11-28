# Tiny test image for the Floci seeding helpers: it sleeps, or, when built with
# EXIT_CODE, exits with that code right away (a crashing service). MARKER makes
# the content (and so the digest) differ between builds.
FROM busybox:1.38.0@sha256:a46257e06c82ad85242d0d38077d0a52d9896389ca60a1a59d3463db586e5fa4
ARG MARKER=default
ARG EXIT_CODE=
ENV AWSTEST_EXIT_CODE=$EXIT_CODE
RUN echo "$MARKER" > /marker
CMD ["sh", "-c", "if [ -n \"$AWSTEST_EXIT_CODE\" ]; then exit \"$AWSTEST_EXIT_CODE\"; fi; exec sleep 3600"]
