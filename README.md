# ssh-agent-inject

Forwards the host's ssh-agent into a Docker container. This is especially useful when working with [devcontainers](https://containers.dev/) using the devcontainer cli but without using VSCode (e.g a vim configuration).

## Why this is needed

While you can bind-mount the `SSH_AUTH_SOCK` from a Linux/macOS host, some
configurations might run into problems:
  - Users who set `SSH_AUTH_SOCK` to a non-default value (e.g. `~/.ssh/agent.sock`) to have multiple sessions reuse the same agent.
  - Users with non-root users inside the container 

With ssh-agent-inject you can skip those annoyances and simply reuse your host's ssh-agent.

## Usage
Make sure ssh-agent-inject runs in the background or just launch it on-demand.

Add the following to your Dockerfile:

```Dockerfile
ENV SSH_AUTH_SOCK=/tmp/.ssh-auth-sock
LABEL inject-ssh-agent=
```

Alternatively, you can run an arbitrary container directly:

```
docker run -e SSH_AUTH_SOCK=/tmp/.ssh-auth-sock -l inject-ssh-agent ...
```

### Non-root user inside container
If you're using a non-root user inside the container, you need to make sure that the user has access to the socket file. You can do this by adding the following label to your Dockerfile:

```Dockerfile
LABEL inject-ssh-uid=1000
```

## How it works

This project consists of two applications that communicate through stdio: `ssh-agent-inject` and `ssh-agent-pipe` which is embedded within the `ssh-agent-inject` binary (that's why you don't see it in the release archive).

The `ssh-agent-inject` command runs on the host and

* watches Docker for containers having the `inject-ssh-agent` label
* copies the embedded `ssh-agent-pipe` binary into those containers
* runs `ssh-agent-pipe` within each container via `docker exec`
* connects to the host's ssh-agent (one connection per container)
* forwards the host's ssh-agent to `ssh-agent-pipe` via stdio

The `ssh-agent-pipe` command runs in the container and

* listens on a UNIX socket at `$SSH_AUTH_SOCK`
* handles parallel connections on that UNIX socket
* serializes all socket<->stdio communication (handles one request-response pair at a time)

The apps communicate via stdio because this keeps the attack surface small and makes it easier to ensure that nobody else can connect to your ssh-agent (assuming you can trust the Docker container, of course).

## Building

All required dependencies are contained in a Docker image defined in `.devcontainer/`, which can be automatically used with Visual Studio Code (or manually via Docker build & run).
That way your host system stays clean and the whole environment is automated, exactly defined, isolated from the host, and easily reproducible.
This saves time and prevents mistakes (wrong version, interference with other software installed on host, etc.).

Run `./build.sh` to build binaries for all platforms.