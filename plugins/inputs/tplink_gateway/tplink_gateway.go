package tplink_gateway

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/influxdata/telegraf"
	"github.com/influxdata/telegraf/plugins/inputs"
)

const (
	defaultAddress  = "http://192.168.0.1"
	defaultUsername = "admin"
	defaultPassword = "admin"
	increment       = math.MaxUint32
)

type TPLink_Gateway struct {
	Address            string
	Username, Password string
	Debug              bool
	CacheFile          string `toml:"cache_file"`
	DumpInterval       string `toml:"dump_interval"`

	ip    string
	cache *overflowCache
}

func (_ *TPLink_Gateway) Description() string {
	return "Fetch values from a TP-Link gateway"
}

var tplinkgatewaySampleConfig = `
  # address = "http://192.168.0.1"
  # username = "admin"
  # password = "admin"
  # cache_file = "/etc/telegraf/tpg_cache"
  # dump_interval = "1m"
`

func (_ *TPLink_Gateway) SampleConfig() string {
	return tplinkgatewaySampleConfig
}

func (g *TPLink_Gateway) Gather(acc telegraf.Accumulator) error {
	if g.ip == "" {
		u, err := url.Parse(g.Address)

		if err != nil {
			return err
		}

		addrs, err := net.LookupHost(u.Host)

		if err != nil {
			return err
		}

		if len(addrs) == 0 {
			return fmt.Errorf("no addresses found for %s", u.Host)
		}

		g.ip = addrs[0]
	}

	if g.CacheFile != "" {
		cachingInterval, err := time.ParseDuration(g.DumpInterval)

		if err != nil {
			return err
		}

		g.cache.setup(g.CacheFile, cachingInterval)
	}

	accs := []func(telegraf.Accumulator) error{
		g.status,
		g.systemStatistic,
	}

	for _, a := range accs {
		if err := a(acc); err != nil {
			return err
		}
	}

	return nil
}

func (g *TPLink_Gateway) status(acc telegraf.Accumulator) error {
	content, err := g.fetch("/userRpm/StatusRpm.htm")

	if err != nil {
		return fmt.Errorf("(status) %s", err)
	}

	statusPara, err := findJSArray(content, "statusPara")

	if err != nil {
		return fmt.Errorf("(status) %s", err)
	}

	if len(statusPara) < 5 {
		return fmt.Errorf("(status) unexpected statusPara size (%d)", len(statusPara))
	}

	uptime, _ := strconv.ParseUint(statusPara[4], 10, 64)

	if _, overflown := g.cache.checkOverflow("status", "uptime", uptime, 0); overflown {
		g.cache.init()
		g.cache.dump()
	}

	statistList, err := findJSArray(content, "statistList")

	if err != nil {
		return fmt.Errorf("(status) %s", err)
	}

	if statusPara[1] != "1" {
		return fmt.Errorf("(status) multiple WANs are not supported")
	}

	if len(statistList) < 4 {
		return fmt.Errorf("(status) unexpected statistList size (%d)", len(statistList))
	}

	values := stringSliceToUint64Slice(statistList)

	tags := map[string]string{
		"page": "status",
		"wan":  "1",
	}

	fields := map[string]interface{}{
		"received_bytes":   g.cache.get("status", "received_bytes", values[0], increment),
		"received_packets": g.cache.get("status", "received_packets", values[2], increment),
		"sent_bytes":       g.cache.get("status", "sent_bytes", values[1], increment),
		"sent_packets":     g.cache.get("status", "sent_packets", values[3], increment),
	}

	acc.AddFields("tplink_gateway", fields, tags)

	return nil
}

func (g *TPLink_Gateway) systemStatistic(acc telegraf.Accumulator) error {
	content, err := g.fetch("/userRpm/SystemStatisticRpm.htm?Num_per_page=100")

	if err != nil {
		return fmt.Errorf("(systemStatistic) %s", err)
	}

	statRulePara, err := findJSArray(content, "StatRulePara")

	if err != nil {
		return fmt.Errorf("(systemStatistic) %s", err)
	}

	if len(statRulePara) < 5 {
		return fmt.Errorf("(systemStatistic) unexpected statRulePara size (%d)", len(statRulePara))
	}

	statList, err := findJSArray(content, "statList")

	if err != nil {
		return fmt.Errorf("(systemStatistic) %s", err)
	}

	listSize, _ := strconv.Atoi(statRulePara[4])
	listFactor, _ := strconv.Atoi(statRulePara[5])

	for i := 0; i < listSize; i++ {
		row := i * listFactor
		mac := strings.Trim(statList[row+2], `"`)

		tags := map[string]string{
			"page": "system_statistic",
			"ip":   strings.Trim(statList[row+1], `"`),
			"mac":  mac,
		}

		values := stringSliceToUint64Slice(statList)

		fields := map[string]interface{}{
			"total_packets":  g.cache.get("system_statistic_"+mac, "total_packets", values[row+3], increment),
			"total_bytes":    g.cache.get("system_statistic_"+mac, "total_bytes", values[row+4], increment),
			"packets":        values[row+5],
			"bytes":          values[row+6],
			"icmp_tx":        values[row+7],
			"icmp_tx_max":    values[row+8],
			"udp_tx":         values[row+9],
			"udp_tx_max":     values[row+10],
			"tcp_syn_tx":     values[row+11],
			"tcp_syn_tx_max": values[row+12],
		}

		acc.AddFields("tplink_gateway", fields, tags)
	}

	return nil
}

// func (g *TPLink_Gateway) wlanStation(acc telegraf.Accumulator) error {
// 	content, err := g.fetch("/userRpm/WlanStationRpm.htm")
//
// 	if err != nil {
// 		return fmt.Errorf("(wlanStation) %s", err)
// 	}
//
// 	wlanHostPara, err := findJSArray(content, "wlanHostPara")
//
// 	if err != nil {
// 		return fmt.Errorf("(wlanStation) %s", err)
// 	}
//
// 	whpValues := make([]uint64, len(wlanHostPara))
//
// 	for i, v := range wlanHostPara {
// 		v, _ := strconv.ParseUint(v, 10, 64)
// 		whpValues[i] = v
// 	}
//
// 	hostList, err := findJSArray(content, "hostList")
//
// 	if err != nil {
// 		return fmt.Errorf("(wlanStation) %s", err)
// 	}
//
// 	return nil
// }

func (g *TPLink_Gateway) fetch(resource string) (string, error) {
	req, err := http.NewRequest("GET", g.Address+resource, nil)

	if err != nil {
		return "", fmt.Errorf("error building request: %s", err)
	}

	req.SetBasicAuth(g.Username, g.Password)
	req.Header.Add("Referer", fmt.Sprintf("http://%s", g.ip))

	response, err := http.DefaultClient.Do(req)

	if err != nil {
		return "", fmt.Errorf("error requesting: %s", err)
	}

	buffer := bytes.NewBuffer([]byte{})

	if _, err = io.Copy(buffer, response.Body); err != nil {
		return "", fmt.Errorf("error reading data from the gateway: %s", err)
	}

	if g.Debug {
		log.Println(strings.Replace(buffer.String(), "\n", "", -1))
	}

	if response.StatusCode/100 != 2 {
		return "", fmt.Errorf("gateway returned %d", response.StatusCode)
	}

	return buffer.String(), nil
}

func findJSArray(content, name string) ([]string, error) {
	start := "var " + name + " = new Array(\n"
	begin := strings.Index(content, start)

	if begin == -1 {
		return []string{content}, fmt.Errorf(name + " not found")
	}

	end := strings.Index(content[begin:], ");")
	content = content[begin+len(start) : begin+end]
	content = strings.Replace(content, "\n", "", -1)
	content = strings.TrimSpace(content)

	return strings.Split(content, ","), nil
}

func stringSliceToUint64Slice(in []string) []uint64 {
	values := make([]uint64, len(in))

	for i, v := range in {
		v, _ := strconv.ParseUint(v, 10, 64)
		values[i] = v
	}

	return values
}

func init() {
	inputs.Add("tplink_gateway", func() telegraf.Input {
		return &TPLink_Gateway{
			Address:  defaultAddress,
			Username: defaultUsername,
			Password: defaultPassword,
			cache:    new(overflowCache).init(),
		}
	})
}
