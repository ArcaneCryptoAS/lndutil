package lndutil

import (
	"context"
	"fmt"
	"github.com/lightningnetwork/lnd/lnrpc"
	"io/ioutil"
	"log"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
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
	defaultNetwork = configDefaultLndNet()
	// defaultPort is the default lnd port (10009)
	defaultPort = configDefaultLndPort()
	// defaultRPCHostPort is the default host port of lnd
	defaultRPCHostPort = fmt.Sprintf("localhost:%d", defaultPort)
	// defaultTLSCertFileName is the default filename of the tls certificate
	defaultTLSCertFileName = "tls.cert"
	// defaultLndDir is the default location of .lnd
	defaultLndDir = configDefaultLndDir()
	// lndNetwork is the default LND network (testnet)
	lndNetwork = configDefaultLndNet()
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

// cleanAndExpandPath expands environment variables and leading ~ in the
// passed path, cleans the result, and returns it.
// This function is taken from https://github.com/btcsuite/btcd
func cleanAndExpandPath(path string) string {
	if path == "" {
		return ""
	}

	// Expand initial ~ to OS specific home directory.
	if strings.HasPrefix(path, "~") {
		var homeDir string
		user, err := user.Current()
		if err == nil {
			homeDir = user.HomeDir
		} else {
			homeDir = os.Getenv("HOME")
		}

		path = strings.Replace(path, "~", homeDir, 1)
	}

	// NOTE: The os.ExpandEnv doesn't work with Windows-style %VARIABLE%,
	// but the variables can still be expanded via POSIX-style $VARIABLE.
	return filepath.Clean(os.ExpandEnv(path))
}

func configDefaultLndDir() string {
	if len(os.Getenv("LND_DIR")) != 0 {
		return os.Getenv("LND_DIR")
	}
	return btcutil.AppDataDir("lnd", false)
}

func configDefaultLndNet() string {
	if env := os.Getenv("LND_NETWORK"); env != "" {
		switch env {
		case "mainnet", "testnet", "regtest", "simnet":
			return env
		default:
			log.Fatalf("Environment variable LND_NETWORK is not a valid network: %s", env)
		}
	}
	return "testnet"
}

func configDefaultLndPort() int {
	env := os.Getenv("LND_PORT")
	if len(env) != 0 {
		port, err := strconv.Atoi(env)
		if err != nil {
			log.Fatalf("Environment variable LND_PORT is not a valid int: %s", env)
		}
		return port
	}
	return 10009
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
		cfg.TLSCertPath = filepath.Join(cfg.LndDir, defaultTLSCertFileName)
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

	backgroundContext := context.Background()
	withTimeout, cancel := context.WithTimeout(backgroundContext, 5*time.Second)
	defer cancel()

	conn, err := grpc.DialContext(withTimeout, cfg.RPCServer, opts...)
	if err != nil {
		err = errors.Wrap(err, "cannot dial to lnd")
		return nil, err
	}
	client := lnrpc.NewLightningClient(conn)

	log.Printf("opened connection to lnd on %s", cfg.RPCServer)

	return client, nil
}
