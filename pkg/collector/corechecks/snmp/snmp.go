package snmp

import (
	"github.com/DataDog/datadog-agent/pkg/aggregator"
	"github.com/DataDog/datadog-agent/pkg/autodiscovery/integration"
	"github.com/DataDog/datadog-agent/pkg/collector/check"
	core "github.com/DataDog/datadog-agent/pkg/collector/corechecks"
	"github.com/DataDog/datadog-agent/pkg/util/log"
	"strings"
)

const (
	snmpCheckName = "snmp"
)

// Check aggregates metrics from one Check instance
type Check struct {
	core.CheckBase
	config  snmpConfig
	session sessionAPI
	sender  aggregator.Sender
}

// Run executes the check
func (c *Check) Run() error {
	sender, err := aggregator.GetSender(c.ID())
	if err != nil {
		return err
	}

	tags := []string{"snmp_device:" + c.config.IPAddress}

	c.sender = sender
	log.Infof("c.config.Metrics: %#v\n", c.config.Metrics) // TODO: remove me
	sender.Gauge("snmp.devices_monitored", float64(1), "", tags)

	// Create connection
	err = c.session.Connect()
	if err != nil {
		log.Errorf("Connect() err: %v", err)
	}
	defer c.session.Close() // TODO: handle error?

	snmpValues, err := c.fetchValues(err)
	if err != nil {
		return err
	}

	log.Infof("scalarValues: %#v\n", snmpValues.scalarValues) // TODO: remove me
	log.Infof("columnValues: %#v\n", snmpValues.columnValues) // TODO: remove me

	// Submit metrics
	c.submitMetrics(snmpValues, tags)

	// Commit
	sender.Commit()
	return nil
}

func (c *Check) fetchValues(err error) (*snmpValues, error) {
	scalarResults, err := fetchScalarOids(c.session, c.config.OidConfig.scalarOids)
	if err != nil {
		log.Errorf("Get() err: %v", err)
		return &snmpValues{}, err
	}

	oids := make(map[string]string)
	for _, value := range c.config.OidConfig.columnOids {
		oids[value] = value
	}
	columnResults, err := fetchColumnOids(c.session, oids)
	if err != nil {
		log.Errorf("GetBulk() err: %v", err)
		return &snmpValues{}, err
	}

	return &snmpValues{scalarResults, columnResults}, nil
}

func (c *Check) submitMetrics(snmpValues *snmpValues, tags []string) {
	for _, metric := range c.config.Metrics {
		if metric.Symbol.OID != "" {
			value, err := snmpValues.getScalarFloat64(metric.Symbol.OID)
			if err != nil {
				log.Warnf("error getting scalar value: %v", err)
				continue
			}
			c.sender.Gauge("snmp."+metric.Symbol.Name, value, "", tags)
		}
		if metric.Table.OID != "" {

			for _, symbol := range metric.Symbols {
				metricName := "snmp." + symbol.Name

				values, err := snmpValues.getColumnValue(symbol.OID)
				if err != nil {
					log.Warnf("error getting column value: %v", err)
					continue
				}
				for fullIndex, value := range values {
					indexes := strings.Split(fullIndex, ".")
					rowTags := tags[:]
					for _, metricTag := range metric.MetricTags {
						if (metricTag.Index == 0) || (metricTag.Index > uint(len(indexes))) {
							log.Warnf("invalid index %v, it must be between 1 and $v", metricTag.Index, len(indexes))
							continue
						}
						rowTags = append(rowTags, metricTag.Tag+":"+indexes[metricTag.Index-1])
					}
					c.sender.Gauge(metricName, value, "", rowTags)
				}
				log.Infof("Table column %v - %v: %#v", symbol.Name, symbol.OID, values)
			}
		}
	}
}

// Configure configures the snmp checks
func (c *Check) Configure(rawInstance integration.Data, rawInitConfig integration.Data, source string) error {
	err := c.CommonConfigure(rawInstance, source)
	if err != nil {
		return err
	}

	config, err := buildConfig(rawInstance, rawInitConfig)
	if err != nil {
		return err
	}

	c.config = config
	c.session.Configure(c.config)

	return nil
}

func snmpFactory() check.Check {
	return &Check{
		session:   &snmpSession{},
		CheckBase: core.NewCheckBase(snmpCheckName),
	}
}

func init() {
	core.RegisterCheck(snmpCheckName, snmpFactory)
}
