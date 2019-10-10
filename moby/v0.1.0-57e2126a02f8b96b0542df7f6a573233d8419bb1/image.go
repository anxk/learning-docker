package docker

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"math/rand"
	"os"
	"path"
	"strings"
	"time"
)

// @anxk: Image即镜像的json表示，包括镜像ID、父镜像、注解、创建时间、创建该镜像的容器ID和容器json、镜像的后端存储。
type Image struct {
	Id              string    `json:"id"`
	Parent          string    `json:"parent,omitempty"`
	Comment         string    `json:"comment,omitempty"`
	Created         time.Time `json:"created"`
	Container       string    `json:"container,omitempty"`
	ContainerConfig Config    `json:"container_config,omitempty"`
	graph           *Graph
}

// @anxk: 从本地文件系统读取某一镜像（json）数据，并检查对应的layer是否存在以及是否是文件夹。
func LoadImage(root string) (*Image, error) {
	// Load the json data
	jsonData, err := ioutil.ReadFile(jsonPath(root))
	if err != nil {
		return nil, err
	}
	var img Image
	if err := json.Unmarshal(jsonData, &img); err != nil {
		return nil, err
	}
	if err := ValidateId(img.Id); err != nil {
		return nil, err
	}
	// Check that the filesystem layer exists
	if stat, err := os.Stat(layerPath(root)); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("Couldn't load image %s: no filesystem layer", img.Id)
		} else {
			return nil, err
		}
	} else if !stat.IsDir() {
		return nil, fmt.Errorf("Couldn't load image %s: %s is not a directory", img.Id, layerPath(root))
	}
	return &img, nil
}

// @anxk: 存储一个镜像（json和layer）在graph的对应位置，注意layer tarball是直接解压后
// 放进/var/lib/docker/<镜像ID>/layer/文件夹下的。
func StoreImage(img *Image, layerData Archive, root string) error {
	// Check that root doesn't already exist
	if _, err := os.Stat(root); err == nil {
		return fmt.Errorf("Image %s already exists", img.Id)
	} else if !os.IsNotExist(err) {
		return err
	}
	// Store the layer
	layer := layerPath(root)
	if err := os.MkdirAll(layer, 0700); err != nil {
		return err
	}
	if err := Untar(layerData, layer); err != nil {
		return err
	}
	// Store the json ball
	jsonData, err := json.Marshal(img)
	if err != nil {
		return err
	}
	if err := ioutil.WriteFile(jsonPath(root), jsonData, 0600); err != nil {
		return err
	}
	return nil
}

// @anxk: 存放镜像（layer）的文件夹，路径即/var/lib/docker/<镜像ID>/layer。
func layerPath(root string) string {
	return path.Join(root, "layer")
}

// @anxk: 存放镜像（json）的文件路径，注意这个文件是一个regular文件，路径是/var/lib/docker/<镜像ID>/json。
func jsonPath(root string) string {
	return path.Join(root, "json")
}

// @anxk: 在该版本docker（v0.1.0）中使用的文件系统是AUFS，MountAUFS根据提供的镜像层路
// 径和读写层路径将其联合挂载到指定挂载点。
func MountAUFS(ro []string, rw string, target string) error {
	// FIXME: Now mount the layers
	rwBranch := fmt.Sprintf("%v=rw", rw)
	roBranches := ""
	for _, layer := range ro {
		roBranches += fmt.Sprintf("%v=ro:", layer)
	}
	branches := fmt.Sprintf("br:%v:%v", rwBranch, roBranches)
	return mount("none", target, "aufs", 0, branches)
}

// @anxk: 挂载该镜像及其所有父镜像和一个空的读写层，根据镜像与其所有父镜像的差异在读写层中
// 添加对应的.wh.*文件，遮盖镜像相对于父镜像们删除的文件或文件夹。
func (image *Image) Mount(root, rw string) error {
	if mounted, err := Mounted(root); err != nil {
		return err
	} else if mounted {
		return fmt.Errorf("%s is already mounted", root)
	}
	layers, err := image.layers()
	if err != nil {
		return err
	}
	// Create the target directories if they don't exist
	if err := os.Mkdir(root, 0755); err != nil && !os.IsExist(err) {
		return err
	}
	if err := os.Mkdir(rw, 0755); err != nil && !os.IsExist(err) {
		return err
	}
	// FIXME: @creack shouldn't we do this after going over changes?
	if err := MountAUFS(layers, rw, root); err != nil {
		return err
	}
	// FIXME: Create tests for deletion
	// FIXME: move this part to change.go
	// Retrieve the changeset from the parent and apply it to the container
	//  - Retrieve the changes
	changes, err := Changes(layers, layers[0])
	if err != nil {
		return err
	}
	// Iterate on changes
	for _, c := range changes {
		// If there is a delete
		if c.Kind == ChangeDelete {
			// Make sure the directory exists
			file_path, file_name := path.Dir(c.Path), path.Base(c.Path)
			if err := os.MkdirAll(path.Join(rw, file_path), 0755); err != nil {
				return err
			}
			// And create the whiteout (we just need to create empty file, discard the return)
			if _, err := os.Create(path.Join(path.Join(rw, file_path),
				".wh."+path.Base(file_name))); err != nil {
				return err
			}
		}
	}
	return nil
}

// @anxk: 返回读写层和所有镜像层的差异。
func (image *Image) Changes(rw string) ([]Change, error) {
	layers, err := image.layers()
	if err != nil {
		return nil, err
	}
	return Changes(layers, rw)
}

// @anxk: 校验镜像ID是否为空或者含非法字符":"。
func ValidateId(id string) error {
	if id == "" {
		return fmt.Errorf("Image id can't be empty")
	}
	if strings.Contains(id, ":") {
		return fmt.Errorf("Invalid character in image id: ':'")
	}
	return nil
}

// @anxk: 用于生成镜像ID，这是一个随机值。
func GenerateId() string {
	// FIXME: don't seed every time
	rand.Seed(time.Now().UTC().UnixNano())
	randomBytes := bytes.NewBuffer([]byte(fmt.Sprintf("%x", rand.Int())))
	id, _ := ComputeId(randomBytes) // can't fail
	return id
}

// @anxk: 根据指定内容生成sha256值。
// ComputeId reads from `content` until EOF, then returns a SHA of what it read, as a string.
func ComputeId(content io.Reader) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, content); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)[:8]), nil
}

// @anxk: 返回镜像及其所有父镜像的列表，按父子关系排序，子在前。
// Image includes convenience proxy functions to its graph
// These functions will return an error if the image is not registered
// (ie. if image.graph == nil)
func (img *Image) History() ([]*Image, error) {
	var parents []*Image
	if err := img.WalkHistory(
		func(img *Image) error {
			parents = append(parents, img)
			return nil
		},
	); err != nil {
		return nil, err
	}
	return parents, nil
}

// @anxk: 返回镜像及其所有父镜像的layer路径列表，按父子关系排序，子在前。
// layers returns all the filesystem layers needed to mount an image
// FIXME: @shykes refactor this function with the new error handling
//        (I'll do it if I have time tonight, I focus on the rest)
func (img *Image) layers() ([]string, error) {
	var list []string
	var e error
	if err := img.WalkHistory(
		func(img *Image) (err error) {
			if layer, err := img.layer(); err != nil {
				e = err
			} else if layer != "" {
				list = append(list, layer)
			}
			return err
		},
	); err != nil {
		return nil, err
	} else if e != nil { // Did an error occur inside the handler?
		return nil, e
	}
	if len(list) == 0 {
		return nil, fmt.Errorf("No layer found for image %s\n", img.Id)
	}
	return list, nil
}

// @anxk: 遍历镜像及其所有父镜像，执行handler函数进行相关操作。
func (img *Image) WalkHistory(handler func(*Image) error) (err error) {
	currentImg := img
	for currentImg != nil {
		if handler != nil {
			if err := handler(currentImg); err != nil {
				return err
			}
		}
		currentImg, err = currentImg.GetParent()
		if err != nil {
			return fmt.Errorf("Error while getting parent image: %v", err)
		}
	}
	return nil
}

// @anxk: 返回父镜像。
func (img *Image) GetParent() (*Image, error) {
	if img.Parent == "" {
		return nil, nil
	}
	if img.graph == nil {
		return nil, fmt.Errorf("Can't lookup parent of unregistered image")
	}
	return img.graph.Get(img.Parent)
}

// @anxk: 返回某一镜像的存储路径，即/var/lib/docker/graph/<镜像ID>。
func (img *Image) root() (string, error) {
	if img.graph == nil {
		return "", fmt.Errorf("Can't lookup root of unregistered image")
	}
	return img.graph.imageRoot(img.Id), nil
}

// @anxk: 返回某一镜像（layer）的存储路径，即/var/lib/docker/graph/<镜像ID>/layer。
// Return the path of an image's layer
func (img *Image) layer() (string, error) {
	root, err := img.root()
	if err != nil {
		return "", err
	}
	return layerPath(root), nil
}
