package docker

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
)

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

// @anxk: 实例化一个新的TagStore，并尝试从本地加载数据。
func NewTagStore(path string, graph *Graph) (*TagStore, error) {
	abspath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	store := &TagStore{
		path:         abspath,
		graph:        graph,
		Repositories: make(map[string]Repository),
	}
	// Load the json file if it exists, otherwise create it.
	if err := store.Reload(); os.IsNotExist(err) {
		if err := store.Save(); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	return store, nil
}

// @anxk: 将store数据存储在本地文件系统。
func (store *TagStore) Save() error {
	// Store the json ball
	jsonData, err := json.Marshal(store)
	if err != nil {
		return err
	}
	if err := ioutil.WriteFile(store.path, jsonData, 0600); err != nil {
		return err
	}
	return nil
}

// @anxk: 从/var/lib/docker/repositories加载store数据。
func (store *TagStore) Reload() error {
	jsonData, err := ioutil.ReadFile(store.path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(jsonData, store); err != nil {
		return err
	}
	return nil
}

// @anxk: 根据镜像ID或者<repoName>:[<tag>]的字符串格式查找镜像（json）。
func (store *TagStore) LookupImage(name string) (*Image, error) {
	img, err := store.graph.Get(name)
	if err != nil {
		// FIXME: standardize on returning nil when the image doesn't exist, and err for everything else
		// (so we can pass all errors here)
		repoAndTag := strings.SplitN(name, ":", 2)
		if len(repoAndTag) == 1 {
			repoAndTag = append(repoAndTag, DEFAULT_TAG)
		}
		if i, err := store.GetImage(repoAndTag[0], repoAndTag[1]); err != nil {
			return nil, err
		} else if i == nil {
			return nil, fmt.Errorf("No such image: %s", name)
		} else {
			img = i
		}
	}
	return img, nil
}

// @anxk: 返回一个{<镜像ID>: {"repoName:tag1", {"repoName:tag2"}}}形式的Map。
// Return a reverse-lookup table of all the names which refer to each image
// Eg. {"43b5f19b10584": {"base:latest", "base:v1"}}
func (store *TagStore) ById() map[string][]string {
	byId := make(map[string][]string)
	for repoName, repository := range store.Repositories {
		for tag, id := range repository {
			name := repoName + ":" + tag
			if _, exists := byId[id]; !exists {
				byId[id] = []string{name}
			} else {
				byId[id] = append(byId[id], name)
			}
		}
	}
	return byId
}

// 根据镜像ID查找形式为<repoName>:<tag>的第一个镜像名，如果没查到则返回镜像ID。
func (store *TagStore) ImageName(id string) string {
	if names, exists := store.ById()[id]; exists && len(names) > 0 {
		return names[0]
	}
	return id
}

// @anxk: 给某一镜像添加新的repoName和tag。
func (store *TagStore) Set(repoName, tag, imageName string, force bool) error {
	img, err := store.LookupImage(imageName)
	if err != nil {
		return err
	}
	if tag == "" {
		tag = DEFAULT_TAG
	}
	if err := validateRepoName(repoName); err != nil {
		return err
	}
	if err := validateTagName(tag); err != nil {
		return err
	}
	if err := store.Reload(); err != nil {
		return err
	}
	var repo Repository
	if r, exists := store.Repositories[repoName]; exists {
		repo = r
	} else {
		repo = make(map[string]string)
		// @anxk: 这个if分支永远执行不到吧？
		if old, exists := store.Repositories[repoName]; exists && !force {
			return fmt.Errorf("Tag %s:%s is already set to %s", repoName, tag, old)
		}
		store.Repositories[repoName] = repo
	}
	repo[tag] = img.Id
	return store.Save()
}

// @anxk: 根据repoName获取一个Repository条目。
func (store *TagStore) Get(repoName string) (Repository, error) {
	if err := store.Reload(); err != nil {
		return nil, err
	}
	if r, exists := store.Repositories[repoName]; exists {
		return r, nil
	}
	return nil, nil
}

// @anxk: 根据repoName和tag获取一个镜像（json）。这是一种查找，返回参数很有意思，查找的
// 结果分三种（1）查到了，返回result, nil；（2）查找失败因为没有对应条目，返回nil, nil；
// 查找失败因为查找过程中出现某种错误，返回nil, err。
func (store *TagStore) GetImage(repoName, tag string) (*Image, error) {
	repo, err := store.Get(repoName)
	if err != nil {
		return nil, err
	} else if repo == nil {
		return nil, nil
	}
	if revision, exists := repo[tag]; exists {
		return store.graph.Get(revision)
	}
	return nil, nil
}

// @anxk: 校验repoName，注意不能包含":"。
// Validate the name of a repository
func validateRepoName(name string) error {
	if name == "" {
		return fmt.Errorf("Repository name can't be empty")
	}
	if strings.Contains(name, ":") {
		return fmt.Errorf("Illegal repository name: %s", name)
	}
	return nil
}

// @anxk: 校验tag，注意不能包含":"和"/"。
// Validate the name of a tag
func validateTagName(name string) error {
	if name == "" {
		return fmt.Errorf("Tag name can't be empty")
	}
	if strings.Contains(name, "/") || strings.Contains(name, ":") {
		return fmt.Errorf("Illegal tag name: %s", name)
	}
	return nil
}
