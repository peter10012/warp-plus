package wiresocks

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/netip"

	"github.com/bepass-org/warp-plus/proxy/pkg/mixed"
	"github.com/bepass-org/warp-plus/proxy/pkg/statute"
	"github.com/bepass-org/warp-plus/wireguard/device"
	"github.com/bepass-org/warp-plus/wireguard/tun/netstack"
)

// VirtualTun stores a reference to netstack network and DNS configuration
type VirtualTun struct {
	Tnet   *netstack.Net
	Logger *slog.Logger
	Dev    *device.Device
	Ctx    context.Context
}

// StartProxy spawns a socks5 server.
func (vt *VirtualTun) StartProxy(bindAddress netip.AddrPort) (netip.AddrPort, error) {
	ln, err := net.Listen("tcp", bindAddress.String())
	if err != nil {
		return netip.AddrPort{}, err // Return error if binding was unsuccessful
	}

	proxy := mixed.NewProxy(
		mixed.WithListener(ln),
		mixed.WithLogger(vt.Logger),
		mixed.WithContext(vt.Ctx),
		mixed.WithUserHandler(func(request *statute.ProxyRequest) error {
			return vt.generalHandler(request)
		}),
		// mixed.WithBytesPool(bufferpool.NewPool(256*1024)),
	)
	go func() {
		_ = proxy.ListenAndServe()
	}()
	go func() {
		<-vt.Ctx.Done()
		vt.Stop()
	}()

	return ln.Addr().(*net.TCPAddr).AddrPort(), nil
}

func (vt *VirtualTun) generalHandler(req *statute.ProxyRequest) error {
	vt.Logger.Info("handling connection", "protocol", req.Network, "destination", req.Destination)
	conn, err := vt.Tnet.Dial(req.Network, req.Destination)
	if err != nil {
		return err
	}
	// Close the connections when this function exits
	defer conn.Close()
	defer req.Conn.Close()
	// Channel to notify when copy operation is done
	done := make(chan error, 1)
	// Copy data from req.Conn to conn
	buf1 := make([]byte, 4*1024)
	buf2 := make([]byte, 4*1024)
	go func() {
		_, err := io.CopyBuffer(conn, req.Conn, buf1)
		done <- err
	}()
	// Copy data from conn to req.Conn
	go func() {
		_, err := io.CopyBuffer(req.Conn, conn, buf2)
		done <- err
	}()
	// Wait for one of the copy operations to finish
	err = <-done
	if err != nil {
		vt.Logger.Warn(err.Error())
	}

	// Close connections and wait for the other copy operation to finish
	<-done
	conn.Close()
	req.Conn.Close()

	return nil
}

func (vt *VirtualTun) Stop() {
	if vt.Dev != nil {
		if err := vt.Dev.Down(); err != nil {
			vt.Logger.Warn(err.Error())
		}
	}
}
