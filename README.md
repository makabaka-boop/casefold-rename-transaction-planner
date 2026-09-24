# Case-insensitive rename planner

`renameplanner` is a dry-run command-line planner for swapping ASCII file names
or changing only letter case on a case-insensitive shared filesystem. It never
performs filesystem renames: it reads a JSON manifest and prints a JSON plan
consisting of forward moves and matching rollback moves.

## Why a planner is needed

On a case-insensitive filesystem, `a` and `A` are different requested spellings
but occupy the same directory entry after lower-casing. Renaming files in the
order supplied by a user can therefore overwrite an uninvolved file or attempt
to move into an occupied name.

The planner models the requested operations as directed edges:

- `source -> target` where `target` is a new name: a path edge;
- `source -> target` where the normalized target is another participating
  source: an edge in the dependency graph;
- `A -> a`: a one-node cycle;
- exact `A -> A` entries are accepted but produce no move.

Path components are executed from the leaf backwards. Cycles need exactly one
additional temporary move per cycle. The smallest unused name of the form
`__planner_tmp_N` is chosen after checking every input name and target name.

## Manifest format

```json
{
  "files": ["README.md", "NOTES.txt", "Archive.zip"],
  "mappings": [
    {"source": "NOTES.txt", "target": "Archive.zip"},
    {"source": "Archive.zip", "target": "notes.txt"}
  ]
}
```

Constraints:

- `files` contains 1 through 200 ASCII names;
- current names must be unique after ASCII lower-casing;
- every `source` must exactly match a current name;
- sources may not repeat;
- a target may not occupy the current normalized name of an uninvolved file;
- targets must be unique after ASCII lower-casing.

## Output plan

Each step has:

- `component`: smallest source name in the dependency component;
- `from`: exact current name for that move;
- `to`: exact destination name for that move;
- `temporary`: `true` for either move using the scratch name.

Forward components are sorted by ASCII source name. Path steps within a
component run in reverse dependency order. Rollback steps are the exact inverse
of the forward sequence, so their component order is reversed as well.

## Run locally

```sh
go run ./cmd/planner examples/manifest.json
# or
cat examples/manifest.json | go run ./cmd/planner
```

The command writes only the plan to standard output. Validation errors go to
standard error with a non-zero exit status.

## Run in Compose

The Compose service runs as an unprivileged user, with a read-only root
filesystem, no network, no Linux capabilities, and no bind mount by default:

```sh
docker compose build
docker compose run --rm planner
```

To replace the manifest without rebuilding, mount it read-only:

```sh
docker compose run --rm \
  -v "$PWD/examples/manifest.json:/manifest.json:ro" \
  planner
```

## Tests

The Go tests use small exhaustive mapping sets and an in-memory case-insensitive
file table. They verify:

- every planned destination is unoccupied at that moment;
- final exact-case names and file identities are correct;
- move count is `active edges + number of cycles`;
- every cycle introduces exactly one temporary move;
- the selected scratch name is the smallest non-conflicting numbered name;
- forward and rollback replay cannot overwrite a file;
- invalid current names, sources, targets, and duplicates are rejected.

```sh
go test ./...
go test -race ./...
```
