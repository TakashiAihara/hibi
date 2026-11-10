# hibi

Personal diary and habit tracker: API, web UI, and a Telegram nightly prompt.

This repository currently holds the HTTP API, the SQLite store, and a CLI client in one binary. The web UI and the Telegram prompt come later and are clients of the same API.

## Run the server

```bash
go build -o hibi .
HIBI_TOKEN=change-me HIBI_DB=./hibi.db HIBI_ADDR=127.0.0.1:8080 ./hibi serve
```

| Variable | Default | Meaning |
|---|---|---|
| `HIBI_TOKEN` | (required) | Bearer token every request except `/healthz` must send |
| `HIBI_DB` | `hibi.db` | SQLite file path. Set it to a path on a persistent volume in containers. The file is set to mode 0600 on start |
| `HIBI_ADDR` | `:8080` | Listen address |

The server does not restrict source addresses. Put it behind a reverse proxy that limits who can reach it. Request bodies are capped at 1 MiB (413 above that). SIGINT / SIGTERM stop it gracefully.

## Use the CLI

```bash
export HIBI_URL=http://127.0.0.1:8080 HIBI_TOKEN=change-me
echo "Wrote the API today" | ./hibi entry put today
./hibi entry list -from 2026-11-01
./hibi habit done today stretch
./hibi habit done today walk 8123
./hibi habit list today
```

Every command except `entry delete` and `habit undo` prints the JSON response (those two print nothing on success). Errors go to stderr with exit code 1. Text read from stdin is stored as is, including the trailing newline `echo` adds.

## API

| Method | Path | Body | Meaning |
|---|---|---|---|
| GET | `/healthz` | | Liveness, no token needed |
| GET | `/entries?from=&to=` | | Diary entries in a date range, newest first |
| GET | `/entries/{date}` | | One entry, 404 if none |
| PUT | `/entries/{date}` | `{"body": "..."}` | Create or replace the entry for the date |
| DELETE | `/entries/{date}` | | Remove the entry, 404 if none |
| GET | `/habits/{date}` | | Habit logs for the date |
| PUT | `/habits/{date}/{habit}` | empty or `{"value": 8123}` | Mark a habit done, optionally with a number |
| DELETE | `/habits/{date}/{habit}` | | Remove the log, 404 if none |

`date` is `YYYY-MM-DD`. `habit` matches `^[a-z0-9][a-z0-9_-]{0,63}$`.

## Develop

```bash
go test ./...
```
