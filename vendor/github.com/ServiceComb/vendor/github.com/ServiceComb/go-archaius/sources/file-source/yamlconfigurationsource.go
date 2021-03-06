/*
 * Copyright 2017 Huawei Technologies Co., Ltd
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *    http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

//Package filesource created on 2017/6/22.
package filesource

import (
	"errors"
	"fmt"
	"io/ioutil"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sync"

	"github.com/ServiceComb/go-archaius/core"
	"github.com/ServiceComb/go-archaius/lager"
	"github.com/fsnotify/fsnotify"
	"gopkg.in/yaml.v2"
)

const (
	//FileConfigSourceConst is a variable of type string
	FileConfigSourceConst = "FileSource"
	fileSourcePriority    = 4
	//DefaultFilePriority is a variable of type string
	DefaultFilePriority = 0
)

//FileSourceTypes is a string
type FileSourceTypes string

const (
	//RegularFile is a variable of type string
	RegularFile FileSourceTypes = "RegularFile"
	//Directory is a variable of type string
	Directory FileSourceTypes = "Directory"
	//InvalidFileType is a variable of type string
	InvalidFileType FileSourceTypes = "InvalidType"
)

//ConfigInfo is s struct
type ConfigInfo struct {
	FilePath string
	Value    interface{}
}

type yamlConfigurationSource struct {
	Configurations map[string]*ConfigInfo
	files          []file
	watchPool      *watch
	filelock       sync.Mutex
	sync.RWMutex
}

type file struct {
	filePath string
	priority uint32
}

type watch struct {
	//files   []string
	watcher    *fsnotify.Watcher
	callback   core.DynamicConfigCallback
	fileSource *yamlConfigurationSource
	sync.RWMutex
}

var _ core.ConfigSource = &yamlConfigurationSource{}
var _ FileSource = &yamlConfigurationSource{}

var fileConfigSource *yamlConfigurationSource

/*
	accepts files and directories as file-source
  		1. Directory: all yaml files considered as file source
  		2. File: specified yaml file considered as file source

  	TODO: Currently file sources priority not considered. if key conflicts then latest key will get considered
*/

//FileSource is a interface
type FileSource interface {
	core.ConfigSource
	AddFileSource(filePath string, priority uint32) error
}

//NewYamlConfigurationSource creates new yaml configuration
func NewYamlConfigurationSource() FileSource {
	if fileConfigSource == nil {
		fileConfigSource = new(yamlConfigurationSource)
		fileConfigSource.files = make([]file, 0)
	}

	return fileConfigSource
}

func (fSource *yamlConfigurationSource) AddFileSource(p string, priority uint32) error {
	path, err := filepath.Abs(p)
	if err != nil {
		return err
	}

	// check existence of file
	fs, err := os.Open(path)
	if os.IsNotExist(err) {
		return fmt.Errorf("[%s] file not exist", path)
	}
	defer fs.Close()

	// prevent duplicate file source
	if fSource.isFileSrcExist(path) {
		return nil
	}

	fileType := fileType(fs)
	switch fileType {
	case Directory:
		// handle Directory input. Include all yaml files as file source.
		err := fSource.handleDirectory(fs, priority)
		if err != nil {
			lager.Logger.Errorf(err, "Failed to handle directory [%s]", path)
			return err
		}
	case RegularFile:
		// handle file and include as file source.
		err := fSource.handleFile(fs, priority)
		if err != nil {
			lager.Logger.Errorf(err, "Failed to handle file [%s]", path)
			return err
		}
	case InvalidFileType:
		lager.Logger.Errorf(nil, "File type of [%s] not supported", path)
		return fmt.Errorf("file type of [%s] not supported", path)
	}

	if fSource.watchPool != nil {
		fSource.watchPool.AddWatchFile(path)
	}

	return nil
}

func (fSource *yamlConfigurationSource) isFileSrcExist(filePath string) bool {
	var exist bool
	for _, file := range fSource.files {
		if filePath == file.filePath {
			return true
		}
	}

	return exist
}

func fileType(fs *os.File) FileSourceTypes {
	fileInfo, err := fs.Stat()
	if err != nil {
		return InvalidFileType
	}

	fileMode := fileInfo.Mode()

	if fileMode.IsDir() {
		return Directory
	} else if fileMode.IsRegular() {
		return RegularFile
	}

	return InvalidFileType
}

func (fSource *yamlConfigurationSource) handleDirectory(dir *os.File, priority uint32) error {

	filesInfo, err := dir.Readdir(-1)
	if err != nil {
		return errors.New("failed to read Directory contents")
	}

	for _, fileInfo := range filesInfo {
		filePath := filepath.Join(dir.Name(), fileInfo.Name())

		fs, err := os.Open(filePath)
		if err != nil {
			lager.Logger.Errorf(err, "error in file open for %s file", err.Error())
			continue
		}

		err = fSource.handleFile(fs, priority)
		if err != nil {
			lager.Logger.Errorf(err, "error processing %s file source handler with error : %s ", fs.Name(),
				err.Error())
		}
		fs.Close()

	}

	return nil
}

func (fSource *yamlConfigurationSource) handleFile(file *os.File, priority uint32) error {
	config, err := fileConfigSource.pullYamlFileConfig(file.Name())
	if err != nil {
		return fmt.Errorf("failed to pull configurations from [%s] file, %s", file.Name(), err)
	}

	err = fSource.handlePriority(file.Name(), priority)
	if err != nil {
		return fmt.Errorf("failed to handle priority of [%s], %s", file.Name(), err)
	}

	events := fSource.compareUpdate(config, file.Name())
	if fSource.watchPool != nil && fSource.watchPool.callback != nil { // if file source already added and try to add
		for _, e := range events {
			fSource.watchPool.callback.OnEvent(e)
		}
	}

	return nil
}

func (fSource *yamlConfigurationSource) handlePriority(filePath string, priority uint32) error {
	fSource.Lock()
	newFilePriority := make([]file, 0)
	var prioritySet bool
	for _, f := range fSource.files {

		if f.filePath == filePath && f.priority == priority {
			prioritySet = true
			newFilePriority = append(newFilePriority, file{
				filePath: filePath,
				priority: priority,
			})
		}
		newFilePriority = append(newFilePriority, f)
	}

	if !prioritySet {
		newFilePriority = append(newFilePriority, file{
			filePath: filePath,
			priority: priority,
		})
	}

	fSource.files = newFilePriority
	fSource.Unlock()

	return nil
}

func (fSource *yamlConfigurationSource) pullYamlFileConfig(fileName string) (map[string]interface{}, error) {
	configMap := make(map[string]interface{})
	yamlContent, err := ioutil.ReadFile(fileName)
	if err != nil {
		return nil, err
	}

	ss := yaml.MapSlice{}
	err = yaml.Unmarshal([]byte(yamlContent), &ss)
	if err != nil {
		return nil, fmt.Errorf("yaml unmarshal [%s] failed, %s", fileName, err)
	}
	configMap = retrieveItems("", ss)

	return configMap, nil
}

func (fSource *yamlConfigurationSource) GetConfigurations() (map[string]interface{}, error) {
	configMap := make(map[string]interface{})

	fSource.Lock()
	defer fSource.Unlock()
	for key, confInfo := range fSource.Configurations {
		if confInfo == nil {
			configMap[key] = nil
			continue
		}

		configMap[key] = confInfo.Value
	}

	return configMap, nil
}

func retrieveItems(prefix string, subItems yaml.MapSlice) map[string]interface{} {
	if prefix != "" {
		prefix += "."
	}

	result := map[string]interface{}{}

	for _, item := range subItems {
		//If there are sub-items existing
		_, isSlice := item.Value.(yaml.MapSlice)
		if isSlice {
			subResult := retrieveItems(prefix+item.Key.(string), item.Value.(yaml.MapSlice))
			for k, v := range subResult {
				result[k] = v
			}
		} else {
			result[prefix+item.Key.(string)] = item.Value
		}
	}

	return result
}

func (fSource *yamlConfigurationSource) GetConfigurationByKey(key string) (interface{}, error) {
	fSource.Lock()
	defer fSource.Unlock()

	for ckey, confInfo := range fSource.Configurations {
		if confInfo == nil {
			confInfo.Value = nil
			continue
		}

		if ckey == key {
			return confInfo.Value, nil
		}
	}

	return nil, errors.New("key does not exist")
}

func (*yamlConfigurationSource) GetSourceName() string {
	return FileConfigSourceConst
}

func (*yamlConfigurationSource) GetPriority() int {
	return fileSourcePriority
}

func (fSource *yamlConfigurationSource) DynamicConfigHandler(callback core.DynamicConfigCallback) error {
	if callback == nil {
		return errors.New("call back can not be nil")
	}

	watchPool, err := newWatchPool(callback, fSource)
	if err != nil {
		return err
	}

	fSource.watchPool = watchPool

	go fSource.watchPool.startWatchPool()

	return nil
}

func newWatchPool(callback core.DynamicConfigCallback, cfgSrc *yamlConfigurationSource) (*watch, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		lager.Logger.Error("New file watcher failed", err)
		return nil, err
	}

	watch := new(watch)
	watch.callback = callback
	//watch.files = make([]string, 0)
	watch.fileSource = cfgSrc
	watch.watcher = watcher

	return watch, nil
}

func (wth *watch) startWatchPool() {
	go wth.watchFile()
	for _, file := range wth.fileSource.files {
		dir, err := filepath.Abs(filepath.Dir(file.filePath))
		if err != nil {
			lager.Logger.Errorf(err, "failed to get Directory info from: %s file.", file.filePath)
			return
		}

		err = wth.watcher.Add(dir)
		if err != nil {
			lager.Logger.Errorf(err, "add watcher file: %+v fail.", file)
			return
		}
	}
}

func (wth *watch) AddWatchFile(filePath string) {
	dir, err := filepath.Abs(filepath.Dir(filePath))
	if err != nil {
		lager.Logger.Errorf(err, "failed to get Directory info from: %s file.", filePath)
		return
	}

	err = wth.watcher.Add(dir)
	if err != nil {
		lager.Logger.Errorf(err, "add watcher file: %s fail.", filePath)
		return
	}
}

func (wth *watch) watchFile() {

	//watchAgain:
	//	err := wth.watcher.Add(filePath)
	//	if err != nil {
	//		log.Logger.Errorf("add watcher file: %s fail.", filePath)
	//		return
	//	}
	for {
		select {
		case event, ok := <-wth.watcher.Events:
			if !ok {
				lager.Logger.Warnf("file watcher stop")
				return
			}
			lager.Logger.Debugf("the file %s is change for %s. reload it.", event.Name, event.Op.String())

			if event.Op == fsnotify.Remove {
				lager.Logger.Warnf("the file change mode: %s. So stop watching file",
					event.String())
				continue
			}

			if event.Op == fsnotify.Rename {
				wth.watcher.Remove(event.Name)
				// check existence of file
				_, err := os.Open(event.Name)
				if os.IsNotExist(err) {
					lager.Logger.Warnf("[%s] file does not exist so not able to watch further", event.Name, err)
				} else {
					wth.AddWatchFile(event.Name)
				}

				continue
			}

			yamlContent, err := ioutil.ReadFile(event.Name)
			if err != nil {
				lager.Logger.Error("yaml parsing error ", err)
				continue
			}
			ss := yaml.MapSlice{}
			err = yaml.Unmarshal([]byte(yamlContent), &ss)
			if err != nil {
				lager.Logger.Warnf("unmarshaling failed may be due to invalid file data format", err)
				continue
			}

			newConf := retrieveItems("", ss)
			events := wth.fileSource.compareUpdate(newConf, event.Name)
			lager.Logger.Debugf("Event generated events", events)
			for _, e := range events {
				wth.callback.OnEvent(e)
			}

		case err := <-wth.watcher.Errors:
			lager.Logger.Debugf("watch file error:", err)
			return
		}
	}

}

func (fSource *yamlConfigurationSource) compareUpdate(newconf map[string]interface{}, filePath string) []*core.Event {
	events := make([]*core.Event, 0)
	fileConfs := make(map[string]*ConfigInfo)
	if fSource == nil {
		return nil
	}

	fSource.Lock()
	defer fSource.Unlock()

	var filePathPriority uint32 = math.MaxUint32
	for _, file := range fSource.files {
		if file.filePath == filePath {
			filePathPriority = file.priority
		}
	}

	if filePathPriority == math.MaxUint32 {
		return nil
	}

	// update and delete with latest configs

	for key, confInfo := range fSource.Configurations {
		if confInfo == nil {
			confInfo.Value = nil
			continue
		}

		switch confInfo.FilePath {
		case filePath:
			newConfValue, ok := newconf[key]
			if !ok {
				events = append(events, &core.Event{EventSource: FileConfigSourceConst, Key: key,
					EventType: core.Delete, Value: confInfo.Value})
				continue
			} else if reflect.DeepEqual(confInfo.Value, newConfValue) {
				fileConfs[key] = confInfo
				continue
			}

			confInfo.Value = newConfValue
			fileConfs[key] = confInfo

			events = append(events, &core.Event{EventSource: FileConfigSourceConst, Key: key,
				EventType: core.Update, Value: newConfValue})

		default: // configuration file not same
			// no need to handle delete scenario
			// only handle if configuration conflicts between two sources
			newConfValue, ok := newconf[key]
			if ok {
				var priority uint32 = math.MaxUint32
				for _, file := range fSource.files {
					if file.filePath == confInfo.FilePath {
						priority = file.priority
					}
				}

				if priority == filePathPriority {
					fileConfs[key] = confInfo
					lager.Logger.Infof("Two files have same priority. keeping %s value", confInfo.FilePath)

				} else if filePathPriority < priority { // lower the vale higher is the priority
					confInfo.Value = newConfValue
					fileConfs[key] = confInfo
					events = append(events, &core.Event{EventSource: FileConfigSourceConst,
						Key: key, EventType: core.Update, Value: newConfValue})

				} else {
					fileConfs[key] = confInfo
				}
			} else {
				fileConfs[key] = confInfo
			}
		}
	}

	// create add/create new config
	fileConfs = fSource.addOrCreateConf(fileConfs, newconf, events, filePath)
	fSource.Configurations = fileConfs

	return events
}

func (fSource *yamlConfigurationSource) addOrCreateConf(fileConfs map[string]*ConfigInfo, newconf map[string]interface{},
	events []*core.Event, filePath string) map[string]*ConfigInfo {
	for key, value := range newconf {
		handled := false

		_, ok := fileConfs[key]
		if ok {
			handled = true
		}

		if !handled {
			events = append(events, &core.Event{EventSource: FileConfigSourceConst, Key: key,
				EventType: core.Create, Value: value})
			fileConfs[key] = &ConfigInfo{
				FilePath: filePath,
				Value:    value,
			}
		}
	}

	return fileConfs
}

//func generateKey(key, filepath string) string {
//	return key + `#` + filepath
//}
//
//func getFileKeyNPath(configKey string) []string {
//	return strings.Split(configKey, `#`)
//}

func (fSource *yamlConfigurationSource) Cleanup() error {

	fSource.filelock.Lock()
	defer fSource.filelock.Unlock()

	if fileConfigSource == nil || fSource == nil {
		return nil
	}

	if fSource.watchPool != nil && fSource.watchPool.watcher != nil {
		fSource.watchPool.watcher.Close()
	}

	if fSource.watchPool != nil {
		fSource.watchPool.fileSource = nil
		fSource.watchPool.callback = nil
		fSource.watchPool = nil
	}
	fSource.Configurations = nil
	fSource.files = make([]file, 0)
	return nil
}

func (fSource *yamlConfigurationSource) GetConfigurationByKeyAndDimensionInfo(key, di string) (interface{}, error) {
	return nil, nil
}

func (fSource *yamlConfigurationSource) AddDimensionInfo(dimensionInfo string) (map[string]string, error) {
	return nil, nil
}

func (fSource *yamlConfigurationSource) GetConfigurationsByDI(dimensionInfo string) (map[string]interface{}, error) {
	return nil, nil
}
