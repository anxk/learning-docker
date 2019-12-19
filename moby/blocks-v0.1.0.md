# blocks-v0.1.0

### github.com/dotcloud/docker

```go
type Archive io.Reader

type Compression uint32

const (
    Uncompressed Compression = iota
    Bzip2
    Gzip
)
type ChangeType int

const (
    ChangeModify = iota
    ChangeAdd
    ChangeDelete
)

type Change struct {
    Path string
    Kind ChangeType
}
// Ports type - Used to parse multiple -p flags
type ports []int
// ListOpts type
type ListOpts []string
type Server struct {
    runtime *Runtime
}
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
type NetworkSettings struct {
    IpAddress   string
    IpPrefixLen int
    Gateway     string
    PortMapping map[string]string
}
// @anxk: Graph表示的是镜像（json和layer）的存储后端，并提供相关的操作。
type Graph struct {
    // @anxk: Root 在 runtime.go 中被设置为 "/var/lib/docker/graph"，是存放所有本地镜像（json和layer）的路径。
    Root string
}
// @anxk: Image即镜像的json表示，包括镜像ID、父镜像ID、注解、创建时间、创建该镜像的容器ID和容器json、镜像的后端存储。
type Image struct {
    Id              string    `json:"id"`
    Parent          string    `json:"parent,omitempty"`
    Comment         string    `json:"comment,omitempty"`
    Created         time.Time `json:"created"`
    Container       string    `json:"container,omitempty"`
    ContainerConfig Config    `json:"container_config,omitempty"`
    // 在镜像中放*Graph的原因在于graph是存储后端，需要通过Image操作镜像的内容（json或layer）。
    graph *Graph
}
var LxcTemplateCompiled *template.Template
// anxk: docker默认网桥名字和使用的主机端口范围。
const (
    networkBridgeIface = "lxcbr0"
    portRangeStart     = 49153
    portRangeEnd       = 65535
)
// @anxk: 管理端口映射，存储端口映射关系。
// Port mapper takes care of mapping external ports to containers by setting
// up iptables rules.
// It keeps track of all mappings and is able to unmap at will
type PortMapper struct {
    mapping map[int]net.TCPAddr
}
// @anxk: 相当于一个端口池子，使用通道来保存端口。
// Port allocator: Atomatically allocate and release networking ports
type PortAllocator struct {
    ports chan (int)
}
// @anxk: IP地址池。
// IP allocator: Atomatically allocate and release networking ports
type IPAllocator struct {
    network *net.IPNet
    queue   chan (net.IP)
}
// @anxk: 表示容器中的网络栈。
// Network interface represents the networking stack of a container
type NetworkInterface struct {
    IPNet   net.IPNet
    Gateway net.IP

    manager  *NetworkManager
    extPorts []int
}
// @anxk: 管理docker下辖的网络。
// Network Manager manages a set of network interfaces
// Only *one* manager per host machine should be used
type NetworkManager struct {
    bridgeIface   string
    bridgeNetwork *net.IPNet

    ipAllocator   *IPAllocator
    portAllocator *PortAllocator
    portMapper    *PortMapper
}
//FIXME: Set the endpoint in a conf file or via commandline
//const REGISTRY_ENDPOINT = "http://registry-creack.dotcloud.com/v1"
const REGISTRY_ENDPOINT = auth.REGISTRY_SERVER + "/v1"
// @anxk: 运行时是docker服务的主体，包括了镜像存储后端、容器存储、镜像仓库和标签存储、认证信息、网络和容器列表。
// root是运行时的根路径，即/var/lib/docker。
// repository是容器的根路径，即/var/lib/docker/containers，其中每个文件夹的路径形式是./<容器ID>。
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
// @anxk: History是一个容器列表，其实现了sort.Interface的方法Len、
// Less、Swap，以便对其根据启动时间进行排序。
type History []*Container
// @anxk: State表示容器的当前状态。
type State struct {
    Running   bool
    Pid       int
    ExitCode  int
    StartedAt time.Time

    stateChangeLock *sync.Mutex
    stateChangeCond *sync.Cond
}
const DEFAULT_TAG = "latest"

// @anxk: TagStore表示镜像名（repoName:tag）和镜像ID之间的映射关系。
type TagStore struct {

    // @anxk: path为/var/lib/docker/repositories，这是一个regular文件。
    path  string
    graph *Graph

    // @anxk: Repositories的形式为{<repoName>: {<tag>: <镜像ID>}}，例如：现在本地有
    // centos和alpine两个repository的一些镜像，那么，此时Repositories的json应该类似于
    // {
    //     "centos": {"v1": "coerf38gj9jf...", {"v2": "fhq874fjiwef..."}},
    //     "alpine": {"v2": "f34rf34rf34r...", {"v3": "4r34r34r4rgf..."}
    // }。
    Repositories map[string]Repository
}

// @anxk: 表示一个形式为{<tag>: <镜像ID>}的镜像条目。
type Repository map[string]string
// @anxk: 带进度条的Reader。
// Reader with progress bar
type progressReader struct {
    reader        io.ReadCloser // Stream to read from
    output        io.Writer     // Where to send progress bar to
    read_total    int           // Expected stream length (bytes)
    read_progress int           // How much has been read so far (bytes)
    last_update   int           // How many bytes read at least update
}
// @anxk: 定义了一个类似于io/ioutil的func NopCloser(r io.Reader) io.ReadCloser。
type nopWriteCloser struct {
    io.Writer
}
type bufReader struct {
    buf    *bytes.Buffer
    reader io.Reader
    err    error
    l      sync.Mutex
    wait   sync.Cond
}
// @anxk: 实现一个组Writer。
type writeBroadcaster struct {
    writers *list.List
}
```

### github.com/dotcloud/docker/auth

```go
// Where we store the config file
const CONFIGFILE = ".dockercfg"

// the registry server we want to login against
const REGISTRY_SERVER = "https://registry.docker.io"

type AuthConfig struct {
    Username string `json:"username"`
    Password string `json:"password"`
    Email    string `json:"email"`
    rootPath string `json:-`
}
```


### github.com/dotcloud/docker/rcli

```go
type Service interface {
    Name() string
    Help() string
}

type Cmd func(io.ReadCloser, io.Writer, ...string) error
type CmdMethod func(Service, io.ReadCloser, io.Writer, ...string) error

// Use this key to encode an RPC call into an URL,
// eg. domain.tld/path/to/method?q=get_user&q=gordon
const ARG_URL_KEY = "q"

type AutoFlush struct {
    http.ResponseWriter
}
// Note: the globals are here to avoid import cycle
// FIXME: Handle debug levels mode?
var DEBUG_FLAG bool = false
var CLIENT_SOCKET io.Writer = nil
```
