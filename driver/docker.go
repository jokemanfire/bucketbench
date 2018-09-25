package driver

import (
	"bufio"
	"fmt"
	"io/ioutil"
	"strconv"
	"strings"

	"github.com/estesp/bucketbench/utils"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
)

const defaultDockerBinary = "docker"

// DockerDriver is an implementation of the driver interface for the Docker engine.
// IMPORTANT: This implementation does not protect instance metadata for thread safely.
// At this time there is no understood use case for multi-threaded use of this implementation.
type DockerDriver struct {
	dockerBinary string
	dockerInfo   string
	logDriver    string
}

// DockerContainer is an implementation of the container metadata needed for docker
type DockerContainer struct {
	name        string
	imageName   string
	cmdOverride string
	detached    bool
	trace       bool
}

// NewDockerDriver creates an instance of the docker driver, providing a path to the docker client binary
func NewDockerDriver(binaryPath string, logDriver string) (Driver, error) {
	if binaryPath == "" {
		binaryPath = defaultDockerBinary
	}

	resolvedBinPath, err := utils.ResolveBinary(binaryPath)
	if err != nil {
		return &DockerDriver{}, err
	}

	driver := &DockerDriver{
		dockerBinary: resolvedBinPath,
		logDriver:    logDriver,
	}

	info, err := driver.Info()
	if err != nil {
		return nil, err
	}

	log.Debugf("running docker driver: '%s', log driver: '%s'", info, logDriver)
	return driver, nil
}

// newDockerContainer creates the metadata object of a docker-specific container with
// image name, container runtime name, and any required additional information
func newDockerContainer(name, image, cmd string, detached bool, trace bool) Container {
	return &DockerContainer{
		name:        name,
		imageName:   image,
		cmdOverride: cmd,
		detached:    detached,
		trace:       trace,
	}
}

// Name returns the name of the container
func (c *DockerContainer) Name() string {
	return c.name
}

// Detached returns whether the container should be started in detached mode
func (c *DockerContainer) Detached() bool {
	return c.detached
}

// Trace returns whether the container should be started with tracing enabled
func (c *DockerContainer) Trace() bool {
	return c.trace
}

// Image returns the image name that Docker will use
func (c *DockerContainer) Image() string {
	return c.imageName
}

// Command returns the optional overriding command that Docker will use
// when executing a container based on this container's image
func (c *DockerContainer) Command() string {
	return c.cmdOverride
}

//GetPodID return pod-id associated with container.
//only used by CRI-based drivers
func (c *DockerContainer) GetPodID() string {
	return ""
}

// Type returns a driver.Type to indentify the driver implementation
func (d *DockerDriver) Type() Type {
	return Docker
}

// Path returns the binary path of the docker binary in use
func (d *DockerDriver) Path() string {
	return d.dockerBinary
}

// Close allows the driver to handle any resource free/connection closing
// as necessary. Docker has no need to perform any actions on close.
func (d *DockerDriver) Close() error {
	return nil
}

func (d *DockerDriver) PID() (int, error) {
	buf, err := ioutil.ReadFile("/var/run/docker.pid")
	if err != nil {
		return 0, errors.Wrap(err, "could not read Docker pid file")
	}

	return strconv.Atoi(string(buf))
}

// Wait will block until container stop
func (d *DockerDriver) Wait(ctr Container) (string, int, error) {
	return utils.ExecTimedCmd(d.dockerBinary, "wait "+ctr.Name())
}

// Info returns
func (d *DockerDriver) Info() (string, error) {
	if d.dockerInfo != "" {
		return d.dockerInfo, nil
	}

	infoStart := "docker driver (binary: " + d.dockerBinary + ")\n"
	version, err := utils.ExecCmd(d.dockerBinary, "version")
	info, err := utils.ExecCmd(d.dockerBinary, "info")
	if err != nil {
		return "", fmt.Errorf("Error trying to retrieve docker daemon info: %v", err)
	}
	d.dockerInfo = infoStart + parseDaemonInfo(version, info)
	return d.dockerInfo, nil
}

// Create will create a container instance matching the specific needs
// of a driver
func (d *DockerDriver) Create(name, image, cmdOverride string, detached bool, trace bool) (Container, error) {
	return newDockerContainer(name, image, cmdOverride, detached, trace), nil
}

// Clean will clean the environment; removing any exited containers
func (d *DockerDriver) Clean() error {
	// clean up any containers from a prior run
	log.Info("Docker: Stopping any running containers created during bucketbench runs")
	cmd := "docker stop `docker ps -qf name=bb-ctr-`"
	out, err := utils.ExecShellCmd(cmd)
	if err != nil {
		// first make sure the error isn't simply that there were no
		// containers to stop:
		if !strings.Contains(out, "requires at least 1 argument") {
			log.Warnf("Docker: Failed to stop running bb-ctr-* containers: %v (output: %s)", err, out)
		}
	}
	log.Info("Docker: Removing exited containers from bucketbench runs")
	cmd = "docker rm -f `docker ps -aqf name=bb-ctr-`"
	out, err = utils.ExecShellCmd(cmd)
	if err != nil {
		// first make sure the error isn't simply that there were no
		// exited containers to remove:
		if !strings.Contains(out, "requires at least 1 argument") {
			log.Warnf("Docker: Failed to remove exited bb-ctr-* containers: %v (output: %s)", err, out)
		}
	}
	return nil
}

// Run will execute a container using the driver
func (d *DockerDriver) Run(ctr Container) (string, int, error) {
	args := []string{"run"}

	if ctr.Detached() {
		args = append(args, "-d")
	}

	if d.logDriver != "" {
		args = append(args, "--log-driver", d.logDriver)
	}

	args = append(args, "--name", ctr.Name(), ctr.Image())

	if ctr.Command() != "" {
		args = append(args, ctr.Command())
	}

	return utils.ExecTimedCmd(d.dockerBinary, strings.Join(args, " "))
}

// Stop will stop/kill a container
func (d *DockerDriver) Stop(ctr Container) (string, int, error) {
	return utils.ExecTimedCmd(d.dockerBinary, "kill "+ctr.Name())
}

// Remove will remove a container
func (d *DockerDriver) Remove(ctr Container) (string, int, error) {
	return utils.ExecTimedCmd(d.dockerBinary, "rm "+ctr.Name())
}

// Pause will pause a container
func (d *DockerDriver) Pause(ctr Container) (string, int, error) {
	return utils.ExecTimedCmd(d.dockerBinary, "pause "+ctr.Name())
}

// Unpause will unpause/resume a container
func (d *DockerDriver) Unpause(ctr Container) (string, int, error) {
	return utils.ExecTimedCmd(d.dockerBinary, "unpause "+ctr.Name())
}

func (d *DockerDriver) Metrics(ctr Container) (interface{}, error) {
	output, err := utils.ExecCmd(d.dockerBinary, "stats --no-stream "+ctr.Name())
	return output, err
}

// return a condensed string of version and daemon information
func parseDaemonInfo(version, info string) string {
	var (
		clientVer string
		clientAPI string
		serverVer string
	)
	vReader := strings.NewReader(version)
	vScan := bufio.NewScanner(vReader)

	for vScan.Scan() {
		line := vScan.Text()
		parts := strings.Split(line, ":")
		switch strings.TrimSpace(parts[0]) {
		case "Version":
			if clientVer == "" {
				// first time is client
				clientVer = strings.TrimSpace(parts[1])
			} else {
				serverVer = strings.TrimSpace(parts[1])
			}
		case "API version":
			if clientAPI == "" {
				// first instance is client
				clientAPI = parts[1]
				clientVer = clientVer + "|API:" + strings.TrimSpace(parts[1])
			} else {
				serverVer = serverVer + "|API:" + strings.TrimSpace(parts[1])
			}
		default:
		}

	}
	iReader := strings.NewReader(info)
	iScan := bufio.NewScanner(iReader)

	for iScan.Scan() {
		line := iScan.Text()
		parts := strings.Split(line, ":")
		switch strings.TrimSpace(parts[0]) {
		case "Kernel Version":
			serverVer = serverVer + "|Kernel:" + strings.TrimSpace(parts[1])
		case "Storage Driver":
			serverVer = serverVer + "|Storage:" + strings.TrimSpace(parts[1])
		case "Backing Filesystem":
			serverVer = serverVer + "|BackingFS:" + strings.TrimSpace(parts[1])
		default:
		}

	}
	return fmt.Sprintf("[CLIENT:%s][SERVER:%s]", clientVer, serverVer)
}
