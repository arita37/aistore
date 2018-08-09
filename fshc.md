Filesystem Health Checker (FSHC)
---------------------------------

## Overview

FSHC monitors and manages filesystems used by DFC. Every time DFC triggers an IO error when reading or writing data, FSHC checks health of the fileystem. Checking a filesystem includes testing the filesystem availability, reading existing data, and creating temporary files. A filesystem that does not pass the test is automatically disabled and excluded from all next DFC operations. Once a disabled filesystem is repaired, it can be marked as available for DFC again.

### How FSHC detects a faulty filesystem

When an error is triggered, FSHC receives the error and a filename. If the error is not an IO error or it is not severe one(e.g, file not found error does not mean a trouble) no extra tests are performed. If the error needs attention, FSHC tries to find out to which filesystem the filename belongs. In case of the filesystem is already disabled, or it is being tested at that moment, or filename is outside of any filesystem utilized by DFC, FSHC returns immediately. Otherwise FSHC starts the filesystem check.

Filesystem check includes the following tests: availability, reading existing files, and writing to temporary files. Unavailable or readonly filesystem is disabled immediately without extra tests. For other filesystems FSHC selects a few random file to read, then creates a few temporary files filled with random data. The final decision about filesystem health is based on the number of errors of each operation and their severity.

## Getting started

Check FSHC configuration before deploying a cluster. All settings are in the section `fschecker` of [DFC configuration file](./dfc/setup/config.sh)

| Name | Default value | Description |
|---|---|---|
| fschecker_enabled | true | Enables or disables launching FHSC at startup. If FSHC is disabled it does not test any filesystem even a read/write error triggered |
| fschecker_test_files | 4 | The maximum number of existing files to read and temporary files to create when running a filesystem test |
| fschecker_error_limit | 2 | If the number of triggered IO errors for reading or writing test is greater or equal this limit the filesystem is disabled. The number of read and write errors are not summed up, so if the test triggered 1 read error and 1 write error the filesystem is considered unstable but it is not disabled |

When DFC is running, FSHC can be disabled and enabled on a given target via REST API.

Disable FSHC on a given target:

```
curl -i -X PUT -H 'Content-Type: application/json' \
	-d '{"action": "setconfig","name": "fschecker_enabled", "value": "false"}' \
	http://localhost:8084/v1/daemon
```

Enable FSHC on a given target:

```
curl -i -X PUT -H 'Content-Type: application/json' \
	-d '{"action": "setconfig","name": "fschecker_enabled", "value": "true"}' \
	http://localhost:8084/v1/daemon
```

## REST operations

| Operation | HTTP Action | Example |
|---|---|---|
| List target's filesystems (target only) | GET /v1/daemon?what=mountpaths | `curl -X GET http://localhost:8084/v1/daemon?what=mountpaths` |
| List all targets' filesystems (proxy only) | GET /v1/cluster?what=mountpaths | `curl -X GET http://localhost:8080/v1/cluster?what=mountpaths` |
| Disable mountpath in target | POST {"action": "disable", "value": "/existing/mountpath"} /v1/daemon/mountpaths | `curl -X POST -L -H 'Content-Type: application/json' -d '{"action": "disable", "value":"/mount/path"}' http://localhost:8083/v1/daemon/mountpaths`<sup>[1](#ft1)</sup> |
| Enable mountpath in target | POST {"action": "enable", "value": "/existing/mountpath"} /v1/daemon/mountpaths | `curl -X POST -L -H 'Content-Type: application/json' -d '{"action": "enable", "value":"/mount/path"}' http://localhost:8083/v1/daemon/mountpaths`<sup>[1](#ft1)</sup> |
| Add mountpath in target | PUT {"action": "add", "value": "/new/mountpath"} /v1/daemon/mountpaths | `curl -X PUT -L -H 'Content-Type: application/json' -d '{"action": "add", "value":"/mount/path"}' http://localhost:8083/v1/daemon/mountpaths` |
| Remove mountpath from target | DELETE {"action": "remove", "value": "/existing/mountpath"} /v1/daemon/mountpaths | `curl -X DELETE -L -H 'Content-Type: application/json' -d '{"action": "remove", "value":"/mount/path"}' http://localhost:8083/v1/daemon/mountpaths` |

<a name="ft1">1</a>: The request returns an HTTP status code 204 if the mountpath is already enabled/disabled or 404 if mountpath was not found.
