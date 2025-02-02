//go:generate go run genassets.go

package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
	"github.com/ensody/ssh-agent-inject/assets"
	"github.com/ensody/ssh-agent-inject/common"
)

var (
	verbose = flag.Bool("v", false, "verbose output on stderr")
)

const injectionLabel = "inject-ssh-agent"
const userLabel = "inject-ssh-uid"

func main() {
	flag.Parse()
	if len(flag.Args()) != 0 {
		fmt.Fprintln(flag.CommandLine.Output(), "Error: No positional arguments allowed.")
		flag.Usage()
		os.Exit(2)
	}

	for {
		err := scanContainers()
		if err != nil {
			log.Println(err)
			time.Sleep(5 * time.Second)
			continue
		}
		time.Sleep(1 * time.Second)
	}
}

func getInjectUserId(container types.Container) string {
	for label, value := range container.Labels {
		if label == userLabel && len(value) > 0 {
			return value
		}
	}
	return "0"
}

func scanContainers() error {
	ctx := context.Background()
	cli, err := client.NewEnvClient()
	defer cli.Close()
	if err != nil {
		return fmt.Errorf("Error connecting to Docker: %w", err)
	}

	filters := filters.NewArgs()
	filters.Add("label", injectionLabel)

	containers, err := cli.ContainerList(ctx, types.ContainerListOptions{Filters: filters})
	if err != nil {
		return fmt.Errorf("Error listing containers: %w", err)
	}

	for _, container := range containers {
		uid := getInjectUserId(container)
		injectAgent(ctx, cli, container.ID, uid)
	}

	return nil
}

var injectedAgents = struct {
	sync.RWMutex
	containers map[string]bool
}{containers: map[string]bool{}}

func injectAgent(ctx context.Context, cli *client.Client, containerID string, uid string) {
	injectedAgents.Lock()
	defer injectedAgents.Unlock()
	if _, ok := injectedAgents.containers[containerID]; ok {
		return
	}
	if *verbose {
		log.Println(containerID, "Starting ssh-agent injection")
	}
	info, err := cli.ContainerInspect(ctx, containerID)
	if err != nil {
		log.Println(containerID, "Failed inspecting container", err)
		return
	}
	socketPath := "/tmp/.ssh-auth-sock"
	const authSockEnvPrefix = common.AuthSockEnv + "="
	for _, env := range info.Config.Env {
		if strings.HasPrefix(env, authSockEnvPrefix) && len(env) > len(authSockEnvPrefix) {
			socketPath = strings.TrimPrefix(env, authSockEnvPrefix)
			break
		}
	}
	if *verbose {
		log.Println(containerID, "Copying ssh-agent-pipe into container")
	}
	err = cli.CopyToContainer(ctx, containerID, "/usr/local/bin/",
		strings.NewReader(assets.AgentArchive), types.CopyToContainerOptions{AllowOverwriteDirWithFile: true})
	if err != nil {
		log.Println(containerID, "Failed copying ssh-agent-pipe into container", err)
		return
	}
	injectedAgents.containers[containerID] = true
	go injectAgentBg(containerID, socketPath, uid)
}

func injectAgentBg(containerID string, socketPath string, uid string) {
	defer func() {
		injectedAgents.Lock()
		defer injectedAgents.Unlock()
		delete(injectedAgents.containers, containerID)
	}()

	conn, err := openAgentSocket()
	if err != nil {
		log.Println(containerID, "Failed connecting to host ssh-agent", err)
		return
	}
	if *verbose {
		log.Println(containerID, "Connected to host ssh-agent")
	}

	args := []string{
		"exec", "-i",
		"-u", uid,
		"-e", common.AuthSockEnv + "=" + socketPath,
		containerID, "/usr/local/bin/ssh-agent-pipe",
	}
	if *verbose {
		args = append(args, "-v")
	}
	cmd := exec.Command("docker", args...)
	setupCommandForPlatform(cmd)
	stdin, err := cmd.StdinPipe()
	stdout, err := cmd.StdoutPipe()
	stderr, err := cmd.StderrPipe()

	connLock := sync.RWMutex{}

	var wg sync.WaitGroup
	cleanup := func() {
		connLock.Lock()
		defer connLock.Unlock()
		if conn != nil {
			conn.Close()
			conn = nil
		}
		stdin.Close()
		stdout.Close()
		stderr.Close()
		wg.Done()
	}

	wg.Add(1)
	go func() {
		defer cleanup()
		_, err = io.Copy(conn, stdout)
		if err != nil {
			log.Println(containerID, "Copy from sock to agent failed:", err)
		}
	}()

	wg.Add(1)
	go func() {
		defer cleanup()
		_, err = io.Copy(stdin, conn)
		if err != nil {
			log.Println(containerID, "Copy from agent to sock failed:", err)
		}
	}()

	wg.Add(1)
	go func() {
		defer cleanup()
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			log.Println(containerID, scanner.Text())
			if err := scanner.Err(); err != nil {
				log.Println(containerID, "Failed reading logs:", err)
				return
			}
		}
	}()

	if *verbose {
		log.Println(containerID, "Injecting ssh-agent")
	}

	err = cmd.Run()
	if *verbose && err != nil {
		log.Println(containerID, "Failed injecting ssh-agent", err)
	} else if *verbose {
		log.Println(containerID, "Injected ssh-agent terminated")
	}

	wg.Wait()
}
