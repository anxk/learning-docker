package docker

import (
	"fmt"
	"io/ioutil"
	"os"
	"path"
	"path/filepath"
	"time"
)

// @anxk: Graph表示的是一个镜像（json和layer）的存储后端，并提供相关的操作。
type Graph struct {
	// @anxk: Root 在 runtime.go 中被设置为 "/var/lib/docker/graph"，是存放所有本地镜像（json和layer）的路径。
	Root string
}

// @anxk: 在本地文件系统新建一个graph，如果路径不存在则新建，否则不建。
func NewGraph(root string) (*Graph, error) {
	abspath, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	// Create the root directory if it doesn't exists
	if err := os.Mkdir(root, 0700); err != nil && !os.IsExist(err) {
		return nil, err
	}
	return &Graph{
		Root: abspath,
	}, nil
}

// @anxk: 以镜像ID检测指定镜像是否存在于本地文件系统上。
func (graph *Graph) Exists(id string) bool {
	if _, err := graph.Get(id); err != nil {
		return false
	}
	return true
}

// @anxk: 根据镜像ID读取镜像（json）数据，并检测与json数据中的镜像ID是否一致。最后设置镜像json数据中的graph字段。
func (graph *Graph) Get(id string) (*Image, error) {
	// FIXME: return nil when the image doesn't exist, instead of an error
	img, err := LoadImage(graph.imageRoot(id))
	if err != nil {
		return nil, err
	}
	if img.Id != id {
		return nil, fmt.Errorf("Image stored at '%s' has wrong id '%s'", id, img.Id)
	}
	img.graph = graph
	return img, nil
}

// @anxk: 基于容器新建一个镜像，这里需要的镜像层数据就是该容器的读写层。
func (graph *Graph) Create(layerData Archive, container *Container, comment string) (*Image, error) {
	img := &Image{
		Id:      GenerateId(),
		Comment: comment,
		Created: time.Now(),
	}
	if container != nil {
		img.Parent = container.Image
		img.Container = container.Id
		img.ContainerConfig = *container.Config
	}
	if err := graph.Register(layerData, img); err != nil {
		return nil, err
	}
	return img, nil
}

// @anxk: Register将镜像（json和layer）存储在本地文件系统上，通过两步（1）创建临时graph存放镜像（2）rename
// 来避免竞争条件。最后设置镜像json的graph字段。
// Register注册的意义在于，建立镜像与graph的联系，将graph放入Image对象中使通过镜像（json）就能操作其数据（json和layer）。
func (graph *Graph) Register(layerData Archive, img *Image) error {
	if err := ValidateId(img.Id); err != nil {
		return err
	}
	// (This is a convenience to save time. Race conditions are taken care of by os.Rename)
	if graph.Exists(img.Id) {
		return fmt.Errorf("Image %s already exists", img.Id)
	}
	tmp, err := graph.Mktemp(img.Id)
	defer os.RemoveAll(tmp)
	if err != nil {
		return fmt.Errorf("Mktemp failed: %s", err)
	}
	if err := StoreImage(img, layerData, tmp); err != nil {
		return err
	}
	// Commit
	if err := os.Rename(tmp, graph.imageRoot(img.Id)); err != nil {
		return err
	}
	img.graph = graph
	return nil
}

// @anxk: 创建一个临时graph（/var/lib/docker/graph/:tmp:/），返回某个镜像（json和layer）的存储路径，即，
// /var/lib/docker/graph/:tmp:/<镜像ID>。
func (graph *Graph) Mktemp(id string) (string, error) {
	tmp, err := NewGraph(path.Join(graph.Root, ":tmp:"))
	if err != nil {
		return "", fmt.Errorf("Couldn't create temp: %s", err)
	}
	if tmp.Exists(id) {
		return "", fmt.Errorf("Image %d already exists", id)
	}
	return tmp.imageRoot(id), nil
}

// @anxk: 创建一个新的graph（/var/lib/docker/graph/:garbage:）用于镜像的垃圾回收，相当于windows系统的回收站。
func (graph *Graph) Garbage() (*Graph, error) {
	return NewGraph(path.Join(graph.Root, ":garbage:"))
}

// @anxk: Delete并不是真的从本地文件系统删除了指定的镜像数据，而是将其放入“回收站”。
func (graph *Graph) Delete(id string) error {
	garbage, err := graph.Garbage()
	if err != nil {
		return err
	}
	return os.Rename(graph.imageRoot(id), garbage.imageRoot(id))
}

// @anxk: Undelete是Delete的相反操作。
func (graph *Graph) Undelete(id string) error {
	garbage, err := graph.Garbage()
	if err != nil {
		return err
	}
	return os.Rename(garbage.imageRoot(id), graph.imageRoot(id))
}

// @anxk: GarbageCollect真正从本地文件系统删除了已经放入“回收站”的镜像。
func (graph *Graph) GarbageCollect() error {
	garbage, err := graph.Garbage()
	if err != nil {
		return err
	}
	return os.RemoveAll(garbage.Root)
}

// @anxk: 返回本地的镜像（json）Map，镜像ID作为键。
func (graph *Graph) Map() (map[string]*Image, error) {
	// FIXME: this should replace All()
	all, err := graph.All()
	if err != nil {
		return nil, err
	}
	images := make(map[string]*Image, len(all))
	for _, image := range all {
		images[image.Id] = image
	}
	return images, nil
}

// @anxk: 返回本地的镜像（json）列表。
func (graph *Graph) All() ([]*Image, error) {
	var images []*Image
	err := graph.WalkAll(func(image *Image) {
		images = append(images, image)
	})
	return images, err
}

// @anxk: 遍历本地镜像，根据提供的handler函数对镜像（json）进行相应的操作。
func (graph *Graph) WalkAll(handler func(*Image)) error {
	files, err := ioutil.ReadDir(graph.Root)
	if err != nil {
		return err
	}
	for _, st := range files {
		if img, err := graph.Get(st.Name()); err != nil {
			// Skip image
			continue
		} else if handler != nil {
			handler(img)
		}
	}
	return nil
}

// @anxk: 以镜像的父镜像ID为键，子镜像组成的列表为值，创建一个Map。注意这个Map中不包含没有父镜像的镜像。
func (graph *Graph) ByParent() (map[string][]*Image, error) {
	byParent := make(map[string][]*Image)
	err := graph.WalkAll(func(image *Image) {
		image, err := graph.Get(image.Parent)
		if err != nil {
			return
		}
		// @anxk: 下面这个分支判断是写反了吧？
		if children, exists := byParent[image.Parent]; exists {
			byParent[image.Parent] = []*Image{image}
		} else {
			byParent[image.Parent] = append(children, image)
		}
	})
	return byParent, err
}

// @anxk: Heads返回一个没有父镜像的Map，镜像ID为键。
func (graph *Graph) Heads() (map[string]*Image, error) {
	heads := make(map[string]*Image)
	byParent, err := graph.ByParent()
	if err != nil {
		return nil, err
	}
	err = graph.WalkAll(func(image *Image) {
		// If it's not in the byParent lookup table, then
		// it's not a parent -> so it's a head!
		if _, exists := byParent[image.Id]; !exists {
			heads[image.Id] = image
		}
	})
	return heads, err
}

// @anxk: 某个镜像（json和layer）的存储路径，即/var/lib/docker/graph/<镜像ID>，注意这个镜像ID是随机生成的。
func (graph *Graph) imageRoot(id string) string {
	return path.Join(graph.Root, id)
}
