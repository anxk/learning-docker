package docker

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path"
	"strconv"
	"syscall"
	"time"

	"github.com/kr/pty"
)

type Container struct {
	root string

	Id string

	Created time.Time

	Path string
	Args []string

	Config *Config
	State  State
	Image  string

	network         *NetworkInterface
	NetworkSettings *NetworkSettings

	SysInitPath string
	cmd         *exec.Cmd
	stdout      *writeBroadcaster
	stderr      *writeBroadcaster
	stdin       io.ReadCloser
	stdinPipe   io.WriteCloser

	stdoutLog *os.File
	stderrLog *os.File
	runtime   *Runtime
}

type Config struct {
	Hostname   string
	User       string
	Memory     int64 // Memory limit (in bytes)
	MemorySwap int64 // Total memory usage (memory + swap); set `-1' to disable swap
	Detach     bool
	Ports      []int
	Tty        bool // Attach standard streams to a tty, including stdin if it is not closed.
	OpenStdin  bool // Open stdin
	Env        []string
	Cmd        []string
	Image      string // Name of the image as it was passed by the operator (eg. could be symbolic)
}

// @anxk: 根据run参数列表生成容器config。
func ParseRun(args []string) (*Config, error) {
	cmd := flag.NewFlagSet("", flag.ContinueOnError)
	cmd.SetOutput(ioutil.Discard)
	fl_user := cmd.String("u", "", "Username or UID")
	fl_detach := cmd.Bool("d", false, "Detached mode: leave the container running in the background")
	fl_stdin := cmd.Bool("i", false, "Keep stdin open even if not attached")
	fl_tty := cmd.Bool("t", false, "Allocate a pseudo-tty")
	fl_memory := cmd.Int64("m", 0, "Memory limit (in bytes)")
	var fl_ports ports

	cmd.Var(&fl_ports, "p", "Map a network port to the container")
	var fl_env ListOpts
	cmd.Var(&fl_env, "e", "Set environment variables")
	if err := cmd.Parse(args); err != nil {
		return nil, err
	}
	config := &Config{
		Ports:     fl_ports,
		User:      *fl_user,
		Tty:       *fl_tty,
		OpenStdin: *fl_stdin,
		Memory:    *fl_memory,
		Detach:    *fl_detach,
		Env:       fl_env,
		Cmd:       cmd.Args()[1:],
		Image:     cmd.Arg(0),
	}
	return config, nil
}

// @anxk: 容器的网络配置包括ip、网络地址长度、网关和端口映射。
type NetworkSettings struct {
	IpAddress   string
	IpPrefixLen int
	Gateway     string
	PortMapping map[string]string
}

// @anxk: 返回容器的cmd。
func (container *Container) Cmd() *exec.Cmd {
	return container.cmd
}

// @anxk: 返回容器启动时间。
func (container *Container) When() time.Time {
	return container.Created
}

// @anxk: 从本地文件系统加载容器json。
func (container *Container) FromDisk() error {
	data, err := ioutil.ReadFile(container.jsonPath())
	if err != nil {
		return err
	}
	// Load container settings
	if err := json.Unmarshal(data, container); err != nil {
		return err
	}
	return nil
}

// @anxk: 存储容器json。
func (container *Container) ToDisk() (err error) {
	data, err := json.Marshal(container)
	if err != nil {
		return
	}
	return ioutil.WriteFile(container.jsonPath(), data, 0666)
}

// @anxk: 渲染lxc模板，并将生成的lxc配置文件存储在本地磁盘。
func (container *Container) generateLXCConfig() error {
	fo, err := os.Create(container.lxcConfigPath())
	if err != nil {
		return err
	}
	defer fo.Close()
	if err := LxcTemplateCompiled.Execute(fo, container); err != nil {
		return err
	}
	return nil
}

// @anxk: 设置容器的pty配置，然后启动容器内的进程。
/*
-------------       ---------
| Container |       |  cmd  |
+++++++++++++       +++++++++
|  stdin    | <-->  | stdin |
|  stdout   | <-->  | stdout|
|  stderr   | <-->  | stderr|
-------------       ---------
*/
/*
如果main函数不退出，则即使其中调用的函数start()退出，start中启动的go程也会继续执行直到main函数退出。
package main

import(
	"fmt"
	"time"
)

func main(){
	start()
	time.Sleep(time.Second * 11)
}

func start() {
	fmt.Println("start")
	go echo("hello")
	fmt.Println("start exit")
}

func echo(str string) {
	fmt.Println("echo start")
	endTime := time.Now().Add(time.Duration(10 * time.Second))
	for {
		fmt.Println("hello, now is %v", time.Now())
		time.Sleep(time.Second)
		if time.Now().After(endTime) {
			break
		}
	}
	fmt.Println("echo stop")
}
*/
func (container *Container) startPty() error {
	stdout_master, stdout_slave, err := pty.Open()
	if err != nil {
		return err
	}
	container.cmd.Stdout = stdout_slave

	stderr_master, stderr_slave, err := pty.Open()
	if err != nil {
		return err
	}
	container.cmd.Stderr = stderr_slave

	// Copy the PTYs to our broadcasters
	go func() {
		defer container.stdout.Close()
		io.Copy(container.stdout, stdout_master)
	}()

	go func() {
		defer container.stderr.Close()
		io.Copy(container.stderr, stderr_master)
	}()

	// stdin
	var stdin_slave io.ReadCloser
	if container.Config.OpenStdin {
		stdin_master, stdin_slave, err := pty.Open()
		if err != nil {
			return err
		}
		container.cmd.Stdin = stdin_slave
		// FIXME: The following appears to be broken.
		// "cannot set terminal process group (-1): Inappropriate ioctl for device"
		// container.cmd.SysProcAttr = &syscall.SysProcAttr{Setctty: true, Setsid: true}
		go func() {
			defer container.stdin.Close()
			io.Copy(stdin_master, container.stdin)
		}()
	}
	if err := container.cmd.Start(); err != nil {
		return err
	}
	stdout_slave.Close()
	stderr_slave.Close()
	if stdin_slave != nil {
		stdin_slave.Close()
	}
	return nil
}

// @anxk: 设置容器的标准输入、标准输出和标准错误输出，然后启动容器内的主进程。
func (container *Container) start() error {
	container.cmd.Stdout = container.stdout
	container.cmd.Stderr = container.stderr
	if container.Config.OpenStdin {
		stdin, err := container.cmd.StdinPipe()
		if err != nil {
			return err
		}
		go func() {
			defer stdin.Close()
			io.Copy(stdin, container.stdin)
		}()
	}
	return container.cmd.Start()
}

// @anxk: 准备资源，使用lxc-start启动容器，然后修改容器状态并持久化容器json到存储，最后
// 开启新的go程用于等待容器退出即退出后的一些列操作。
func (container *Container) Start() error {
	if err := container.EnsureMounted(); err != nil {
		return err
	}
	if err := container.allocateNetwork(); err != nil {
		return err
	}
	if err := container.generateLXCConfig(); err != nil {
		return err
	}
	params := []string{
		"-n", container.Id,
		"-f", container.lxcConfigPath(),
		"--",
		"/sbin/init",
	}

	// Networking
	params = append(params, "-g", container.network.Gateway.String())

	// User
	if container.Config.User != "" {
		params = append(params, "-u", container.Config.User)
	}

	// Program
	params = append(params, "--", container.Path)
	params = append(params, container.Args...)

	container.cmd = exec.Command("/usr/bin/lxc-start", params...)

	// Setup environment
	container.cmd.Env = append(
		[]string{
			"HOME=/",
			"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		},
		container.Config.Env...,
	)

	var err error
	if container.Config.Tty {
		err = container.startPty()
	} else {
		err = container.start()
	}
	if err != nil {
		return err
	}
	// FIXME: save state on disk *first*, then converge
	// this way disk state is used as a journal, eg. we can restore after crash etc.
	container.State.setRunning(container.cmd.Process.Pid)
	container.ToDisk()
	go container.monitor()
	return nil
}

// @anxk: 启动容器并等待其退出。
func (container *Container) Run() error {
	if err := container.Start(); err != nil {
		return err
	}
	container.Wait()
	return nil
}

// @anxk: 启动容器，并返回容器的输出，直到容器退出。
func (container *Container) Output() (output []byte, err error) {
	pipe, err := container.StdoutPipe()
	if err != nil {
		return nil, err
	}
	defer pipe.Close()
	if err := container.Start(); err != nil {
		return nil, err
	}
	output, err = ioutil.ReadAll(pipe)
	container.Wait()
	return output, err
}

// @anxk: 返回连接到容器标准输入的pipe。
// StdinPipe() returns a pipe connected to the standard input of the container's
// active process.
//
func (container *Container) StdinPipe() (io.WriteCloser, error) {
	return container.stdinPipe, nil
}

// @anxk: 返回连接到容器标准输出的pipe。
func (container *Container) StdoutPipe() (io.ReadCloser, error) {
	reader, writer := io.Pipe()
	container.stdout.AddWriter(writer)
	return newBufReader(reader), nil
}

// @anxk: 返回连接到容器标准错误输出的pipe。
func (container *Container) StderrPipe() (io.ReadCloser, error) {
	reader, writer := io.Pipe()
	container.stderr.AddWriter(writer)
	return newBufReader(reader), nil
}

// @anxk: 设置容器的网络资源。
func (container *Container) allocateNetwork() error {
	iface, err := container.runtime.networkManager.Allocate()
	if err != nil {
		return err
	}
	container.NetworkSettings.PortMapping = make(map[string]string)
	for _, port := range container.Config.Ports {
		if extPort, err := iface.AllocatePort(port); err != nil {
			iface.Release()
			return err
		} else {
			container.NetworkSettings.PortMapping[strconv.Itoa(port)] = strconv.Itoa(extPort)
		}
	}
	container.network = iface
	container.NetworkSettings.IpAddress = iface.IPNet.IP.String()
	container.NetworkSettings.IpPrefixLen, _ = iface.IPNet.Mask.Size()
	container.NetworkSettings.Gateway = iface.Gateway.String()
	return nil
}

// @anxk: 释放容器的网络资源。
func (container *Container) releaseNetwork() error {
	err := container.network.Release()
	container.network = nil
	container.NetworkSettings = &NetworkSettings{}
	return err
}

// @anxk: 等待容器内主进程退出，然后释放网络资源、关闭容器的标准输出和标准错误输出、卸载
// 根文件系统、更新容器状态、最后持久化到本地的容器存储（/var/lib/docker/containers/<容器ID>/<config.json>）
func (container *Container) monitor() {
	// Wait for the program to exit
	container.cmd.Wait()
	exitCode := container.cmd.ProcessState.Sys().(syscall.WaitStatus).ExitStatus()

	// Cleanup
	if err := container.releaseNetwork(); err != nil {
		log.Printf("%v: Failed to release network: %v", container.Id, err)
	}
	container.stdout.Close()
	container.stderr.Close()
	if err := container.Unmount(); err != nil {
		log.Printf("%v: Failed to umount filesystem: %v", container.Id, err)
	}

	// @anxk: 这一步为什么？
	// Re-create a brand new stdin pipe once the container exited
	if container.Config.OpenStdin {
		container.stdin, container.stdinPipe = io.Pipe()
	}

	// Report status back
	container.State.setStopped(exitCode)
	container.ToDisk()
}

// @anxk: 调用os.Process.Kill()杀掉容器内主进程。
func (container *Container) kill() error {
	if err := container.cmd.Process.Kill(); err != nil {
		return err
	}
	// Wait for the container to be actually stopped
	container.Wait()
	return nil
}

// @anxk: 杀掉容器内主进程。
func (container *Container) Kill() error {
	if !container.State.Running {
		return nil
	}
	return container.kill()
}

// @anxk: 停止容器，通过两步（1）调用lxc-kill发送SIGTERM停止lxc容器，如果失败则杀掉容器主进程。
// （2）等待10秒钟，如果容器仍未停止，则再杀掉容器主进程一次。
func (container *Container) Stop() error {
	if !container.State.Running {
		return nil
	}

	// 1. Send a SIGTERM
	if output, err := exec.Command("/usr/bin/lxc-kill", "-n", container.Id, "15").CombinedOutput(); err != nil {
		log.Printf(string(output))
		log.Printf("Failed to send SIGTERM to the process, force killing")
		if err := container.Kill(); err != nil {
			return err
		}
	}

	// 2. Wait for the process to exit on its own
	if err := container.WaitTimeout(10 * time.Second); err != nil {
		log.Printf("Container %v failed to exit within 10 seconds of SIGTERM - using the force", container.Id)
		if err := container.Kill(); err != nil {
			return err
		}
	}
	return nil
}

// @anxk: 重启容器。
func (container *Container) Restart() error {
	if err := container.Stop(); err != nil {
		return err
	}
	if err := container.Start(); err != nil {
		return err
	}
	return nil
}

// @anxk: 阻塞直到容器状态变为停止，返回退出码。
// Wait blocks until the container stops running, then returns its exit code.
func (container *Container) Wait() int {

	for container.State.Running {
		container.State.wait()
	}
	return container.State.ExitCode
}

// @anxk: 返回容器读写层的tarball的io.Reader。
func (container *Container) ExportRw() (Archive, error) {
	return Tar(container.rwPath(), Uncompressed)
}

// @anxk: 返回容器整个根文件系统的tarball的io.Reader。
func (container *Container) Export() (Archive, error) {
	if err := container.EnsureMounted(); err != nil {
		return nil, err
	}
	return Tar(container.RootfsPath(), Uncompressed)
}

// @anxk: 设置容器的超时。
func (container *Container) WaitTimeout(timeout time.Duration) error {
	done := make(chan bool)
	go func() {
		container.Wait()
		done <- true
	}()

	select {
	case <-time.After(timeout):
		return errors.New("Timed Out")
	case <-done:
		return nil
	}
	return nil
}

// @anxk: 检测容器是否挂载根文件系，否则挂载。
func (container *Container) EnsureMounted() error {
	if mounted, err := container.Mounted(); err != nil {
		return err
	} else if mounted {
		return nil
	}
	return container.Mount()
}

// @anxk: 挂载容器的根文件系统。
func (container *Container) Mount() error {
	image, err := container.GetImage()
	if err != nil {
		return err
	}
	return image.Mount(container.RootfsPath(), container.rwPath())
}

// @anxk: 获取容器读写层的变更。
func (container *Container) Changes() ([]Change, error) {
	image, err := container.GetImage()
	if err != nil {
		return nil, err
	}
	return image.Changes(container.rwPath())
}

// @anxk: 获取容器对应的镜像（json）。runtime一边和容器交互另一边和镜像交互。
func (container *Container) GetImage() (*Image, error) {
	if container.runtime == nil {
		return nil, fmt.Errorf("Can't get image of unregistered container")
	}
	return container.runtime.graph.Get(container.Image)
}

// @anxk: 检测容器的根文件系统是否已经挂载。
func (container *Container) Mounted() (bool, error) {
	return Mounted(container.RootfsPath())
}

// @anxk: 卸载容器根文件系统。
func (container *Container) Unmount() error {
	return Unmount(container.RootfsPath())
}

// @anxk: 容器log文件的路径，即/var/lib/docker/containers/<容器ID>/<容器ID-stdout/stderr.log>。
func (container *Container) logPath(name string) string {
	return path.Join(container.root, fmt.Sprintf("%s-%s.log", container.Id, name))
}

// @anxk: 返回容器log的io.Reader。
func (container *Container) ReadLog(name string) (io.Reader, error) {
	return os.Open(container.logPath(name))
}

// @anxk: 容器json的路径，即/var/lib/docker/containers/<容器ID>/config.json。
func (container *Container) jsonPath() string {
	return path.Join(container.root, "config.json")
}

// @anxk: 容器对应的lxc配置文件的路径，即/var/lib/docker/containers/<容器ID>/config.lxc。
func (container *Container) lxcConfigPath() string {
	return path.Join(container.root, "config.lxc")
}

// @anxk: 容器的根目录所在的路径，即/var/lib/docker/containers/<容器ID>/rootfs。
// This method must be exported to be used from the lxc template
func (container *Container) RootfsPath() string {
	return path.Join(container.root, "rootfs")
}

// @anxk: 容器读写层的路径，即/var/lib/docker/containers/<容器ID>/rw。
func (container *Container) rwPath() string {
	return path.Join(container.root, "rw")
}

// @anxk: 校验容器ID是否为空。
func validateId(id string) error {
	if id == "" {
		return fmt.Errorf("Invalid empty id")
	}
	return nil
}
