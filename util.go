package lndutil

import (
	"context"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/breez/lnd/lnrpc"
	"github.com/btcsuite/btcutil"
	"github.com/lightningnetwork/lnd/macaroons"
	"github.com/pkg/errors"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"gopkg.in/macaroon.v2"
)

var (
	// DefaultNetwork is the default network
	DefaultNetwork = configDefaultLndNet()
	// DefaultPort is the default lnd port (10009)
	DefaultPort = configDefaultLndPort()
	// DefaultRPCHostPort is the default host port of lnd
	DefaultRPCHostPort = fmt.Sprintf("localhost:%d", DefaultPort)
	// DefaultTLSCertFileName is the default filename of the tls certificate
	DefaultTLSCertFileName = "tls.cert"
	// DefaultLndDir is the default location of .lnd
	DefaultLndDir = configDefaultLndDir()
	// LndNetwork is the default LND network (testnet)
	LndNetwork = configDefaultLndNet()
	// DefaultTLSCertPath is the default location of tls.cert
	DefaultTLSCertPath = filepath.Join(DefaultLndDir, "tls.cert")
	// DefaultMacaroonPath is the default dir of x.macaroon
	DefaultMacaroonPath = filepath.Join(DefaultLndDir, "data", "chain",
		"bitcoin", DefaultNetwork, "admin.macaroon")

	// DefaultCfg is a config interface with default values
	DefaultCfg = LightningConfig{
		LndDir:       DefaultLndDir,
		TLSCertPath:  DefaultTLSCertPath,
		MacaroonPath: DefaultMacaroonPath,
		Network:      DefaultNetwork,
		RPCServer:    DefaultRPCHostPort,
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
	cfg := LightningConfig{
		LndDir:       options.LndDir,
		TLSCertPath:  cleanAndExpandPath(options.TLSCertPath),
		MacaroonPath: cleanAndExpandPath(options.MacaroonPath),
		Network:      options.Network,
		RPCServer:    options.RPCServer,
	}

	if options.LndDir != DefaultLndDir {
		cfg.LndDir = options.LndDir
		cfg.TLSCertPath = filepath.Join(cfg.LndDir, DefaultTLSCertFileName)
		cfg.MacaroonPath = filepath.Join(cfg.LndDir,
			filepath.Join("data/chain/bitcoin",
				filepath.Join(cfg.Network, "admin.macaroon")))
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
