// Package docker is the only package that talks to the Docker daemon directly.
// It wraps the Docker Go SDK (github.com/moby/moby/client), applies a per-call
// timeout, and maps SDK errors onto the sentinel errors in errors.go.
package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

// DefaultCallTimeout bounds every single daemon call unless overridden.
const DefaultCallTimeout = 30 * time.Second

// Options configure the daemon client.
type Options struct {
	// Host overrides the daemon address (e.g. unix:///var/run/docker.sock). Empty uses DOCKER_HOST or the SDK default.
	Host string
	// CallTimeout bounds each daemon call. Zero uses DefaultCallTimeout.
	CallTimeout time.Duration
	// HTTPClient, if set, is used for daemon requests (tests inject a fake daemon through it).
	HTTPClient *http.Client
}

// Client is the Runtime's view of the Docker daemon.
type Client struct {
	cli         *client.Client
	callTimeout time.Duration
}

// New creates a daemon client. It does not contact the daemon; call Ping to verify connectivity.
func New(opts Options) (*Client, error) {
	ops := []client.Opt{client.WithAPIVersionNegotiation()}
	if opts.Host != "" {
		ops = append(ops, client.WithHost(opts.Host))
	} else {
		ops = append(ops, client.WithHostFromEnv())
	}
	if opts.HTTPClient != nil {
		ops = append(ops, client.WithHTTPClient(opts.HTTPClient))
	}
	cli, err := client.NewClientWithOpts(ops...)
	if err != nil {
		return nil, classify("create docker client", err)
	}
	timeout := opts.CallTimeout
	if timeout <= 0 {
		timeout = DefaultCallTimeout
	}
	return &Client{cli: cli, callTimeout: timeout}, nil
}

// Close releases client resources.
func (c *Client) Close() error { return c.cli.Close() }

func (c *Client) call(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, c.callTimeout)
}

// Ping verifies that the daemon is reachable and negotiates the API version.
func (c *Client) Ping(ctx context.Context) error {
	ctx, cancel := c.call(ctx)
	defer cancel()
	_, err := c.cli.Ping(ctx, client.PingOptions{NegotiateAPIVersion: true})
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return classify("ping", err)
	}
	// Any other failure to ping means the daemon cannot be used. The SDK reports
	// connection refusals as plain errors, so they are not matched by type.
	return fmt.Errorf("ping: %w: %w", ErrUnavailable, err)
}

// ImageExists reports whether a local image reference is present.
func (c *Client) ImageExists(ctx context.Context, ref string) (bool, error) {
	ctx, cancel := c.call(ctx)
	defer cancel()
	_, err := c.cli.ImageInspect(ctx, ref)
	if err == nil {
		return true, nil
	}
	e := classify("image inspect", err)
	if errors.Is(e, ErrNotFound) {
		return false, nil
	}
	return false, e
}

// ContainerCreate creates a container from spec and returns its ID.
// The container is not started.
func (c *Client) ContainerCreate(ctx context.Context, spec Spec) (string, error) {
	ctx, cancel := c.call(ctx)
	defer cancel()

	port := network.MustParsePort(strconv.Itoa(spec.ContainerPort) + "/tcp")
	stop := int(math.Ceil(spec.StopTimeout.Seconds()))
	pids := spec.PidsLimit
	cfg := &container.Config{
		Image:        spec.Image,
		Env:          spec.Env,
		Labels:       spec.Labels,
		ExposedPorts: network.PortSet{port: struct{}{}},
		StopTimeout:  &stop,
	}
	hostIP := spec.BindHost
	hc := &container.HostConfig{
		Resources: container.Resources{
			NanoCPUs:  int64(spec.CPUs * 1e9),
			Memory:    spec.MemoryBytes,
			PidsLimit: &pids,
		},
		CapDrop:     []string{"ALL"},
		SecurityOpt: []string{"no-new-privileges"},
		PortBindings: network.PortMap{
			port: []network.PortBinding{{HostIP: hostIP, HostPort: ""}},
		},
		Privileged: false,
	}
	res, err := c.cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:     cfg,
		HostConfig: hc,
		Name:       spec.Name,
	})
	if err != nil {
		return "", classify("container create", err)
	}
	return res.ID, nil
}

// ContainerStart starts a created or stopped container.
func (c *Client) ContainerStart(ctx context.Context, id string) error {
	ctx, cancel := c.call(ctx)
	defer cancel()
	_, err := c.cli.ContainerStart(ctx, id, client.ContainerStartOptions{})
	return classify("container start", err)
}

// ContainerStop sends SIGTERM and waits up to timeout before SIGKILL.
func (c *Client) ContainerStop(ctx context.Context, id string, timeout time.Duration) error {
	ctx, cancel := c.call(ctx)
	defer cancel()
	secs := int(math.Ceil(timeout.Seconds()))
	_, err := c.cli.ContainerStop(ctx, id, client.ContainerStopOptions{Timeout: &secs})
	return classify("container stop", err)
}

// ContainerRemove removes a container. Force is not used: callers stop first.
func (c *Client) ContainerRemove(ctx context.Context, id string) error {
	ctx, cancel := c.call(ctx)
	defer cancel()
	_, err := c.cli.ContainerRemove(ctx, id, client.ContainerRemoveOptions{})
	return classify("container remove", err)
}

// ContainerInspect returns the state of a container.
func (c *Client) ContainerInspect(ctx context.Context, id string) (Inspect, error) {
	ctx, cancel := c.call(ctx)
	defer cancel()
	res, err := c.cli.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		return Inspect{}, classify("container inspect", err)
	}
	return toInspect(res.Container), nil
}

// ContainersByLabel lists containers (including stopped ones) that carry label key=value.
func (c *Client) ContainersByLabel(ctx context.Context, key, value string) ([]Summary, error) {
	ctx, cancel := c.call(ctx)
	defer cancel()
	f := client.Filters{}.Add("label", key+"="+value)
	res, err := c.cli.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: f})
	if err != nil {
		return nil, classify("container list", err)
	}
	out := make([]Summary, 0, len(res.Items))
	for _, s := range res.Items {
		name := ""
		if len(s.Names) > 0 {
			name = strings.TrimPrefix(s.Names[0], "/")
		}
		out = append(out, Summary{ID: s.ID, Name: name, Labels: s.Labels, Status: string(s.State)})
	}
	return out, nil
}

// CopyFileToContainer writes a single regular file into dir inside the container.
// dir must already exist.
func (c *Client) CopyFileToContainer(ctx context.Context, id, dir, name string, content []byte, mode int64) error {
	ctx, cancel := c.call(ctx)
	defer cancel()
	archive, err := singleFileTar(name, content, mode)
	if err != nil {
		return fmt.Errorf("build archive: %w", err)
	}
	_, err = c.cli.CopyToContainer(ctx, id, client.CopyToContainerOptions{
		DestinationPath: dir,
		Content:         archive,
	})
	return classify("copy to container", err)
}

// ContainerStatsOnce returns a single resource sample with CPU deltas populated.
func (c *Client) ContainerStatsOnce(ctx context.Context, id string) (Stats, error) {
	ctx, cancel := c.call(ctx)
	defer cancel()
	res, err := c.cli.ContainerStats(ctx, id, client.ContainerStatsOptions{IncludePreviousSample: true})
	if err != nil {
		return Stats{}, classify("container stats", err)
	}
	defer res.Body.Close()
	var raw container.StatsResponse
	if err := json.NewDecoder(res.Body).Decode(&raw); err != nil {
		return Stats{}, fmt.Errorf("decode stats: %w", err)
	}
	return toStats(raw), nil
}

func toInspect(r container.InspectResponse) Inspect {
	out := Inspect{ID: r.ID, Name: strings.TrimPrefix(r.Name, "/"), Image: r.Image, HostPorts: map[string]int{}}
	if t, err := time.Parse(time.RFC3339Nano, r.Created); err == nil {
		out.Created = t
	}
	if r.Config != nil {
		out.Labels = r.Config.Labels
	}
	if r.State != nil {
		out.Status = string(r.State.Status)
		out.Running = r.State.Running
		out.ExitCode = r.State.ExitCode
		if r.State.Health != nil {
			out.Health = string(r.State.Health.Status)
		}
	}
	if r.NetworkSettings != nil {
		for port, bindings := range r.NetworkSettings.Ports {
			for _, b := range bindings {
				if p, err := strconv.Atoi(b.HostPort); err == nil && p > 0 {
					out.HostPorts[port.String()] = p
					break
				}
			}
		}
	}
	return out
}

func toStats(r container.StatsResponse) Stats {
	s := Stats{
		Read:        r.Read,
		MemoryUsage: r.MemoryStats.Usage,
		MemoryLimit: r.MemoryStats.Limit,
		PidsCurrent: r.PidsStats.Current,
	}
	cpuDelta := float64(r.CPUStats.CPUUsage.TotalUsage) - float64(r.PreCPUStats.CPUUsage.TotalUsage)
	sysDelta := float64(r.CPUStats.SystemUsage) - float64(r.PreCPUStats.SystemUsage)
	if sysDelta > 0 && cpuDelta >= 0 {
		online := float64(r.CPUStats.OnlineCPUs)
		if online == 0 {
			online = 1
		}
		s.CPUPercent = cpuDelta / sysDelta * online * 100
	}
	return s
}

func singleFileTar(name string, content []byte, mode int64) (io.Reader, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	hdr := &tar.Header{Name: name, Mode: mode, Size: int64(len(content)), Typeflag: tar.TypeReg}
	if err := tw.WriteHeader(hdr); err != nil {
		return nil, err
	}
	if _, err := tw.Write(content); err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return &buf, nil
}
