package modbus

import (
	"encoding/binary"
	"fmt"
	"log"
	"net/url"
	"strconv"
	"time"

	"github.com/goburrow/modbus"
	"github.com/influxdata/telegraf"
	"github.com/influxdata/telegraf/plugins/inputs"
)

const (
	description  = "Reads data from modbus sources"
	sampleConfig = `
  ### connection config
  ## for tcp connections
  address = "tcp://127.0.0.1:503"
  ## for rtu connections
  # address = "rtu:///dev/ttyS0"
  #
  ## for ascii readings
  # address = "ascii:///dev/ttyS0"
  #
  ### generic options
	## atomicity defines what will happen when there is an error fetching data from the device
  ## when atomic is 0 it will just log and continue accumulating
	##                1 it won't accumulate values for the kind
	##                2 it won't accumulate any values
  atomicity = 0
  timeout = "2s"
  slave_id = 1
  #
	## addresses
	## kind can be: discrete_inputs|coils|input_registers|holding_registers|fifo_queue
	## there is no need do explicit length if it is less than 2
  # [[modbus.{kind}]]
  #   address = 100
  #   length = 1
  `
)

type Modbus struct {
	Address   string `toml:"address"`
	Timeout   string
	SlaveID   byte `toml:"slave_id"`
	BaudRate  int  `toml:"baud_rate"`
	DataBits  int  `toml:"data_bits "`
	Parity    string
	StopBits  int `toml:"stop_bits"`
	Atomicity int `toml:"atomicity_level"`

	DiscreteInputs   []address `toml:"discrete_inputs"`
	Coils            []address `toml:"coils"`
	InputRegisters   []address `toml:"input_registers"`
	HoldingRegisters []address `toml:"holding_registers"`
	FIFOQueue        []address `toml:"fifo_queue"`

	device string
	client modbus.Client
}

type address struct {
	Address, Length uint16
}

type reader func(map[uint16]uint16) (map[uint16]uint16, error)

type reading struct {
	reader    func(uint16, uint16) ([]byte, error)
	addresses []address
}

func (*Modbus) Description() string {
	return description
}

func (*Modbus) SampleConfig() string {
	return sampleConfig
}

func (m *Modbus) Gather(acc telegraf.Accumulator) error {
	var (
		uri *url.URL
		err error
	)

	if uri, err = url.Parse(m.Address); err != nil {
		return err
	}

	m.client, err = m.buildClient(uri)

	if err != nil {
		return err
	}

	readings := map[string]reading{
		"input_register":   reading{m.client.ReadInputRegisters, m.InputRegisters},
		"discrete_input":   reading{m.client.ReadDiscreteInputs, m.DiscreteInputs},
		"coil":             reading{m.client.ReadCoils, m.Coils},
		"holding_register": reading{m.client.ReadHoldingRegisters, m.HoldingRegisters},
		"fifo_queue":       reading{fAdapter(m.client.ReadFIFOQueue), m.FIFOQueue},
	}

	for kind, reading := range readings {
	Reading:
		for _, address := range reading.addresses {
			if address.Length < 0 {
				address.Length = 1
			}

			data, err := reading.reader(address.Address, address.Length)

			if err != nil {
				err = fmt.Errorf("readRegisters (%s, %d): %s", kind, address.Address, err)

				if m.Atomicity < 2 {
					log.Println(err)

					if m.Atomicity == 1 {
						continue Reading
					}
				}

				return err
			}

			fields := make(map[string]interface{})

			for i := uint16(0); i < address.Length; i++ {
				addr := strconv.Itoa(int(address.Address + i))
				fields[addr] = binary.BigEndian.Uint16(data[i : i+2])
			}

			acc.AddFields("modbus", fields, map[string]string{
				"kind":   kind,
				"device": m.device,
			})
		}
	}

	return nil
}

func (m *Modbus) buildClient(uri *url.URL) (modbus.Client, error) {
	var (
		err     error
		timeout = 2 * time.Second
	)

	if m.Timeout != "" {
		if timeout, err = time.ParseDuration(m.Timeout); err != nil {
			return nil, fmt.Errorf("modbus.buildClient: %s", err)
		}
	}

	switch uri.Scheme {
	case "tcp":
		handler := modbus.NewTCPClientHandler(uri.Host)

		handler.Timeout = timeout
		handler.SlaveId = m.SlaveID

		if err := handler.Connect(); err != nil {
			return nil, fmt.Errorf("modbus.buildClient: %s", err)
		}

		m.device = uri.Host

		return modbus.NewClient(handler), nil
	case "rtu":
		handler := modbus.NewRTUClientHandler(uri.Path)

		handler.Timeout = timeout
		handler.SlaveId = m.SlaveID
		handler.BaudRate = m.BaudRate
		handler.DataBits = m.DataBits
		handler.Parity = m.Parity
		handler.StopBits = m.StopBits

		if err := handler.Connect(); err != nil {
			return nil, fmt.Errorf("modbus.buildClient: %s", err)
		}

		m.device = uri.Path

		return modbus.NewClient(handler), nil
	case "ascii":
		handler := modbus.NewASCIIClientHandler(uri.Path)

		handler.SlaveId = m.SlaveID

		if err := handler.Connect(); err != nil {
			return nil, fmt.Errorf("modbus.buildClient: %s", err)
		}

		m.device = uri.Path

		return modbus.NewClient(handler), nil
	}

	return nil, fmt.Errorf("modbus.buildClient: scheme '%s' not recognized", uri.Scheme)
}

func fAdapter(f func(uint16) ([]byte, error)) func(uint16, uint16) ([]byte, error) {
	return func(a, _ uint16) ([]byte, error) { return f(a) }
}

func init() {
	inputs.Add("modbus", func() telegraf.Input {
		return &Modbus{}
	})
}
