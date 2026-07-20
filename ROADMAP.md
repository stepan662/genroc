# Planned improvements

## CLI
- [x] commands mirroring the API capabilities
- [x] yaml support
- [x] config file with the url to the server
- [] test with different auth types + add helpers for genctl

## server
- [x] versioning channels
- [x] child process compatibility check and versioning made convenient
- [x] external tasks (outgoing request to start, incoming request to complete), human or long running
- [x] non-idempotent tasks - steps which can't be safely repateated
- [x] logs for each process
- [x] let user to repeat the task manually (how will it interact with parents?)
- [x] pagination
- [x] filtering
- [x] env variables
- [x] map function (lambdas + object/array literals; own parser)
- [] think about error handling child -> parent
- [] think about action extensivity/passability from parent
- [x] look at naming conventions - cancel -> pause, then resume. Retry only for errored processes.
- [] pause as a debugging tool: start an instance paused, then step it with tick

# docs

- [] write docs, plan and motivation
- rename ideas: "a-genroc", "dura-genroc", "duragent", "genroc-rock", "genrock", "genroc"?


