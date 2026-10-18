# The triage agent as a container image (chapter 15). Build it from the
# book's code directory, which holds the module:
#
#	docker build -f ch15/Dockerfile -t triage .
#
# The image keeps the Go toolchain, since run_tests runs go test, and
# git, since each task's workspace is built as a repository at the
# crash's commit (catalog.BuildRepo); that makes it a Go image rather
# than a minimal one. Running the agent in a container
# doesn't put its tests in a sandbox: they run here, beside the agent's
# API key (chapter 14).

FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /triage ./ch15

FROM golang:1.27
RUN useradd --create-home --uid 10001 triage \
	&& mkdir /state && chown triage /state
COPY --from=build /triage /usr/local/bin/triage
COPY --from=build /src/catalog /book/catalog
COPY --from=build /src/remind /book/remind
USER triage
WORKDIR /book
ENV TRIAGE_STATE=/state
VOLUME /state
# A platform's stop signal is SIGTERM: the agent cancels its running
# turn with ErrShutdown, saves, and exits well inside a 10 s grace.
STOPSIGNAL SIGTERM
ENTRYPOINT ["triage", "-repo", "/book"]
