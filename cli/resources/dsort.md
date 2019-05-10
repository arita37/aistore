## Distributed Sort

[AIS DSort](/dsort/README.md) supports following types of dSort requests:

* **gen** - put randomly generated shards which then can be used for dSort testing
* **start** - start new dSort job with provided specification
* **status** - retrieve statistics and metrics of currently running dSort job
* **abort** - abort currently running dSort job
* **rm** - remove finished dSort job from the dsort job list
* **ls** - list all dSort jobs and their states

## Command List

### gen

`ais dsort gen --template <value> --fsize <value> --fcount <value>`

Puts randomly generated shards which then can be used for dSort testing.

| Flag | Type | Description | Default |
| --- | --- | --- | --- |
| `--ext` | `string` | extension for shards (either '.tar' or '.tgz') | `.tar` |
| `--bucket` | `string` | bucket where shards will be put | `dsort-testing` |
| `--template` | `string` | template of input shard name | `shard-{0..9}` |
| `--fsize` | `int` | single file size (in bytes) inside the shard | `1024` |
| `--fcount` | `int` | number of files inside single shard | `5` |
| `--cleanup` | `bool` | when set, the old bucket will be deleted and created again | `false` |
| `--conc` | `int` | limits number of concurrent put requests and number of concurrent shards created | `10` |


Examples:
* `ais dsort gen --fsize 262144 --fcount 100` generates 10 shards each containing 100 files of size 256KB and puts them inside `dsort-testing` bucket. Shards will be named: `shard-0.tar`, `shard-1.tar`, ..., `shard-9.tar`. 
* `ais dsort gen --ext .tgz --template "super_shard_{000..099}_last" --fsize 262144 --cleanup` generates 100 shards each containing 5 files of size 256KB and puts them inside `dsort-testing` bucket. Shards will be compressed and named: `super_shard_000_last.tgz`, `super_shard_001_last.tgz`, ..., `super_shard_099_last.tgz`. 


### start

`ais dsort start <json_specification>`

Starts new dSort job with provided specification. Upon creation, `id` of the 
job is returned - it can then be used to abort it or retrieve metrics. Following
table describes json keys which can be used in specification.

| Key | Type | Description | Required |
| --- | --- | --- | --- |
| `extension` | `string` | extension of input and output shards (either `.tar`, `.tgz` or `.zip`) | yes |
| `bucket` | `string` | bucket where shards objects are stored | yes |
| `bprovider` | `string` | describes if the bucket is local or cloud | no (default: `"local"`) |
| `output_bucket` | `string` | bucket where new output shards will be saved | no (default: same as `bucket`) |
| `output_bprovider` | `string` | describes if the output bucket is local or cloud | no (default: same as `bpovider`) |
| `input_format` | `string` | name template for input shard | yes |
| `output_format` | `string` | name template for output shard | yes |
| `description` | `string` | description of dsort job | no (default: `""`) |
| `shard_size` | `int` | size of output of shard | yes |
| `algorithm.kind` | `string` | determines which algorithm should be during dSort job, available are: `"alphanumeric"`, `"shuffle"`, `"content"` | no (default: `"alphanumeric"`) |
| `algorithm.decreasing` | `bool` | determines if the algorithm should sort the records in decreasing or increasing order, used for `kind=alphanumeric` or `kind=content` | no (default: `false`) |
| `algorithm.seed` | `string` | seed provided to random generator, used when `kind=shuffle` | no (default: `""`, `time.Now()` is used) |
| `algorithm.extension` | `string` | content of the file with provided extension will be used as sorting key, used when `kind=content` | yes (only when `kind=content`) |
| `algorithm.format_type` | `string` | format type (`int`, `float` or `string`) describes how the content of the file should be interpreted, used when `kind=content` | yes (only when `kind=content`) |
| `max_mem_usage` | `string` | limits maximum of total memory until extraction starts spilling data to the disk, can be 60% or 10GB | no (default: same as in `config.sh`) |
| `extract_concurrency_limit` | `string` | limits number of concurrent shards extracted | no (default: same as in `config.sh`) |
| `create_concurrency_limit` | `string` | limits number of concurrent shards created | no (default: same as in `config.sh`) |
| `extended_metrics` | `bool` | determines if dsort should collect extended statistics | no (default: `false`) |

Examples:
* starts (alphanumeric) sorting dSort job with extended metrics for shards with names `shard-0.tar`, `shard-1.tar`, ..., `shard-9.tar`. Each of output shards will have at least `10240` bytes and will be named `new-shard-0000.tar`, `new-shard-0001.tar`, ... 
```bash
ais dsort start '{
    "extension": ".tar",
    "bucket": "dsort-testing",
    "input_format": "shard-{0..9}",
    "output_format": "new-shard-{0000..1000}",
    "shard_size": 10240,
    "description": "sort shards from 0 to 9",
    "algorithm": {
        "kind": "alphanumeric"
    },
    "extract_concurrency_limit": 30,
    "create_concurrency_limit": 50,
    "extended_metrics": true
}'
```

### status

`ais dsort status --id <value>`

Retrieves status of the dSort with provided `id` which is returned upon creation.

| Flag | Type | Description | Default |
| --- | --- | --- | --- |
| `--id` | `string` | unique identifier of dSort job returned upon job creation | `""` |
| `--progress` | `bool` | if set, displays a progress bar that illustrates the progress of the dSort | `false` |
| `--refresh` | `int` | refreshing rate of the progress bar refresh or metrics refresh (in milliseconds) | `1000` |
| `--log` | `string` | path to file where the metrics will be saved (does not work with progress bar) | `/tmp/dsort_run.txt` |

Examples:
* `ais dsort status --id "5JjIuGemR"` returns the metrics of the dSort job
* `ais dsort status --id "5JjIuGemR" --progress --refresh 500` creates progress bar for the dSort job and refreshes it every `500` milliseconds
* `ais dsort status --id "5JjIuGemR" --refresh 500` every `500` milliseconds returns newly fetched metrics of the dSort job
* `ais dsort status --id "5JjIuGemR" --refresh 500 --log "/tmp/dsort_run.txt"` every `500` milliseconds saves newly fetched metrics of the dSort job to `/tmp/dsort_run.txt` file

### abort

`ais dsort abort --id <value>`

Aborts dSort job given its id.

| Flag | Type | Description | Default |
| --- | --- | --- | --- |
| `--id` | `string` | unique identifier of dSort job returned upon job creation | `""` |

Examples:
* `ais dsort abort --id "5JjIuGemR"` aborts the dSort job

### rm

`ais dsort rm --id <value>`

Removes finished dSort job from the list given its id.

| Flag | Type | Description | Default |
| --- | --- | --- | --- |
| `--id` | `string` | unique identifier of dSort job returned upon job creation | `""` |

Examples:
* `ais dsort rm --id "5JjIuGemR"` removes the dSort job

### ls

`ais dsort ls --regex <value>`

Lists dSort jobs whose descriptions match given `regex`.

| Flag | Type | Description | Default |
| --- | --- | --- | --- |
| `--regex` | `string` | regex for the description of dSort jobs | `""` |

Examples:
* `ais dsort ls` lists all dSorts jobs
* `ais dsort ls --regex "^dsort-(.*)"` lists all dSorts jobs which description starts with `dsort-` prefix


