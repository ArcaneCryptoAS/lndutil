package lndutil

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/user"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/btcsuite/btcd/chaincfg"
	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/lnrpc/invoicesrpc"
	"github.com/lightningnetwork/lnd/macaroons"
	"github.com/sirupsen/logrus"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
	"gopkg.in/macaroon.v2"
)

var log = logrus.New()

// Client can execute all LN functionality used by Teslacoil
type Client interface {
	lnrpc.LightningClient
	invoicesrpc.InvoicesClient
}

type client struct {
	lnrpc.LightningClient
	invoicesrpc.InvoicesClient
}

var _ Client = &client{}

// LightningConfig is a struct containing all possible options for configuring
// a connection to lnd
type LightningConfig struct {
	LndDir      string
	TLSCertPath string
	TLSKeyPath  string
	// MacaroonPath corresponds to the --adminmacaroonpath startup option of
	// lnd
	MacaroonPath string
	Network      chaincfg.Params
	RPCHost      string
	RPCPort      int
}

// DefaultRelativeMacaroonPath extracts the macaroon path using a specific network
func DefaultRelativeMacaroonPath(network chaincfg.Params) string {
	name := network.Name
	if name == "testnet3" {
		name = "testnet"
	}
	return filepath.Join("data", "chain",
		"bitcoin", name, "admin.macaroon")
}

// NewLNDClient opens a new connection to LND and returns the client
func NewLNDClient(options LightningConfig) (Client, error) {

	// no reason to attempt to connect if macaroon does not exist yet
	if err := awaitLndMacaroonFile(options); err != nil {
		return nil, err
	}

	cfg := LightningConfig{
		LndDir:       options.LndDir,
		TLSCertPath:  cleanAndExpandPath(options.TLSCertPath),
		MacaroonPath: cleanAndExpandPath(options.MacaroonPath),
		Network:      options.Network,
		RPCHost:      options.RPCHost,
		RPCPort:      options.RPCPort,
	}

	if cfg.TLSCertPath == "" {
		cfg.TLSCertPath = filepath.Join(cfg.LndDir, "tls.cert")
	}

	if cfg.MacaroonPath == "" {
		cfg.MacaroonPath = filepath.Join(cfg.LndDir,
			DefaultRelativeMacaroonPath(options.Network))
	}

	tlsCreds, err := credentials.NewClientTLSFromFile(cfg.TLSCertPath, "")
	if err != nil {
		err = fmt.Errorf("cannot get node tls credentials: %w", err)
		return nil, err
	}

	macaroonBytes, err := ioutil.ReadFile(cfg.MacaroonPath)
	if err != nil {
		err = fmt.Errorf("cannot read macaroon file: %w", err)
		return nil, err
	}

	mac := &macaroon.Macaroon{}
	if err = mac.UnmarshalBinary(macaroonBytes); err != nil {
		err = fmt.Errorf("cannot unmarshal macaroon: %w", err)
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

	log.WithFields(logrus.Fields{
		"certpath":     cfg.TLSCertPath,
		"macaroonpath": cfg.MacaroonPath,
		"network":      cfg.Network.Name,
		"rpchost":      cfg.RPCHost,
		"rpcport":      cfg.RPCPort,
	}).Info("Connecting to LND")

	conn, err := grpc.DialContext(withTimeout, fmt.Sprintf("%s:%d", cfg.RPCHost, cfg.RPCPort), opts...)
	if err != nil {
		err = fmt.Errorf("cannot dial to lnd: %w", err)
		return nil, err
	}
	var lnd client
	lnd.LightningClient = lnrpc.NewLightningClient(conn)
	lnd.InvoicesClient = invoicesrpc.NewInvoicesClient(conn)

	// we did not successfully open a connection if we can't query GetInfo
	// no point in returning a bad client
	if err = awaitLnd(lnd); err != nil {
		return nil, err
	}

	log.WithFields(logrus.Fields{
		"rpchost": cfg.RPCHost,
		"rpcport": cfg.RPCPort,
	}).Info("opened connection to LND")

	return lnd, nil
}

// awaitLnd tries to get a RPC response from lnd, returning an error
// if that isn't possible within a set of attempts
func awaitLnd(lncli lnrpc.LightningClient) error {
	var info *lnrpc.GetInfoResponse
	var err error
	retry := func() error {
		info, err = lncli.GetInfo(context.Background(), &lnrpc.GetInfoRequest{})
		return err
	}
	err = RetryBackoff(5, time.Second, retry)
	if err != nil {
		return fmt.Errorf("could not reach lnd: %w", err)
	}

	return nil
}

// awaitLndMacaroonFile waits for the creation of the macaroon file in the given
// configuration
func awaitLndMacaroonFile(config LightningConfig) error {
	macaroonPath := config.MacaroonPath
	if macaroonPath == "" {
		macaroonPath = path.Join(config.LndDir,
			DefaultRelativeMacaroonPath(config.Network))
	}
	retry := func() bool {
		_, err := os.Stat(macaroonPath)
		return err == nil
	}
	return Await(5, time.Second,
		retry, fmt.Sprintf("couldn't read macaroon file %q", macaroonPath))
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
		if u, err := user.Current(); err == nil {
			homeDir = u.HomeDir
		} else {
			homeDir = os.Getenv("HOME")
		}

		path = strings.Replace(path, "~", homeDir, 1)
	}

	// NOTE: The os.ExpandEnv doesn't work with Windows-style %VARIABLE%,
	// but the variables can still be expanded via POSIX-style $VARIABLE.
	return filepath.Clean(os.ExpandEnv(path))
}

// ListenShutdown listens for errors from LND, calling the given callback if LND quits
func ListenShutdown(lncli lnrpc.LightningClient, onShutdown func()) error {
	client, err := lncli.SubscribeInvoices(context.Background(), &lnrpc.InvoiceSubscription{})
	if err != nil {
		log.WithError(err).Error("Could not subscribe to invoice updates")
		return errors.New("could not subscribe to LND updates")
	}

	for {
		err := client.RecvMsg(&lnrpc.Invoice{})
		switch {
		case err == nil:
			// pass
		case errors.Is(err, io.EOF):
			log.WithError(err).Warn("Received EOF, lost LND connection")
			onShutdown()
			return nil
		default:
			if stat, ok := status.FromError(err); ok {
				log.WithError(stat.Err()).WithFields(logrus.Fields{
					"message": stat.Message(),
					"details": stat.Details(),
					"code":    stat.Code(),
				}).Warn("Received grpc status error")

				if stat.Message() == "transport is closing" {
					onShutdown()
					return nil
				}

				continue
			}
			log.WithError(err).WithField("type", fmt.Sprintf("%T", err)).Warn("Received unknown error when listening for invoices/shutdown")
		}
	}
}

// ListenInvoices subscribes to lnd invoices
func ListenInvoices(lncli lnrpc.LightningClient, msgCh chan<- *lnrpc.Invoice) {
	invoiceSubDetails := &lnrpc.InvoiceSubscription{}

	invoiceClient, err := lncli.SubscribeInvoices(
		context.Background(),
		invoiceSubDetails)
	if err != nil {
		log.WithError(err).Error("Could not subscribe to invoices")
		return
	}

	for {
		invoice := lnrpc.Invoice{}
		err := invoiceClient.RecvMsg(&invoice)
		if err != nil {
			log.WithError(err).Error("Could not receive message")
			return
		}
		log.WithFields(logrus.Fields{
			"paymentRequest": invoice.PaymentRequest,
			"hash":           hex.EncodeToString(invoice.RHash),
		}).Info("Received invoice")

		msgCh <- &invoice
	}

}

const (
	// MaxAmountSatPerChannel is the maximum amount of satoshis a channel can be for
	// https://github.com/lightningnetwork/lnd/blob/b9816259cb520fc169cb2cd829edf07f1eb11e1b/fundingmanager.go#L64
	MaxAmountSatPerChannel = (1 << 24) - 1
	// MaxAmountMsatPerChannel is the maximum amount of millisatoshis a channel can be for
	MaxAmountMsatPerChannel = MaxAmountSatPerChannel * 1000
	// MaxAmountSatPerInvoice is the maximum amount of satoshis an invoice can be for
	MaxAmountSatPerInvoice = MaxAmountMsatPerInvoice / 1000
	// MaxAmountMsatPerInvoice is the maximum amount of millisatoshis an invoice can be for
	MaxAmountMsatPerInvoice = 4294967295
)

// Known LND error messages
const (
	ErrRouteNotFound = "unable to find a path to destination"
	ErrAlreadyPaid   = "invoice is already paid"
)

// WaitTimeout waits for the waitgroup for the specified max timeout.
// Returns true if waiting timed out.
// Cribbed from https://stackoverflow.com/a/32843750/10359642
func WaitTimeout(wg *sync.WaitGroup, timeout time.Duration) bool {
	c := make(chan struct{})
	go func() {
		defer close(c)
		wg.Wait()
	}()
	select {
	case <-c:
		return false // completed normally
	case <-time.After(timeout):
		return true // timed out
	}
}

// WaitTimeoutErr waits for the waitgroup for the specified max timeout.
// Returns the first error from the waitgroup, a timeout error or nil.
func WaitTimeoutErr(wg *errgroup.Group, timeout time.Duration) error {
	c := make(chan error)
	go func() {
		defer close(c)
		c <- wg.Wait()
	}()

	select {
	case err := <-c:
		if err != nil {
			return fmt.Errorf("WaitTimeoutErr: %w", err)
		}
		return nil
	case <-time.After(timeout):
		return fmt.Errorf("timed out after %s", timeout)
	}
}

// RetryBackoff retries the given function until it doesn't fail. It doubles the
// period between attempts each time.
// Cribbed from https://upgear.io/blog/simple-golang-retry-function/
func RetryBackoff(attempts int, sleep time.Duration, fn func() error, msg ...interface{}) error {
	const format = "15:04:05" // HH:mm:ss
	start := time.Now()
	if err := innerRetry(attempts, sleep, fn); err != nil {
		end := time.Now()
		args := []interface{}{attempts, start.Format(format), end.Format(format), err}
		args = append(args, msg...)
		return fmt.Errorf("tried %d times from %s to %s: %w %v", args...)
	}
	return nil
}

func innerRetry(attempts int, sleep time.Duration, fn func() error) error {
	if err := fn(); err != nil {
		if attempts > 1 {
			time.Sleep(sleep)
			return innerRetry(attempts-1, 2*sleep, fn)
		}
		return err
	}
	return nil
}

// RetryNoBackoff retries the given function until it doesn't fail. It keeps
// the amount of time between attempts constant.
// Cribbed from https://upgear.io/blog/simple-golang-retry-function/
func RetryNoBackoff(attempts int, sleep time.Duration, fn func() error) error {
	start := time.Now()
	if err := innerRetryNoBackoff(attempts, sleep, fn); err != nil {
		end := time.Now()
		return fmt.Errorf(
			"failed after %d attempts between %s and %s: %w",
			attempts, start.Format("15:04:05"), end.Format("15:04:05"), err)
	}
	return nil
}

func innerRetryNoBackoff(attempts int, sleep time.Duration, fn func() error) error {
	if err := fn(); err != nil {
		if attempts > 1 {
			time.Sleep(sleep)
			return innerRetryNoBackoff(attempts-1, sleep, fn)
		}
		return err
	}
	return nil
}

// AwaitNoBackoff attempts the given condition the specified amount of times,
// sleeping between each attempt. If the condition doesn't succeed,
// it returns an error saying how many times we tried and how much time it
// took altogether.
func AwaitNoBackoff(attempts int, sleep time.Duration, fn func() bool, msgs ...string) error {
	return await(attempts, sleep, fn, innerAwaitNoBackoff, msgs...)
}

// Await attempts the given condition the specified amount of times, doubling
// the amount of time between each attempt. If the condition doesn't succeed,
// it returns an error saying how many times we tried and how much time it
// took altogether.
func Await(attempts int, sleep time.Duration, fn func() bool, msgs ...string) error {
	return await(attempts, sleep, fn, innerAwait, msgs...)
}

func await(attempts int, sleep time.Duration, fn func() bool, waiter func(int, time.Duration, func() bool) bool, msgs ...string) error {
	start := time.Now()
	if !waiter(attempts, sleep, fn) {
		end := time.Now()
		msg := fmt.Sprintf("Condition was not true after %d attempts and %s total waiting time",
			attempts, end.Sub(start))
		if len(msgs) != 0 {
			msg += ": "
			for _, m := range msgs {
				msg += m + " "
			}
		}
		return errors.New(msg)
	}
	return nil
}

func innerAwaitNoBackoff(attempts int, sleep time.Duration, fn func() bool) bool {
	if !fn() {
		if attempts > 1 {
			time.Sleep(sleep)
			return innerAwaitNoBackoff(attempts-1, sleep, fn)
		}
		return false
	}
	return true
}

func innerAwait(attempts int, sleep time.Duration, fn func() bool) bool {
	if !fn() {
		if attempts > 1 {
			time.Sleep(sleep)
			return innerAwait(attempts-1, 2*sleep, fn)
		}
		return false
	}
	return true
}
