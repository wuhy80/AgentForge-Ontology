FROM golang:1.25-alpine
ENV CGO_ENABLED=0 GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off GOENV=off GOTELEMETRY=off
COPY runner.go /opt/ontology/runner.go
RUN mkdir -p /opt/ontology/cache && \
    GOCACHE=/opt/ontology/cache go build -trimpath -o /opt/ontology/go-runner /opt/ontology/runner.go && \
    go list std > /opt/ontology/std.txt && \
    chmod -R a+rX /opt/ontology
USER 65534:65534
ENTRYPOINT []
CMD ["/opt/ontology/go-runner"]
