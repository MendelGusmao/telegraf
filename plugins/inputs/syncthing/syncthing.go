package syncthing

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/influxdata/telegraf"
	"github.com/influxdata/telegraf/plugins/inputs"
)

const (
	endpointDBStatus          = "/db/status"
	endpointSystemStatus      = "/system/status"
	endpointSystemConnections = "/system/connections"
	endpointStatsFolder       = "/stats/folder"
)

type Syncthing struct {
	URL    string `toml:"url"`
	APIKey string `toml:"api_key"`
}

func (_ *Syncthing) Description() string {
	return ""
}

const sampleConfig = `
  ## syncthing rest url
  url = "http://localhost:8384/rest"
  ## api key
  apikey = "" # required
`

func (_ *Syncthing) SampleConfig() string {
	return sampleConfig
}

func (s *Syncthing) Gather(acc telegraf.Accumulator) error {
	gatherers := []func(telegraf.Accumulator) error{
		s.gatherDBStatus,
		s.gatherSystemConnections,
		s.gatherSystemStatus,
	}

	errs := make(chan error, len(gatherers)*2)
	wg := sync.WaitGroup{}
	wg.Add(len(gatherers))

	for _, gatherer := range gatherers {
		go func(acc telegraf.Accumulator, gatherer func(telegraf.Accumulator) error, errs chan error) {
			if err := gatherer(acc); err != nil {
				errs <- err
			}

			wg.Done()
		}(acc, gatherer, errs)
	}

	wg.Wait()
	close(errs)

	errorStrings := []string{}

	for err := range errs {
		errorStrings = append(errorStrings, err.Error())
	}

	if len(errorStrings) == 0 {
		return nil
	}

	return errors.New(strings.Join(errorStrings, "\n"))
}

func (s *Syncthing) gatherDBStatus(acc telegraf.Accumulator) error {
	folders, err := s.fetchFolders()

	if err != nil {
		return err
	}

	errs := make(chan error, len(folders)*2)
	wg := sync.WaitGroup{}
	wg.Add(len(folders))

	for _, folder := range folders {
		go func(acc telegraf.Accumulator, endpoint, folder string, errs chan error) {
			defer wg.Done()
			stats, err := s.fetch(endpointDBStatus + "?folder=" + folder)

			if err != nil {
				errs <- fmt.Errorf("error gathering folder data from syncthing (%s, %s): %s", endpoint, folder, err)
				return
			}

			tags := map[string]string{
				"endpoint": endpoint,
				"folder":   folder,
			}

			fields := map[string]interface{}{
				"globalBytes":   int64(stats["globalBytes"].(float64)),
				"globalDeleted": int64(stats["globalDeleted"].(float64)),
				"globalFiles":   int64(stats["globalFiles"].(float64)),
				"inSyncBytes":   int64(stats["inSyncBytes"].(float64)),
				"inSyncFiles":   int64(stats["inSyncFiles"].(float64)),
				"localBytes":    int64(stats["localBytes"].(float64)),
				"localDeleted":  int64(stats["localDeleted"].(float64)),
				"localFiles":    int64(stats["localFiles"].(float64)),
				"needBytes":     int64(stats["needBytes"].(float64)),
				"needFiles":     int64(stats["needFiles"].(float64)),
			}

			acc.AddFields("syncthing", fields, tags)
		}(acc, endpointDBStatus, folder, errs)
	}

	wg.Wait()
	close(errs)

	errorStrings := []string{}

	for err := range errs {
		errorStrings = append(errorStrings, err.Error())
	}

	if len(errorStrings) == 0 {
		return nil
	}

	return errors.New(strings.Join(errorStrings, "\n"))
}

func (s *Syncthing) gatherSystemStatus(acc telegraf.Accumulator) error {
	stats, err := s.fetch(endpointSystemStatus)

	if err != nil {
		return err
	}

	tags := map[string]string{
		"endpoint": endpointSystemStatus,
	}

	fields := map[string]interface{}{
		"alloc":            int64(stats["alloc"].(float64)),
		"cpuPercent":       stats["cpuPercent"],
		"discoveryMethods": int64(stats["discoveryMethods"].(float64)),
		"discoveryErrors":  float64(len(stats["discoveryErrors"].(map[string]interface{}))),
		"goroutines":       int64(stats["goroutines"].(float64)),
		"sys":              int64(stats["sys"].(float64)),
		"uptime":           int64(stats["uptime"].(float64)),
	}

	acc.AddFields("syncthing", fields, tags)

	return nil
}

func (s *Syncthing) gatherSystemConnections(acc telegraf.Accumulator) error {
	stats, err := s.fetch(endpointSystemConnections)

	if err != nil {
		return err
	}

	connections := stats["connections"].(map[string]interface{})
	connections["total"] = stats["total"].(map[string]interface{})

	for device, info := range connections {
		info := info.(map[string]interface{})

		tags := map[string]string{
			"endpoint":        endpointSystemConnections,
			"device":          strings.Split(device, "-")[0],
			"connection_type": info["type"].(string),
			"clientVersion":   info["clientVersion"].(string),
		}

		fields := map[string]interface{}{
			"inBytesTotal":  int64(info["inBytesTotal"].(float64)),
			"outBytesTotal": int64(info["outBytesTotal"].(float64)),
		}

		acc.AddFields("syncthing", fields, tags)
	}

	return nil
}

func (s *Syncthing) fetchFolders() ([]string, error) {
	folders, err := s.fetch(endpointStatsFolder)

	if err != nil {
		return nil, fmt.Errorf("error gathering folders data from syncthing: %s", err)
	}

	foldersList := make([]string, len(folders))
	i := 0

	for folder := range folders {
		foldersList[i] = folder
		i++
	}

	return foldersList, nil
}

func (s *Syncthing) fetch(endpoint string) (map[string]interface{}, error) {
	client := &http.Client{}
	request, err := http.NewRequest("GET", s.URL+endpoint, nil)
	request.Header.Add("X-API-Key", s.APIKey)

	response, err := client.Do(request)

	if err != nil {
		return nil, fmt.Errorf("error getting json from syncthing (%s): %s", endpoint, err)
	}

	buffer := bytes.NewBuffer([]byte{})

	if _, err = io.Copy(buffer, response.Body); err != nil {
		return nil, fmt.Errorf("error reading json from syncthing (%s): %s", endpoint, err)
	}

	stats := map[string]interface{}{}

	if err = json.Unmarshal(buffer.Bytes(), &stats); err != nil {
		return nil, fmt.Errorf("error unmarshalling json from syncthing (%s): %s", endpoint, err)
	}

	return stats, nil
}

func init() {
	inputs.Add("syncthing", func() telegraf.Input {
		return &Syncthing{URL: "http://localhost:8384/rest"}
	})
}
