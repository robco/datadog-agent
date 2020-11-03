// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-2020 Datadog, Inc.

// +build linux_bpf

package module

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/pkg/errors"
	"google.golang.org/grpc"

	"github.com/DataDog/datadog-agent/cmd/system-probe/api"
	sapi "github.com/DataDog/datadog-agent/pkg/security/api"
	"github.com/DataDog/datadog-agent/pkg/security/config"
	"github.com/DataDog/datadog-agent/pkg/security/policy"
	"github.com/DataDog/datadog-agent/pkg/security/probe"
	sprobe "github.com/DataDog/datadog-agent/pkg/security/probe"
	"github.com/DataDog/datadog-agent/pkg/security/rules"
	"github.com/DataDog/datadog-agent/pkg/security/secl/eval"
	"github.com/DataDog/datadog-agent/pkg/util/log"
	"github.com/DataDog/datadog-go/statsd"
)

// Module represents the system-probe module for the runtime security agent
type Module struct {
	sync.RWMutex
	probe        *sprobe.Probe
	config       *config.Config
	ruleSet      *rules.RuleSet
	eventServer  *EventServer
	grpcServer   *grpc.Server
	listener     net.Listener
	statsdClient *statsd.Client
	rateLimiter  *RateLimiter
}

// Register the runtime security agent module
func (m *Module) Register(httpMux *http.ServeMux) error {
	// force socket cleanup of previous socket not cleanup
	os.Remove(m.config.SocketPath)

	ln, err := net.Listen("unix", m.config.SocketPath)
	if err != nil {
		return errors.Wrap(err, "unable to register security runtime module")
	}
	if err := os.Chmod(m.config.SocketPath, 0700); err != nil {
		return errors.Wrap(err, "unable to register security runtime module")
	}

	m.listener = ln

	go func() {
		if err := m.grpcServer.Serve(ln); err != nil {
			log.Error(err)
		}
	}()

	go m.statsMonitor(context.Background())

	// initialize the eBPF manager and load the programs and maps in the kernel. At this stage, the probes are not
	// running yet.
	if err := m.probe.Init(); err != nil {
		return errors.Wrap(err, "failed to init probe")
	}

	// start the manager and its probes / perf maps
	if err := m.probe.Start(); err != nil {
		return errors.Wrap(err, "failed to start probe")
	}

	// fetch the current state of the system (example: mount points, running processes, ...) so that our user space
	// context is ready when we start the probes
	if err := m.probe.Snapshot(); err != nil {
		return err
	}

	if err := m.Reload(); err != nil {
		return err
	}

	m.probe.SetEventHandler(m)

	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGHUP)

	go func() {
		for range c {
			log.Info("Reload configuration")

			if err := m.Reload(); err != nil {
				log.Errorf("failed to reload configuration: %s", err)
			}
		}
	}()

	return nil
}

func (m *Module) displayReport(report *probe.Report) {
	content, _ := json.MarshalIndent(report, "", "\t")
	log.Debug(string(content))
}

// Reload the rule set
func (m *Module) Reload() error {
	m.Lock()
	defer m.Unlock()

	rsa := sprobe.NewRuleSetApplier(m.config, m.probe)

	ruleSet := m.probe.NewRuleSet(rules.NewOptsWithParams(sprobe.SECLConstants, sprobe.SupportedDiscarders))
	if err := policy.LoadPolicies(m.config, ruleSet); err != nil {
		return err
	}

	// analyze the ruleset, push default policies in the kernel and generate the policy report
	report, err := rsa.Apply(ruleSet)
	if err != nil {
		return err
	}

	ruleSet.AddListener(m)
	ruleIDs := ruleSet.ListRuleIDs()

	m.eventServer.Apply(ruleIDs)
	m.rateLimiter.Apply(ruleIDs)
	m.ruleSet = ruleSet

	m.displayReport(report)

	return nil
}

// Close the module
func (m *Module) Close() {
	if m.grpcServer != nil {
		m.grpcServer.Stop()
	}

	if m.listener != nil {
		m.listener.Close()
		os.Remove(m.config.SocketPath)
	}

	m.probe.Close()
}

// RuleMatch is called by the ruleset when a rule matches
func (m *Module) RuleMatch(rule *eval.Rule, event eval.Event) {
	if m.rateLimiter.Allow(rule.ID) {
		m.eventServer.SendEvent(rule, event)
	} else {
		log.Tracef("Event on rule %s was dropped due to rate limiting", rule.ID)
	}
}

// EventDiscarderFound is called by the ruleset when a new discarder discovered
func (m *Module) EventDiscarderFound(rs *rules.RuleSet, event eval.Event, field eval.Field, eventType eval.EventType) {
	if err := m.probe.OnNewDiscarder(rs, event.(*sprobe.Event), field, eventType); err != nil {
		log.Trace(err)
	}
}

// HandleEvent is called by the probe when an event arrives from the kernel
func (m *Module) HandleEvent(event *sprobe.Event) {
	m.RLock()
	m.ruleSet.Evaluate(event)
	m.RUnlock()
}

func (m *Module) statsMonitor(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := m.probe.SendStats(m.statsdClient); err != nil {
				log.Debug(err)
			}
			if err := m.rateLimiter.SendStats(m.statsdClient); err != nil {
				log.Debug(err)
			}
			if err := m.eventServer.SendStats(m.statsdClient); err != nil {
				log.Debug(err)
			}
		case <-ctx.Done():
			return
		}
	}
}

// GetStats returns statistics about the module
func (m *Module) GetStats() map[string]interface{} {
	probeStats, err := m.probe.GetStats()
	if err != nil {
		return nil
	}

	return map[string]interface{}{
		"probe": probeStats,
	}
}

// GetProbe returns the module's probe
func (m *Module) GetProbe() *sprobe.Probe {
	return m.probe
}

// GetRuleSet returns the set of loaded rules
func (m *Module) GetRuleSet() *rules.RuleSet {
	m.RLock()
	defer m.RUnlock()

	return m.ruleSet
}

// NewModule instantiates a runtime security system-probe module
func NewModule(cfg *config.Config) (api.Module, error) {
	var statsdClient *statsd.Client
	var err error
	// statsd segfaults on 386 because of atomic primitive usage with wrong alignment
	// https://github.com/golang/go/issues/37262
	if runtime.GOARCH != "386" && cfg != nil {
		if statsdClient, err = statsd.New(cfg.StatsdAddr); err != nil {
			return nil, err
		}
	} else {
		log.Warn("Logs won't be send to DataDog")
	}

	probe, err := sprobe.NewProbe(cfg, statsdClient)
	if err != nil {
		return nil, err
	}

	m := &Module{
		config:       cfg,
		probe:        probe,
		eventServer:  NewEventServer(cfg),
		grpcServer:   grpc.NewServer(),
		statsdClient: statsdClient,
		rateLimiter:  NewRateLimiter(),
	}

	sapi.RegisterSecurityModuleServer(m.grpcServer, m.eventServer)

	return m, nil
}
