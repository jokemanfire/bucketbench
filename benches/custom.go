package benches

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/estesp/bucketbench/driver"
	log "github.com/sirupsen/logrus"
)

// CustomBench benchmark runs a series of container lifecycle operations as
// defined in the provided YAML against specified image and driver types
type CustomBench struct {
	driver.Config
	benchName   string
	driver      driver.Driver
	imageInfo   string
	cmdOverride string
	trace       bool
	stats       []RunStatistics
	elapsed     time.Duration
	state       State
}

// Init initializes the benchmark
func (cb *CustomBench) Init(ctx context.Context, name string, driverType driver.Type, binaryPath, imageInfo, cmdOverride string, trace bool) error {
	cb.DriverType = driverType
	cb.Path = binaryPath

	driver, err := driver.New(ctx, &cb.Config)
	if err != nil {
		return fmt.Errorf("error during driver initialization for CustomBench: %v", err)
	}

	if driver == nil {
		return fmt.Errorf("driver initialization failed for type %v", driverType.String())
	}

	// get driver info; will also validate for daemon-based variants whether system is ready/up
	// and running for benchmarking
	info, err := driver.Info(ctx)
	if err != nil {
		return fmt.Errorf("error during driver info query: %v", err)
	}

	log.Infof("driver initialized: %s", info)

	// prepare environment
	err = driver.Clean(ctx)
	if err != nil {
		return fmt.Errorf("error during driver init cleanup: %v", err)
	}

	cb.benchName = name
	cb.imageInfo = imageInfo
	cb.cmdOverride = cmdOverride
	cb.driver = driver
	cb.trace = trace
	return nil
}

// Validate the unit of benchmark execution (create-run-stop-remove) against
// the initialized driver.
func (cb *CustomBench) Validate(ctx context.Context) error {
	ctr, err := cb.driver.Create(ctx, "bb-test", cb.imageInfo, cb.cmdOverride, true, cb.trace)
	if err != nil {
		return fmt.Errorf("Driver validation: error creating test container: %v", err)
	}

	_, _, err = cb.driver.Run(ctx, ctr)
	if err != nil {
		return fmt.Errorf("Driver validation: error running test container: %v", err)
	}

	_, _, err = cb.driver.Stop(ctx, ctr)
	if err != nil {
		return fmt.Errorf("Driver validation: error stopping test container: %v", err)
	}
	// allow time for quiesce of stopped state in process and container executor metadata
	time.Sleep(50 * time.Millisecond)

	_, _, err = cb.driver.Remove(ctx, ctr)
	if err != nil {
		return fmt.Errorf("Driver validation: error deleting test container: %v", err)
	}
	return nil
}

// Run executes the benchmark iterations against a specific engine driver type
// for a specified number of iterations
func (cb *CustomBench) Run(ctx context.Context, threads, iterations int, commands []string) error {
	log.Infof("Start CustomBench run: threads (%d); iterations (%d)", threads, iterations)
	statChan := make([]chan RunStatistics, threads)
	for i := range statChan {
		statChan[i] = make(chan RunStatistics, iterations)
	}
	cb.state = Running
	start := time.Now()

	var wg sync.WaitGroup
	for i := 0; i < threads; i++ {
		// create a driver instance for each thread to protect from drivers
		// which may not be threadsafe (e.g. gRPC client connection in containerd?)
		drv, err := driver.New(ctx, &cb.Config)
		if err != nil {
			return fmt.Errorf("error creating new driver for thread %d: %v", i, err)
		}

		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			cb.runThread(ctx, drv, index, iterations, commands, statChan[index])
		}(i)
	}
	wg.Wait()
	cb.elapsed = time.Since(start)

	log.Infof("CustomBench threads complete in %v time elapsed", cb.elapsed)
	//collect stats
	for _, ch := range statChan {
		for statEntry := range ch {
			cb.stats = append(cb.stats, statEntry)
		}
	}
	cb.state = Completed
	// final environment cleanup
	if err := cb.driver.Clean(ctx); err != nil {
		return fmt.Errorf("Error during driver final cleanup: %v", err)
	}
	return nil
}

func (cb *CustomBench) runThread(ctx context.Context, runner driver.Driver, threadNum, iterations int, commands []string, stats chan RunStatistics) {
	defer func() {
		if err := runner.Close(); err != nil {
			log.Errorf("error on closing driver: %v", err)
		}
		close(stats)
	}()

	for i := 0; i < iterations; i++ {
		errors := make(map[string]int)
		durations := make(map[string]time.Duration)
		// commands are specified in the passed in array; we will need
		// a container for each set of commands:
		name := fmt.Sprintf("%s-%d-%d", driver.ContainerNamePrefix, threadNum, i)
		ctr, err := runner.Create(ctx, name, cb.imageInfo, cb.cmdOverride, true, cb.trace)
		if err != nil {
			log.Errorf("Error on creating container %q from image %q: %v", name, cb.imageInfo, err)
			return
		}

		log_error := func(cmd string, name string, err error, out string, elapsed time.Duration) {
			if err != nil {
				errors[cmd]++
				log.Warnf("Error during container command %q on %q: %v\n  Output: %s", cmd, name, err, out)
			}
			durations[cmd] = elapsed
			log.Debug(out)
		}
		// Stats calls must be stopped at the end of current iteration if streaming
		statsCtx, statsCancel := context.WithCancel(ctx)

		for _, cmd := range commands {
			// add binary expression
			parts := strings.SplitN(cmd, " ", 2)
			var args []string
			if len(parts) == 2 {
				args = strings.Split(parts[1], " ")
				cmd = parts[0]
				log.Debugf("running command: %s args: %s", cmd, args)
			}
			log.Debugf("running command: %s", cmd)
			switch strings.ToLower(cmd) {
			case "run", "start":
				out, runElapsed, err := runner.Run(ctx, ctr)
				log_error("run", name, err, out, runElapsed)
			case "stop", "kill":
				out, stopElapsed, err := runner.Stop(ctx, ctr)
				log_error("stop", name, err, out, stopElapsed)
			case "remove", "erase", "delete":
				out, rmElapsed, err := runner.Remove(ctx, ctr)
				log_error("remove", name, err, out, rmElapsed)
			case "pause":
				out, pauseElapsed, err := runner.Pause(ctx, ctr)
				log_error(cmd, name, err, out, pauseElapsed)
			case "unpause", "resume":
				out, unpauseElapsed, err := runner.Unpause(ctx, ctr)
				log_error("resume", name, err, out, unpauseElapsed)
			case "wait":
				out, waitElapsed, err := runner.Wait(ctx, ctr)
				log_error(cmd, name, err, out, waitElapsed)
			case "metrics", "stats":
				if reader, err := runner.Stats(statsCtx, ctr); err != nil {
					errors["metrics"]++
					log.Warnf("Error during container command %q on %q: %v", cmd, name, err)
				} else {
					go func() {
						// We want to measure the overhead of collecting stats, we're not interested in stats data itself,
						// so just discard the stream output
						io.Copy(io.Discard, reader)
						reader.Close()
					}()
				}
			case "execsync":
				out, execElapsed, err := runner.Execsync(ctx, ctr, args)
				log_error(cmd, name, err, out, execElapsed)
			default:
				log.Errorf("Command %q unrecognized from YAML commands list; skipping", cmd)
			}
		}

		statsCancel()

		stats <- RunStatistics{
			Durations: durations,
			Errors:    errors,
			Timestamp: time.Now().UTC(),
		}
	}
}

// Stats returns the statistics of the benchmark run
func (cb *CustomBench) Stats() []RunStatistics {
	if cb.state == Completed {
		return cb.stats
	}
	return []RunStatistics{}
}

// State returns Created, Running, or Completed
func (cb *CustomBench) State() State {
	return cb.state
}

// Elapsed returns the time.Duration taken to run the benchmark
func (cb *CustomBench) Elapsed() time.Duration {
	return cb.elapsed
}

// Type returns the type of benchmark
func (cb *CustomBench) Type() Type {
	return Custom
}

// Info returns a string with the driver type and custom benchmark name
func (cb *CustomBench) Info(ctx context.Context) (string, error) {
	return cb.driver.Info(ctx)
}
