package docker

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"net"
	"os/exec"
	"strconv"
	"strings"
)

// anxk: docker默认网桥名字和使用的主机端口范围。
const (
	networkBridgeIface = "lxcbr0"
	portRangeStart     = 49153
	portRangeEnd       = 65535
)

// Calculates the first and last IP addresses in an IPNet
func networkRange(network *net.IPNet) (net.IP, net.IP) {
	netIP := network.IP.To4()
	firstIP := netIP.Mask(network.Mask)
	lastIP := net.IPv4(0, 0, 0, 0).To4()
	for i := 0; i < len(lastIP); i++ {
		lastIP[i] = netIP[i] | ^network.Mask[i]
	}
	return firstIP, lastIP
}

// Converts a 4 bytes IP into a 32 bit integer
func ipToInt(ip net.IP) (int32, error) {
	buf := bytes.NewBuffer(ip.To4())
	var n int32
	if err := binary.Read(buf, binary.BigEndian, &n); err != nil {
		return 0, err
	}
	return n, nil
}

// Converts 32 bit integer into a 4 bytes IP address
func intToIp(n int32) (net.IP, error) {
	var buf bytes.Buffer
	if err := binary.Write(&buf, binary.BigEndian, &n); err != nil {
		return net.IP{}, err
	}
	ip := net.IPv4(0, 0, 0, 0).To4()
	for i := 0; i < net.IPv4len; i++ {
		ip[i] = buf.Bytes()[i]
	}
	return ip, nil
}

// Given a netmask, calculates the number of available hosts
func networkSize(mask net.IPMask) (int32, error) {
	m := net.IPv4Mask(0, 0, 0, 0)
	for i := 0; i < net.IPv4len; i++ {
		m[i] = ^mask[i]
	}
	buf := bytes.NewBuffer(m)
	var n int32
	if err := binary.Read(buf, binary.BigEndian, &n); err != nil {
		return 0, err
	}
	return n + 1, nil
}

// @anxk: 主机和容器间的端口映射通过iptables实现。
// Wrapper around the iptables command
func iptables(args ...string) error {
	if err := exec.Command("/sbin/iptables", args...).Run(); err != nil {
		return fmt.Errorf("iptables failed: iptables %v", strings.Join(args, " "))
	}
	return nil
}

// @anxk: 获取网络接口的ipv4地址，如果有多个地址，选择第一个。
// Return the IPv4 address of a network interface
func getIfaceAddr(name string) (net.Addr, error) {
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return nil, err
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return nil, err
	}
	var addrs4 []net.Addr
	for _, addr := range addrs {
		ip := (addr.(*net.IPNet)).IP
		if ip4 := ip.To4(); len(ip4) == net.IPv4len {
			addrs4 = append(addrs4, addr)
		}
	}
	switch {
	case len(addrs4) == 0:
		return nil, fmt.Errorf("Interface %v has no IP addresses", name)
	case len(addrs4) > 1:
		fmt.Printf("Interface %v has more than 1 IPv4 address. Defaulting to using %v\n",
			name, (addrs4[0].(*net.IPNet)).IP)
	}
	return addrs4[0], nil
}

// @anxk: 管理端口映射，存储端口映射关系。
// Port mapper takes care of mapping external ports to containers by setting
// up iptables rules.
// It keeps track of all mappings and is able to unmap at will
type PortMapper struct {
	mapping map[int]net.TCPAddr
}

// @anxk: 删除nat表的PREROUTING、OUTPUT链中DOCKER规则，清空DOCKER链中的规则，删除DOCKER自定义链。
func (mapper *PortMapper) cleanup() error {
	// Ignore errors - This could mean the chains were never set up
	iptables("-t", "nat", "-D", "PREROUTING", "-j", "DOCKER")
	iptables("-t", "nat", "-D", "OUTPUT", "-j", "DOCKER")
	iptables("-t", "nat", "-F", "DOCKER")
	iptables("-t", "nat", "-X", "DOCKER")
	mapper.mapping = make(map[int]net.TCPAddr)
	return nil
}

// @anxk: 建立DOCKER相关的iptables规则。
func (mapper *PortMapper) setup() error {
	// @anxk: 在nat表中新建自定义链DOCKER。
	if err := iptables("-t", "nat", "-N", "DOCKER"); err != nil {
		return errors.New("Unable to setup port networking: Failed to create DOCKER chain")
	}
	// @anxk: 在nat表的PREROUTING链追加规则DOCKER。
	if err := iptables("-t", "nat", "-A", "PREROUTING", "-j", "DOCKER"); err != nil {
		return errors.New("Unable to setup port networking: Failed to inject docker in PREROUTING chain")
	}
	// @anxk: 在nat表的OUTPUT链中追加规则DOCKER。
	if err := iptables("-t", "nat", "-A", "OUTPUT", "-j", "DOCKER"); err != nil {
		return errors.New("Unable to setup port networking: Failed to inject docker in OUTPUT chain")
	}
	return nil
}

// @anxk: 使用规则前插入或追加给DOCKER链添加TCP类型的NAT规则。
func (mapper *PortMapper) iptablesForward(rule string, port int, dest net.TCPAddr) error {
	return iptables("-t", "nat", rule, "DOCKER", "-p", "tcp", "--dport", strconv.Itoa(port),
		"-j", "DNAT", "--to-destination", net.JoinHostPort(dest.IP.String(), strconv.Itoa(dest.Port)))
}

// @anxk: 在DOCKER链中追加规则，将发往主机目的端口为port的tcp包转发到相应的TCP endpoint。
func (mapper *PortMapper) Map(port int, dest net.TCPAddr) error {
	if err := mapper.iptablesForward("-A", port, dest); err != nil {
		return err
	}
	mapper.mapping[port] = dest
	return nil
}

// @anxk: 根据主机端口删除指定的端口映射规则。
func (mapper *PortMapper) Unmap(port int) error {
	dest, ok := mapper.mapping[port]
	if !ok {
		return errors.New("Port is not mapped")
	}
	if err := mapper.iptablesForward("-D", port, dest); err != nil {
		return err
	}
	delete(mapper.mapping, port)
	return nil
}

func newPortMapper() (*PortMapper, error) {
	mapper := &PortMapper{}
	if err := mapper.cleanup(); err != nil {
		return nil, err
	}
	if err := mapper.setup(); err != nil {
		return nil, err
	}
	return mapper, nil
}

// @anxk: 相当于一个端口池子，使用通道来保存端口。
// Port allocator: Atomatically allocate and release networking ports
type PortAllocator struct {
	ports chan (int)
}

// @anxk: 向端口池里面存放端口。
func (alloc *PortAllocator) populate(start, end int) {
	alloc.ports = make(chan int, end-start)
	for port := start; port < end; port++ {
		alloc.ports <- port
	}
}

// @anxk: 从端口池中取出一个端口。
func (alloc *PortAllocator) Acquire() (int, error) {
	select {
	case port := <-alloc.ports:
		return port, nil
	default:
		return -1, errors.New("No more ports available")
	}
	return -1, nil
}

// @anxk: 释放、返还端口给端口池。
func (alloc *PortAllocator) Release(port int) error {
	select {
	case alloc.ports <- port:
		return nil
	default:
		return errors.New("Too many ports have been released")
	}
	return nil
}

func newPortAllocator(start, end int) (*PortAllocator, error) {
	allocator := &PortAllocator{}
	allocator.populate(start, end)
	return allocator, nil
}

// @anxk: IP地址池。
// IP allocator: Atomatically allocate and release networking ports
type IPAllocator struct {
	network *net.IPNet
	queue   chan (net.IP)
}

// @anxk: 根据对应网络地址向IP池中存放IP地址。
func (alloc *IPAllocator) populate() error {
	firstIP, _ := networkRange(alloc.network)
	size, err := networkSize(alloc.network.Mask)
	if err != nil {
		return err
	}
	// The queue size should be the network size - 3
	// -1 for the network address, -1 for the broadcast address and
	// -1 for the gateway address
	alloc.queue = make(chan net.IP, size-3)
	for i := int32(1); i < size-1; i++ {
		ipNum, err := ipToInt(firstIP)
		if err != nil {
			return err
		}
		ip, err := intToIp(ipNum + int32(i))
		if err != nil {
			return err
		}
		// Discard the network IP (that's the host IP address)
		if ip.Equal(alloc.network.IP) {
			continue
		}
		alloc.queue <- ip
	}
	return nil
}

// @anxk: 从IP池中获取一个IP。
func (alloc *IPAllocator) Acquire() (net.IP, error) {
	select {
	case ip := <-alloc.queue:
		return ip, nil
	default:
		return net.IP{}, errors.New("No more IP addresses available")
	}
	return net.IP{}, nil
}

// @anxk: 释放一个IP。
func (alloc *IPAllocator) Release(ip net.IP) error {
	select {
	case alloc.queue <- ip:
		return nil
	default:
		return errors.New("Too many IP addresses have been released")
	}
	return nil
}

func newIPAllocator(network *net.IPNet) (*IPAllocator, error) {
	alloc := &IPAllocator{
		network: network,
	}
	if err := alloc.populate(); err != nil {
		return nil, err
	}
	return alloc, nil
}

// @anxk: 表示容器中的网络栈。
// Network interface represents the networking stack of a container
type NetworkInterface struct {
	IPNet   net.IPNet
	Gateway net.IP

	manager  *NetworkManager
	extPorts []int
}

// @anxk: 获取一个主机端口并映射进容器。
// Allocate an external TCP port and map it to the interface
func (iface *NetworkInterface) AllocatePort(port int) (int, error) {
	extPort, err := iface.manager.portAllocator.Acquire()
	if err != nil {
		return -1, err
	}
	if err := iface.manager.portMapper.Map(extPort, net.TCPAddr{IP: iface.IPNet.IP, Port: port}); err != nil {
		iface.manager.portAllocator.Release(extPort)
		return -1, err
	}
	iface.extPorts = append(iface.extPorts, extPort)
	return extPort, nil
}

// @anxk: 释放容器的网络资源。
// Release: Network cleanup - release all resources
func (iface *NetworkInterface) Release() error {
	for _, port := range iface.extPorts {
		// @anxk: 删除对应得端口映射。
		if err := iface.manager.portMapper.Unmap(port); err != nil {
			log.Printf("Unable to unmap port %v: %v", port, err)
		}
		// @anxk: 释放对应的主机端口。
		if err := iface.manager.portAllocator.Release(port); err != nil {
			log.Printf("Unable to release port %v: %v", port, err)
		}

	}
	// @anxk: 释放IP地址。
	return iface.manager.ipAllocator.Release(iface.IPNet.IP)
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

// @anxk: 分配一个网络栈。
// Allocate a network interface
func (manager *NetworkManager) Allocate() (*NetworkInterface, error) {
	ip, err := manager.ipAllocator.Acquire()
	if err != nil {
		return nil, err
	}
	iface := &NetworkInterface{
		IPNet:   net.IPNet{IP: ip, Mask: manager.bridgeNetwork.Mask},
		Gateway: manager.bridgeNetwork.IP,
		manager: manager,
	}
	return iface, nil
}

func newNetworkManager(bridgeIface string) (*NetworkManager, error) {
	addr, err := getIfaceAddr(bridgeIface)
	if err != nil {
		return nil, err
	}
	network := addr.(*net.IPNet)

	ipAllocator, err := newIPAllocator(network)
	if err != nil {
		return nil, err
	}

	portAllocator, err := newPortAllocator(portRangeStart, portRangeEnd)
	if err != nil {
		return nil, err
	}

	portMapper, err := newPortMapper()

	manager := &NetworkManager{
		bridgeIface:   bridgeIface,
		bridgeNetwork: network,
		ipAllocator:   ipAllocator,
		portAllocator: portAllocator,
		portMapper:    portMapper,
	}
	return manager, nil
}
