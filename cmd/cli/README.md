AIS Command Line Interface is a tool used to interact with resources of AIStore. It gives users the ability to query information from specific daemons,
create or delete resources or download files to buckets directly from the command line.

<img src="/docs/images/ais2.13.gif" alt="CLI-playback" width="900">

## Getting Started

Run `make cli` in AIStore repo root directory to install AIS CLI binary in your `$GOPATH/bin` directory.
You can also install [shell autocompletions](#ais-cli-shell-autocomplete) for AIS CLI with `make cli-autocompletions`.

> The rest of the README assumes that user's `PATH` environment variable contains `$GOPATH/bin` directory.
> Run `export PATH=$PATH:$GOPATH/bin` if this is not the case for you.
> You can read more about GOPATH environment variable [here](https://golang.org/doc/code.html#GOPATH).

## Using AIS CLI

### Config

On first use, CLI will create `config.json` file in `$XDG_CONFIG_HOME/ais` (or if `XDG_CONFIG_HOME` is not set, in `~/.config/ais`) directory.
The content of the file presents as follows:

```json
{
  "cluster": {
    "url": "http://127.0.0.1:8080",
    "default_ais_host": "http://127.0.0.1:8080",
    "default_docker_host": "http://172.50.0.2:8080"
  },
  "timeout": {
    "tcp_timeout": "60s",
    "http_timeout": "0s"
  }
}
```

Simply change config file so next time CLI will use updated values.

### First steps

To get the list of commands run following command:

```console
$ ais --help
```

To check if the CLI can correctly contact the cluster and to get cluster status run following command:

```console
$ ais show cluster
```

## AIS CLI Shell Autocomplete

The CLI tool supports `bash` and `zsh` auto-complete functionality.

### Installing

When running `install.sh` you will be asked if you want to install autocompletions.
To install them manually, run `bash autocomplete/install.sh`.

### Uninstalling

To uninstall autocompletions run `bash autocomplete/uninstall.sh`.

## Supported Resources

List of available CLI resources

* [Bucket](resources/bucket.md)

* [Object](resources/object.md)

* [Daemon/Cluster](resources/daeclu.md)

* [Xaction](resources/xaction.md)

* [Downloader](resources/download.md)

* [DSort](resources/dsort.md)

* [Mountpath](resources/mpath.md)

* [Remote cluster](resources/remote.md)

* [Auth](resources/users.md)

## Info For Developers

The CLI uses [urfave/cli](https://github.com/urfave/cli) framework.

### Adding New Commands

Currently, the CLI has the format of `ais <command> <resource>`.

To add a new resource to an existing command,

1. Create a subcommand entry for the resource in the command object
2. Create an entry in the command's flag map for the new resource
3. Register flags in the subcommand object
4. Register the handler function (named `XXXHandler`) in the subcommand object

To add a new resource to a new command,

1. Create a new Go file (named `xxx_hdlr.go`) with the name of the new command and follow the format of existing files
2. Once the new command and subcommands are implemented, make sure to register the new command with the CLI (see `setupCommands()` in `app.go`)

## Default flag and argument values via environment variables

#### Bucket Provider

Provider syntax `[provider://]BUCKET_NAME` is valid CLI-wide, meaning that every command supporting `BUCKET_NAME` argument
also supports provider syntax. For more details refer to each command's documentation.

Allowed values: `''` (autodetect provider), `ais` (local cluster), `aws` (Amazon Web Services), `gcp` (Google Cloud Platform),
`azure` (Microsoft Azure), `cloud` (anonymous - cloud provider determined automatically).
Additionally `provider://` syntax supports aliases `s3` (for `aws`), `gs` (for `gcp`) and `az` (for `azure`).
