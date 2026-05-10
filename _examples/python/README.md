# Python Example

Runs the official `google-cloud-bigquery` client against bigquery-emulator,
querying a YAML-seeded table.

### Requirements

- Docker (with the `docker compose` plugin)

### Setup

Build the example image and start the emulator container:

```
make setup
```

### Run

Run `example.py` against the running emulator:

```
make run
```

### Cleanup

Stop and remove the emulator container + its volumes:

```
make down
```
