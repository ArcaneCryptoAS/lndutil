package lndutil

import (
	"context"
	"fmt"
	"github.com/lightningnetwork/lnd/lnrpc"
	"io/ioutil"
	"path/filepath"
	"time"

	"github.com/btcsuite/btcutil"
	"github.com/lightningnetwork/lnd/macaroons"
	"github.com/pkg/errors"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"gopkg.in/macaroon.v2"
)

var (
	// defaultNetwork is the default network
	defaultNetwork = "testnet"
	// defaultPort is the default lnd port
	defaultPort = 10009
	// defaultRPCHostPort is the default host port of lnd
	defaultRPCHostPort = fmt.Sprintf("localhost:%d", defaultPort)
	// defaultLndDir is the default location of .lnd
	defaultLndDir = btcutil.AppDataDir("lnd", false)
	// defaultTLSCertPath is the default location of tls.cert
	defaultTLSCertPath = filepath.Join(defaultLndDir, "tls.cert")
	// defaultMacaroonPath is the default dir of x.macaroon
	defaultMacaroonPath = filepath.Join(defaultLndDir, "data", "chain",
		"bitcoin", defaultNetwork, "admin.macaroon")

	// defaultCfg is a config interface with default values
	defaultCfg = LightningConfig{
		LndDir:       defaultLndDir,
		TLSCertPath:  defaultTLSCertPath,
		MacaroonPath: defaultMacaroonPath,
		Network:      defaultNetwork,
		RPCServer:    defaultRPCHostPort,
	}
)

// LightningConfig is a struct containing all possible options for configuring
// a connection to lnd
type LightningConfig struct {
	LndDir       string
	TLSCertPath  string
	MacaroonPath string
	Network      string
	RPCServer    string
}

// NewLNDClient opens a new connection to LND and returns the client
func NewLNDClient(options LightningConfig) (
	lnrpc.LightningClient, error) {
	cfg := defaultCfg

	// check for empty string in case it is just initialized to
	// the empty struct value
	if cfg.Network != options.Network && options.Network != "" {
		cfg.Network = options.Network
	}
	if cfg.LndDir != options.LndDir && options.LndDir != "" {
		cfg.LndDir = options.LndDir
		cfg.TLSCertPath = filepath.Join(cfg.LndDir, "tls.cert")
		cfg.MacaroonPath = filepath.Join(cfg.LndDir,
			filepath.Join("data/chain/bitcoin",
				filepath.Join(cfg.Network, "admin.macaroon")))
	}
	if cfg.MacaroonPath != options.MacaroonPath && options.MacaroonPath != "" {
		cfg.MacaroonPath = options.MacaroonPath
	}
	if cfg.TLSCertPath != options.TLSCertPath && options.TLSCertPath != "" {
		cfg.TLSCertPath = options.TLSCertPath
	}
	if cfg.RPCServer != options.RPCServer && options.RPCServer != "" {
		cfg.RPCServer = options.RPCServer
	}

	tlsCreds, err := credentials.NewClientTLSFromFile(cfg.TLSCertPath, "")
	if err != nil {
		err = errors.Wrap(err, "Cannot get node tls credentials")
		return nil, err
	}

	macaroonBytes, err := ioutil.ReadFile(cfg.MacaroonPath)
	if err != nil {
		err = errors.Wrap(err, "Cannot read macaroon file")
		return nil, err
	}

	mac := &macaroon.Macaroon{}
	if err = mac.UnmarshalBinary(macaroonBytes); err != nil {
		err = errors.Wrap(err, "Cannot unmarshal macaroon")
		return nil, err
	}

	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(tlsCreds),
		grpc.WithBlock(),
		grpc.WithPerRPCCredentials(macaroons.NewMacaroonCredential(mac)),
	}

	withTimeout, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	conn, err := grpc.DialContext(withTimeout, cfg.RPCServer, opts...)
	if err != nil {
		return nil, fmt.Errorf("cannot dial to lnd with config: %+v: %w", cfg, err)
	}
	client := lnrpc.NewLightningClient(conn)

	return client, nil
}
