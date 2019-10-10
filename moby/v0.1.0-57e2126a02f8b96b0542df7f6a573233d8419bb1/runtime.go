package docker

import (
	"container/list"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path"
	"sort"
	"sync"
	"time"

	"github.com/dotcloud/docker/auth"
)

// @anxk: 运行时是docker服务的主体，包括了镜像存储后端、容器存储、镜像仓库和标签存储、认证信息、网络和容器列表。
// root是运行时的根路径，即/var/lib/docker。
// repository是容器的根路径，即/var/lib/docker/containers，其中每个文件夹的路径形式是
// ./<容器ID>，他们是对于容器的根目录（挂载点）。
// graph是镜像的存储后端，即/var/lib/docker/graph。
// repositories是镜像标签和仓库的存储路径，即/var/lib/docker/repositories。
type Runtime struct {
	root           string
	repository     string
	containers     *list.List
	networkManager *NetworkManager
	graph          *Graph
	repositories   *TagStore
	authConfig     *auth.AuthConfig
}

// @anxk: sysInitPath是docker二进制的路径。
var sysInitPath string

// @anxk: 在main函数之前初始化docker二进制的路径。
func init() {
	sysInitPath = SelfPath()
}

// @anxk: 生成运行时中注册的容器列表。
func (runtime *Runtime) List() []*Container {
	containers := new(History)
	for e := runtime.containers.Front(); e != nil; e = e.Next() {
		containers.Add(e.Value.(*Container))
	}
	return *containers
}

// @anxk: 根据容器ID获取指定容器。
func (runtime *Runtime) getContainerElement(id string) *list.Element {
	for e := runtime.containers.Front(); e != nil; e = e.Next() {
		container := e.Value.(*Container)
		if container.Id == id {
			return e
		}
	}
	return nil
}

// @anxk: 根据容器ID获取指定容器。
func (runtime *Runtime) Get(id string) *Container {
	e := runtime.getContainerElement(id)
	if e == nil {
		return nil
	}
	return e.Value.(*Container)
}

// @anxk: 检测指定容器ID的容器是否存在。
func (runtime *Runtime) Exists(id string) bool {
	return runtime.Get(id) != nil
}

// @anxk: 某一容器的根目录（挂载点），即/var/lib/docker/containers/<容器ID>
func (runtime *Runtime) containerRoot(id string) string {
	return path.Join(runtime.repository, id)
}

// @anxk: 创建一个新的容器，包括存储容器json和注册到运行时。
func (runtime *Runtime) Create(config *Config) (*Container, error) {
	// Lookup image
	img, err := runtime.repositories.LookupImage(config.Image)
	if err != nil {
		return nil, err
	}
	container := &Container{
		// FIXME: we should generate the ID here instead of receiving it as an argument
		Id:              GenerateId(),
		Created:         time.Now(),
		Path:            config.Cmd[0],
		Args:            config.Cmd[1:], //FIXME: de-duplicate from config
		Config:          config,
		Image:           img.Id, // Always use the resolved image id
		NetworkSettings: &NetworkSettings{},
		// FIXME: do we need to store this in the container?
		SysInitPath: sysInitPath,
	}
	container.root = runtime.containerRoot(container.Id)
	// Step 1: create the container directory.
	// This doubles as a barrier to avoid race conditions.
	if err := os.Mkdir(container.root, 0700); err != nil {
		return nil, err
	}
	// Step 2: save the container json
	if err := container.ToDisk(); err != nil {
		return nil, err
	}
	// Step 3: register the container
	if err := runtime.Register(container); err != nil {
		return nil, err
	}
	return container, nil
}

// @anxk: 从本地加载一个容器并注册到运行时。
func (runtime *Runtime) Load(id string) (*Container, error) {
	container := &Container{root: runtime.containerRoot(id)}
	if err := container.FromDisk(); err != nil {
		return nil, err
	}
	if container.Id != id {
		return container, fmt.Errorf("Container %s is stored at %s", container.Id, id)
	}
	if err := runtime.Register(container); err != nil {
		return nil, err
	}
	return container, nil
}

// Register makes a container object usable by the runtime as <container.Id>
func (runtime *Runtime) Register(container *Container) error {
	if container.runtime != nil || runtime.Exists(container.Id) {
		return fmt.Errorf("Container is already loaded")
	}
	if err := validateId(container.Id); err != nil {
		return err
	}
	container.runtime = runtime
	// Setup state lock (formerly in newState()
	lock := new(sync.Mutex)
	container.State.stateChangeLock = lock
	container.State.stateChangeCond = sync.NewCond(lock)
	// Attach to stdout and stderr
	container.stderr = newWriteBroadcaster()
	container.stdout = newWriteBroadcaster()
	// Attach to stdin
	if container.Config.OpenStdin {
		container.stdin, container.stdinPipe = io.Pipe()
	} else {
		container.stdinPipe = NopWriteCloser(ioutil.Discard) // Silently drop stdin
	}
	// Setup logging of stdout and stderr to disk
	if err := runtime.LogToDisk(container.stdout, container.logPath("stdout")); err != nil {
		return err
	}
	if err := runtime.LogToDisk(container.stderr, container.logPath("stderr")); err != nil {
		return err
	}
	// done
	runtime.containers.PushBack(container)
	return nil
}

func (runtime *Runtime) LogToDisk(src *writeBroadcaster, dst string) error {
	log, err := os.OpenFile(dst, os.O_RDWR|os.O_APPEND|os.O_CREATE, 0600)
	if err != nil {
		return err
	}
	src.AddWriter(NopWriteCloser(log))
	return nil
}

func (runtime *Runtime) Destroy(container *Container) error {
	element := runtime.getContainerElement(container.Id)
	if element == nil {
		return fmt.Errorf("Container %v not found - maybe it was already destroyed?", container.Id)
	}

	if err := container.Stop(); err != nil {
		return err
	}
	if mounted, err := container.Mounted(); err != nil {
		return err
	} else if mounted {
		if err := container.Unmount(); err != nil {
			return fmt.Errorf("Unable to unmount container %v: %v", container.Id, err)
		}
	}
	// Deregister the container before removing its directory, to avoid race conditions
	runtime.containers.Remove(element)
	if err := os.RemoveAll(container.root); err != nil {
		return fmt.Errorf("Unable to remove filesystem for %v: %v", container.Id, err)
	}
	return nil
}

// Commit creates a new filesystem image from the current state of a container.
// The image can optionally be tagged into a repository
func (runtime *Runtime) Commit(id, repository, tag string) (*Image, error) {
	container := runtime.Get(id)
	if container == nil {
		return nil, fmt.Errorf("No such container: %s", id)
	}
	// FIXME: freeze the container before copying it to avoid data corruption?
	// FIXME: this shouldn't be in commands.
	rwTar, err := container.ExportRw()
	if err != nil {
		return nil, err
	}
	// Create a new image from the container's base layers + a new layer from container changes
	img, err := runtime.graph.Create(rwTar, container, "")
	if err != nil {
		return nil, err
	}
	// Register the image if needed
	if repository != "" {
		if err := runtime.repositories.Set(repository, tag, img.Id, true); err != nil {
			return img, err
		}
	}
	return img, nil
}

func (runtime *Runtime) restore() error {
	dir, err := ioutil.ReadDir(runtime.repository)
	if err != nil {
		return err
	}
	for _, v := range dir {
		id := v.Name()
		container, err := runtime.Load(id)
		if err != nil {
			Debugf("Failed to load container %v: %v", id, err)
			continue
		}
		Debugf("Loaded container %v", container.Id)
	}
	return nil
}

// @anxk: 创建一个运行时。
func NewRuntime() (*Runtime, error) {
	return NewRuntimeFromDirectory("/var/lib/docker")
}

// @anxk: 基于本地创建一个运行时。
func NewRuntimeFromDirectory(root string) (*Runtime, error) {
	runtime_repo := path.Join(root, "containers")

	if err := os.MkdirAll(runtime_repo, 0700); err != nil && !os.IsExist(err) {
		return nil, err
	}

	g, err := NewGraph(path.Join(root, "graph"))
	if err != nil {
		return nil, err
	}
	repositories, err := NewTagStore(path.Join(root, "repositories"), g)
	if err != nil {
		return nil, fmt.Errorf("Couldn't create Tag store: %s", err)
	}
	netManager, err := newNetworkManager(networkBridgeIface)
	if err != nil {
		return nil, err
	}
	authConfig, err := auth.LoadConfig(root)
	if err != nil && authConfig == nil {
		// If the auth file does not exist, keep going
		return nil, err
	}

	runtime := &Runtime{
		root:           root,
		repository:     runtime_repo,
		containers:     list.New(),
		networkManager: netManager,
		graph:          g,
		repositories:   repositories,
		authConfig:     authConfig,
	}

	if err := runtime.restore(); err != nil {
		return nil, err
	}
	return runtime, nil
}

// @anxk: History是一个容器列表，表示本地启动过的容器。其实现了sort.Interface的方法Len、
// Less、Swap，以便对其根据启动时间进行排序。
type History []*Container

func (history *History) Len() int {
	return len(*history)
}

func (history *History) Less(i, j int) bool {
	containers := *history
	return containers[j].When().Before(containers[i].When())
}

func (history *History) Swap(i, j int) {
	containers := *history
	tmp := containers[i]
	containers[i] = containers[j]
	containers[j] = tmp
}

// @anxk: 向history中添加一个新的容器，并排序。
func (history *History) Add(container *Container) {
	*history = append(*history, container)
	sort.Sort(history)
}
