package omron

import (
	"sync"
	"testing"
	"time"
)

// Item 10: ConnectionMode / GetSourceNode used to read c.fins and c.transport
// without c.mu while Reconnect replaced c.fins (briefly nil). Run with -race.
func TestClientAccessorsDuringReconnect(t *testing.T) {
	f := newFakeFINS()
	port := startFINSTCP(t, f)
	c, err := Connect("127.0.0.1", WithTransport(TransportFINSTCP), WithPort(port), WithTimeout(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = c.ConnectionMode()
				_ = c.GetSourceNode()
				_ = c.IsConnected()
			}
		}()
	}
	for i := 0; i < 20; i++ {
		if err := c.Reconnect(); err != nil {
			t.Error(err)
			break
		}
	}
	close(stop)
	wg.Wait()
}
