package tplink_gateway

import (
	"encoding/json"
	"io/ioutil"
	"log"
	"time"
)

type overflowCache struct {
	filename   string
	values     map[string]uint64
	baseValues map[string]uint64
}

func (c *overflowCache) init() *overflowCache {
	c.values = make(map[string]uint64)
	c.baseValues = make(map[string]uint64)

	return c
}

func (c *overflowCache) setup(filename string, dumpInterval time.Duration) {
	c.filename = filename

	if err := c.load(); err != nil {
		log.Printf("loading %s: %s\n", c.filename, err)
	}
}

func (c *overflowCache) checkOverflow(section, key string, currentValue, increment uint64) (uint64, bool) {
	key = section + "_" + key
	overflown := currentValue < c.values[key]

	if overflown {
		log.Printf("detected overflow in key %s (old: %d, new: %d)\n", key, c.values[key], currentValue)

		if _, ok := c.baseValues[key]; !ok {
			c.baseValues[key] = 0
		}

		c.baseValues[key] += increment

		defer func() {
			if err := c.dump(); err != nil {
				log.Printf("dumping %s: %s\n", c.filename, err)
			}
		}()
	}

	c.values[key] = currentValue

	return currentValue + c.baseValues[key], overflown
}

func (c *overflowCache) get(section, key string, currentValue, increment uint64) uint64 {
	value, _ := c.checkOverflow(section, key, currentValue, increment)
	return value
}

func (c *overflowCache) load() error {
	content, err := ioutil.ReadFile(c.filename)

	if err != nil {
		return err
	}

	return json.Unmarshal(content, &c.baseValues)
}

func (c *overflowCache) dump() error {
	content, err := json.Marshal(c.baseValues)

	if err != nil {
		return err
	}

	return ioutil.WriteFile(c.filename, content, 0644)
}
