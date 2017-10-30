// Copyright 2016 The etcd Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package e2e

import (
	"fmt"
	"io/ioutil"
	"net/url"
	"os"
	"strings"

	"github.com/coreos/etcd/etcdserver"
)

const etcdProcessBasePort = 20000

type clientConnType int

const (
	clientNonTLS clientConnType = iota
	clientTLS
	clientTLSAndNonTLS
)

var (
	configNoTLS = etcdProcessClusterConfig{
		clusterSize:  3,
		initialToken: "new",
	}
	configAutoTLS = etcdProcessClusterConfig{
		clusterSize:   3,
		isPeerTLS:     true,
		isPeerAutoTLS: true,
		initialToken:  "new",
	}
	configTLS = etcdProcessClusterConfig{
		clusterSize:  3,
		clientTLS:    clientTLS,
		isPeerTLS:    true,
		initialToken: "new",
	}
	configClientTLS = etcdProcessClusterConfig{
		clusterSize:  3,
		clientTLS:    clientTLS,
		initialToken: "new",
	}
	configClientBoth = etcdProcessClusterConfig{
		clusterSize:  1,
		clientTLS:    clientTLSAndNonTLS,
		initialToken: "new",
	}
	configClientAutoTLS = etcdProcessClusterConfig{
		clusterSize:     1,
		isClientAutoTLS: true,
		clientTLS:       clientTLS,
		initialToken:    "new",
	}
	configPeerTLS = etcdProcessClusterConfig{
		clusterSize:  3,
		isPeerTLS:    true,
		initialToken: "new",
	}
	configClientTLSCertAuth = etcdProcessClusterConfig{
		clusterSize:           1,
		clientTLS:             clientTLS,
		initialToken:          "new",
		clientCertAuthEnabled: true,
	}
)

func configStandalone(cfg etcdProcessClusterConfig) *etcdProcessClusterConfig {
	ret := cfg
	ret.clusterSize = 1
	return &ret
}

type etcdProcessCluster struct {
	cfg   *etcdProcessClusterConfig
	procs []etcdProcess
}

type etcdProcessClusterConfig struct {
	execPath    string
	dataDirPath string
	keepDataDir bool

	clusterSize int

	baseScheme string
	basePort   int

	metricsURLScheme string

	snapCount int // default is 10000

	clientTLS             clientConnType
	clientCertAuthEnabled bool
	isPeerTLS             bool
	isPeerAutoTLS         bool
	isClientAutoTLS       bool
	isClientCRL           bool

	forceNewCluster   bool
	initialToken      string
	quotaBackendBytes int64
	noStrictReconfig  bool
}

// newEtcdProcessCluster launches a new cluster from etcd processes, returning
// a new etcdProcessCluster once all nodes are ready to accept client requests.
func newEtcdProcessCluster(cfg *etcdProcessClusterConfig) (*etcdProcessCluster, error) {
	etcdCfgs := cfg.etcdServerProcessConfigs()
	epc := &etcdProcessCluster{
		cfg:   cfg,
		procs: make([]etcdProcess, cfg.clusterSize),
	}

	// launch etcd processes
	for i := range etcdCfgs {
		proc, err := newEtcdProcess(etcdCfgs[i])
		if err != nil {
			epc.Close()
			return nil, err
		}
		epc.procs[i] = proc
	}

	if err := epc.Start(); err != nil {
		return nil, err
	}
	return epc, nil
}

func (cfg *etcdProcessClusterConfig) clientScheme() string {
	if cfg.clientTLS == clientTLS {
		return "https"
	}
	return "http"
}

func (cfg *etcdProcessClusterConfig) peerScheme() string {
	peerScheme := cfg.baseScheme
	if peerScheme == "" {
		peerScheme = "http"
	}
	if cfg.isPeerTLS {
		peerScheme += "s"
	}
	return peerScheme
}

func (cfg *etcdProcessClusterConfig) etcdServerProcessConfigs() []*etcdServerProcessConfig {
	if cfg.basePort == 0 {
		cfg.basePort = etcdProcessBasePort
	}
	if cfg.execPath == "" {
		cfg.execPath = binPath
	}
	if cfg.snapCount == 0 {
		cfg.snapCount = etcdserver.DefaultSnapCount
	}

	etcdCfgs := make([]*etcdServerProcessConfig, cfg.clusterSize)
	initialCluster := make([]string, cfg.clusterSize)
	for i := 0; i < cfg.clusterSize; i++ {
		var curls []string
		var curl, curltls string
		port := cfg.basePort + 5*i
		curlHost := fmt.Sprintf("localhost:%d", port)

		switch cfg.clientTLS {
		case clientNonTLS, clientTLS:
			curl = (&url.URL{Scheme: cfg.clientScheme(), Host: curlHost}).String()
			curls = []string{curl}
		case clientTLSAndNonTLS:
			curl = (&url.URL{Scheme: "http", Host: curlHost}).String()
			curltls = (&url.URL{Scheme: "https", Host: curlHost}).String()
			curls = []string{curl, curltls}
		}

		purl := url.URL{Scheme: cfg.peerScheme(), Host: fmt.Sprintf("localhost:%d", port+1)}
		name := fmt.Sprintf("testname%d", i)
		dataDirPath := cfg.dataDirPath
		if cfg.dataDirPath == "" {
			var derr error
			dataDirPath, derr = ioutil.TempDir("", name+".etcd")
			if derr != nil {
				panic(fmt.Sprintf("could not get tempdir for datadir: %s", derr))
			}
		}
		initialCluster[i] = fmt.Sprintf("%s=%s", name, purl.String())

		args := []string{
			"--name", name,
			"--listen-client-urls", strings.Join(curls, ","),
			"--advertise-client-urls", strings.Join(curls, ","),
			"--listen-peer-urls", purl.String(),
			"--initial-advertise-peer-urls", purl.String(),
			"--initial-cluster-token", cfg.initialToken,
			"--data-dir", dataDirPath,
			"--snapshot-count", fmt.Sprintf("%d", cfg.snapCount),
		}
		args = addV2Args(args)
		if cfg.forceNewCluster {
			args = append(args, "--force-new-cluster")
		}
		if cfg.quotaBackendBytes > 0 {
			args = append(args,
				"--quota-backend-bytes", fmt.Sprintf("%d", cfg.quotaBackendBytes),
			)
		}
		if cfg.noStrictReconfig {
			args = append(args, "--strict-reconfig-check=false")
		}
		var murl string
		if cfg.metricsURLScheme != "" {
			murl = (&url.URL{
				Scheme: cfg.metricsURLScheme,
				Host:   fmt.Sprintf("localhost:%d", port+2),
			}).String()
			args = append(args, "--listen-metrics-urls", murl)
		}

		args = append(args, cfg.tlsArgs()...)
		etcdCfgs[i] = &etcdServerProcessConfig{
			execPath:     cfg.execPath,
			args:         args,
			tlsArgs:      cfg.tlsArgs(),
			dataDirPath:  dataDirPath,
			keepDataDir:  cfg.keepDataDir,
			name:         name,
			purl:         purl,
			acurl:        curl,
			murl:         murl,
			initialToken: cfg.initialToken,
		}
	}

	initialClusterArgs := []string{"--initial-cluster", strings.Join(initialCluster, ",")}
	for i := range etcdCfgs {
		etcdCfgs[i].initialCluster = strings.Join(initialCluster, ",")
		etcdCfgs[i].args = append(etcdCfgs[i].args, initialClusterArgs...)
	}

	return etcdCfgs
}

func (cfg *etcdProcessClusterConfig) tlsArgs() (args []string) {
	if cfg.clientTLS != clientNonTLS {
		if cfg.isClientAutoTLS {
			args = append(args, "--auto-tls")
		} else {
			tlsClientArgs := []string{
				"--cert-file", certPath,
				"--key-file", privateKeyPath,
				"--ca-file", caPath,
			}
			args = append(args, tlsClientArgs...)

			if cfg.clientCertAuthEnabled {
				args = append(args, "--client-cert-auth")
			}
		}
	}

	if cfg.isPeerTLS {
		if cfg.isPeerAutoTLS {
			args = append(args, "--peer-auto-tls")
		} else {
			tlsPeerArgs := []string{
				"--peer-cert-file", certPath,
				"--peer-key-file", privateKeyPath,
				"--peer-ca-file", caPath,
			}
			args = append(args, tlsPeerArgs...)
		}
	}

	if cfg.isClientCRL {
		args = append(args, "--client-crl-file", crlPath, "--client-cert-auth")
	}

	return args
}

func (epc *etcdProcessCluster) EndpointsV2() []string {
	return epc.endpoints(func(ep etcdProcess) []string { return ep.EndpointsV2() })
}

func (epc *etcdProcessCluster) EndpointsV3() []string {
	return epc.endpoints(func(ep etcdProcess) []string { return ep.EndpointsV3() })
}

func (epc *etcdProcessCluster) endpoints(f func(ep etcdProcess) []string) (ret []string) {
	for _, p := range epc.procs {
		ret = append(ret, f(p)...)
	}
	return ret
}

func (epc *etcdProcessCluster) Start() error {
	return epc.start(func(ep etcdProcess) error { return ep.Start() })
}

func (epc *etcdProcessCluster) Restart() error {
	return epc.start(func(ep etcdProcess) error { return ep.Restart() })
}

func (epc *etcdProcessCluster) start(f func(ep etcdProcess) error) error {
	readyC := make(chan error, len(epc.procs))
	for i := range epc.procs {
		go func(n int) { readyC <- f(epc.procs[n]) }(i)
	}
	for range epc.procs {
		if err := <-readyC; err != nil {
			epc.Close()
			return err
		}
	}
	return nil
}

func (epc *etcdProcessCluster) Stop() (err error) {
	for _, p := range epc.procs {
		if p == nil {
			continue
		}
		if curErr := p.Stop(); curErr != nil {
			if err != nil {
				err = fmt.Errorf("%v; %v", err, curErr)
			} else {
				err = curErr
			}
		}
	}
	return err
}

func (epc *etcdProcessCluster) Close() error {
	err := epc.Stop()
	for _, p := range epc.procs {
		// p is nil when newEtcdProcess fails in the middle
		// Close still gets called to clean up test data
		if p == nil {
			continue
		}
		if cerr := p.Close(); cerr != nil {
			err = cerr
		}
	}
	return err
}

func (epc *etcdProcessCluster) WithStopSignal(sig os.Signal) (ret os.Signal) {
	for _, p := range epc.procs {
		ret = p.WithStopSignal(sig)
	}
	return ret
}